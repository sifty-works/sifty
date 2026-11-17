BINARY   := bin/sifty
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X main.version=$(VERSION)
GO_BUILD := go build -ldflags "$(LDFLAGS)"

.PHONY: build dev check budget clean

build:
ifeq ($(wildcard web/package.json),)
else
	@if [ ! -d web/node_modules ]; then npm --prefix web ci; fi
	npm --prefix web run build
endif
	mkdir -p bin
	$(GO_BUILD) -o $(BINARY) ./cmd/sifty

dev: build
ifeq ($(wildcard web/package.json),)
	$(BINARY) serve --offline --data-dir .dev/data --port 7374
else
	$(BINARY) serve --offline --data-dir .dev/data --port 7374 & \
	sifty_pid=$$!; \
	trap "kill $$sifty_pid 2>/dev/null" INT TERM EXIT; \
	npm --prefix web run dev; \
	kill $$sifty_pid 2>/dev/null; true
endif

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
ifeq ($(wildcard web/package.json),)
else
	@echo "--- npm check ---"
	npm --prefix web run check
endif

budget:
	@bash scripts/budget.sh

clean:
	rm -rf bin/ .dev/
