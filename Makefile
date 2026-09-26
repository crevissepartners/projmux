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
PROJMUX_INSTALL_SOCKET ?= projmux
INSTALL_MV ?= mv

# The command that lists Go paths, not its output. fmt and fmt-check stream
# its NUL-separated output into xargs, so the recipe stays the same size as
# the tree grows instead of passing every path in one shell argument, which
# Linux caps at MAX_ARG_STRLEN (128 KiB).
GO_FILES_FIND := find . -type f -name '*.go' \
	-not -path './.git/*' \
	-not -path './.wt/*' \
	-not -path '*/node_modules/*'

# GOFMT_EACH runs gofmt with the given flags on the file arguments xargs
# supplies, and never with none (gofmt would read stdin). BSD xargs skips an
# empty input; GNU xargs runs once with no arguments, which the guard absorbs.
GOFMT_EACH = xargs -0 sh -c '[ "$$\#" -eq 0 ] || exec $(GOFMT) $(1) "$$@"' sh

DEADCODE_ALLOWLIST ?= .deadcode-allowlist.txt
DEADCODE_MUST_KEEP ?= .deadcode-must-keep.txt
DEADCODE_BASELINE_GATE ?= scripts/deadcode_baseline.py

SECURITY_BIN_DIR ?= $(BUILD_DIR)/security-tools
SECURITY_TOOL_MANIFEST ?= .security/security-tools.versions

DOCS_REFERENCE ?= docs/cli.md

.PHONY: fmt fmt-check mod-tidy-check fix vet build install npm-pack docs test smoke-assert-contract build-vcs-contract docker-workspace-contract fmt-contract test-integration test-install-smoke test-e2e test-e2e-contract e2e-pipe-contract e2e-terminal-line-contract test-e2e-reliability test-e2e-residual-policy test-e2e-shards test-e2e-manifest test-e2e-coverage test-e2e-update e2e verify deadcode deadcode-contract release-contract ci-contract security-pin-contract security-pin-refresh security security-serial security-go security-static security-policy security-contract security-tools

build:
	@mkdir -p $(BUILD_DIR)
	@vcs_flag="$$(scripts/build-vcs-flag.sh)" && \
	  echo "$(GO) build $${vcs_flag:+$$vcs_flag }-o $(PROJMUX_BIN) ./cmd/projmux" && \
	  $(GO) build $$vcs_flag -o $(PROJMUX_BIN) ./cmd/projmux
	@echo ">> built $(PROJMUX_BIN)"

install: build
	@mkdir -p $(INSTALL_DIR)
	@echo ">> converging live config before binary publication..."
	@$(PROJMUX_BIN) config apply --bin $(INSTALL_BIN) --socket $(PROJMUX_INSTALL_SOCKET) || { \
	  echo "install pre-publication convergence failed; binary publication not started; recovery: run \`projmux config apply --socket $(PROJMUX_INSTALL_SOCKET)\`" >&2; \
	  exit 1; \
	}
	@tmpfile="$(INSTALL_BIN).tmp.$$$$"; \
	  cp $(PROJMUX_BIN) "$$tmpfile" && \
	  chmod 0755 "$$tmpfile" && \
	  $(INSTALL_MV) "$$tmpfile" $(INSTALL_BIN) || { \
	    echo "install binary publication failed; install not successful; recovery: run \`projmux config apply --socket $(PROJMUX_INSTALL_SOCKET)\`" >&2; \
	    exit 1; \
	  }
	@echo ">> atomically replaced $(INSTALL_BIN)"
	@echo ">> verifying post-publication live config..."
	@$(INSTALL_BIN) config apply --socket $(PROJMUX_INSTALL_SOCKET) || { \
	  echo "install post-publication convergence failed; install not successful; recovery: run \`projmux config apply --socket $(PROJMUX_INSTALL_SOCKET)\`" >&2; \
	  exit 1; \
	}
	@echo ">> reconciling notify queue..."
	@$(INSTALL_BIN) notification reconcile || true
	@echo ">> replacing long-lived processes running the superseded image..."
	@replacement_status=0; \
	  PROJMUX_INSTALLER=make $(INSTALL_BIN) internal install-replace || replacement_status=$$?; \
	  PROJMUX_INSTALLER=make $(INSTALL_BIN) internal install-residue || true; \
	  exit $$replacement_status

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
	@$(GO_FILES_FIND) -print0 | $(call GOFMT_EACH,-w)

# Prints unformatted paths and fails when any exist. A gofmt failure (for
# example a parse error) fails the target too, even with empty stdout.
fmt-check:
	@status=0; \
	out="$$($(GO_FILES_FIND) -print0 | $(call GOFMT_EACH,-l))" || status=$$?; \
	if [ -n "$$out" ]; then \
		printf '%s\n' "$$out"; \
		[ "$$status" -ne 0 ] || status=1; \
	fi; \
	if [ "$$status" -ne 0 ] && [ -z "$$out" ]; then \
		echo ">> fmt-check: gofmt failed (exit $$status)" >&2; \
	fi; \
	exit "$$status"

mod-tidy-check:
	$(GO) mod tidy -diff

fix:
	$(GO) fix ./...
	@$(MAKE) --no-print-directory deadcode

# The current baseline is an exact set of symbols reported by deadcode. The
# proactive file protects migration/compatibility/proof APIs whether currently
# reachable, test-only, or reported. New findings are rejected against the
# union, while stale rows are rejected from the current baseline alone.
deadcode: deadcode-contract
	@findings="$$(mktemp)"; \
	trap 'rm -f "$$findings"' EXIT HUP INT TERM; \
	if ! $(GO) tool deadcode ./... > "$$findings"; then \
		echo ">> deadcode: tool execution failed; baseline was not evaluated" >&2; \
		exit 1; \
	fi; \
	python3 $(DEADCODE_BASELINE_GATE) \
		--allowlist $(DEADCODE_ALLOWLIST) \
		--must-keep $(DEADCODE_MUST_KEEP) \
		--findings "$$findings"

deadcode-contract:
	python3 -m unittest discover -s test -p 'deadcode_baseline_test.py'

vet:
	$(GO) vet ./...

test: deadcode-contract release-contract ci-contract security-pin-contract smoke-assert-contract build-vcs-contract docker-workspace-contract fmt-contract e2e-admission-contract e2e-pipe-contract e2e-terminal-line-contract
	$(GO) test ./...

smoke-assert-contract:
	bash test/smoke-assert-contract.sh

build-vcs-contract:
	bash test/build-vcs-contract.sh

docker-workspace-contract:
	bash test/docker-workspace-contract.sh

fmt-contract:
	bash test/fmt-contract.sh

release-contract:
	python3 -m unittest discover -s test -p 'release_workflow_contract_test.py'

ci-contract:
	python3 -m unittest discover -s test -p 'ci_workflow_contract_test.py'
	python3 -m unittest discover -s test -p 'agent_dialogue*_test.py'

# The reviewed security baselines have one pin. This keeps a second copy from
# coming back, proves a changed baseline still fails the gate, and checks the
# ./... package set against its pin.
security-pin-contract:
	GO="$(GO)" python3 -m unittest discover -s test -p 'security_pin_contract_test.py'

# After adding or removing a Go package: rewrite only package_count and
# package_set_sha256 in the pin. A no-op when the package set already matches.
security-pin-refresh:
	@GO="$(GO)" python3 scripts/security-package-pin.py --refresh

test-integration:
	scripts/test-integration-docker.sh

test-install-smoke:
	scripts/test-install-smoke.sh

test-e2e: test-e2e-manifest
	scripts/test-e2e-admission.sh scripts/test-e2e-docker.sh

test-e2e-contract:
	test/e2e/admission-contract.sh
	test/e2e/evidence-contract.sh

e2e-admission-contract:
	test/e2e/admission-contract.sh

e2e-pipe-contract:
	test/e2e/pipe-consumer-contract.sh

e2e-terminal-line-contract:
	test/e2e/terminal-line-contract.sh

test-e2e-reliability:
	test/e2e/reliability-contract.sh

test-e2e-residual-policy:
	test/e2e/residual-policy-contract.sh

test-e2e-shards:
	test/e2e/shard-contract.sh
	test/e2e/shard-isolation-stress.sh

test-e2e-manifest:
	E2E_COVERAGE_SKIP_GO=1 test/e2e/coverage-contract.sh

test-e2e-coverage:
	test/e2e/coverage-contract.sh

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
	@GO="$(GO)" \
		SECURITY_TOOL_MANIFEST="$(abspath $(SECURITY_TOOL_MANIFEST))" \
		SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" \
		scripts/security-tools.sh

security: security-tools
	@SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" scripts/security-aggregate.sh

security-serial: security-tools
	@SECURITY_AGGREGATE_MODE=serial SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" scripts/security-aggregate.sh

security-go: security-tools
	@SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" scripts/security.sh go-security

security-static: security-tools
	@SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" scripts/security.sh go-static

security-policy: security-tools
	@SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" scripts/security.sh repository-policy

security-contract: security-tools
	@SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" test/security-contract.sh
