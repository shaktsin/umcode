.DEFAULT_GOAL := help

BLUE   := \033[0;34m
GREEN  := \033[0;32m
YELLOW := \033[0;33m
NC     := \033[0m

help: ## Show this help
	@echo "$(BLUE)UMCode$(NC)"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  $(YELLOW)%-20s$(NC) %s\n", $$1, $$2}'

# ---------------------------------------------------------------------------
# Go engine + Mac app (see GO_ENGINE.md)
# ---------------------------------------------------------------------------
.PHONY: go-build go-test go-vet go-engine go-universal app-dev app-check app-build app-build-universal

GO         ?= go
GO_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO_COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
GO_LDFLAGS := -s -w -X github.com/shaktsin/umcode/internal/version.Version=$(GO_VERSION) \
              -X github.com/shaktsin/umcode/internal/version.Commit=$(GO_COMMIT)

go-build: ## Build the Go engine + CLI → bin/umcode
	@mkdir -p bin
	$(GO) build -trimpath -ldflags '$(GO_LDFLAGS)' -o bin/umcode ./cmd/umcode

go-test: ## Run Go tests with the race detector
	$(GO) test -race ./...

go-vet: ## gofmt check + go vet
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; echo "run gofmt -w"; exit 1)
	$(GO) vet ./...

go-engine: go-build ## Run the Go engine in the foreground
	./bin/umcode engine

app-dev: go-build ## Run the Mac app's UI in a browser against a running engine
	@echo "$(YELLOW)Start the engine first: ./bin/umcode engine$(NC)"
	cd app/frontend && npm install && npm run dev

app-check: ## Type-check and test the app's UI
	cd app/frontend && npm install && npx svelte-check && npx vitest run

app-build: ## Build UMCode.app (macOS only) → bin/UMCode.app
	app/build/macos/bundle.sh

app-build-universal: ## Build a universal UMCode.app (arm64 + x86_64)
	app/build/macos/bundle.sh --universal

go-universal: ## Build a universal (arm64 + x86_64) macOS binary → bin/umcode-darwin
	@mkdir -p bin
	GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -ldflags '$(GO_LDFLAGS)' -o bin/umcode-darwin-arm64 ./cmd/umcode
	GOOS=darwin GOARCH=amd64 $(GO) build -trimpath -ldflags '$(GO_LDFLAGS)' -o bin/umcode-darwin-amd64 ./cmd/umcode
	lipo -create -output bin/umcode-darwin bin/umcode-darwin-arm64 bin/umcode-darwin-amd64
