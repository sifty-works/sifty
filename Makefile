BINARY   := bin/sifty
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X main.version=$(VERSION)
GO_BUILD := go build -ldflags "$(LDFLAGS)"

.PHONY: build dev dev-reset check budget clean

build:
	@if [ ! -d web/node_modules ]; then npm --prefix web ci; fi
	npm --prefix web run build
	mkdir -p bin
	$(GO_BUILD) -o $(BINARY) ./cmd/sifty

dev: build
	@if [ ! -d .dev/data ] && [ -n "$$SIFTY_V1_DIR" ]; then \
	  $(BINARY) import-v1 --from "$$SIFTY_V1_DIR" --data-dir .dev/data; \
	fi
	$(BINARY) serve --offline $$([ -n "$$SIFTY_REPLAY_DIR" ] && echo "--replay $$SIFTY_REPLAY_DIR") --data-dir .dev/data --port 7374 & \
	sifty_pid=$$!; \
	trap "kill $$sifty_pid 2>/dev/null" INT TERM EXIT; \
	npm --prefix web run dev; \
	kill $$sifty_pid 2>/dev/null; true

dev-reset:
	rm -rf .dev/data

check: build
	@echo "--- gofmt ---"
	@gofmt_out=$$(gofmt -l .); \
	 if [ -n "$$gofmt_out" ]; then \
	   echo "Files not formatted:"; echo "$$gofmt_out"; exit 1; \
	 fi
	@echo "--- go vet ---"
	go vet ./...
	@echo "--- go test ---"
	go test ./...
	@echo "--- openapi ---"
	@tmp=$$(mktemp); \
	 $(BINARY) gen-api --out "$$tmp"; \
	 if ! diff -q api/openapi.yaml "$$tmp" > /dev/null 2>&1; then \
	   echo "api/openapi.yaml is stale; run '$(BINARY) gen-api' to regenerate"; \
	   diff api/openapi.yaml "$$tmp"; \
	   rm -f "$$tmp"; exit 1; \
	 fi; \
	 rm -f "$$tmp"
	@echo "--- openapi-typescript ---"
	@tmp=$$(mktemp); \
	 npm --prefix web exec -- openapi-typescript api/openapi.yaml -o "$$tmp" --quiet 2>/dev/null || \
	   npm --prefix web exec -- openapi-typescript api/openapi.yaml -o "$$tmp" 2>&1; \
	 if ! diff -q web/src/api/schema.gen.ts "$$tmp" > /dev/null 2>&1; then \
	   echo "web/src/api/schema.gen.ts is stale; run 'npm --prefix web run gen-api' to regenerate"; \
	   rm -f "$$tmp"; exit 1; \
	 fi; \
	 rm -f "$$tmp"
	@echo "--- npm check ---"
	npm --prefix web run check

budget:
	@bash scripts/budget.sh

clean:
	rm -rf bin/ .dev/
