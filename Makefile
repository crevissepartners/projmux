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

SECURITY_BIN_DIR ?= $(BUILD_DIR)/security-tools
SECURITY_TOOL_MANIFEST ?= .security/security-tools.versions

DOCS_REFERENCE ?= docs/cli.md

.PHONY: fmt fmt-check fix build install npm-pack docs test test-integration test-install-smoke test-e2e test-e2e-update e2e verify deadcode security security-tools

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
	@$(INSTALL_BIN) config apply
	@echo ">> reconciling notify queue..."
	@$(INSTALL_BIN) notification reconcile || true

npm-pack:
	scripts/package-npm.sh --pack

# docs regenerates the generated CLI reference from the command manifest. The
# render goes through a temp file so a failed generator can never leave a
# truncated page behind. `make test` fails when the checked-in page and the
# manifest disagree, so regenerating is mandatory whenever a route changes.
docs:
	@$(GO) run ./internal/tools/gendocs > $(DOCS_REFERENCE).tmp
	@mv $(DOCS_REFERENCE).tmp $(DOCS_REFERENCE)
	@echo ">> regenerated $(DOCS_REFERENCE)"

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

# Opt-in / local: depends on the public npm registry and published projmux
# package, so it is not part of `verify`.
test-e2e-update:
	scripts/test-e2e-update-docker.sh

e2e: test-e2e

verify: fmt-check test test-integration test-install-smoke test-e2e

# Go-based security tools are pinned to the versions used to produce the
# checked-in baselines. shellcheck, python3, and git are host dependencies;
# scripts/security.sh reports actionable installation guidance when missing.
security-tools:
	@set -eu; \
	manifest="$(abspath $(SECURITY_TOOL_MANIFEST))"; \
	bin_dir="$(abspath $(SECURITY_BIN_DIR))"; \
	if [ ! -f "$$manifest" ] || [ -L "$$manifest" ]; then \
		echo "security-tools: canonical manifest is missing or not a regular file: $$manifest" >&2; \
		exit 2; \
	fi; \
	if ! awk -F= 'BEGIN { expected[1]="govulncheck"; expected[2]="gosec"; expected[3]="staticcheck"; expected[4]="gitleaks"; expected[5]="actionlint" } NF != 2 || $$1 != expected[NR] || $$2 !~ /^v[0-9][^[:space:]=]*$$/ { exit 1 } END { if (NR != 5) exit 1 }' "$$manifest"; then \
		echo "security-tools: invalid canonical manifest (expected exactly five ordered tool=vVERSION entries): $$manifest" >&2; \
		exit 2; \
	fi; \
	mkdir -p "$$bin_dir"; \
	goos="$$( $(GO) env GOOS )"; \
	goarch="$$( $(GO) env GOARCH )"; \
	tools_ok=1; \
	while IFS='=' read -r tool version; do \
		case "$$tool" in \
			govulncheck) package="golang.org/x/vuln/cmd/govulncheck"; module="golang.org/x/vuln" ;; \
			gosec) package="github.com/securego/gosec/v2/cmd/gosec"; module="github.com/securego/gosec/v2" ;; \
			staticcheck) package="honnef.co/go/tools/cmd/staticcheck"; module="honnef.co/go/tools" ;; \
			gitleaks) package="github.com/zricethezav/gitleaks/v8"; module="github.com/zricethezav/gitleaks/v8" ;; \
			actionlint) package="github.com/rhysd/actionlint/cmd/actionlint"; module="github.com/rhysd/actionlint" ;; \
		esac; \
		if [ ! -f "$$bin_dir/$$tool" ] || [ -L "$$bin_dir/$$tool" ] || [ ! -x "$$bin_dir/$$tool" ] || \
		   ! $(GO) version -m "$$bin_dir/$$tool" 2>/dev/null | awk -v want_package="$$package" -v want_module="$$module" -v want_version="$$version" -v want_goos="GOOS=$$goos" -v want_goarch="GOARCH=$$goarch" '$$1 == "path" && $$2 == want_package { package_ok=1 } $$1 == "mod" && $$2 == want_module && $$3 == want_version { module_ok=1 } $$1 == "build" && $$2 == want_goos { goos_ok=1 } $$1 == "build" && $$2 == want_goarch { goarch_ok=1 } END { exit(package_ok && module_ok && goos_ok && goarch_ok ? 0 : 1) }'; then \
			tools_ok=0; \
		fi; \
	done < "$$manifest"; \
	if [ ! -f "$$bin_dir/.versions" ] || [ -L "$$bin_dir/.versions" ] || ! cmp -s "$$manifest" "$$bin_dir/.versions"; then \
		tools_ok=0; \
	fi; \
	if [ "$$tools_ok" = "1" ]; then \
		echo ">> pinned security tools already installed in $$bin_dir"; \
		exit 0; \
	fi; \
	echo ">> installing pinned security tools into $$bin_dir"; \
	stage="$$(mktemp -d "$$(dirname "$$bin_dir")/.security-tools.XXXXXX")"; \
	trap 'rm -rf "$$stage"' EXIT HUP INT TERM; \
	while IFS='=' read -r tool version; do \
		case "$$tool" in \
			govulncheck) package="golang.org/x/vuln/cmd/govulncheck" ;; \
			gosec) package="github.com/securego/gosec/v2/cmd/gosec" ;; \
			staticcheck) package="honnef.co/go/tools/cmd/staticcheck" ;; \
			gitleaks) package="github.com/zricethezav/gitleaks/v8" ;; \
			actionlint) package="github.com/rhysd/actionlint/cmd/actionlint" ;; \
		esac; \
		GOBIN="$$stage" $(GO) install "$$package@$$version"; \
	done < "$$manifest"; \
	cp "$$manifest" "$$stage/.versions"; \
	for tool in govulncheck gosec staticcheck gitleaks actionlint; do \
		mv "$$stage/$$tool" "$$bin_dir/$$tool"; \
	done; \
	mv "$$stage/.versions" "$$bin_dir/.versions"; \
	rmdir "$$stage"; \
	trap - EXIT HUP INT TERM

security: security-tools
	@SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" scripts/security.sh
