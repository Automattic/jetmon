BINARY      := bin/jetmon2
VERIFLIER   := bin/veriflier2
GO          ?= $(shell if command -v go >/dev/null 2>&1; then command -v go; elif [ -x /usr/local/go/bin/go ]; then printf /usr/local/go/bin/go; else printf go; fi)
GOCACHE     ?= /tmp/jetmon-go-cache
GO_ENV      := GOCACHE=$(GOCACHE)
BUILD_FLAGS := -ldflags "-X main.version=$(shell git describe --tags --always --dirty) \
                         -X main.buildDate=$(shell date -u +%Y-%m-%dT%H:%M:%SZ) \
                         -X main.goVersion=$(shell $(GO) version | awk '{print $$3}')"

.PHONY: all build build-veriflier generate test test-race lint clean

all: build build-veriflier

build:
	mkdir -p bin
	$(GO_ENV) CGO_ENABLED=0 $(GO) build $(BUILD_FLAGS) -o $(BINARY) ./cmd/jetmon2/

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

clean:
	rm -f $(BINARY) $(VERIFLIER)
