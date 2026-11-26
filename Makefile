# Common tasks. Run `make help` to list them.
SHELL      := /bin/bash
CONFIG     ?= config.local.yaml
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE      ?= llm-gateway
MOCK_IMAGE ?= llm-gateway-mock
TAG        ?= dev

COMPOSE    := docker compose -f deploy/compose/compose.yaml --env-file .env
KIND_NAME  := llm-gateway
METRICS_SERVER_VERSION := v0.9.1

.PHONY: help build test test-integration lint fmt run-mock run-gateway gen-key clean dev-up dev-down \
	docker-build docker-build-multiarch compose-up compose-down helm-lint kind-up deploy smoke scan kind-down

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

build: ## build the binaries into bin/
	CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o bin/gateway ./cmd/gateway
	CGO_ENABLED=0 go build -o bin/mockprovider ./cmd/mockprovider

docker-build: ## build the gateway and mock images for this machine
	docker build --target gateway --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG) .
	docker build --target mockprovider -t $(MOCK_IMAGE):$(TAG) .

docker-build-multiarch: ## build both images for linux/amd64 and linux/arm64 (kept locally, not pushed)
	docker buildx build --platform linux/amd64,linux/arm64 --target gateway --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG) .
	docker buildx build --platform linux/amd64,linux/arm64 --target mockprovider -t $(MOCK_IMAGE):$(TAG) .

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

test-integration: ## tests against the local Redis and Postgres from dev-up
	set -a; [ -f .env ] && . ./.env; set +a; \
	GATEWAY_TEST_REDIS_URL=redis://localhost:6380/15 GATEWAY_TEST_DATABASE_URL="$$GATEWAY_DATABASE_URL" \
	go test -race -count=1 -run 'Redis|Postgres' -v ./internal/limits/ ./internal/usage/

# Local services for development, in Docker. Ports are bound to 127.0.0.1 only,
# so nobody else on the network can reach them. Host ports 6380 and 5433 avoid
# clashes with a Redis or Postgres already on the laptop.
dev-up: ## start the local Redis and Postgres (Postgres password from .env)
	docker start llm-gw-redis 2>/dev/null || docker run -d --name llm-gw-redis -p 127.0.0.1:6380:6379 redis:8-alpine
	set -a; [ -f .env ] && . ./.env; set +a; \
	[ -n "$$GATEWAY_DB_PASSWORD" ] || { echo "set GATEWAY_DB_PASSWORD in .env first"; exit 1; }; \
	docker start llm-gw-postgres 2>/dev/null || docker run -d --name llm-gw-postgres -p 127.0.0.1:5433:5432 \
	  -e POSTGRES_USER=gateway -e POSTGRES_DB=gateway -e POSTGRES_PASSWORD="$$GATEWAY_DB_PASSWORD" postgres:18-alpine

dev-down: ## remove the local Redis and Postgres (the ledger data goes too)
	docker rm -f llm-gw-redis llm-gw-postgres

compose-up: ## start the laptop stack: gateway, Redis, Postgres, Ollama, 2 mocks (needs .env)
	$(COMPOSE) up -d --build

compose-down: ## stop the laptop stack (data volumes are kept)
	$(COMPOSE) down

helm-lint: ## lint the chart with its defaults and with the kind values
	helm lint deploy/helm/llm-gateway
	helm lint deploy/helm/llm-gateway -f deploy/kind/values.yaml

# metrics-server feeds CPU numbers to the HPA. kind's kubelets serve
# self-signed certificates that no cluster CA signed, so metrics-server cannot
# verify them; --kubelet-insecure-tls skips that check. Never use it on a real cluster.
kind-up: ## create the local 3-node kind cluster with metrics-server
	kind create cluster --config deploy/kind/cluster.yaml
	kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/download/$(METRICS_SERVER_VERSION)/components.yaml
	kubectl -n kube-system patch deployment metrics-server --type=json \
	  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
	kubectl -n kube-system rollout status deployment/metrics-server --timeout=180s

# Each deploy uses a new image tag, so Helm sees a changed pod spec and rolls
# only the pods whose image changed (the dev Redis and Postgres keep running).
deploy: TAG := kind-$(shell date +%s)
deploy: docker-build ## build the images, load them into kind, install the chart
	kind load docker-image $(IMAGE):$(TAG) $(MOCK_IMAGE):$(TAG) --name $(KIND_NAME)
	helm upgrade --install llm-gateway deploy/helm/llm-gateway -f deploy/kind/values.yaml \
	  --set image.tag=$(TAG) --set dev.mockProviders.image.tag=$(TAG) --wait --timeout 5m

smoke: ## smoke test the gateway on kind (after deploy)
	scripts/ci/kind-smoke.sh

scan: ## the CI scans: Trivy on both images (fails on CRITICAL), Checkov, gitleaks
	$(MAKE) docker-build TAG=scan
	trivy image --no-progress --scanners vuln --severity CRITICAL --exit-code 1 $(IMAGE):scan
	trivy image --no-progress --scanners vuln --severity CRITICAL --exit-code 1 $(MOCK_IMAGE):scan
	checkov -d .
	gitleaks git . --redact --no-banner

kind-down: ## delete the kind cluster
	kind delete cluster --name $(KIND_NAME)

lint: ## go vet + golangci-lint
	go vet ./...
	golangci-lint run ./...

fmt: ## format all Go code
	gofmt -w .
