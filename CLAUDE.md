# golibinfra — гайд для агентов

Набор инфраструктурных Go-библиотек («кит'ов») для сервисов stit: observability,
auth, БД, messaging, кэш, аудит и т.д. Не сервис и не stack — модули, которые
импортирует бизнес-репо (`go get git.stit.tech/stit-core/golibinfra@vX.Y.Z`).

Этот файл — **ядро (always-on) + роутер**. Полная human-доку — `README.md`
(использование, observability, стабильность API, версионирование). Контекстные
правила — `.claude/rules/` (path-scoped). Процедуры — `.claude/skills/`. План —
`.claude/plans/roadmap.md`.

## Точка входа

- **Добавить новый kit** — skill `/add-kit` (scaffold пакета по конвенциям:
  `Module`/fx, observability-by-default, envconfig, README кита (+ `docs/` у
  сложного), строка-индекс в корневом README, testkit-сабпакет).
- **Выпустить версию** — skill `/release` (`make gate-clean` → аннотированный тег
  `vX.Y.Z` с release notes → push → гайд апдейта консьюмера).
- Точечная правка существующего kit'а — прямой Edit; scaffold не нужен.

## Команды (gate перед claim «готово»)

```bash
make check        # fmt-check + vet + lint + test -short -race + build + tidy-check + govulncheck + envdoc-check
make test-full    # + testcontainers (Postgres/NATS/Redis/MinIO), нужен Docker
make gate-clean   # make check в чистом worktree от HEAD — перед тегом
make tools        # golangci-lint + govulncheck
```

Зелёный `make check` = можно claim «готово». Перед тегом — `make gate-clean`:
он ловит файлы, которые есть локально, но не в git.

## Карта пакетов

Полный список — `README.md § Пакеты`. Назначение, конфиг и эмитимые сигналы
каждого кита — в его `README.md` рядом с кодом (у сложных китов ещё `docs/`);
это единый источник правды по киту. Кратко:

- **Фундамент:** `logkit` (slog + PII-redact + trace-id, JSON в stdout + OTel
  log-bridge), `telemetry` (opt-in OTel bootstrap: провайдеры + OTLP-экспортёры),
  `config` (композиция ENV-источников для App-конфига), `clockkit`, `timerkit`
  (periodic jobs), `flagkit`, `tzkit` (Zone/Day/WallTime), `idkit` (UUIDv7),
  `ptrkit`, `mapkit`, `bizmetric` (конвенция `biz_*`), `healthkit` (тип пробы
  для `/ready`).
- **Транспорт:** `httpkit` (huma/chi server + middleware), `httpclient` (retry +
  breaker на хост + SSE), `grpckit` (gRPC-сервер) + `grpcclient` (gRPC-клиент).
- **Данные и messaging:** `pgkit` (pgx pool + миграции + Transactor), `natskit`
  (NATS/JetStream), `outboxkit` (transactional outbox), `cachekit` (Redis +
  idempotency store), `storagekit` (S3/MinIO), `auditkit`, `bruteforcekit`.
- **Auth и интеграции:** `authnkit` (Argon2id/JWT/TOTP/RBAC), `oidckit` (OIDC BFF),
  `hubkit` (SDK хаба stit: sso/issuer/client/sync/projection), `anthropickit`
  (клиент Anthropic с `Usage`+`Cost`), `amocrmkit` (транспорт к amoCRM).
- **Тесты:** `testkit` (testcontainers + per-kit хелперы).
- **Служебное:** `internal/otelkit` (scope + `Must` для OTel-инструментов),
  `internal/requestid` (ctx-ключ request-id), `internal/jwks` (кэш JWKS с
  троттлингом). Общие helper'ы живут в `internal/`, наружу не экспортируются.

## Observability-инварианты

Киты инструментированы OTel и эмитят метрики/спаны через **глобальные** провайдеры
(`otelkit.Meter`/`otelkit.Tracer` поверх `otel.Meter`/`otel.Tracer`); без
зарегистрированного `MeterProvider`/`TracerProvider` инструменты — no-op, kit не
падает. Экспорт и бэкенд — `telemetry` или собственная проводка OTel SDK на
стороне потребителя. Подробно — `README.md § Observability`.

- Имена метрик — Prometheus-стиль `<kit_prefix>_<what>_<unit|total>`; label'ы —
  конечные множества (route-pattern, а не путь; хост, а не URL).
- **`biz_<domain>_<event>_total|_seconds|<gauge>`** — единственная конвенция имён
  business-метрик; enforce'ится `bizmetric.Counter/Histogram/Gauge` (panic при нарушении).
- Health-проба кита — `healthkit.Probe`; в `/ready` попадает через
  `healthkit.Provide(...)` (fx-группа `health`).

## Стиль (инварианты)

- **gofumpt + golangci-lint** (`.golangci.yml`).
- **fx-first.** Каждый kit отдаёт `Module`/`<Kit>Module` (`fx.Options`); консьюмер
  собирает `App` из них. Прямой `New*()` без fx допустим, но менее идиоматичен.
- **Config провайдит caller.** `Module` не читает ENV сам: `fx.Provide(<kit>.NewConfig)`
  или App-структура. `NewConfig` валидирует (`ErrConfig`) и падает на старте, а
  не на первом запросе.
- **envconfig-теги — часть public API.** Имена полей/тегов конфигов (`README.md §
  Стабильность API`) менять = breaking → major (`/vN` в module path).
- **Observability by default.** Новый kit эмитит метрики/спаны через `internal/otelkit`;
  resource attrs и экспорт настраивает потребитель.
- **Минимум зависимостей.** Перед новым import — проверь stdlib + существующие deps;
  каждый новый import обсуждается с пользователем. Одна библиотека на задачу
  (JWT — go-jose, Postgres — pgx).
- **Комментарии — только WHY** (rule `.claude/rules/comments.md`).

## Git workflow

- **Conventional Commits**: `feat|fix|refactor|docs|test|chore` + `<type>(<scope>)?: <subject>`;
  breaking — `!` и футер `BREAKING CHANGE:`.
- Линейная история; версия — тег `vX.Y.Z` (breaking — major, добавления — minor,
  фиксы — patch). Аннотация тега — release notes: что изменилось и как мигрировать.
- Коммит/пуш — гейт человека (без явной просьбы не коммитить).

## Границы

### Делать всегда
- Перед claim «готово» — `make check` зелёный; перед тегом — `make gate-clean`.
- Новый kit — через `/add-kit`; собственный `README.md` кита (+ `docs/` у сложного)
  и строка-индекс в `README.md § Пакеты` обязательны.
- Тронул конфиг / эмитимые метрики / labels / span-attrs — обнови `README.md`
  (+ `docs/`) соответствующего кита (§ Конфигурация / § Observability); `envdoc-check`
  сверяет env-переменные с доками.
- Каждый багфикс — с регрессионным тестом.

### Сначала спросить
- Менять конвенцию имён метрик (`bizmetric`, `biz_*`) — затрагивает все сервисы.
- Добавлять новую зависимость в `go.mod`.
- Удалять кит или менять его границы.

### Не делать никогда
- `:latest` в примерах и тестах. `fmt.Print*` / `slog.Default()` в production-коде —
  только `logkit.Logger`.
- Коммитить product-specific логику — golibinfra общая lib; доменное живёт в
  сервис-репо. Исключения по решению оператора — `hubkit` (SDK хаба) и `amocrmkit`;
  триггер выноса — в `roadmap.md § Backlog`.
- `http.DefaultClient` без таймаута; `context.Background()` в обработчике, где
  доступен контекст запроса.

## Forward-only

Любой текст в репо описывает **только текущее состояние** — не «было/стало», без дат
прошлого. История — в `git log` + Conventional Commits. Правило — `.claude/rules/forward-only.md`.
