# golibinfra — гайд для агентов

Набор инфраструктурных Go-библиотек («кит'ов») для сервисов stit: observability,
auth, БД, messaging, кэш, аудит и т.д. Не сервис и не stack — модули, которые
импортирует бизнес-репо (`go get git.stit.tech/stit-core/golibinfra@vX.Y.Z`).

Этот файл — **ядро (always-on) + роутер**. Полная human-доку — `README.md`
(использование, observability, стабильность API, версионирование). Контекстные
правила — `.claude/rules/` (path-scoped). Процедуры — `.claude/skills/`.

## Точка входа

- **Добавить новый kit** — skill `/add-kit` (scaffold пакета по конвенциям:
  `Module`/fx, observability-by-default, envconfig, README кита (+ `docs/` у
  сложного), строка-индекс в корневом README, testkit-сабпакет).
- **Выпустить версию** — skill `/release` (`git tag vX.Y.Z` + push + гайд апдейта консьюмера).
- Точечная правка существующего kit'а — прямой Edit; scaffold не нужен.

## Команды (gate перед claim «готово»)

Makefile'а нет — команды сырые `go`, как в CI (`.gitea/workflows/ci.yml`):

```bash
golangci-lint run ./...          # lint (gofumpt + .golangci.yml)
go test -short -race ./...        # unit (-race при CGO_ENABLED=1)
go build ./...                    # build
```

Зелёные все три = можно claim «готово».

## Карта пакетов

Полный список — `README.md § Пакеты` (тонкий индекс). Назначение, конфиг и
эмитимые сигналы каждого кита — в его `README.md` рядом с кодом (у сложных китов
ещё `docs/`); это единый источник правды по киту. Кратко:
`telemetry` (opt-in OTel bootstrap) · `logkit` (slog + PII-redact + trace-id, JSON в stdout), `httpkit` (huma/chi
server + middleware), `httpclient` (retry + circuit breaker), provider-киты `anthropickit` / `openaikit`
/ `googleaikit` (SDK) + `yandexaikit` / `sberaikit` (raw HTTP) — LLM-вызовы
(chat/embeddings/images/audio) с нормализованным `Usage`+`Cost` (USD у
anthropic/openai/google, RUB у yandex/sber), `grpckit` (gRPC-сервер) + `grpcclient` (gRPC-клиент),
`pgkit` (pgx pool +
otelpgx), `natskit` (NATS/JetStream), `outboxkit` (transactional outbox),
`authnkit` (JWT/Argon2id/TOTP/RBAC), `oidckit` (OIDC BFF: RP-verify + login-роуты +
cookie-сессия), `cachekit` (Redis + idempotency), `bizmetric`
(business-метрики), `auditkit`, `bruteforcekit`, `flagkit`, `clockkit`, `timerkit`,
`config` (ENV-loader), `testkit` (testcontainers helpers).

## Observability-инварианты

Киты инструментированы OTel и эмитят метрики/спаны через **глобальные** провайдеры
(`otel.Meter`/`otel.Tracer`); без зарегистрированного `MeterProvider`/`TracerProvider`
инструменты — no-op, kit не падает. Экспорт и бэкенд — проводка OTel SDK на стороне
потребителя. Подробно — `README.md § Observability`.

- **`biz_<domain>_<event>_total|_seconds|<gauge>`** — единственная конвенция имён
  business-метрик; enforce'ится `bizmetric.Counter/Histogram/Gauge` (panic при нарушении).

## Стиль (инварианты)

- **gofumpt + golangci-lint** (`.golangci.yml`).
- **fx-first.** Каждый kit отдаёт `Module`/`<Kit>Module` (`fx.Options`); консьюмер
  собирает `App` из них. Прямой `New*()` без fx допустим, но менее идиоматичен.
- **envconfig-теги — часть public API.** Имена полей/тегов конфигов (`README.md §
  Стабильность API`) менять = breaking → новый major.
- **Observability by default.** Новый kit эмитит метрики/спаны через otel-go SDK
  (глобальные `otel.Meter`/`otel.Tracer`, no-op без провайдера); resource attrs и
  экспорт настраивает потребитель.
- **Минимум зависимостей.** Перед новым import — проверь stdlib + существующие deps;
  каждый новый import обсуждается с пользователем.
- **Комментарии — только WHY** (rule `.claude/rules/comments.md`).

## Git workflow

- **Conventional Commits**: `feat|fix|refactor|docs|test|chore` + `<type>(<scope>)?: <subject>`.
- Линейная история; версия — тег `vX.Y.Z` (pre-v1: minor может быть breaking).
- Коммит/пуш — гейт человека (без явной просьбы не коммитить).

## Границы

### Делать всегда
- Перед claim «готово» — `golangci-lint run` + `go test -short -race` + `go build` зелёные.
- Новый kit — через `/add-kit`; собственный `README.md` кита (+ `docs/` у сложного)
  и строка-индекс в `README.md § Пакеты` обязательны.
- Тронул конфиг / эмитимые метрики / labels / span-attrs — обнови `README.md`
  (+ `docs/`) соответствующего кита (§ Конфигурация / § Observability).

### Сначала спросить
- Менять public конфиг-структуры (имена полей, envconfig-теги, типы) — breaking, нужен major.
- Менять конвенцию имён метрик (`bizmetric`, `biz_*`) — затрагивает все сервисы.
- Добавлять новую зависимость в `go.mod`.

### Не делать никогда
- `:latest` в примерах. `fmt.Print*` / `slog.Default()` в production-коде — только `logkit.Logger`.
- Коммитить product-specific логику — golibinfra общая lib; доменное живёт в сервис-репо.

## Forward-only

Любой текст в репо описывает **только текущее состояние** — не «было/стало», без дат
прошлого. История — в `git log` + Conventional Commits. Правило — `.claude/rules/forward-only.md`.
