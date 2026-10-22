.DEFAULT_GOAL := help

.PHONY: db-migrate db-test memory-backfill
.PHONY: web web-build web-check deploy

DEPLOY_CONFIG ?= local/deploy.mk
-include $(DEPLOY_CONFIG)

DEPLOY_SSH ?= jetaime-server
DEPLOY_DEST ?= $(DEPLOY_SSH):~/
DEPLOY_WEB_DIR ?= /var/www/jetaime

deploy:
	bash deploy/push.sh "$(DEPLOY_DEST)" "$(DEPLOY_WEB_DIR)"

web:
	cd apps/web && npm run dev

web-build:
	cd apps/web && npm run build

web-check:
	cd apps/web && npm test && npm run test:e2e && npm run build

GO_DIR := services/api
UV ?= uv
EVAL_ARGS ?=
FEISHU_ARGS ?=
API_ARGS ?=
ASSISTANT_ARGS ?=
GOLANGCI_LINT_VERSION := v2.14.0
TOOLS_DIR := $(CURDIR)/$(GO_DIR)/bin/golangci-lint/$(GOLANGCI_LINT_VERSION)
GOLANGCI_LINT := $(TOOLS_DIR)/golangci-lint

.PHONY: dev api-build feishu feishu-build model-check eval eval-check eval-services eval-test assistant assistant-check help tools fmt fmt-check build test vet lint check \
	go-tools go-fmt go-fmt-check go-build go-test go-vet go-lint go-check

help:
	@echo "make deploy     Build, upload and restart API; deploy Web (local/deploy.mk, DEPLOY_SSH, DEPLOY_DEST, DEPLOY_WEB_DIR)"
	@echo "make web        Start responsive Web client at http://127.0.0.1:5173/page/auth/login/"
	@echo "make web-build  Build Web client into outputs/web"
	@echo "make web-check  Run Web unit/browser tests and build"
	@echo "make db-migrate Apply PostgreSQL migrations using JETAIME_DATABASE_URL"
	@echo "make db-test    Run database integration tests using JETAIME_TEST_DATABASE_URL"
	@echo "make memory-backfill Resume pending historical memory processing and exit (API also processes automatically)"
	@echo "make dev        Start HTTP API with authentication (API_ARGS; PostgreSQL required)"
	@echo "make api-build  Package unified HTTP API into outputs/api"
	@echo "make feishu     Start Feishu with local environment"
	@echo "make model-check Validate default model configuration without inference"
	@echo "make feishu-build Build the private Feishu deployment package"
	@echo "make eval       Run local V2 evaluation (EVAL_ARGS)"
	@echo "make eval-check Validate case schema offline"
	@echo "make eval-services Check model configuration and installed Ollama models"
	@echo "make eval-test  Run isolated evaluator tests"
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

dev:
	python3 scripts/dev.py api $(API_ARGS)

api-build:
	bash deploy/api/build.sh

db-migrate:
	cd $(GO_DIR) && go run ./cmd/api -migrate

db-test:
	@test -n "$$JETAIME_TEST_DATABASE_URL" || { echo "Set JETAIME_TEST_DATABASE_URL to a test database" >&2; exit 1; }
	cd $(GO_DIR) && go test -race -count=1 ./internal/database ./internal/ai/memory

memory-backfill:
	python3 scripts/dev.py api -memory-backfill -memory-config ../../local/memory.json $(API_ARGS)

assistant:
	python3 scripts/dev.py assistant $(ASSISTANT_ARGS)

feishu:
	python3 scripts/dev.py feishu $(FEISHU_ARGS)

feishu-build:
	python3 scripts/dev.py feishu-build

model-check:
	python3 scripts/dev.py model-check $(ASSISTANT_ARGS)

assistant-check:
	python3 scripts/dev.py assistant-check

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

eval:
	JETAIME_UV="$(UV)" python3 scripts/dev.py eval $(EVAL_ARGS)

eval-check:
	JETAIME_UV="$(UV)" python3 scripts/dev.py eval-check $(EVAL_ARGS)

eval-services:
	JETAIME_UV="$(UV)" python3 scripts/dev.py eval-services $(EVAL_ARGS)

eval-test:
	$(UV) run --project evals --locked python -m unittest discover -s evals/tests -p 'test_*.py'
