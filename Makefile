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
FEISHU_ARGS ?=
API_ARGS ?=
API_BUILD_ARGS ?=
MODEL_CHECK_ARGS ?=
SQLC_VERSION := v1.30.0
SQLC_DIR := $(CURDIR)/$(GO_DIR)/bin/sqlc/$(SQLC_VERSION)
SQLC := $(SQLC_DIR)/sqlc

GOLANGCI_LINT_VERSION := v2.14.0
TOOLS_DIR := $(CURDIR)/$(GO_DIR)/bin/golangci-lint/$(GOLANGCI_LINT_VERSION)
GOLANGCI_LINT := $(TOOLS_DIR)/golangci-lint

.PHONY: dev api-build feishu feishu-build model-check scripts-test help tools fmt fmt-check build test vet lint check \
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
	@echo "make eval-replay-check Validate isolated snapshot replay without model requests"
	@echo "make eval-replay Run local snapshot evaluation (EVAL_ARGS)"
	@echo "make feishu-build Build the private Feishu deployment package"
	@echo "make sqlc-generate Generate typed Go queries from SQL"
	@echo "make sqlc-check Check that generated queries match SQL"
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
	bash deploy/api/build.sh $(API_BUILD_ARGS)

db-migrate:
	cd $(GO_DIR) && go run ./cmd/api -migrate

db-test:
	@test -n "$$JETAIME_TEST_DATABASE_URL" || { echo "Set JETAIME_TEST_DATABASE_URL to a test database" >&2; exit 1; }
	cd $(GO_DIR) && go test -race -count=1 ./internal/database ./internal/ai/memory

memory-backfill:
	python3 scripts/dev.py api -memory-backfill -memory-config ../../local/memory.json $(API_ARGS)

feishu:
	python3 scripts/dev.py feishu $(FEISHU_ARGS)

feishu-build:
	python3 scripts/dev.py feishu-build

model-check:
	python3 scripts/dev.py model-check $(MODEL_CHECK_ARGS)

tools: go-tools
fmt: go-fmt
fmt-check: go-fmt-check
build: go-build
test: go-test scripts-test
vet: go-vet
lint: go-lint
check: go-check scripts-test

go-tools: $(GOLANGCI_LINT) $(SQLC)

.PHONY: sqlc-generate sqlc-check

$(SQLC):
	GOBIN="$(SQLC_DIR)" go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

sqlc-generate: $(SQLC)
	cd $(GO_DIR) && "$(SQLC)" generate

sqlc-check: $(SQLC)
	cd $(GO_DIR) && "$(SQLC)" diff

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

go-check: sqlc-check go-fmt-check go-build go-vet go-test go-lint

scripts-test:
	python3 -m unittest discover -s scripts/tests -p 'test_*.py'

# Isolated snapshot replay; never starts business API/workers.
EVAL_ARGS ?=
.PHONY: eval-replay eval-replay-build eval-replay-check

eval-replay:
	python3 scripts/eval_replay.py run -- $(EVAL_ARGS)

eval-replay-build:
	python3 scripts/eval_replay.py build

eval-replay-check:
	cd $(GO_DIR) && go test -race ./internal/ai/replay ./cmd/eval-replay
	python3 scripts/eval_replay.py run -- -snapshot evals/replay/messages.example.jsonl -cases evals/replay/cases.example.json -dry-run -output -
