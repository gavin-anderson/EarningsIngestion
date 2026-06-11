.PHONY: help dev start build test clean install-tools \
        run backfill up down migrate reset verify tidy

# Detect OS for binary extension.
ifeq ($(OS),Windows_NT)
BIN_EXT := .exe
# Use Git Bash explicitly. Windows ships its own bash.exe in System32 that
# launches WSL — if no WSL distro is installed, it fails. Pinning the path
# avoids the PATH-ordering footgun. No quotes here — they're added at call sites.
BASH := C:/Program Files/Git/bin/bash.exe
else
BIN_EXT :=
BASH := bash
endif

INGEST_BIN   := bin/ingest$(BIN_EXT)
BACKFILL_BIN := bin/backfill$(BIN_EXT)

help:            ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# =============================================================================
# Primary workflow (npm-style)
# =============================================================================

dev:             ## Start everything: docker + migrate (if needed) + hot-reload ingest
	@"$(BASH)" ./scripts/dev-up.sh
	@"$(BASH)" -c 'command -v air >/dev/null 2>&1 || { echo "air not on PATH. Run \"make install-tools\" and add \"$$(go env GOPATH)/bin\" to PATH."; exit 1; }'
	air

build:           ## Compile both binaries to ./bin/
	@mkdir -p bin
	go build -o $(INGEST_BIN) ./cmd/ingest
	go build -o $(BACKFILL_BIN) ./cmd/backfill
	@echo "Built: $(INGEST_BIN), $(BACKFILL_BIN)"

start:           ## Run the built ingest binary (run 'make build' first)
	@"$(BASH)" -c 'if [ ! -x "$(INGEST_BIN)" ]; then echo "No build found at $(INGEST_BIN). Run \"make build\" first."; exit 1; fi'
	./$(INGEST_BIN)

test:            ## Run all go tests
	go test ./...

clean:           ## Remove build outputs and air temp dir
	rm -rf bin tmp

install-tools:   ## Install dev tools (air for hot reload)
	go install github.com/air-verse/air@latest
	@echo ""
	@echo "Installed to: $$(go env GOPATH)/bin"
	@echo "Make sure that directory is on your PATH."
	@echo ""
	@echo "PowerShell (this session):"
	@echo "    \$$env:PATH += \";$$(go env GOPATH)/bin\""
	@echo "PowerShell (permanent):"
	@echo "    [Environment]::SetEnvironmentVariable(\"PATH\", \"\$$env:PATH;$$(go env GOPATH)/bin\", \"User\")"
	@echo "bash:"
	@echo "    export PATH=\"\$$PATH:$$(go env GOPATH)/bin\""

# =============================================================================
# Lower-level building blocks (use these when you want to skip the magic)
# =============================================================================

run:             ## Run ingest via 'go run' (no hot reload, no auto-bringup)
	go run ./cmd/ingest

backfill:        ## Run backfill. Usage: make backfill COIN=NVDA [DEX=xyz] [SINCE=2025-12-07] | make backfill ALL=1 [SINCE=2025-12-07]
	@"$(BASH)" -c '\
	  if [ -n "$(ALL)" ]; then \
	    go run ./cmd/backfill --all $(if $(SINCE),--since=$(SINCE)) $(if $(UNTIL),--until=$(UNTIL)); \
	  elif [ -z "$(COIN)" ]; then \
	    echo "Usage: make backfill COIN=NVDA [DEX=xyz] [SINCE=2025-12-07]"; \
	    echo "       make backfill ALL=1 [SINCE=2025-12-07]"; \
	    exit 1; \
	  else \
	    go run ./cmd/backfill --coin=$(COIN) $(if $(DEX),--dex=$(DEX)) $(if $(SINCE),--since=$(SINCE)) $(if $(UNTIL),--until=$(UNTIL)); \
	  fi'

up:              ## Start ClickHouse (data persists across restarts)
	docker compose up -d
	@echo "Waiting for ClickHouse to be ready..."
	@"$(BASH)" -c 'until docker exec clickhouse clickhouse-client --query "SELECT 1" >/dev/null 2>&1; do sleep 1; done'
	@echo "ClickHouse is ready."

down:            ## Stop ClickHouse (data persists)
	docker compose down

migrate:         ## Apply schema.sql to ClickHouse
	"$(BASH)" ./scripts/migrate.sh

reset:           ## DESTROY all data and recreate schema
	"$(BASH)" ./scripts/reset.sh

verify:          ## Quick sanity-check queries
	"$(BASH)" ./scripts/verify.sh

tidy:            ## go mod tidy
	go mod tidy
