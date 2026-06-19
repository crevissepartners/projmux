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

DEADCODE_ALLOWLIST ?= .deadcode-allowlist.txt

.PHONY: fmt fmt-check fix build install npm-pack test test-integration test-install-smoke test-e2e e2e verify deadcode

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
	@$(MAKE) --no-print-directory deadcode

# deadcode runs golang.org/x/tools/cmd/deadcode (pinned via the go.mod tool
# directive) over the module and filters findings against an allowlist of
# intentional / MUST-KEEP symbols. It exits non-zero only when a NEW
# (non-allowlisted) unreachable function appears, so the checked-in baseline
# stays green while genuinely new dead code is surfaced.
deadcode:
	@findings="$$( $(GO) tool deadcode ./... )"; \
	if [ -z "$$findings" ]; then \
		echo ">> deadcode: no unreachable functions reported"; \
		exit 0; \
	fi; \
	allow="$$(mktemp)"; \
	grep -v '^[[:space:]]*#' $(DEADCODE_ALLOWLIST) | grep -v '^[[:space:]]*$$' > "$$allow"; \
	remaining="$$( printf '%s\n' "$$findings" | while IFS= read -r line; do \
		sym="$${line##*unreachable func: }"; \
		if grep -Fxq -- "$$sym" "$$allow"; then \
			continue; \
		fi; \
		printf '%s\n' "$$line"; \
	done )"; \
	rm -f "$$allow"; \
	if [ -n "$$remaining" ]; then \
		echo ">> deadcode: NEW unreachable functions (not in $(DEADCODE_ALLOWLIST)):"; \
		printf '%s\n' "$$remaining"; \
		exit 1; \
	fi; \
	echo ">> deadcode: clean (all findings allowlisted in $(DEADCODE_ALLOWLIST))"; \
	exit 0

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
