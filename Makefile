GO      ?= go
PKGS    ?= ./...
GOLANGCI_LINT_VERSION ?= v2.12.2

.DEFAULT_GOAL := help

.PHONY: help check fmt fmt-check vet lint test test-full build tidy-check vuln docs-check gate-clean tools

help: ## Список целей
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

check: fmt-check vet lint test build tidy-check vuln docs-check ## Канонический gate: fmt-check+vet+lint+test+build+tidy+vuln+docs (перед тегом)

fmt: ## Форматирование (gofumpt через golangci-lint)
	golangci-lint fmt $(PKGS)

fmt-check: ## Проверка форматирования без записи
	golangci-lint fmt --diff $(PKGS)

vet: ## go vet
	$(GO) vet $(PKGS)

lint: ## golangci-lint (.golangci.yml)
	golangci-lint run $(PKGS)

test: ## Unit-тесты: -short -race (без контейнеров)
	$(GO) test -short -race -count=1 $(PKGS)

test-full: ## Все тесты, включая testcontainers (нужен Docker)
	$(GO) test -race -count=1 $(PKGS)

build: ## go build
	$(GO) build $(PKGS)

tidy-check: ## go.mod/go.sum синхронны с кодом
	$(GO) mod tidy -diff

vuln: ## govulncheck по всем пакетам
	govulncheck $(PKGS)

docs-check: ## envconfig-теги в коде описаны в README кита
	scripts/envdoc-check.sh

gate-clean: ## `make check` в чистом worktree HEAD (ловит gitignored-файлы)
	scripts/gate-clean.sh

tools: ## Установить golangci-lint и govulncheck
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@latest
