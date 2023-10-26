//go:build ffi

// go test -v -race -gcflags="-l" -ldflags="-s=false" -tags ffi ./...
package core_test

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/scroll-tech/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"

	"scroll-tech/common/types/message"

	"scroll-tech/prover/config"
	"scroll-tech/prover/core"
)

var (
	paramsPath = flag.String("params", "/assets/test_params", "params dir")
	assetsPath = flag.String("assets", "/assets/test_assets", "assets dir")
	tracePath1 = flag.String("trace1", "/assets/traces/1_transfer.json", "chunk trace 1")
)

func TestFFI(t *testing.T) {
	as := assert.New(t)

	chunkProverConfig := &config.ProverCoreConfig{
		ParamsPath: *paramsPath,
		AssetsPath: *assetsPath,
		ProofType:  message.ProofTypeChunk,
	}
	chunkProverCore, err := core.NewProverCore(chunkProverConfig)
	as.NoError(err)
	t.Log("Constructed chunk prover")

	chunkTrace1 := readChunkTrace(*tracePath1, as)
	t.Log("Loaded chunk traces")

	for i := 1; i <= 50; i++ {
		t.Log("Proof-", i, " BEGIN mem: ", memUsage(as))

		_, _ := chunkProverCore.ProveChunk("chunk_proof1", chunkTrace1)
		// as.NoError(err)
		// t.Log("Generated chunk proof")

		t.Log("Proof-", i, " END mem: ", memUsage(as))

		runtime.GC()
		t.Log("Cleared GC manually")
	}
}

func readChunkTrace(filePat string, as *assert.Assertions) []*types.BlockTrace {
	f, err := os.Open(filePat)
	as.NoError(err)
	byt, err := io.ReadAll(f)
	as.NoError(err)

	trace := &types.BlockTrace{}
	as.NoError(json.Unmarshal(byt, trace))

	return []*types.BlockTrace{trace}
}

func memUsage(as *assert.Assertions) string {
	mem := "echo \"$(date '+%Y-%m-%d %H:%M:%S') $(free -g | grep Mem: | sed 's/Mem://g')\""
	cmd := exec.Command("bash", "-c", mem)

	output, err := cmd.Output()
	as.NoError(err)

	return string(output)
}
