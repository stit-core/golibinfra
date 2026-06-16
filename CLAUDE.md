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
ещё `docs/`); это единый источник правды по киту. Кратко: `telemetry`
(OTel bootstrap), `logkit` (slog + PII-redact + trace-id), `httpkit` (huma/chi
server + middleware), `httpclient` (retry + circuit breaker), provider-киты `anthropickit` / `openaikit`
/ `googleaikit` (SDK) + `yandexaikit` / `sberaikit` (raw HTTP) — LLM-вызовы
(chat/embeddings/images/audio) с нормализованным `Usage`+`Cost` (USD у
anthropic/openai/google, RUB у yandex/sber), `grpckit` (gRPC-сервер) + `grpcclient` (gRPC-клиент),
`pgkit` (pgx pool +
otelpgx), `natskit` (NATS/JetStream), `outboxkit` (outbox + lag-reporter),
`authnkit` (JWT/Argon2id/TOTP/RBAC), `cachekit` (Redis + idempotency), `bizmetric`
(business-метрики), `auditkit`, `bruteforcekit`, `flagkit`, `clockkit`, `timerkit`,
`config` (ENV + Vault), `testkit` (testcontainers helpers).

## Инварианты, на которые завязан корп-стек

Полно — `README.md § Minimum viable observability`. Чек-лист:

1. **`service.name` / `service.version` / `deployment.environment.name`** — выставляет
   приложение через `telemetry.Config`. Без них дашборды Apps/* теряют `$service`/`$env`.
2. **`tenant` / `service.namespace`** — приложение **не** выставляет; их ставит корп
   `otel-collector` (managed — инстанс на `*.stit.tech`; self-hosted — ваш форк
   `corp-infra-template`). Выставишь сам — будет молча перезатёрто.
3. **`biz_<domain>_<event>_total|_seconds|<gauge>`** — единственная конвенция имён
   business-метрик; enforce'ится `bizmetric.Counter/Histogram/Gauge` (panic при нарушении).

Правка `telemetry/`, `logkit/`, `bizmetric/` или resource-attrs init-флоу — breaking
для всех сервисов; сверяйся с корп obs-контрактом (`corp-infra-template/docs/ecosystem.md`
§ Tenant identity / Observability resource-attrs) перед изменением инвариантов.

## Стиль (инварианты)

- **gofumpt + golangci-lint** (`.golangci.yml`).
- **fx-first.** Каждый kit отдаёт `Module`/`<Kit>Module` (`fx.Options`); консьюмер
  собирает `App` из них. Прямой `New*()` без fx допустим, но менее идиоматичен.
- **envconfig-теги — часть public API.** Имена полей/тегов конфигов (`README.md §
  Стабильность API`) менять = breaking → новый major.
- **Observability by default.** Новый kit эмитит метрики/спаны через otel-go SDK,
  читая resource attrs из `telemetry.Config` (не хардкодить).
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
- Менять `telemetry/`/`bizmetric/`/resource-attrs — затрагивает все сервисы (корп obs-контракт).
- Добавлять новую зависимость в `go.mod`.

### Не делать никогда
- Выставлять `tenant`/`service.namespace` дефолтом в `telemetry.Config`.
- `:latest` в примерах. `fmt.Print*` / `slog.Default()` в production-коде — только `logkit.Logger`.
- Коммитить product-specific логику — golibinfra общая lib; доменное живёт в сервис-репо.

## Forward-only

Любой текст в репо описывает **только текущее состояние** — не «было/стало», без дат
прошлого. История — в `git log` + Conventional Commits. Правило — `.claude/rules/forward-only.md`.
