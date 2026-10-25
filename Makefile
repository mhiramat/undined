.PHONY: all build test test-e2e run clean build-vscode test-vscode

BIN_DIR := bin
BIN := $(BIN_DIR)/undined
GO ?= go

all: build

build:
	@mkdir -p $(BIN_DIR)
	GOTOOLCHAIN=local $(GO) build -tags "fts5" -o $(BIN) ./cmd/undined

test:
	GOTOOLCHAIN=local $(GO) test -v -tags "fts5" ./...

test-e2e:
	GOTOOLCHAIN=local $(GO) test -v -tags "fts5" ./test/e2e/...

build-vscode:
	$(MAKE) -C vscode-extension build

test-vscode:
	$(MAKE) -C vscode-extension test

run: build
	./$(BIN)

clean:
	rm -rf $(BIN_DIR)
