# golibinfra — гайд для агентов

Набор инфраструктурных Go-библиотек («кит'ов») для сервисов stit: observability,
auth, БД, messaging, кэш, аудит и т.д. Не сервис и не stack — модули, которые
импортирует бизнес-репо (`go get git.stit.tech/stit-core/golibinfra@vX.Y.Z`).

Этот файл — **ядро (always-on) + роутер**. Полная human-доку — `README.md`
(использование, observability, стабильность API, версионирование). Контекстные
правила — `.claude/rules/` (path-scoped). Процедуры — `.claude/skills/`. Планы —
`.claude/plans/` (skill `/plan`).

## Место в экосистеме

Экосистема stit — 7 шаблонов `stit-core/*`, которые форкаются в репо клиентской орги
`<org>/*`, и две библиотеки: эта и `pylibinfra` (Python-аналог с теми же китами).
golibinfra не форкается: `<org>/backend` (форк `go-backend-template`) и бэкенды
`ai-gateway-template` / `amocrm-gateway-template` импортируют модуль по тегу. Отсюда
уходит только тег (`/release`): обновление зависимости и миграцию по аннотации тега
потребитель делает своей обвязкой, выкатку образа — `<org>/infra` (`make bump`).

Сюда приходят прямым заходом — сопровождать библиотеку — или с задачей из сервис-репо:
generic-примитив, которого нет в ките, `go-backend-template` оформляет как PR в golibinfra.
Работа здесь идёт обвязкой этого репо, гейт — `make check`. Канон карты репо и
операционного флоу — `docs/reference/ecosystem.md` репо `project-infra-template-compose`
(в workspace — `../project-infra-template-compose/docs/reference/ecosystem.md`).

## Точка входа

- **Добавить новый kit** — skill `/add-kit` (scaffold пакета по конвенциям:
  `Module`/fx, observability-by-default, envconfig, README кита (+ `docs/` у
  сложного), строка-индекс в корневом README, testkit-сабпакет).
- **Выпустить версию** — skill `/release` (`make gate-clean` → аннотированный тег
  `vX.Y.Z` с release notes → push → гайд апдейта консьюмера).
- **Содержательное изменение** (breaking в ките, миграция, работа на несколько коммитов) —
  через план: skill `/plan` (`.claude/plans/<slug>.md`, бэклог — `.claude/plans/backlog.md`).
- Точечная правка существующего kit'а — прямой Edit; scaffold не нужен.

## Команды (gate перед claim «готово»)

```bash
make check        # fmt-check + vet + lint + test-full + build + tidy-check + govulncheck + envdoc-check + plan-check
make test-full    # unit + integration (testcontainers: Postgres/NATS/Redis/MinIO) -race; без Docker — FAIL
make test         # быстрый цикл: unit -short -race, без контейнеров
make gate-clean   # make check в чистом worktree от HEAD — перед тегом
make tools        # golangci-lint + govulncheck пиненных версий
make hooks        # core.hooksPath=.githooks: pre-push гоняет make check
make coverage     # покрытие -short + сводка; ещё есть bench, fuzz и test-timing (вне гейта)
```

Зелёный `make check` = можно claim «готово»: он гоняет и integration, поэтому нужны
Docker и сеть (govulncheck). Перед тегом — `make gate-clean`: он ловит файлы,
которые есть локально, но не в git.

## Карта пакетов

Полный список — `README.md § Пакеты`. Назначение, конфиг и эмитимые сигналы
каждого кита — в его `README.md` рядом с кодом (у сложных китов ещё `docs/`);
это единый источник правды по киту. Кратко:

- **Фундамент:** `logkit` (slog + PII-redact по умолчанию + trace-id, JSON/text в
  stdout + OTel log-bridge; события fx — `FxLogger`), `telemetry` (opt-in OTel
  bootstrap: провайдеры + OTLP-экспортёры; требует в графе `logkit.Config`), `config`
  (композиция ENV-источников для App-конфига), `clockkit` (`Now`/`Since`/`After`/
  `NewTimer` + фейки), `timerkit` (periodic jobs; регистрация — `timerkit.Provide`,
  требует `timerkit.Module`; группа `timerkit.Group`),
  `flagkit`, `tzkit` (Zone/Day/WallTime), `idkit` (UUIDv7 + `Validate`),
  `ptrkit`, `mapkit`, `bizmetric` (конвенция `biz_*`), `healthkit`
  (`Probe{Name, Check}` + `WithTimeout` для `/ready`).
- **Транспорт:** `httpkit` (huma/chi server + middleware + контракт ошибок
  `APIError`/`ErrorMap`), `httpclient` (retry +
  breaker на хост + redirect-политика + SSE), `grpckit` (gRPC-сервер) +
  `grpcclient` (gRPC-клиент).
- **Данные и messaging:** `pgkit` (pgx pool + миграции + Transactor), `natskit`
  (NATS/JetStream), `outboxkit` (transactional outbox), `cachekit` (Redis +
  idempotency store), `storagekit` (S3/MinIO), `auditkit`, `bruteforcekit`.
- **Auth и интеграции:** `authnkit` (Argon2id/JWT/TOTP/RBAC), `oidckit` (OIDC BFF),
  `hubkit` (SDK хаба stit: sso/issuer/client/sync/projection/hubclaims), `anthropickit`
  (клиент Anthropic с `Usage`+`Cost`), `amocrmkit` (транспорт к amoCRM).
- **Тесты:** `testkit` (testcontainers) + сабпакеты `testkit/<name>test`
  (`anthropictest`, `audittest`, `authntest`, `bruteforcetest`, `grpctest`,
  `handlertest`, `hubtest`, `logtest`, `natstest`, `oidctest`, `outboxtest`,
  `pgtest`, `storagetest`) — внутри китов test-хелперов нет.
- **Служебное:** `internal/otelkit` (scope, версия инструментации, `Must`,
  две шкалы бакетов — `DurationBuckets` и `LongDurationBuckets`),
  `internal/requestid` (ctx-ключ request-id),
  `internal/jwks` (кэш JWKS с троттлингом), `internal/httpdoer` (общий `Doer`),
  `internal/grpcstatus` (классификация gRPC-кодов), `internal/grpclogkit`
  (мост grpclog → logkit). Общие helper'ы живут в `internal/`, наружу не
  экспортируются.

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
- Гистограммы длительности — в секундах, `WithUnit("s")` + общие границы
  `otelkit.DurationBuckets()`; для операций, штатно идущих минутами, —
  `otelkit.LongDurationBuckets()` (сейчас только `anthropickit`). Своих
  bucket-границ киты не заводят.
- Health-проба кита — именованная `healthkit.Probe{Name, Check}`; в `/ready` и в
  health-loop `grpckit` попадает через `healthkit.Provide(...)` (fx-группа
  `healthkit.Group` = `healthkit.probes`).
- Имя fx-группы кита — `<kit>.<thing>` и всегда экспортируемая константа
  (`healthkit.Group`, `timerkit.Group`, `grpckit.ServicesGroup`,
  `pgkit.MigrationsGroup`, `httpkit.SecuritySchemeGroup`, …); имя `fx.Module` —
  `<kit>` или `<kit>/<sub>` (у параметризованных модулей — `<kit>:<instance>`).

## Стиль (инварианты)

- **gofumpt + golangci-lint** (`.golangci.yml`).
- **fx-first.** Кит с конфигом или ресурсом отдаёт `Module` (`fx.Option`; у хранилищ —
  в `pgstore`, дополнительные — `MigrateModule`, `FactoryModule`); консьюмер собирает
  `App` из них. Хелперы без состояния (`ptrkit`, `mapkit`, `idkit`, `bizmetric`) и
  `healthkit` (`Provide`) модуля не имеют. Прямой `New*()` без fx допустим, но менее
  идиоматичен.
- **Config провайдит caller.** `Module` не читает ENV сам: `fx.Provide(<kit>.NewConfig)`
  или App-структура. Каждый конфиг имеет `Validate() error` с sentinel'ом
  `<kit>.ErrConfig`; `NewConfig` и конструкторы (`New*`) его зовут, поэтому
  невалидный конфиг падает на старте, а не на первом запросе. Zero-value `Config`
  либо безопасен и полезен, либо отвергается `Validate` — «молча ломается» нельзя.
- **envconfig-теги — часть public API.** Имена полей/тегов конфигов (`README.md §
  Стабильность API`) менять = breaking: `!` в коммите + строка миграции в
  аннотации тега. Порядок тегов — `envconfig` первым, затем `default`.
- **Doc-comment на каждом экспорте** (Go-конвенция: одна строка, начинается с
  имени). `revive.exported` ловит типы, функции, var и const; **методы на
  экспортируемом приёмнике он пропускает** — там правило держит ревью. Package-doc
  — 1–3 строки: что это и какие сигналы эмитит, детали — в README кита.
- **Язык:** тексты ошибок и логов — английские, sentinel'ы
  `errors.New("<kit>: …")`, обёртки `fmt.Errorf("<kit>: …: %w", err)`; комментарии
  и doc-comment'ы — русские, как в окружающем коде.
- **Observability by default.** Новый kit эмитит метрики/спаны через `internal/otelkit`;
  resource attrs и экспорт настраивает потребитель.
- **Минимум зависимостей.** Перед новым import — проверь stdlib + существующие deps;
  каждый новый import обсуждается с пользователем. Одна библиотека на задачу
  (JWT — go-jose, Postgres — pgx).
- **Комментарии внутри функций — только WHY** (rule `.claude/rules/comments.md`);
  doc-comment на экспорте — обязателен и правилом не ограничен.

## Git workflow

- **Conventional Commits**: `feat|fix|refactor|docs|test|chore` + `<type>(<scope>)?: <subject>`;
  breaking — `!` и футер `BREAKING CHANGE:`.
- Линейная история; версия — тег `vX.Y.Z`. Совместимость между minor-версиями
  `v1` не гарантируется: breaking идёт **minor**'ом с `!` в коммите и строкой
  миграции в аннотации тега; patch — только фиксы без смены сигнатур, дефолтов,
  имён метрик и envconfig-тегов; major — смена module path (`/vN`).
  Аннотация тега — release notes: `Breaking` по китам (что изменилось → как
  мигрировать), `Поведение`, `Изменения`, `Гейт`.
- Коммит/пуш — гейт человека (без явной просьбы не коммитить).

## Границы

### Делать всегда
- Перед claim «готово» — `make check` зелёный; перед тегом — `make gate-clean`
  (§ Команды).
- Новый kit — через `/add-kit`: без собственного `README.md` кита (+ `docs/` у сложного)
  и строки-индекса в `README.md § Пакеты` потребитель его не найдёт.
- Тронул конфиг / эмитимые метрики / labels / span-attrs — обнови `README.md`
  (+ `docs/`) соответствующего кита (§ Конфигурация / § Observability) и, если
  переменная — endpoint, строку в `README.md § Per-concern endpoints`:
  `make envdoc-check` сверяет обе стороны и падает на расхождении.
- Каждый багфикс — с регрессионным тестом; у `Module` кита — fx-тест
  (`fx.ValidateApp` с минимальным набором провайдеров).

### Сначала спросить пользователя
- Смена конвенции имён метрик (`bizmetric`, `biz_*`) — на неё опираются все сервисы.
- Новая зависимость в `go.mod` — она уезжает в граф каждого потребителя.
- Удаление кита или смена его границ — breaking для потребителей.

### Не делать
- `:latest` в примерах и тестах: тег плавает, и тест перестаёт быть воспроизводимым.
- `fmt.Print*` / `slog.Default()` в production-коде: лог идёт только через
  `logkit.Logger`, иначе теряет redact, `trace_id`/`request_id` и OTel log-bridge.
- Product-specific логика: golibinfra — общая lib, доменное живёт в сервис-репо.
  Исключения — `hubkit` (SDK хаба) и `amocrmkit`; их выносят в отдельные модули, когда
  появляется второй потребитель вне stit-core или конфликт версий зависимостей у потребителя.
- `http.DefaultClient` без таймаута и `context.Background()` в обработчике, где
  доступен контекст запроса: первый вешает вызов без предела, второй теряет отмену,
  дедлайн и трейс запроса.

## Forward-only

Любой текст в репо описывает **только текущее состояние** — не «было/стало», без дат
прошлого. История — в `git log` + Conventional Commits. Правило — `.claude/rules/forward-only.md`.
