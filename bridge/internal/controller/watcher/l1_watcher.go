package watcher

import (
	"context"
	"math/big"

	geth "github.com/scroll-tech/go-ethereum"
	"github.com/scroll-tech/go-ethereum/accounts/abi"
	"github.com/scroll-tech/go-ethereum/common"
	gethTypes "github.com/scroll-tech/go-ethereum/core/types"
	"github.com/scroll-tech/go-ethereum/crypto"
	"github.com/scroll-tech/go-ethereum/ethclient"
	"github.com/scroll-tech/go-ethereum/log"
	gethMetrics "github.com/scroll-tech/go-ethereum/metrics"
	"github.com/scroll-tech/go-ethereum/rpc"
	"gorm.io/gorm"

	"scroll-tech/common/bytecode/scroll/L1"
	"scroll-tech/common/bytecode/scroll/L1/rollup"
	"scroll-tech/common/metrics"
	"scroll-tech/common/types"

	"scroll-tech/bridge/internal/orm"
	"scroll-tech/bridge/internal/utils"
)

var (
	bridgeL1MsgsSyncHeightGauge          = gethMetrics.NewRegisteredGauge("bridge/l1/msgs/sync/height", metrics.ScrollRegistry)
	bridgeL1MsgsSentEventsTotalCounter   = gethMetrics.NewRegisteredCounter("bridge/l1/msgs/sent/events/total", metrics.ScrollRegistry)
	bridgeL1MsgsRollupEventsTotalCounter = gethMetrics.NewRegisteredCounter("bridge/l1/msgs/rollup/events/total", metrics.ScrollRegistry)
)

type rollupEvent struct {
	batchHash common.Hash
	txHash    common.Hash
	status    types.RollupStatus
}

// L1WatcherClient will listen for smart contract events from Eth L1.
type L1WatcherClient struct {
	ctx          context.Context
	client       *ethclient.Client
	l1MessageOrm *orm.L1Message
	l1BlockOrm   *orm.L1Block
	batchOrm     *orm.Batch

	// The number of new blocks to wait for a block to be confirmed
	confirmations rpc.BlockNumber

	messengerAddress common.Address
	messengerABI     *abi.ABI

	messageQueueAddress common.Address
	messageQueueABI     *abi.ABI

	scrollChainAddress common.Address
	scrollChainABI     *abi.ABI

	// The height of the block that the watcher has retrieved event logs
	processedMsgHeight uint64
	// The height of the block that the watcher has retrieved header rlp
	processedBlockHeight uint64
}

// NewL1WatcherClient returns a new instance of L1WatcherClient.
func NewL1WatcherClient(ctx context.Context, client *ethclient.Client, startHeight uint64, confirmations rpc.BlockNumber, messengerAddress, messageQueueAddress, scrollChainAddress common.Address, db *gorm.DB) *L1WatcherClient {
	l1MessageOrm := orm.NewL1Message(db)
	savedHeight, err := l1MessageOrm.GetLayer1LatestWatchedHeight()
	if err != nil {
		log.Warn("Failed to fetch height from db", "err", err)
		savedHeight = 0
	}
	if savedHeight < int64(startHeight) {
		savedHeight = int64(startHeight)
	}

	l1BlockOrm := orm.NewL1Block(db)
	savedL1BlockHeight, err := l1BlockOrm.GetLatestL1BlockHeight(ctx)
	if err != nil {
		log.Warn("Failed to fetch latest L1 block height from db", "err", err)
		savedL1BlockHeight = 0
	}
	if savedL1BlockHeight < startHeight {
		savedL1BlockHeight = startHeight
	}

	return &L1WatcherClient{
		ctx:           ctx,
		client:        client,
		l1MessageOrm:  l1MessageOrm,
		l1BlockOrm:    l1BlockOrm,
		batchOrm:      orm.NewBatch(db),
		confirmations: confirmations,

		messengerAddress: messengerAddress,
		messengerABI:     L1.L1ScrollMessengerABI,

		messageQueueAddress: messageQueueAddress,
		messageQueueABI:     rollup.L1MessageQueueABI,

		scrollChainAddress: scrollChainAddress,
		scrollChainABI:     rollup.ScrollChainABI,

		processedMsgHeight:   uint64(savedHeight),
		processedBlockHeight: savedL1BlockHeight,
	}
}

// ProcessedBlockHeight get processedBlockHeight
// Currently only use for unit test
func (w *L1WatcherClient) ProcessedBlockHeight() uint64 {
	return w.processedBlockHeight
}

// Confirmations get confirmations
// Currently only use for unit test
func (w *L1WatcherClient) Confirmations() rpc.BlockNumber {
	return w.confirmations
}

// SetConfirmations set the confirmations for L1WatcherClient
// Currently only use for unit test
func (w *L1WatcherClient) SetConfirmations(confirmations rpc.BlockNumber) {
	w.confirmations = confirmations
}

// FetchBlockHeader pull latest L1 blocks and save in DB
func (w *L1WatcherClient) FetchBlockHeader(blockHeight uint64) error {
	fromBlock := int64(w.processedBlockHeight) + 1
	toBlock := int64(blockHeight)
	if toBlock < fromBlock {
		return nil
	}
	if toBlock > fromBlock+contractEventsBlocksFetchLimit {
		toBlock = fromBlock + contractEventsBlocksFetchLimit - 1
	}

	var blocks []orm.L1Block
	var err error
	height := fromBlock
	for ; height <= toBlock; height++ {
		var block *gethTypes.Header
		block, err = w.client.HeaderByNumber(w.ctx, big.NewInt(height))
		if err != nil {
			log.Warn("Failed to get block", "height", height, "err", err)
			break
		}
		var baseFee uint64
		if block.BaseFee != nil {
			baseFee = block.BaseFee.Uint64()
		}
		blocks = append(blocks, orm.L1Block{
			Number:          uint64(height),
			Hash:            block.Hash().String(),
			BaseFee:         baseFee,
			GasOracleStatus: int16(types.GasOraclePending),
		})
	}

	// failed at first block, return with the error
	if height == fromBlock {
		return err
	}
	toBlock = height - 1

	// insert succeed blocks
	err = w.l1BlockOrm.InsertL1Blocks(w.ctx, blocks)
	if err != nil {
		log.Warn("Failed to insert L1 block to db", "fromBlock", fromBlock, "toBlock", toBlock, "err", err)
		return err
	}

	// update processed height
	w.processedBlockHeight = uint64(toBlock)
	return nil
}

// FetchContractEvent pull latest event logs from given contract address and save in DB
func (w *L1WatcherClient) FetchContractEvent() error {
	defer func() {
		log.Info("l1 watcher fetchContractEvent", "w.processedMsgHeight", w.processedMsgHeight)
	}()
	blockHeight, err := utils.GetLatestConfirmedBlockNumber(w.ctx, w.client, w.confirmations)
	if err != nil {
		log.Error("failed to get block number", "err", err)
		return err
	}

	fromBlock := int64(w.processedMsgHeight) + 1
	toBlock := int64(blockHeight)

	for from := fromBlock; from <= toBlock; from += contractEventsBlocksFetchLimit {
		to := from + contractEventsBlocksFetchLimit - 1

		if to > toBlock {
			to = toBlock
		}

		// warning: uint int conversion...
		query := geth.FilterQuery{
			FromBlock: big.NewInt(from), // inclusive
			ToBlock:   big.NewInt(to),   // inclusive
			Addresses: []common.Address{
				w.messengerAddress,
				w.scrollChainAddress,
				w.messageQueueAddress,
			},
			Topics: make([][]common.Hash, 1),
		}
		query.Topics[0] = make([]common.Hash, 5)
		query.Topics[0][0] = rollup.L1MessageQueueQueueTransactionEventSignature
		query.Topics[0][1] = L1.L1ScrollMessengerRelayedMessageEventSignature
		query.Topics[0][2] = L1.L1ScrollMessengerFailedRelayedMessageEventSignature
		query.Topics[0][3] = rollup.ScrollChainCommitBatchEventSignature
		query.Topics[0][4] = rollup.ScrollChainFinalizeBatchEventSignature

		logs, err := w.client.FilterLogs(w.ctx, query)
		if err != nil {
			log.Warn("Failed to get event logs", "err", err)
			return err
		}
		if len(logs) == 0 {
			w.processedMsgHeight = uint64(to)
			bridgeL1MsgsSyncHeightGauge.Update(to)
			continue
		}
		log.Info("Received new L1 events", "fromBlock", from, "toBlock", to, "cnt", len(logs))

		sentMessageEvents, rollupEvents, err := w.parseBridgeEventLogs(logs)
		if err != nil {
			log.Error("Failed to parse emitted events log", "err", err)
			return err
		}
		sentMessageCount := int64(len(sentMessageEvents))
		rollupEventCount := int64(len(rollupEvents))
		bridgeL1MsgsSentEventsTotalCounter.Inc(sentMessageCount)
		bridgeL1MsgsRollupEventsTotalCounter.Inc(rollupEventCount)
		log.Info("L1 events types", "SentMessageCount", sentMessageCount, "RollupEventCount", rollupEventCount)

		// use rollup event to update rollup results db status
		var batchHashes []string
		for _, event := range rollupEvents {
			batchHashes = append(batchHashes, event.batchHash.String())
		}
		statuses, err := w.batchOrm.GetRollupStatusByHashList(w.ctx, batchHashes)
		if err != nil {
			log.Error("Failed to GetRollupStatusByHashList", "err", err)
			return err
		}
		if len(statuses) != len(batchHashes) {
			log.Error("RollupStatus.Length mismatch with batchHashes.Length", "RollupStatus.Length", len(statuses), "batchHashes.Length", len(batchHashes))
			return nil
		}

		for index, event := range rollupEvents {
			batchHash := event.batchHash.String()
			status := statuses[index]
			// only update when db status is before event status
			if event.status > status {
				if event.status == types.RollupFinalized {
					err = w.batchOrm.UpdateFinalizeTxHashAndRollupStatus(w.ctx, batchHash, event.txHash.String(), event.status)
				} else if event.status == types.RollupCommitted {
					err = w.batchOrm.UpdateCommitTxHashAndRollupStatus(w.ctx, batchHash, event.txHash.String(), event.status)
				}
				if err != nil {
					log.Error("Failed to update Rollup/Finalize TxHash and Status", "err", err)
					return err
				}
			}
		}

		if err = w.l1MessageOrm.SaveL1Messages(w.ctx, sentMessageEvents); err != nil {
			return err
		}

		w.processedMsgHeight = uint64(to)
		bridgeL1MsgsSyncHeightGauge.Update(to)
	}

	return nil
}

func (w *L1WatcherClient) parseBridgeEventLogs(logs []gethTypes.Log) ([]*orm.L1Message, []rollupEvent, error) {
	// Need use contract abi to parse event Log
	// Can only be tested after we have our contracts set up
	var l1Messages []*orm.L1Message
	var rollupEvents []rollupEvent
	for _, vLog := range logs {
		switch vLog.Topics[0] {
		case rollup.L1MessageQueueQueueTransactionEventSignature:
			event := rollup.L1MessageQueueQueueTransactionEvent{}
			err := utils.UnpackLog(w.messageQueueABI, &event, "QueueTransaction", vLog)
			if err != nil {
				log.Warn("Failed to unpack layer1 QueueTransaction event", "err", err)
				return l1Messages, rollupEvents, err
			}

			msgHash := common.BytesToHash(crypto.Keccak256(event.Data))

			l1Messages = append(l1Messages, &orm.L1Message{
				QueueIndex: event.QueueIndex,
				MsgHash:    msgHash.String(),
				Height:     vLog.BlockNumber,
				Sender:     event.Sender.String(),
				Value:      event.Value.String(),
				Target:     event.Target.String(),
				Calldata:   common.Bytes2Hex(event.Data),
				GasLimit:   event.GasLimit.Uint64(),
				Layer1Hash: vLog.TxHash.Hex(),
			})
		case rollup.ScrollChainCommitBatchEventSignature:
			event := rollup.ScrollChainCommitBatchEvent{}
			err := utils.UnpackLog(w.scrollChainABI, &event, "CommitBatch", vLog)
			if err != nil {
				log.Warn("Failed to unpack layer1 CommitBatch event", "err", err)
				return l1Messages, rollupEvents, err
			}

			rollupEvents = append(rollupEvents, rollupEvent{
				batchHash: event.BatchHash,
				txHash:    vLog.TxHash,
				status:    types.RollupCommitted,
			})
		case rollup.ScrollChainFinalizeBatchEventSignature:
			event := rollup.ScrollChainFinalizeBatchEvent{}
			err := utils.UnpackLog(w.scrollChainABI, &event, "FinalizeBatch", vLog)
			if err != nil {
				log.Warn("Failed to unpack layer1 FinalizeBatch event", "err", err)
				return l1Messages, rollupEvents, err
			}

			rollupEvents = append(rollupEvents, rollupEvent{
				batchHash: event.BatchHash,
				txHash:    vLog.TxHash,
				status:    types.RollupFinalized,
			})
		default:
			log.Error("Unknown event", "topic", vLog.Topics[0], "txHash", vLog.TxHash)
		}
	}

	return l1Messages, rollupEvents, nil
}
