GO ?= go
GOFMT ?= gofmt

GO_FILES := $(shell find . -type f -name '*.go' \
	-not -path './.git/*' \
	-not -path './.wt/*')

.PHONY: fmt fmt-check fix test test-integration test-e2e e2e verify

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
	@if [ -d ./test/integration ]; then \
		$(GO) test -count=1 ./test/integration/...; \
	elif [ -d ./tests/integration ]; then \
		$(GO) test -count=1 ./tests/integration/...; \
	else \
		echo "no integration test suites yet"; \
	fi

test-e2e:
	@if [ -d ./test/e2e ]; then \
		$(GO) test -count=1 ./test/e2e/...; \
	elif [ -d ./tests/e2e ]; then \
		$(GO) test -count=1 ./tests/e2e/...; \
	else \
		echo "no e2e test suites yet"; \
	fi

e2e: test-e2e

verify: fmt-check test test-integration test-e2e
