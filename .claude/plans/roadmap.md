# Roadmap

Единственный артефакт планирования — этот файл. Подпапок, `<slug>.md`, архивов и
changelog'ов нет: они расходятся с правдой и мешают.

**Planning-first.** Изменение идёт через план — `### <slug>`-секцию под `## Active` ниже.
Прямая правка без плана допустима только для truly-atomic (опечатка, один очевидный файл) и
багфиксов (RED-first). Отклонение от плана — осознанное, проговаривается с юзером.

**Жизненный цикл.** `planning → in-progress → blocked? → done`. По `done` секция удаляется
целиком — история остаётся в `git log`. Незакрытый follow-up переезжает в бэклог или новой
`### `-секцией, а не хвостом в удаляемой.

**Где это запускается.** Репо — библиотека, живого стека нет: гейт локальный — `make check`
(fmt, vet, lint, test -short -race, build, tidy, govulncheck, envdoc-check), перед тегом —
`make gate-clean` (то же в чистом worktree); `make hooks` вешает `make check` на pre-push.
Выпуск версии — тег `vX.Y.Z` (skill `/release`), консьюмеры подтягивают `go get` сами.

## Active

Пусто — активных планов нет.

## Backlog

- **Миграция потребителей на `v1.x`.** Источник правды по шагам — аннотация тега
  (`git tag -l --format='%(contents)' vX.Y.Z`), здесь — только крупные шаги по китам.
  Шаблоны стоят до `v1.0.0`: `go-backend-template` — `v0.55.0`, `ai-gateway-template`
  и `amocrm-gateway-template` — `v0.39.0`. Путь у них цепочкой из трёх аннотаций:
  `v0.39.0 → v0.55.0` (единственный breaking на отрезке — `feat(natskit)!: инстансы
  шины первого класса`), `v0.55.0 → v1.0.0` (в т.ч. `pgkit.MigrateUpMulti`/
  `MultiSourceConfig` → `Source` + `MigrateUpAll` и `RENAME schema_migrations` →
  `schema_migrations_app`), затем `v1.0.0 → v1.x` — список ниже.
  - logkit: `NewWithWriter(cfg, w)` → `New(cfg, WithWriter(w))`, `Config.Stdout` →
    `Config.Silent` (`LOG_STDOUT=false` → `LOG_SILENT=true`), `RegisterAttr(key, fn)`,
    в `Logger` появился `Enabled` (свои фейки дополнить).
  - fx-группы: `group:"health"` → `healthkit.Provide(...)` (`healthkit.probes`),
    `group:"timer.job"` → `timerkit.Provide(...)` (`timerkit.jobs`),
    `group:"grpc.services"` → `grpckit.services`.
  - pgkit: `DBProbe` → `Probe`, метрики `db_pool_*` → `pg_pool_*`,
    `MigrateRunnerConfig.Timeout` обязателен, драйвер миграций — `pgx/v5`.
  - outboxkit: `Repository.Insert` с `json.RawMessage`, `MarkPublished`/`RecordFailures`
    с `MessageRef`, инстансы через `pgstore.FactoryModule` + `outboxkit.Instance`
    (вместо ручного `outboxkit.New` и `RegisterBacklogGauges`), дефолт
    `OUTBOX_REQUIRE_TX=true`, миграция `0002` (`next_attempt_at`) для кастомных
    таблиц, Postgres ≥ 14 при retention.
  - natskit: `fx.Provide(natskit.NewConfig)` рядом с `natskit.Module("")`,
    `Bus.Publish` принимает `[]byte`, label `subject` на publish-метриках — opt-in.
  - testkit: `testkit/txkit` → `testkit/pgtest`, `testkit/eventbus` → `testkit/outboxtest`,
    `grpckit/testkit/grpctest` → `testkit/grpctest`,
    `anthropickit/testkit/anthropictest` → `testkit/anthropictest`.
  - small kits: `ptrkit.Ptr` → `ptrkit.OmitZero`, `mapkit.IntPtr` → `*int`,
    `config.Env("p")` → `config.EnvPrefixed("p")`, `flagkit.NewEnvStore` возвращает
    ошибку и `Store` требует `Names()`.
  - amocrmkit: `Tokens` → `TokenPair`, OAuth-методы принимают `Account`+`OAuthConfig`,
    id сущностей — `int64`, `TokenSource` удалён.
  - httpkit/oidckit/authnkit: `NewServer(cfg, opts...)`, `RegisterHealth(..., timeout, probes...)`,
    `HumaAuth(log, extract)`, поле access-лога `dur` → `duration_ms`;
    `oidckit.CookieConfig.Secure` → `Insecure` (`COOKIE_SECURE` → `COOKIE_INSECURE`
    — только у тех, кто берёт конфиг кита, а не держит свой тег с тем же именем);
    `Parse` → `ParseAccess`/`ParseRefresh`, метрики `authn_*`.
  - ENV в `.env.example` и `test/stack/compose.yml`: `LOG_STDOUT` → `LOG_SILENT`,
    `BRUTEFORCE_WINDOW` и `AMOCRM_TIMEOUT` удалить; `OTEL_DISABLED` →
    `OTEL_SDK_DISABLED` — у тех, кто подключил `telemetry`.
- **CI на Gitea Actions**, когда появится раннер: `make check` + `make test-full`.
  До раннера гейт держат `.githooks/pre-push` (`make hooks`).
- **Вынос `hubkit` (SDK хаба) и `amocrmkit`** в отдельные модули; триггер — второй
  потребитель вне stit-core или конфликт версий зависимостей у потребителя.
- **`hubkit/projection`: продуктовое решение не принято.** Шаблон схемы расходится со
  всеми четырьмя продуктами: `hub_employees.department` недостижим из фида, нет
  `hub_departments`/`hub_tags`/`hub_entity_tags`, FK `hub_projects.company_id` ломается
  при независимых тиках. Варианты — канонизировать продуктовую схему
  (`department_ids UUID[]` + `hub_departments`) и снять FK, либо перенести шаблон в
  шаблон продукта. Факты — `hubkit/docs/architecture.md § Projection`.
- **Каноническое имя customer-схемы OpenAPI** у `hubkit/sso`: имя security-схемы
  настраивается (`HUB_SSO_SECURITY_SCHEME`, дефолт `EmployeeSession`), но общего для
  экосистемы имени схемы заказчика нет — решение за оператором, иначе клиенты
  продуктов разъедутся.
- **`Idempotency-Key` у хаба.** Хаб его не поддерживает, поэтому `CreateCompany` и
  `CreateProject` в `hubkit/client` не ретраятся (зафиксировано в
  `hubkit/docs/architecture.md § Retry-safety`). Появится поддержка — включить retry.
- **Kommo (`kommo.com`) в `amocrmkit`.** Allowlist доменов — `amocrm.ru`/`amocrm.com`;
  поддержка Kommo требует отдельного решения (другой домен, TLD-специфика лимитов).
- **Ревизия имён метрик.** Инвариант: `<kit_prefix>_<what>_<unit|total>` без исключений.
  Триггер пересмотра — появление кита, чей естественный префикс не совпадает с именем
  пакета.
