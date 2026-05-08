GO ?= go
GOFMT ?= gofmt
BUILD_DIR ?= .bin
PROJMUX_BIN ?= $(BUILD_DIR)/projmux

GO_INSTALL_DIR := $(strip $(shell $(GO) env GOBIN 2>/dev/null))
ifeq ($(GO_INSTALL_DIR),)
GO_INSTALL_DIR := $(strip $(shell $(GO) env GOPATH 2>/dev/null))/bin
endif
INSTALL_DIR ?= $(GO_INSTALL_DIR)
INSTALL_BIN := $(INSTALL_DIR)/projmux

GO_FILES := $(shell find . -type f -name '*.go' \
	-not -path './.git/*' \
	-not -path './.wt/*')

.PHONY: fmt fmt-check fix build install npm-pack test test-integration test-install-smoke test-e2e e2e verify

build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build -o $(PROJMUX_BIN) ./cmd/projmux
	@echo ">> built $(PROJMUX_BIN)"

install: build
	@mkdir -p $(INSTALL_DIR)
	@tmpfile="$(INSTALL_BIN).tmp.$$$$"; \
	  cp $(PROJMUX_BIN) "$$tmpfile" && \
	  chmod 0755 "$$tmpfile" && \
	  mv "$$tmpfile" $(INSTALL_BIN)
	@echo ">> atomically replaced $(INSTALL_BIN)"
	@echo ">> applying live config..."
	@$(INSTALL_BIN) tmux apply
	@echo ">> reconciling notify queue..."
	@$(INSTALL_BIN) notify reconcile || true

npm-pack:
	scripts/package-npm.sh --pack

fmt:
	@if [ -n "$(GO_FILES)" ]; then \
		$(GOFMT) -w $(GO_FILES); \
	else \
		echo "no Go files to format"; \
	fi

fmt-check:
	@if [ -n "$(GO_FILES)" ]; then \
		out="$$( $(GOFMT) -l $(GO_FILES) )"; \
		if [ -n "$$out" ]; then \
			echo "$$out"; \
			exit 1; \
		fi; \
	else \
		echo "no Go files to check"; \
	fi

fix:
	$(GO) fix ./...

test:
	$(GO) test ./...

test-integration:
	scripts/test-integration-docker.sh

test-install-smoke:
	scripts/test-install-smoke.sh

test-e2e:
	scripts/test-e2e-docker.sh

e2e: test-e2e

verify: fmt-check test test-integration test-install-smoke test-e2e
