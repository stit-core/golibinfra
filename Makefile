GO      ?= go
PKGS    ?= ./...
COVERPROFILE          ?= coverage.out
FUZZTIME              ?= 10s

# Версии тулов — константы: окружение и командная строка их не меняют. Чужая версия молча меняет
# вердикт, поэтому шаг сверяет с пином бинарь из PATH — по его build info, не запуская.
override GOLANGCI_LINT_VERSION := v2.12.2
override GOVULNCHECK_VERSION   := v1.8.0
override APIDIFF_VERSION       := v0.0.0-20260908205506-85c1c2202aba
override pinned = @go version -m "$$(command -v $(1))" 2>/dev/null | \
	awk -v mod=$(2) -v ver=$(3) '$$1 == "mod" && $$2 == mod && $$3 == ver { ok = 1 } END { exit !ok }' || \
	{ echo "FAIL: $(1) ($$(command -v $(1) || echo 'нет в PATH')) — не $(3): пиненную версию ставит make tools"; exit 1; }
override golangci_pinned = $(call pinned,golangci-lint,github.com/golangci/golangci-lint/v2,$(GOLANGCI_LINT_VERSION))

# Кеш golangci-lint — свой у каждого checkout'а: ключ кеша — содержимое пакета, а замечание
# хранит путь checkout'а, где его нашли первым, поэтому общий кеш судит по чужим файлам.
override GOLANGCI_LINT_CACHE := $(CURDIR)/.cache/golangci-lint
export GOLANGCI_LINT_CACHE
# Патч из окружения оставил бы в отчёте lint только замечания по нему, cache-программа — чужой кеш.
unexport GOLANGCI_DIFF_PROCESSOR_PATCH GOLANGCI_LINT_CACHEPROG
# Окружение оболочки вызывающего до шагов не доходит: файл автозапуска bash (`exit 0` в нём — и шаг
# «прошёл»), опции (SHELLOPTS=noexec — рецепт читается, но не исполняется) и экспортированные
# функции с именами команд шага. Скрипты шагов и хуки стартуют `bash -p` и сами их не читают;
# снятие отсюда закрывает рецепты там, где sh — это bash, и bash, запущенный шагами.
unexport BASH_ENV SHELLOPTS BASHOPTS $(filter BASH_FUNC_%,$(.VARIABLES))

.DEFAULT_GOAL := help

.PHONY: help check hooks-check plan-check fmt fmt-check vet lint test test-full test-timing build tidy-check vuln envdoc-check coverage bench fuzz gate-clean release-diff hooks tools

help: ## Список целей
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

# Шаги гейта — по очереди, каждый своим make: первый красный останавливает check. PKGS, GO и A
# шагам не доходят ни из окружения, ни из командной строки: гейт проверяет всё дерево.
override GATE_PKGS  := ./...
override GATE_STEPS := hooks-check fmt-check vet lint test-full build tidy-check vuln envdoc-check plan-check
override GATE       := $(MAKE) --no-print-directory PKGS=$(GATE_PKGS) GO=go A=

# Цели и список makefile'ов задаёт сам make. MAKECMDGOALS из командной строки, окружения или
# makefile'а, прочитанного раньше, выключил бы отказы ниже (они включаются по цели), MAKEFILE_LIST
# из командной строки — сверку makefile'ов: с такими переменными Makefile не читается ни для
# какой цели.
override gate_forged := $(if $(filter-out default undefined,$(origin MAKECMDGOALS)),MAKECMDGOALS) \
	$(if $(filter command% environment% override,$(origin MAKEFILE_LIST)),MAKEFILE_LIST)
ifneq (,$(strip $(gate_forged)))
$(error make не запускается с переменной $(strip $(gate_forged)) — её задаёт сам make)
endif

# check и gate-clean не запускаются там, где сказали бы «clean», не проверив всё: -i и -k глушат
# ошибки шагов, -n/-q/-t их не выполняют, -p печатает рецепты, подмена MAKE, SHELL, .SHELLFLAGS и
# MAKEFILES (и --eval до чтения Makefile) меняет шаги, makefile, прочитанный раньше этого, подменяет
# рецепт, GOFLAGS (из env или go env -w) — go test, vet и govulncheck. Отказ — дважды: при чтении
# Makefile по цели (с -n, -q и -t рецепт не исполняется) и первой строкой рецепта — когда прочитаны
# все makefile'ы и какой бы целью до рецепта ни дошли (--eval 'x: check').
override gate_bad = $(strip $(foreach f,i k n p q t,$(if $(findstring $f,$(firstword -$(MAKEFLAGS))),-$f)) \
	$(if $(filter --eval=%,$(MAKEFLAGS)),--eval) \
	$(filter-out $(abspath Makefile),$(abspath $(MAKEFILE_LIST))) \
	$(foreach v,MAKE SHELL .SHELLFLAGS MAKEFILES,$(if $(filter command% environment%,$(origin $v)),$v=…)) \
	$(if $(shell go env GOFLAGS),GOFLAGS=$(shell go env GOFLAGS)))
ifneq (,$(filter check gate-clean,$(MAKECMDGOALS)))
ifneq (,$(gate_bad))
$(error make $(filter check gate-clean,$(MAKECMDGOALS)) не запускается с $(gate_bad) — запусти без них)
endif
endif

# Makefile, прочитанный после этого (make -f Makefile -f extra.mk), подменил бы рецепт. Рецепты check
# и gate-clean — правила «::»: чужое «check:» — ошибка разбора, чужое «check::» идёт после этого, а
# его первая строка повторяет отказы, когда прочитаны все makefile'ы, — до первого шага и при любых
# флагах make. Makefile, написанный под подделку (переприсваивает MAKEFILE_LIST), обходит любую
# сверку внутри make — это правка гейта; вердикт для remote — pre-push.
override gate_refuse = $(if $(gate_bad),$(error make $@ не запускается с $(gate_bad) — запусти без них))

check:: ## Канонический gate: хуки, fmt-check, vet, lint, test-full, build, tidy, vuln, envdoc, планы (Docker и сеть)
	$(gate_refuse)
	@set -e; for step in $(GATE_STEPS); do echo "→ $$step"; $(GATE) $$step; done
	@echo "✓ check clean ($(GATE_PKGS))"

# Коммит и пуш без хуков никто не видит, поэтому неактивный хук — красный check, а не предупреждение.
override HOOKS := commit-msg pre-push
hooks-check: ## Хуки git активны: путь хуков — scripts/git-hooks, файлы исполняемые (иначе — make hooks)
	@active=$$(cd "$$(git rev-parse --git-path hooks)" 2>/dev/null && pwd -P); \
	if [ "$$active" != "$$(cd scripts/git-hooks && pwd -P)" ]; then \
		echo "FAIL: хуки не активны — git берёт их из $$(git rev-parse --git-path hooks); запусти make hooks"; \
		exit 1; \
	fi; \
	for hook in $(HOOKS); do \
		if [ ! -x scripts/git-hooks/$$hook ]; then \
			echo "FAIL: scripts/git-hooks/$$hook не исполняемый — git его пропустит; запусти make hooks"; \
			exit 1; \
		fi; \
	done; \
	echo "OK: хуки активны (scripts/git-hooks: $(HOOKS))"

fmt: ## Форматирование (gofumpt через golangci-lint)
	$(golangci_pinned)
	golangci-lint fmt $(PKGS)

fmt-check: ## Проверка форматирования без записи
	$(golangci_pinned)
	golangci-lint fmt --diff $(PKGS)

vet: ## go vet
	$(GO) vet $(PKGS)

lint: ## golangci-lint (.golangci.yml): схема конфига, набор линтеров по эталону scripts/linters.txt, прогон
	$(golangci_pinned)
	scripts/lint.sh $(PKGS)

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
	$(call pinned,govulncheck,golang.org/x/vuln,$(GOVULNCHECK_VERSION))
	govulncheck $(PKGS)

plan-check: ## Формат планов .claude/plans/ (A=--summary — сводка активных планов)
	scripts/validate-plan.sh $(A)

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

# Реализация одна — хук: gate-clean отдаёт ему HEAD так же, как git отдаёт пушимый коммит.
gate-clean:: ## `make check` HEAD в свежем worktree, как pre-push на пушимом коммите: без make-окружения вызывающего, с холодным кешем lint
	$(gate_refuse)
	@echo "HEAD $$(git rev-parse HEAD) - -" | scripts/git-hooks/pre-push

release-diff: ## release-diff PREV=vX.Y.Z — что изменилось для потребителя с тега: API (apidiff), env-теги с дефолтами, имена метрик
	$(call pinned,apidiff,golang.org/x/exp,$(APIDIFF_VERSION))
	@scripts/release-diff.sh "$(PREV)"

hooks: ## Включить хуки scripts/git-hooks (commit-msg, pre-push): путь хуков git и право на запуск
	git config core.hooksPath scripts/git-hooks
	chmod +x $(addprefix scripts/git-hooks/,$(HOOKS))

tools: ## Установить golangci-lint, govulncheck и apidiff пиненных версий
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(GO) install golang.org/x/exp/cmd/apidiff@$(APIDIFF_VERSION)
