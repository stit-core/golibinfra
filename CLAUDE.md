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

## Маршрут задачи

Маршрут — первая подходящая строка сверху.

| Задача | Маршрут | Пример |
|---|---|---|
| Упало или баг | регрессионный тест, воспроизводящий дефект → фикс, одним коммитом | красный `make check`, гонка в ките, дефолт не по доке |
| Больше одного коммита; breaking для потребителя (сигнатуры, дефолты, имена метрик, envconfig-теги); миграция кита; вход, сессии, токены, маски логов; фаза программы из хаба | `/plan` — план в `.claude/plans/` (бэклог — `backlog.md`), его шаги идут по этой же таблице | `Revoke` отдаёт владельца сессии; колонка в схеме outbox |
| Новый кит | `/add-kit` — scaffold по конвенциям: `Module`/fx, observability, envconfig, README кита, строка в корневом README, testkit-сабпакет | «нужен переиспользуемый кит для X» |
| Выпуск версии | `/release` — `make gate-clean` → `make release-diff` → аннотированный тег с release notes → push → гайд консьюмеру | «релизим vX.Y.Z» |
| Правка существующего кита в один коммит | напрямую; меняется поведение — сначала тест | опция конструктора, текст README кита |

## Команды (gate перед claim «готово»)

```bash
make check        # hooks-check + fmt-check + vet + lint + test-full + build + tidy-check + govulncheck + envdoc-check + plan-check
make test-full    # unit + integration (testcontainers: Postgres/NATS/Redis/MinIO) -race; без Docker — FAIL
make test         # быстрый цикл: unit -short -race, без контейнеров
make gate-clean   # make check в чистом worktree от HEAD — перед тегом
make release-diff PREV=vX.Y.Z  # что изменилось с тега: API (apidiff), env-теги и дефолты, имена метрик
make tools        # golangci-lint, govulncheck и apidiff пиненных версий
make hooks        # один раз: core.hooksPath=scripts/git-hooks — commit-msg и pre-push
make coverage     # покрытие -short + сводка; ещё есть bench, fuzz и test-timing (вне гейта)
```

Зелёный `make check` = можно claim «готово»: он гоняет и integration, поэтому нужны
Docker и сеть (govulncheck). Гейт всегда проверяет `./...` (последняя строка —
`✓ check clean (./...)`): `PKGS`, `GO` и `A` до шагов не доходят, а с `-i`/`-k`/`-n`/`-p`/`-q`/`-t`,
`--eval`, лишним makefile в обычных формах (`-f`, `MAKEFILES`), `GOFLAGS` (env или `go env -w`) и
подменой `MAKE`/`SHELL`/`.SHELLFLAGS` он не запускается. Makefile, написанный под подделку
(переприсваивает `MAKEFILE_LIST`), сверку внутри make обходит — это правка гейта (§ Границы);
вердикт для remote — pre-push. Кеш golangci-lint — в `.cache/` checkout'а. Без активных хуков
`make check` красный (`hooks-check`). Перед тегом — `make gate-clean`: тот же прогон, что pre-push
делает на пушимом коммите, — он ловит файлы, которые есть локально, но не в git, и гоняет гейт без
make-окружения вызывающего и с холодным кешем lint.

`fmt-check`, `lint` и `vuln` отказывают, если `golangci-lint` или `govulncheck` в PATH — не версии
пина `Makefile` (ставит `make tools`). `make lint` до прогона проверяет схему `.golangci.yml` и
сверяет набор включённых линтеров и форматтеров с эталоном `scripts/linters.txt`: линтер, снятый
из `enable`, — отказ.

## Карта пакетов

Полный список — `README.md § Пакеты`. Назначение, конфиг и эмитимые сигналы
каждого кита — в его `README.md` рядом с кодом (у сложных китов ещё `docs/`);
это единый источник правды по киту. Кратко:

- **Фундамент:** `logkit` (slog + PII-redact по умолчанию + trace-id, JSON/text в
  stdout + OTel log-bridge; события fx — `FxLogger`), `telemetry` (opt-in OTel
  bootstrap: провайдеры + OTLP-экспортёры; требует в графе `logkit.Config`), `config`
  (композиция ENV-источников для App-конфига), `clockkit` (`Now`/`Since`/`After`/
  `NewTimer` + фейки), `timerkit` (periodic jobs; регистрация — `timerkit.ProvideJob`,
  требует `timerkit.Module`; группа `timerkit.JobsGroup`),
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
- **Auth и интеграции:** `authnkit` (Argon2id/JWT/TOTP/одноразовые токены/RBAC, сессии refresh —
  `authnkit/session` + `session/pgstore`), `oidckit` (OIDC BFF),
  `hubkit` (SDK хаба stit: sso/issuer/client/sync/projection/hubclaims), `anthropickit`
  (клиент Anthropic с `Usage`+`Cost`), `amocrmkit` (транспорт к amoCRM).
- **Раскладка сервиса:** `archkit` (правила `internal/domain/<d>`, `internal/platform` и `pkg` одним
  вызовом из теста потребителя — `archkit.Check`; настройки и известные нарушения — его опции).
- **Тесты:** `testkit` (testcontainers, лесенка миграций `MigrationLadder`) + сабпакеты
  `testkit/<name>test`
  (`anthropictest`, `audittest`, `authntest`, `bruteforcetest`, `cachetest`, `grpctest`,
  `handlertest`, `hubtest`, `idempotencytest`, `logtest`, `metrictest`, `natstest`,
  `oidctest`, `outboxtest`, `pgtest`, `sessiontest`, `storagetest`); в самих китах — только исключения
  § Стиль «Одна форма API».
- **Служебное:** `internal/otelkit` (scope, версия инструментации, `Must`,
  две шкалы бакетов — `DurationBuckets` и `LongDurationBuckets`),
  `internal/envconf` (ридер ENV — `New*Config` китов и `config.Env`),
  `internal/requestid` (ctx-ключ request-id), `internal/clientip` (ctx-ключ IP клиента — httpkit
  и `handlertest`, свёртка IPv6 — у лимитера httpkit и bruteforcekit),
  `internal/jwks` (кэш JWKS с троттлингом), `internal/httpdoer` (общий `Doer`), `internal/redact`
  (URL без секретов — ошибки, логи и спаны natskit, cachekit, storagekit, telemetry, httpclient),
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
- Инструменты — в конструкторе кита (`otelkit.Meter`/`otelkit.Tracer` при сборке объекта), не
  на уровне пакета: пакетный инструмент делегат OTel привязывает к первому провайдеру процесса.
  `TestInstrumentsAreNotPackageLevel` (`internal/otelkit`) ловит его в коде инициализации пакета
  (`var`, `init()`, `sync.OnceValue`/`OnceValues`/`OnceFunc`) — напрямую и через функцию своего
  пакета (и generic), метод, значение функции пакета или замыкание, которое `init()` зовёт сам
  либо отдаёт функции пакета, зовущей свой аргумент; хелпер другого пакета, dot-import и
  присваивание пакетной переменной из функции держит ревью. Запись в тестах проверяет
  `testkit/metrictest` — кит через провайдер процесса (`Collect`), код с переданным `Meter` —
  через `NewProvider`; глобальный `MeterProvider` тест не ставит.
- Ключ ошибки в записи лога — `error` (так пишет и `FxLogger`): запрос по одному ключу видит все
  киты; `TestLogErrorKeyIsError` (`logkit`) ловит литерал `"err"` аргументом вызова, в `[]any` и
  полем составного литерала (`slog.Attr{Key: …}`); ключ из константы или переменной держит ревью.
- **`biz_<domain>_<event>_total|_seconds|<gauge>`** — единственная конвенция имён
  business-метрик; enforce'ится `bizmetric.Counter/Histogram/Gauge` (panic при нарушении).
- Гистограммы длительности — в секундах, `WithUnit("s")` + общие границы
  `otelkit.DurationBuckets()`; для операций, штатно идущих минутами, —
  `otelkit.LongDurationBuckets()` (сейчас только `anthropickit`). Своих
  bucket-границ киты не заводят.
- Health-проба кита — значение `Probe` (`healthkit.Probe{Name, Check}`; имя — как у `fx.Module`:
  `<kit>`, `<kit>/<sub>`, у инстанса — `<kit>:<instance>`); в `/ready` и в health-loop `grpckit`
  попадает через `healthkit.ProvideProbe(ctor)`, у инстанса — `<kit>.ProvideProbe(name)`
  (fx-группа `healthkit.ProbesGroup` = `healthkit.probes`).
- Имя fx-группы кита — `<kit>.<thing>`, константа — `<Thing>Group` (`healthkit.ProbesGroup`,
  `timerkit.JobsGroup`, `grpckit.ServicesGroup`, `pgkit.MigrationsGroup`,
  `httpkit.SecuritySchemeGroup`); имя `fx.Module` — `<kit>` или `<kit>/<sub>` (у
  параметризованных модулей — `<kit>:<instance>`).

## Стиль (инварианты)

- **gofumpt + golangci-lint** (`.golangci.yml`).
- **fx-first.** Кит с конфигом или ресурсом отдаёт `Module` (`fx.Option`; у хранилищ —
  в `pgstore`, дополнительные — `MigrateModule`, `FactoryModule`); консьюмер собирает
  `App` из них. Хелперы без состояния (`ptrkit`, `mapkit`, `idkit`, `bizmetric`, `archkit`) и
  `healthkit` (`ProvideProbe`) модуля не имеют. Прямой `New*()` без fx допустим, но менее
  идиоматичен.
- **Одна форма API.** Регистрация конструктора в fx-группу — `Provide<Thing>(ctor, anns…)`
  (проверяет тип результата), значения — `Supply<Thing>(v)`; набор, где важен порядок, —
  одно значение графа (`httpkit.Middlewares`, `httpclient.Options`), не группа. Конструктор —
  `(cfg, зависимости…, log, opts…)`, впереди — только `ctx` и имя инстанса; функция над готовым
  объектом берёт его первым, как приёмник (`RegisterHealth(api, …)`, `Probe(client, cfg)`);
  логгер — параметр, не опция. Test-хелперы — `testkit/<name>test`; в ките —
  только in-memory реализации двойного назначения (`cachekit.MemoryCache`,
  `storagekit.MemoryStorage`, `httpkit.MemoryStore`), фейки часов `clockkit` (часть контракта
  `Clock`) и маркер транзакции `pgkit.WithTestTx`. Исключения формы — названные: опции харнесса
  `handlertest` (логгер — `WithLogger`), `telemetry.WithErrorLogger(slog.Handler)` (handler ошибок
  SDK, а не логгер кита), `outboxkit/pgstore.NewStoreFor(db, table, …)`; киты вне объёма аудита
  (hubkit, oidckit, amocrmkit, anthropickit) правятся только под API.
- **Config провайдит caller.** `Module` не читает ENV сам: `fx.Provide(<kit>.NewConfig)`
  или App-структура. Каждый конфиг имеет `Validate() error` с sentinel'ом
  `<kit>.ErrConfig`; `NewConfig` и конструкторы (`New*`) его зовут, поэтому
  невалидный конфиг падает на старте, а не на первом запросе. Zero-value `Config`
  либо безопасен и полезен, либо отвергается `Validate` — «молча ломается» нельзя.
  `Validate` — только проверка, value-приёмник; дефолты — в тегах: ноль в литерале —
  значение из доки или `ErrConfig`, а не дефолт кита; формат проверяется и у выключенного
  (`Disabled`, `Enabled=false`); опция с nil ничего не меняет.
- **envconfig-теги — часть public API.** Имена полей/тегов конфигов (`README.md §
  Стабильность API`) менять = breaking: `!` в коммите + строка миграции в
  аннотации тега. Порядок тегов — `envconfig` первым, затем `default`. Конфиги китов и
  `config.Env` читает только `internal/envconf` (`KEY=` — дефолт, пустое значение — `-`,
  тег на вложенной структуре — ошибка), ошибку чтения
  `New*Config` оборачивает в `ErrConfig`; каждый конструктор — строка
  `internal/envconf/kits_test.go`.
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
  breaking — `!` и футер `BREAKING CHANGE:`. Формат держит хук `commit-msg` (`scripts/git-hooks`):
  subject ≤72 символов, `!` и футер — только вместе; сообщения самого git (`Revert "…"`,
  `fixup!`/`squash!`) проходят, merge-коммит — нет.
- Линейная история; версия — тег `vX.Y.Z`. Совместимость между minor-версиями
  `v1` не гарантируется: breaking идёт **minor**'ом с `!` в коммите и строкой
  миграции в аннотации тега; patch — только фиксы без смены сигнатур, дефолтов,
  имён метрик и envconfig-тегов; major — смена module path (`/vN`).
  Аннотация тега — release notes: `Breaking` по китам (что изменилось → как
  мигрировать), `Поведение`, `Изменения`, `Гейт`.
- Коммит/пуш — гейт человека (без явной просьбы не коммитить).
- Push проходит гейт: pre-push (`scripts/git-hooks`) гоняет весь `make check` на верхнем коммите
  каждого пушимого ref'а в свежем worktree (как `make gate-clean`): незакоммиченное в рабочем дереве
  на вердикт не влияет. Обход — только руками человека (`git push --no-verify`).

## Границы

### Делать всегда
- Перед claim «готово» — `make check` зелёный; перед тегом — `make gate-clean`
  (§ Команды).
- Новый kit — через `/add-kit`: без собственного `README.md` кита (+ `docs/` у сложного)
  и строки-индекса в `README.md § Пакеты` потребитель его не найдёт.
- Тронул конфиг / эмитимые метрики / labels / span-attrs — обнови `README.md`
  (+ `docs/`) соответствующего кита (§ Конфигурация / § Observability) и, если
  переменная — endpoint, строку в `README.md § Per-concern endpoints`:
  `make envdoc-check` требует строку таблицы с тем же дефолтом на каждую переменную
  кода, не пускает в доки переменные-призраки и поля конфига без тега; таблицы сигналов
  httpkit (`httpkit/docs/observability.md`) сверяет с живым `Module` `httpkit/signals_test.go`.
- Каждый багфикс — с регрессионным тестом; у `Module` кита — fx-тест
  (`fx.ValidateApp` с минимальным набором провайдеров); у каждого источника миграций
  кита (`pgkit.Source`) — лесенка `testkit.MigrationLadder` (`TestMigrations_Ladder`) и
  проверка схемы на старте (`pgkit.CheckSchemaOnStart`).

### Сначала спросить пользователя
- Смена конвенции имён метрик (`bizmetric`, `biz_*`) — на неё опираются все сервисы.
- Новая зависимость в `go.mod` — она уезжает в граф каждого потребителя.
- Удаление кита или смена его границ — breaking для потребителей.
- Правка гейта, после которой он проверяет меньше: `Makefile`, `scripts/`, `.golangci.yml`,
  `.claude/settings.json`, `.claude/hooks/` — снятый шаг, линтер или запрет виден только в диффе.

### Не делать
- `:latest` в примерах и тестах: тег плавает, и тест перестаёт быть воспроизводимым (в Go-коде
  ловит `TestImageTagsArePinned`, `testkit`).
- `fmt.Print*` / `slog.Default()` в production-коде: лог идёт только через
  `logkit.Logger`, иначе теряет redact, `trace_id`/`request_id` и OTel log-bridge. Ловит forbidigo
  — вместе с `print`/`println`, пакетом `log` и функциями `slog` с логгером по умолчанию; тесты и
  инструменты `scripts/` — вне запрета.
- Product-specific логика: golibinfra — общая lib, доменное живёт в сервис-репо.
  Исключения — `hubkit` (SDK хаба) и `amocrmkit`; их выносят в отдельные модули, когда
  появляется второй потребитель вне stit-core или конфликт версий зависимостей у потребителя.
- `http.DefaultClient` без таймаута и `context.Background()` в обработчике, где
  доступен контекст запроса: первый вешает вызов без предела, второй теряет отмену,
  дедлайн и трейс запроса. Первое ловит forbidigo (и `http.Get`/`Head`/`Post`/`PostForm`), второе
  держит ревью.
- Обходить хуки и гейт: `--no-verify`, `git commit -n`, смена `core.hooksPath`, порча хука —
  красный гейт чинят, а не обходят. Удобные формы из сессии Claude блокируют `permissions.deny`
  (`.claude/settings.json`) и хук `.claude/hooks/guard-bash.sh`: это барьер от случайного обхода, а
  не песочница (чего он не видит — `README.md § Гейт`); правка хуков инструментами Edit и Write —
  правка гейта (§ Сначала спросить).

## Forward-only

Любой текст в репо описывает **только текущее состояние** — не «было/стало», без дат
прошлого. История — в `git log` + Conventional Commits. Правило — `.claude/rules/forward-only.md`.
