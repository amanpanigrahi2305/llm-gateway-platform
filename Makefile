# Common tasks. Run `make help` to list them.
SHELL := /bin/bash

.PHONY: help build test lint fmt run-mock clean

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

build: ## build the binaries into bin/
	CGO_ENABLED=0 go build -o bin/mockprovider ./cmd/mockprovider

run-mock: build ## run the mock LLM on :9090
	./bin/mockprovider -addr :9090

clean: ## remove build output
	rm -rf bin

test: ## unit tests with the race detector
	go test -race -count=1 ./...

lint: ## go vet + golangci-lint
	go vet ./...
	golangci-lint run ./...

fmt: ## format all Go code
	gofmt -w .
