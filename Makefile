.DEFAULT_GOAL := help

GO_DIR := services/api
DEBUG_ARGS ?= -trace
EVAL_ARGS ?=
UV ?= uv
GOLANGCI_LINT_VERSION := v2.14.0
TOOLS_DIR := $(CURDIR)/$(GO_DIR)/bin/golangci-lint/$(GOLANGCI_LINT_VERSION)
GOLANGCI_LINT := $(TOOLS_DIR)/golangci-lint

.PHONY: debug trace eval eval-check eval-jsonl eval-test help tools fmt fmt-check build test vet lint check \
	go-tools go-fmt go-fmt-check go-build go-test go-vet go-lint go-check

help:
	@echo "make debug      Start the local decision debug console"
	@echo "make trace      Start local Phoenix at http://127.0.0.1:6006"
	@echo "make eval       Run Phoenix experiments (optional EVAL_ARGS)"
	@echo "make eval-check Check experiment prerequisites without model generation"
	@echo "make eval-jsonl Score existing JSONL or Phoenix CSV output (optional EVAL_ARGS)"
	@echo "make eval-test  Run isolated Python evaluator tests"
	@echo "make tools      Install development tools"
	@echo "make fmt        Format source files"
	@echo "make fmt-check  Check formatting without changing files"
	@echo "make build      Build all implemented projects"
	@echo "make test       Run project tests"
	@echo "make vet        Run Go-specific vet checks"
	@echo "make lint       Run the configured linters"
	@echo "make check      Run Go checks and Python evaluator tests"
	@echo "make go-check   Run only Go checks"
	@echo "Go targets: go-tools go-fmt go-fmt-check go-build go-test go-vet go-lint"

# Repository entry points. Add client targets here when its TS toolchain exists.
tools: go-tools
fmt: go-fmt
fmt-check: go-fmt-check
build: go-build
test: go-test eval-test
vet: go-vet
lint: go-lint
check: go-check eval-test

go-tools: $(GOLANGCI_LINT)

$(GOLANGCI_LINT):
	@set -eu; \
	installer=$$(mktemp); \
	trap 'rm -f "$$installer"' EXIT HUP INT TERM; \
	curl -fsSL "https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh" -o "$$installer"; \
	sh "$$installer" -b "$(TOOLS_DIR)" "$(GOLANGCI_LINT_VERSION)"

go-fmt:
	gofmt -w $(GO_DIR)

go-fmt-check:
	@set -eu; \
	files=$$(gofmt -l $(GO_DIR)); \
	if [ -n "$$files" ]; then \
		printf 'Go formatting required:\n%s\nRun make go-fmt and review the changes.\n' "$$files"; \
		exit 1; \
	fi

go-build:
	cd $(GO_DIR) && go build ./...

go-test:
	cd $(GO_DIR) && go test ./...

go-vet:
	cd $(GO_DIR) && go vet ./...

go-lint: go-tools
	cd $(GO_DIR) && "$(GOLANGCI_LINT)" config verify --config .golangci.yml
	cd $(GO_DIR) && "$(GOLANGCI_LINT)" run --config .golangci.yml ./...

go-check: go-fmt-check go-build go-vet go-test go-lint

# Local browser console and API, backed by the editable fictional fixture.
debug:
	cd $(GO_DIR) && go run ./cmd/api -fixture ./testdata/demo/fixture.json $(DEBUG_ARGS)

# Keep Phoenix running in a separate terminal while using the debug console.
trace:
	bash deploy/phoenix/start.sh

# Evaluation entry points. Script CLIs own their defaults and validation.
eval:
	$(UV) run scripts/phoenix_experiment.py $(EVAL_ARGS)

eval-check:
	$(UV) run scripts/phoenix_experiment.py $(EVAL_ARGS) --check-only

eval-jsonl:
	$(UV) run scripts/ollama_evaluator.py $(EVAL_ARGS)

# Mock HTTP tests; no running Phoenix, API, or Ollama services are needed.
eval-test:
	$(UV) run --python '>=3.10' --with httpx==0.28.1 python -m unittest discover -s scripts -p 'test_*.py'
