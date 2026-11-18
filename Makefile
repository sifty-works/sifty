BINARY   := bin/sifty
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X main.version=$(VERSION)
GO_BUILD := go build -ldflags "$(LDFLAGS)"

.PHONY: build dev check budget clean web release

# web: download the pinned web client archive, verify its SHA-256, and unpack
# into web/dist. Skipped when web/dist already holds the pinned version.
web:
	@set -euo pipefail; \
	version=$$(grep '^version=' web/release.txt | cut -d= -f2); \
	url=$$(grep '^url=' web/release.txt | cut -d= -f2); \
	want=$$(grep '^sha256=' web/release.txt | cut -d= -f2); \
	marker="web/dist/.version"; \
	if [ -f "$$marker" ] && [ "$$(cat $$marker)" = "$$want" ]; then \
	  echo "web/dist already holds web client $$version"; \
	  exit 0; \
	fi; \
	echo "Downloading web client $$version from $$url"; \
	archive=$$(mktemp /tmp/sifty-web-XXXXXX.tar.gz); \
	curl -fsSL "$$url" -o "$$archive"; \
	got=$$(shasum -a 256 "$$archive" | awk '{print $$1}'); \
	if [ "$$got" != "$$want" ]; then \
	  rm -f "$$archive"; \
	  echo "SHA-256 mismatch: got $$got, want $$want" >&2; \
	  exit 1; \
	fi; \
	rm -rf web/dist; \
	mkdir -p web/dist; touch web/dist/.gitkeep; \
	tar -xzf "$$archive" -C web/dist --strip-components=0; \
	rm -f "$$archive"; \
	echo "$$want" > "$$marker"; \
	echo "Unpacked web client $$version into web/dist"

build: web
	mkdir -p bin
	$(GO_BUILD) -o $(BINARY) ./cmd/sifty

dev:
	@if [ ! -d .dev/data ] && [ -n "$${SIFTY_V1_DIR:-}" ]; then \
	  $(BINARY) import-v1 --from "$$SIFTY_V1_DIR" --data-dir .dev/data; \
	fi
	@echo "Starting server on http://127.0.0.1:7300"
	@echo "To run the web client: npm run dev  (in a sifty-web checkout)"
	$(BINARY) serve --offline $$([ -n "$${SIFTY_REPLAY_DIR:-}" ] && echo "--replay $$SIFTY_REPLAY_DIR") --data-dir .dev/data --port 7300

dev-reset:
	rm -rf .dev/data

# release: verify the pinned web client matches the current API description,
# then build the release binary. Refuses when web/dist/api.sha256 differs from
# the SHA-256 of api/openapi.yaml.
release: web
	@set -euo pipefail; \
	api_sha=$$(shasum -a 256 api/openapi.yaml | awk '{print $$1}'); \
	if [ ! -f web/dist/api.sha256 ]; then \
	  echo "web/dist/api.sha256 not found; run make web first" >&2; \
	  exit 1; \
	fi; \
	pinned=$$(cat web/dist/api.sha256); \
	if [ "$$pinned" != "$$api_sha" ]; then \
	  echo "Release refused: web/dist/api.sha256 ($$pinned) does not match" >&2; \
	  echo "api/openapi.yaml SHA-256 ($$api_sha)." >&2; \
	  echo "Publish a new web client built against the current API description." >&2; \
	  exit 1; \
	fi
	mkdir -p bin
	$(GO_BUILD) -o $(BINARY) ./cmd/sifty
	@echo "Release binary built: $(BINARY) version $(VERSION)"

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

budget:
	@bash scripts/budget.sh

clean:
	rm -rf bin/ .dev/
