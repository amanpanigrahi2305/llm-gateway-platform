# Common tasks. Run `make help` to list them.
SHELL  := /bin/bash
CONFIG ?= config.local.yaml

.PHONY: help build test lint fmt run-mock run-gateway gen-key clean

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

build: ## build the binaries into bin/
	CGO_ENABLED=0 go build -o bin/gateway ./cmd/gateway
	CGO_ENABLED=0 go build -o bin/mockprovider ./cmd/mockprovider

run-mock: build ## run the mock LLM on :9090
	./bin/mockprovider -addr :9090

run-gateway: build ## run the gateway with $(CONFIG); provider keys come from .env
	set -a; [ -f .env ] && . ./.env; set +a; ./bin/gateway serve -config $(CONFIG)

gen-key: build ## make a new client key and its hash
	./bin/gateway gen-key

clean: ## remove build output
	rm -rf bin

test: ## unit tests with the race detector
	go test -race -count=1 ./...

lint: ## go vet + golangci-lint
	go vet ./...
	golangci-lint run ./...

fmt: ## format all Go code
	gofmt -w .
