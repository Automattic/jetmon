BINARY      := bin/jetmon2
DELIVERER   := bin/jetmon-deliverer
VERIFLIER   := bin/veriflier2
API_SMOKE_BATCH ?= local-smoke
API_SMOKE_ARGS  ?=
API_VALIDATE_BATCH ?= api-cli-validate
API_VALIDATE_COUNT ?= 1
API_VALIDATE_MODE  ?= http-500
API_VALIDATE_WAIT  ?= 30s
API_VALIDATE_WEBHOOK_WAIT ?= 60s
API_VALIDATE_SKIP_WEBHOOK ?= 0
API_VALIDATE_SKIP_FAILURE ?= 0
DOCKER_COMPOSE ?= docker compose -f docker/docker-compose.yml
API_CLI_TOKEN_CONSUMER ?= api-cli
API_CLI_TOKEN_SCOPE ?= admin
API_CLI_TOKEN_CREATED_BY ?= docker-local
API_CLI_TOKEN_TTL ?= 0
API_CLI_TOKEN_ID ?=
GO          ?= $(shell if command -v go >/dev/null 2>&1; then command -v go; elif [ -x /usr/local/go/bin/go ]; then printf /usr/local/go/bin/go; else printf go; fi)
GOCACHE     ?= /tmp/jetmon-go-cache
GOMODCACHE  ?= /tmp/jetmon-gomod-cache
GO_ENV      := GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE)
BUILD_FLAGS := -ldflags "-X main.version=$(shell git describe --tags --always --dirty) \
                         -X main.buildDate=$(shell date -u +%Y-%m-%dT%H:%M:%SZ) \
                         -X main.goVersion=$(shell $(GO) version | awk '{print $$3}')"

.PHONY: all build build-deliverer build-veriflier generate test test-race lint rollout-docs-verify api-cli-smoke api-cli-validate api-cli-token-create api-cli-token-list api-cli-token-revoke clean

all: build build-deliverer build-veriflier

build:
	mkdir -p bin
	$(GO_ENV) CGO_ENABLED=0 $(GO) build $(BUILD_FLAGS) -o $(BINARY) ./cmd/jetmon2/

build-deliverer:
	mkdir -p bin
	$(GO_ENV) CGO_ENABLED=0 $(GO) build $(BUILD_FLAGS) -o $(DELIVERER) ./cmd/jetmon-deliverer/

build-veriflier:
	mkdir -p bin
	$(GO_ENV) CGO_ENABLED=0 $(GO) build $(BUILD_FLAGS) -o $(VERIFLIER) ./veriflier2/cmd/


generate:
	protoc --go_out=. --go_opt=paths=source_relative \
	       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
	       proto/veriflier.proto

test:
	$(GO_ENV) $(GO) test ./...

test-race:
	$(GO_ENV) $(GO) test -race ./...

lint:
	$(GO_ENV) $(GO) vet ./...

rollout-docs-verify: all test lint
	scripts/rollout-docs-verify.sh

api-cli-smoke: build
	@test -n "$$JETMON_API_TOKEN" || { echo "JETMON_API_TOKEN is required"; exit 1; }
	$(BINARY) api health --pretty
	$(BINARY) api me --pretty
	$(BINARY) api sites bulk-add --count 3 --batch $(API_SMOKE_BATCH) --dry-run --pretty
	$(BINARY) api smoke --batch $(API_SMOKE_BATCH) --pretty $(API_SMOKE_ARGS)

api-cli-validate: build
	API_CLI_BINARY=$(BINARY) \
	API_VALIDATE_BATCH=$(API_VALIDATE_BATCH) \
	API_VALIDATE_COUNT=$(API_VALIDATE_COUNT) \
	API_VALIDATE_MODE=$(API_VALIDATE_MODE) \
	API_VALIDATE_WAIT=$(API_VALIDATE_WAIT) \
	API_VALIDATE_WEBHOOK_WAIT=$(API_VALIDATE_WEBHOOK_WAIT) \
	API_VALIDATE_SKIP_WEBHOOK=$(API_VALIDATE_SKIP_WEBHOOK) \
	API_VALIDATE_SKIP_FAILURE=$(API_VALIDATE_SKIP_FAILURE) \
	scripts/api-cli-validate.sh

api-cli-token-create:
	$(DOCKER_COMPOSE) exec jetmon ./jetmon2 keys create --consumer $(API_CLI_TOKEN_CONSUMER) --scope $(API_CLI_TOKEN_SCOPE) --ttl $(API_CLI_TOKEN_TTL) --created-by $(API_CLI_TOKEN_CREATED_BY)

api-cli-token-list:
	$(DOCKER_COMPOSE) exec jetmon ./jetmon2 keys list

api-cli-token-revoke:
	@test -n "$(API_CLI_TOKEN_ID)" || { echo "API_CLI_TOKEN_ID is required"; exit 1; }
	$(DOCKER_COMPOSE) exec jetmon ./jetmon2 keys revoke $(API_CLI_TOKEN_ID)

clean:
	rm -f $(BINARY) $(DELIVERER) $(VERIFLIER)
