.PHONY: check update dev_docker build_test_docker run_test_docker clean

L2GETH_TAG=scroll-v4.3.55

help: ## Display this help message
	@grep -h \
		-E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

lint: ## The code's format and security checks.
	make -C rollup lint
	make -C common lint
	make -C coordinator lint
	make -C database lint
	make -C prover lint
	make -C bridge-history-api lint

update: ## update dependencies
	go work sync
	cd $(PWD)/rollup/ && go get -u github.com/scroll-tech/go-ethereum@${L2GETH_TAG} && go mod tidy
	cd $(PWD)/bridge-history-api/ && go get -u github.com/ethereum/go-ethereum@latest && go mod tidy
	cd $(PWD)/common/ && go get -u github.com/scroll-tech/go-ethereum@${L2GETH_TAG} && go mod tidy
	cd $(PWD)/coordinator/ && go get -u github.com/scroll-tech/go-ethereum@${L2GETH_TAG} && go mod tidy
	cd $(PWD)/database/ && go get -u github.com/scroll-tech/go-ethereum@${L2GETH_TAG} && go mod tidy
	cd $(PWD)/prover/ && go get -u github.com/scroll-tech/go-ethereum@${L2GETH_TAG} && go mod tidy
	cd $(PWD)/prover-stats-api/ && go get -u github.com/scroll-tech/go-ethereum@${L2GETH_TAG} && go mod tidy
	cd $(PWD)/tests/integration-test/ && go get -u github.com/scroll-tech/go-ethereum@${L2GETH_TAG} && go mod tidy
	goimports -local $(PWD)/rollup/ -w .
	goimports -local $(PWD)/bridge-history-api/ -w .
	goimports -local $(PWD)/common/ -w .
	goimports -local $(PWD)/coordinator/ -w .
	goimports -local $(PWD)/database/ -w .
	goimports -local $(PWD)/prover/ -w .
	goimports -local $(PWD)/prover-stats-api/ -w .
	goimports -local $(PWD)/tests/integration-test/ -w .

dev_docker: ## build docker images for development/testing usages
	docker build -t scroll_l1geth ./common/docker/l1geth/
	docker build -t scroll_l2geth ./common/docker/l2geth/

build_test_docker: ## build Docker image for local testing on M1/M2 Silicon Mac
	docker build -t scroll_test_image -f ./build/dockerfiles/local_testing.Dockerfile $$(mktemp -d)

run_test_docker: ## run Docker image for local testing on M1/M2 Silicon Mac
	docker run -it --rm --name scroll_test_container --network=host -v /var/run/docker.sock:/var/run/docker.sock -v $(PWD):/go/src/app scroll_test_image

clean: ## Empty out the bin folder
	@rm -rf build/bin
