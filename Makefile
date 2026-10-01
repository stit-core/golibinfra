GO      ?= go
PKGS    ?= ./...
GOLANGCI_LINT_VERSION ?= v2.12.2
GOVULNCHECK_VERSION   ?= v1.8.0
COVERPROFILE          ?= coverage.out
FUZZTIME              ?= 10s

.DEFAULT_GOAL := help

.PHONY: help check plan-check fmt fmt-check vet lint test test-full test-timing build tidy-check vuln envdoc-check coverage bench fuzz gate-clean hooks tools

help: ## Список целей
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

# Шаги гейта — по очереди, каждый своим make: первый красный останавливает check.
GATE_STEPS := fmt-check vet lint test-full build tidy-check vuln envdoc-check plan-check

check: ## Канонический gate: fmt-check, vet, lint, test-full, build, tidy, vuln, envdoc, планы (Docker и сеть)
	@set -e; for step in $(GATE_STEPS); do echo "→ $$step"; $(MAKE) --no-print-directory $$step; done
	@echo "✓ check clean ($(PKGS))"

fmt: ## Форматирование (gofumpt через golangci-lint)
	golangci-lint fmt $(PKGS)

fmt-check: ## Проверка форматирования без записи
	golangci-lint fmt --diff $(PKGS)

vet: ## go vet
	$(GO) vet $(PKGS)

lint: ## golangci-lint (.golangci.yml)
	golangci-lint run $(PKGS)

test: ## Быстрый цикл: unit-тесты -short -race, без контейнеров (не гейт)
	$(GO) test -short -race -count=1 $(PKGS)

test-full: ## Unit + integration (testcontainers) -race; без Docker integration-тест падает
	$(GO) test -race -count=1 $(PKGS)

test-timing: ## Timing-тест logkit (цена маскирования): под нагрузкой флакает, поэтому вне гейта
	GOLIBINFRA_TIMING=1 $(GO) test -race -count=1 ./logkit/

build: ## go build
	$(GO) build $(PKGS)

tidy-check: ## go.mod/go.sum синхронны с кодом
	$(GO) mod tidy -diff

vuln: ## govulncheck по всем пакетам
	govulncheck $(PKGS)

plan-check: ## Формат планов .claude/plans/ (A=--summary — сводка активных планов)
	bash scripts/validate-plan.sh $(A)

envdoc-check: ## envconfig-теги ↔ доки китов и корневая env-таблица
	scripts/envdoc-check.sh

coverage: ## Покрытие -short + сводка по пакетам
	$(GO) test -short -count=1 -coverprofile=$(COVERPROFILE) $(PKGS)
	$(GO) tool cover -func=$(COVERPROFILE) | tail -1
	@echo "детали: go tool cover -html=$(COVERPROFILE)"

bench: ## Бенчмарки без тестов
	$(GO) test -run '^$$' -bench . -benchmem $(PKGS)

fuzz: ## Короткий прогон каждого Fuzz-таргета (FUZZTIME=10s)
	@set -e; \
	for pkg in $$(grep -rl '^func Fuzz' --include='*_test.go' . | xargs -n1 dirname | sort -u); do \
		for fn in $$(grep -hoE '^func (Fuzz[A-Za-z0-9_]*)' $$pkg/*_test.go | awk '{print $$2}'); do \
			echo "== $$pkg $$fn ($(FUZZTIME))"; \
			$(GO) test -run '^$$' -fuzz "^$$fn$$" -fuzztime $(FUZZTIME) ./$$pkg; \
		done; \
	done

gate-clean: ## `make check` в чистом worktree HEAD (ловит gitignored-файлы)
	scripts/gate-clean.sh

hooks: ## Включить .githooks (pre-push = make check)
	git config core.hooksPath .githooks

tools: ## Установить golangci-lint и govulncheck пиненных версий
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
