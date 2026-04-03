BINARY     := tailkitd
BUILD_DIR  := ./bin
CMD        := ./cmd/tailkitd
INSTALL_BIN := /usr/local/bin/tailkitd

# Required for install/uninstall targets — set via env or flag:
#   make dev-install TS_AUTHKEY=tskey-auth-xxxx
#   TS_AUTHKEY=tskey-auth-xxxx make dev-install
TS_AUTHKEY ?=
HOSTNAME   ?=

.PHONY: build dev-install dev-uninstall verify logs run lint

## build: compile the binary into ./bin/tailkitd (nosystemd tag = no CGO, works on any Linux/macOS)
build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build -tags nosystemd \
		-ldflags "-s -w -X main.version=dev -X main.commit=$$(git rev-parse --short HEAD 2>/dev/null || echo dirty)" \
		-o $(BUILD_DIR)/$(BINARY) $(CMD)
	@echo "Built: $(BUILD_DIR)/$(BINARY)"

## dev-install: build and install tailkitd on this machine (requires sudo + TS_AUTHKEY)
##   Usage: make dev-install TS_AUTHKEY=tskey-auth-xxxx [HOSTNAME=my-node]
dev-install: build
	@if [ -z "$(TS_AUTHKEY)" ]; then \
		echo "error: TS_AUTHKEY is required"; \
		echo "  usage: make dev-install TS_AUTHKEY=tskey-auth-xxxx"; \
		exit 1; \
	fi
	@echo "Copying binary to $(INSTALL_BIN)…"
	sudo cp $(BUILD_DIR)/$(BINARY) $(INSTALL_BIN)
	@echo "Running tailkitd install…"
	@if [ -n "$(HOSTNAME)" ]; then \
		sudo $(INSTALL_BIN) install --auth-key "$(TS_AUTHKEY)" --hostname "$(HOSTNAME)"; \
	else \
		sudo $(INSTALL_BIN) install --auth-key "$(TS_AUTHKEY)"; \
	fi

## dev-uninstall: stop, disable, and remove tailkitd from this machine (requires sudo)
dev-uninstall:
	sudo $(INSTALL_BIN) uninstall

## verify: validate the current installation and config files (no root needed)
verify:
	sudo $(INSTALL_BIN) verify

## logs: tail the live tailkitd service journal (Ctrl-C to stop)
logs:
	journalctl -u tailkitd -f --output=short-precise

## run: build and run tailkitd in the foreground for local debugging (no install)
##   Reads TS_AUTHKEY and TAILKITD_HOSTNAME from the environment or /etc/tailkitd/env.
##   Logs in human-readable dev format.
run: build
	TAILKITD_ENV=development $(BUILD_DIR)/$(BINARY) run

## lint: run go vet + staticcheck (install staticcheck with: go install honnef.co/go/tools/cmd/staticcheck@latest)
lint:
	go vet ./...
	@staticcheck ./... || echo "(staticcheck not installed — run: go install honnef.co/go/tools/cmd/staticcheck@latest)"

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
