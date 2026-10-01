GO      ?= go
PKGS    ?= ./...
GOLANGCI_LINT_VERSION ?= v2.12.2
GOVULNCHECK_VERSION   ?= v1.8.0
COVERPROFILE          ?= coverage.out
FUZZTIME              ?= 10s

# Кеш golangci-lint — свой у каждого checkout'а: ключ кеша — содержимое пакета, а замечание
# хранит путь checkout'а, где его нашли первым, поэтому общий кеш судит по чужим файлам.
override GOLANGCI_LINT_CACHE := $(CURDIR)/.cache/golangci-lint
export GOLANGCI_LINT_CACHE
# Патч из окружения оставил бы в отчёте lint только замечания по нему, cache-программа — чужой кеш.
unexport GOLANGCI_DIFF_PROCESSOR_PATCH GOLANGCI_LINT_CACHEPROG

.DEFAULT_GOAL := help

.PHONY: help check hooks-check plan-check fmt fmt-check vet lint test test-full test-timing build tidy-check vuln envdoc-check coverage bench fuzz gate-clean hooks tools

help: ## Список целей
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

# Шаги гейта — по очереди, каждый своим make: первый красный останавливает check. PKGS, GO и A
# шагам не доходят ни из окружения, ни из командной строки: гейт проверяет всё дерево.
override GATE_PKGS  := ./...
override GATE_STEPS := hooks-check fmt-check vet lint test-full build tidy-check vuln envdoc-check plan-check
override GATE       := $(MAKE) --no-print-directory PKGS=$(GATE_PKGS) GO=go A=

# check и gate-clean не запускаются там, где сказали бы «clean», не проверив всё: -i глушит
# ошибки шагов, -n/-q/-t их не выполняют, подмена MAKE, SHELL, .SHELLFLAGS и MAKEFILES меняет
# шаги, GOFLAGS (из env или go env -w) — go test, vet и govulncheck.
ifneq (,$(filter check gate-clean,$(MAKECMDGOALS)))
override gate_bad := $(foreach f,i n q t,$(if $(findstring $f,$(firstword -$(MAKEFLAGS))),-$f)) \
	$(foreach v,MAKE SHELL .SHELLFLAGS MAKEFILES,$(if $(filter command% environment%,$(origin $v)),$v=…)) \
	$(if $(shell go env GOFLAGS),GOFLAGS=$(shell go env GOFLAGS))
ifneq (,$(strip $(gate_bad)))
$(error make $(filter check gate-clean,$(MAKECMDGOALS)) не запускается с $(strip $(gate_bad)) — запусти без них)
endif
endif

check: ## Канонический gate: хуки, fmt-check, vet, lint, test-full, build, tidy, vuln, envdoc, планы (Docker и сеть)
	@set -e; for step in $(GATE_STEPS); do echo "→ $$step"; $(GATE) $$step; done
	@echo "✓ check clean ($(GATE_PKGS))"

# Пуш без гейта никто не видит, поэтому неактивный pre-push — красный check, а не предупреждение.
hooks-check: ## pre-push активен: git берёт хуки из scripts/git-hooks (иначе — make hooks)
	@active=$$(cd "$$(git rev-parse --git-path hooks)" 2>/dev/null && pwd -P); \
	if [ "$$active" != "$$(cd scripts/git-hooks && pwd -P)" ]; then \
		echo "FAIL: pre-push не активен — git берёт хуки из $$(git rev-parse --git-path hooks); запусти make hooks"; \
		exit 1; \
	fi; \
	if [ ! -x scripts/git-hooks/pre-push ]; then \
		echo "FAIL: scripts/git-hooks/pre-push не исполняемый — git его пропустит (chmod +x)"; \
		exit 1; \
	fi; \
	echo "OK: pre-push активен (scripts/git-hooks)"

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

envdoc-check: ## env-переменные кода ↔ строки таблиц доков (имя и дефолт), корневая env-таблица
	$(GO) run ./scripts/envdoc

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

gate-clean: ## `make check` в чистом worktree HEAD: без make-окружения вызывающего, с холодным кешем lint
	scripts/gate-clean.sh

hooks: ## Включить хуки scripts/git-hooks: pre-push гоняет make check на чистом HEAD
	git config core.hooksPath scripts/git-hooks

tools: ## Установить golangci-lint и govulncheck пиненных версий
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
