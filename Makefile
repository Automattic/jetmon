BINARY      := bin/jetmon2
VERIFLIER   := bin/veriflier2
BUILD_FLAGS := -ldflags "-X main.version=$(shell git describe --tags --always --dirty) \
                         -X main.buildDate=$(shell date -u +%Y-%m-%dT%H:%M:%SZ) \
                         -X main.goVersion=$(shell go version | awk '{print $$3}')"

.PHONY: all build build-veriflier generate test test-race lint clean

all: build build-veriflier

build:
	mkdir -p bin
	CGO_ENABLED=0 go build $(BUILD_FLAGS) -o $(BINARY) ./cmd/jetmon2/

build-veriflier:
	mkdir -p bin
	CGO_ENABLED=0 go build $(BUILD_FLAGS) -o $(VERIFLIER) ./veriflier2/cmd/


generate:
	protoc --go_out=. --go_opt=paths=source_relative \
	       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
	       proto/veriflier.proto

test:
	go test ./...

test-race:
	go test -race ./...

lint:
	go vet ./...

clean:
	rm -f $(BINARY) $(VERIFLIER)
