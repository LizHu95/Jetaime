.DEFAULT_GOAL := help

GO_DIR := services/api
UV ?= uv
EVAL_ARGS ?=
ASSISTANT_ARGS ?=
GOLANGCI_LINT_VERSION := v2.14.0
TOOLS_DIR := $(CURDIR)/$(GO_DIR)/bin/golangci-lint/$(GOLANGCI_LINT_VERSION)
GOLANGCI_LINT := $(TOOLS_DIR)/golangci-lint

.PHONY: trace eval eval-check eval-services eval-test eval-smoke assistant assistant-check help tools fmt fmt-check build test vet lint check \
	go-tools go-fmt go-fmt-check go-build go-test go-vet go-lint go-check

help:
	@echo "make trace      Start local Phoenix at http://127.0.0.1:6006"
	@echo "make eval       Run V2 Phoenix experiment (EVAL_ARGS)"
	@echo "make eval-check Validate case schema offline"
	@echo "make eval-services Check Phoenix and installed Ollama models"
	@echo "make eval-test  Run isolated evaluator tests"
	@echo "make eval-smoke Verify protocol with local Phoenix and a test model"
	@echo "make assistant  Start debug console with imported chat history at http://127.0.0.1:8081"
	@echo "make assistant-check Verify real model, confirmation and persistence with imported history"
	@echo "make tools      Install development tools"
	@echo "make fmt        Format source files"
	@echo "make fmt-check  Check formatting without changing files"
	@echo "make build      Build the Go project"
	@echo "make test       Run Go tests"
	@echo "make vet        Run Go vet checks"
	@echo "make lint       Run the configured linters"
	@echo "make check      Run formatting, build, vet, tests and lint"

assistant:
	cd $(GO_DIR) && go run ./cmd/assistant $(ASSISTANT_ARGS)

assistant-check:
	python3 scripts/check-assistant.py

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

trace:
	bash deploy/phoenix/start.sh

eval:
	$(UV) run --project evals --locked python evals/runner.py $(EVAL_ARGS)

eval-check:
	$(UV) run --project evals --locked python evals/runner.py $(EVAL_ARGS) --check-only

eval-services:
	$(UV) run --project evals --locked python evals/runner.py $(EVAL_ARGS) --check-services

eval-test:
	$(UV) run --project evals --locked python -m unittest discover -s evals/tests -p 'test_*.py'

eval-smoke:
	$(UV) run --project evals --locked python evals/tests/smoke_phoenix.py $(EVAL_ARGS)
