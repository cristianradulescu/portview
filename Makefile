BINARY  := portview
PKG     := github.com/cristianradulescu/portview
BIN_DIR := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS ?= -trimpath

# Extra arguments for `make run` / `make dump`, e.g. make run ARGS="--interval 5s"
ARGS ?=

.PHONY: all build run dump test cover vet lint fmt fmt-check tidy check install clean help

all: check build ## Run all checks and build

build: ## Build the binary into ./bin
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) ./cmd/$(BINARY)

run: build ## Build and run the TUI
	./$(BIN_DIR)/$(BINARY) $(ARGS)

dump: build ## Build and print sockets to stdout (compare with `ss -tulpn`)
	./$(BIN_DIR)/$(BINARY) --dump $(ARGS)

test: ## Run unit tests
	go test -race -count=1 ./...

cover: ## Run tests with coverage report
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

vet: ## Run go vet
	go vet ./...

lint: ## Run staticcheck (pinned as a go.mod tool dependency)
	go tool staticcheck ./...

fmt: ## Format sources
	gofmt -s -w .

fmt-check: ## Fail if any file is not gofmt-ed
	@out="$$(gofmt -s -l .)"; if [ -n "$$out" ]; then echo "Not formatted:"; echo "$$out"; exit 1; fi

tidy: ## Tidy go.mod / go.sum
	go mod tidy

check: fmt-check vet lint test ## Formatting, vet, staticcheck and tests

install: ## Install into $GOBIN
	go install $(GOFLAGS) -ldflags '$(LDFLAGS)' ./cmd/$(BINARY)

clean: ## Remove build and test artifacts
	rm -rf $(BIN_DIR) coverage.out

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
