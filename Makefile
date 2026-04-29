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
ROLLOUT_VM_LAB_HOST ?= jetmon-deploy-test
ROLLOUT_VM_LAB_SSH ?= ssh -F $(HOME)/.ssh/config -o ControlMaster=no -o ControlPath=none -o BatchMode=yes -o ConnectTimeout=10
ROLLOUT_VM_LAB_SNAPSHOT ?= pre-guided-flow
GO          ?= $(shell if command -v go >/dev/null 2>&1; then command -v go; elif [ -x /usr/local/go/bin/go ]; then printf /usr/local/go/bin/go; else printf go; fi)
GOCACHE     ?= /tmp/jetmon-go-cache
GOMODCACHE  ?= /tmp/jetmon-gomod-cache
GO_ENV      := GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE)
BUILD_FLAGS := -ldflags "-X main.version=$(shell git describe --tags --always --dirty) \
                         -X main.buildDate=$(shell date -u +%Y-%m-%dT%H:%M:%SZ) \
                         -X main.goVersion=$(shell $(GO) version | awk '{print $$3}')"

.PHONY: all build build-deliverer build-veriflier generate test test-race lint rollout-docs-verify rollout-vm-lab-sync rollout-vm-lab-sync-artifacts rollout-vm-lab-doctor rollout-vm-lab-prepare rollout-vm-lab-smoke rollout-vm-lab-execute-smoke rollout-vm-lab-failure-smoke rollout-vm-lab-resume-smoke rollout-vm-lab-post-start-rollback-smoke rollout-vm-lab-bad-ssh-smoke rollout-vm-lab-snapshot-execute-smoke api-cli-smoke api-cli-validate api-cli-token-create api-cli-token-list api-cli-token-revoke clean

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

rollout-vm-lab-sync:
	$(ROLLOUT_VM_LAB_SSH) $(ROLLOUT_VM_LAB_HOST) 'mkdir -p ~/jetmon-rollout-tools/scripts ~/jetmon-rollout-tools/docs'
	rsync -e "$(ROLLOUT_VM_LAB_SSH)" -a scripts/rollout-vm-lab.sh $(ROLLOUT_VM_LAB_HOST):~/jetmon-rollout-tools/scripts/
	rsync -e "$(ROLLOUT_VM_LAB_SSH)" -a docs/rollout-vm-lab.md $(ROLLOUT_VM_LAB_HOST):~/jetmon-rollout-tools/docs/

rollout-vm-lab-sync-artifacts: build rollout-vm-lab-sync
	$(ROLLOUT_VM_LAB_SSH) $(ROLLOUT_VM_LAB_HOST) 'mkdir -p ~/jetmon-rollout-tools/bin ~/jetmon-rollout-tools/systemd ~/jetmon-rollout-tools/config'
	rsync -e "$(ROLLOUT_VM_LAB_SSH)" -a bin/jetmon2 $(ROLLOUT_VM_LAB_HOST):~/jetmon-rollout-tools/bin/
	rsync -e "$(ROLLOUT_VM_LAB_SSH)" -a systemd/jetmon2.service systemd/jetmon2-logrotate $(ROLLOUT_VM_LAB_HOST):~/jetmon-rollout-tools/systemd/
	rsync -e "$(ROLLOUT_VM_LAB_SSH)" -a config/config-sample.json config/db-config-sample.conf $(ROLLOUT_VM_LAB_HOST):~/jetmon-rollout-tools/config/

rollout-vm-lab-doctor: rollout-vm-lab-sync
	$(ROLLOUT_VM_LAB_SSH) $(ROLLOUT_VM_LAB_HOST) 'cd ~/jetmon-rollout-tools && scripts/rollout-vm-lab.sh doctor'

rollout-vm-lab-prepare: rollout-vm-lab-sync-artifacts
	$(ROLLOUT_VM_LAB_SSH) $(ROLLOUT_VM_LAB_HOST) 'cd ~/jetmon-rollout-tools && scripts/rollout-vm-lab.sh prepare-topology'

rollout-vm-lab-smoke: rollout-vm-lab-sync-artifacts
	$(ROLLOUT_VM_LAB_SSH) $(ROLLOUT_VM_LAB_HOST) 'cd ~/jetmon-rollout-tools && scripts/rollout-vm-lab.sh smoke-preflight'
	$(ROLLOUT_VM_LAB_SSH) $(ROLLOUT_VM_LAB_HOST) 'cd ~/jetmon-rollout-tools && scripts/rollout-vm-lab.sh smoke-guided-dry-run'

rollout-vm-lab-execute-smoke: rollout-vm-lab-sync-artifacts
	$(ROLLOUT_VM_LAB_SSH) $(ROLLOUT_VM_LAB_HOST) 'cd ~/jetmon-rollout-tools && scripts/rollout-vm-lab.sh smoke-guided-execute-rollback'

rollout-vm-lab-failure-smoke: rollout-vm-lab-sync-artifacts
	$(ROLLOUT_VM_LAB_SSH) $(ROLLOUT_VM_LAB_HOST) 'cd ~/jetmon-rollout-tools && scripts/rollout-vm-lab.sh smoke-failure-gates'

rollout-vm-lab-resume-smoke: rollout-vm-lab-sync-artifacts
	$(ROLLOUT_VM_LAB_SSH) $(ROLLOUT_VM_LAB_HOST) 'cd ~/jetmon-rollout-tools && scripts/rollout-vm-lab.sh smoke-interrupted-resume'

rollout-vm-lab-post-start-rollback-smoke: rollout-vm-lab-sync-artifacts
	$(ROLLOUT_VM_LAB_SSH) $(ROLLOUT_VM_LAB_HOST) 'cd ~/jetmon-rollout-tools && scripts/rollout-vm-lab.sh smoke-post-start-rollback'

rollout-vm-lab-bad-ssh-smoke: rollout-vm-lab-sync-artifacts
	$(ROLLOUT_VM_LAB_SSH) $(ROLLOUT_VM_LAB_HOST) 'cd ~/jetmon-rollout-tools && scripts/rollout-vm-lab.sh smoke-bad-ssh'

rollout-vm-lab-snapshot-execute-smoke: rollout-vm-lab-sync-artifacts
	$(ROLLOUT_VM_LAB_SSH) $(ROLLOUT_VM_LAB_HOST) 'cd ~/jetmon-rollout-tools && scripts/rollout-vm-lab.sh snapshot-run $(ROLLOUT_VM_LAB_SNAPSHOT) execute-rollback'

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
