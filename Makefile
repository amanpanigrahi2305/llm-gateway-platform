# Common tasks. Run `make help` to list them.
SHELL := /bin/bash

.PHONY: help test lint fmt

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

test: ## unit tests with the race detector
	go test -race -count=1 ./...

lint: ## go vet + golangci-lint
	go vet ./...
	golangci-lint run ./...

fmt: ## format all Go code
	gofmt -w .
