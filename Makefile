.PHONY: all build test run clean

BIN_DIR := bin
BIN := $(BIN_DIR)/undined
GO ?= go

all: build

build:
	@mkdir -p $(BIN_DIR)
	GOTOOLCHAIN=local $(GO) build -tags "fts5" -o $(BIN) ./cmd/undined

test:
	GOTOOLCHAIN=local $(GO) test -v -tags "fts5" ./...

run: build
	./$(BIN)

clean:
	rm -rf $(BIN_DIR)
