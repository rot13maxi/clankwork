GO := $(shell which go || echo ~/.local/share/mise/installs/go/1.22.12/bin/go)
BIN := bin/clankwork
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
ACP_ADAPTER_VERSION ?= v0.3.7
CLANKWORK_HOME ?= $(HOME)/.clankwork
ACP_ADAPTER_BIN := $(CLANKWORK_HOME)/bin/acp-adapter

.PHONY: build test lint run install-acp-adapter clean

build:
	$(GO) build -ldflags="-X main.version=$(VERSION)" -o $(BIN) ./cmd/clankwork

install-acp-adapter:
	mkdir -p $(CLANKWORK_HOME)/bin
	GOBIN=$(CLANKWORK_HOME)/bin $(GO) install github.com/beyond5959/acp-adapter/cmd/acp@$(ACP_ADAPTER_VERSION)
	mv $(CLANKWORK_HOME)/bin/acp $(ACP_ADAPTER_BIN)

test:
	$(GO) test ./... -count=1 -race

lint:
	$(GO) vet ./...

run: build
	$(BIN) daemon

clean:
	rm -rf bin/
