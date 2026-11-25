# Common tasks. Run `make help` to list them.
SHELL  := /bin/bash
CONFIG ?= config.local.yaml

.PHONY: help build test test-integration lint fmt run-mock run-gateway gen-key clean dev-up dev-down

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

test-integration: ## tests against the local Redis from dev-up (database 15)
	GATEWAY_TEST_REDIS_URL=redis://localhost:6380/15 go test -race -count=1 -run Redis -v ./internal/limits/

# Local services for development, in Docker. Ports are bound to 127.0.0.1 only,
# so nobody else on the network can reach these password-less services.
# Host port 6380 (not 6379) avoids a clash with a Redis already on the laptop.
dev-up: ## start the local Redis
	docker start llm-gw-redis 2>/dev/null || docker run -d --name llm-gw-redis -p 127.0.0.1:6380:6379 redis:8-alpine

dev-down: ## remove the local Redis
	docker rm -f llm-gw-redis

lint: ## go vet + golangci-lint
	go vet ./...
	golangci-lint run ./...

fmt: ## format all Go code
	gofmt -w .
