BINARY      := bin/jetmon2
DELIVERER   := bin/jetmon-deliverer
VERIFLIER   := bin/veriflier2
API_SMOKE_BATCH ?= local-smoke
API_SMOKE_ARGS  ?=
API_VALIDATE_BATCH ?= api-cli-validate
API_VALIDATE_COUNT ?= 1
API_VALIDATE_MODE  ?= http-500
API_VALIDATE_WAIT  ?= 30s
API_VALIDATE_SKIP_FAILURE ?= 0
GO          ?= $(shell if command -v go >/dev/null 2>&1; then command -v go; elif [ -x /usr/local/go/bin/go ]; then printf /usr/local/go/bin/go; else printf go; fi)
GOCACHE     ?= /tmp/jetmon-go-cache
GO_ENV      := GOCACHE=$(GOCACHE)
BUILD_FLAGS := -ldflags "-X main.version=$(shell git describe --tags --always --dirty) \
                         -X main.buildDate=$(shell date -u +%Y-%m-%dT%H:%M:%SZ) \
                         -X main.goVersion=$(shell $(GO) version | awk '{print $$3}')"

.PHONY: all build build-deliverer build-veriflier generate test test-race lint api-cli-smoke api-cli-validate clean

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
	API_VALIDATE_SKIP_FAILURE=$(API_VALIDATE_SKIP_FAILURE) \
	scripts/api-cli-validate.sh

clean:
	rm -f $(BINARY) $(DELIVERER) $(VERIFLIER)
