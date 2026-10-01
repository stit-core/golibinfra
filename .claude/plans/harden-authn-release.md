---
status: in-progress
parent: go-backend-template:audit-program
blocked-by: —
---

# harden-authn-release — партия 2, часть 2: сессии и токены authnkit, аудит и метрики, high и C, релиз v1.3.0

## Цель
Вторая половина партии 2. Пункты шаблона B4–B8: аудит из ctx,
одноразовые токены и сессии refresh в authnkit, `testkit/metrictest`, `config` без ctx. Затем high
базового аудита (natskit, httpclient) и кандидаты C: остановка httpkit с `/ready` 503, iss/aud и
ротация JWT, атрибуты logkit без slog. Итог — аннотированный тег `v1.3.0`, на который шаблон
переходит в `fix-reference-code` S9 без обходов.

## Контекст
- **Порядок.** В `main` уже гейт с integration и лесенкой миграций,
  B1–B3, ключ IP клиента в `internal/`; его Handoff переносится сюда при закрытии. Программа, метод
  и push — `go-backend-template/.claude/plans/audit-program.md`; партии — `M/replan-golibinfra.md`
  §6; находки — `M/kits-audit/summary.md` и отчёты углов (`M` — `/home/mm/.cache/stit-audit-program/`).
- **Потребитель** — шаблон, `fix-reference-code` S9 и его Handoff:
  - сессии на `authnkit/session`: ротация, повтор → отзыв цепочки, `CanLogin` в Refresh, абсолютный
    срок, отзыв всех сессий при блокировке, удалении, смене роли и пароля — в одной транзакции со
    сменой; `seed-admin` не отдаёт права чужим сессиям;
  - снять `pkg/tokens`, client-IP middleware, `publicOperations`, ручные IP и RequestID аудита;
  - S11 — бизнес-метрики с проверкой записи; S2 — `App` читает envconfig напрямую, потому что
    `config.Load` требует ctx.

  Рефреш шаблона сейчас — JWT и `refresh_sessions` с check-then-act
  (`internal/domain/auth/usecase.go:160-222`).
- **Код китов:**
  - authnkit: `authnkit/jwt.go` — stateless refresh (`SignRefresh`/`ParseRefresh`,
    `JWT_REFRESH_TTL`); `authnkit/module.go:26` — `NewHS256JWT` без iss/aud, хотя опции
    `WithIssuer`/`WithAudience` есть;
  - аудит и ctx: `auditkit/audit.go:47-50`, `internal/requestid`;
  - natskit: `natskit/jetstream.go:545-599`, `natskit/config.go:50-52`;
  - httpclient: `httpclient/client.go:163`;
  - остановка и пробы: `httpkit/module.go:153-157`, `httpkit/health.go:77-94`,
    `healthkit/healthkit.go:55`;
  - прочее: `logkit/attrs.go:11`, `config/loader.go`, `config/README.md:74`.
- **Правила шага:** README и `docs/` кита — тем же коммитом (`make envdoc-check`); breaking — `!` и
  `BREAKING CHANGE:`. Новая зависимость в `go.mod` не нужна ни одному шагу.

## Не входит
- Партия 3 — `backlog.md`, триггер «партия 3». Туда же кандидаты C, которые аудит понизил до minor:
  предел длины пароля Argon2id и retention `audit_log`.
- Правки шаблона и шлюзов — по аннотации тега.
- Push `main` и тега — делает дирижёр после PASS валидатора партии.

## Критерии приёмки
- [x] **AC1** — Инструменты китов пишут в текущий глобальный `MeterProvider`, в том числе в
  тестовый. `testkit/metrictest` проверяет запись метрики (имя, тип, unit, labels, значение) и
  отсутствие коллизий имён после трансляции в Prometheus. Проверка:
  `go test -race ./testkit/metrictest/` → PASS; `M/kits-audit/fx-observability/run.sh <rev> obs-provider`
  → KO09 PASS; `M/kits-audit/httpkit-b/run.sh <rev> exp` → HO06 PASS
  - Подтверждено: metrictest PASS (пара KOBS-01 — коллизия); KO09, HO06 → PASS на S1 и S2
- [x] **AC2** — Запись аудита без `RequestID` или `IP` получает их из ctx запроса, явные значения не
  перетираются; `audittest` ведёт себя так же. Проверка:
  `go test -race ./auditkit/... ./testkit/audittest/` → PASS
  - Подтверждено: PASS — `TestEntry_Enrich`, pgstore с Docker, `audittest` за middleware httpkit
- [x] **AC3** — authnkit выдаёт одноразовый токен: 256 бит из `crypto/rand`, hex. Для хранения —
  SHA-256 токена. logkit маскирует такой токен по умолчанию. Проверка:
  `go test -race -run Token ./authnkit/ ./logkit/` → PASS
  - Подтверждено: PASS; токен в base64url (мутант) — `TestRedact_OneTimeTokenOfAuthnkit` красный
- [ ] **AC4** — `authnkit/session` управляет сессиями refresh:
  - вход выдаёт сессию;
  - обновление ротирует токен атомарно: из N параллельных обновлений одним токеном успешно одно;
  - повтор использованного токена отзывает всю цепочку;
  - `RevokeAll(subject)` в транзакции из ctx откатывается вместе с ней;
  - абсолютный срок не продлевается ротацией;
  - без миграций — ошибка старта с подсказкой; схема проходит лесенку;
  - pgstore и фейк проходят один контрактный набор;
  - stateless refresh JWT в ките больше нет.

  Проверка: `go test -race ./authnkit/... ./testkit/sessiontest/` с Docker → PASS;
  `grep -rn "SignRefresh\|ParseRefresh\|JWT_REFRESH_TTL" --include=*.go --include=*.md .` → пусто
- [ ] **AC5** — `config.Load` и `config.Source` работают без ctx; в доках нет
  `fx.Provide(context.Background)`. Проверка: `go test -race ./config/` → PASS;
  `grep -rn "context" config/` → пусто
- [ ] **AC6** — Сообщение JetStream переживает 15-секундный сбой зависимости обработчика при
  дефолтах. `ErrTerminal` уходит в dead сразу. ERROR `jetstream message dead` несёт `stream_seq`.
  Бюджет ретраев по умолчанию назван в доке. Проверка:
  `M/kits-audit/correctness-security/run.sh <rev> nats` → E15 PASS
- [ ] **AC7** — Метрики otelhttp у клиента не эмитятся; имена метрик объёма после трансляции в
  Prometheus без коллизий. Проверка: `fx-observability/run.sh <rev> obs-unit obs` → KO01 PASS,
  `promname` → 0 коллизий
- [ ] **AC8** — Остановка httpkit:
  - `/ready` отвечает 503, пока listener ещё открыт;
  - долгий запрос или SSE не съедают бюджет fx: после дедлайна HTTP ctx хендлеров отменён,
    соединения закрыты, остальные OnStop выполняются;
  - panic пробы — проба `down` и ERROR со стеком, процесс жив.

  Проверка: `httpkit-b/run.sh <rev> exp` → HX02 a–c, HX03, HO07 PASS;
  `M/kits-audit/httpkit-a/run.sh <rev> lifecycle` → H31, H32, H33 PASS
- [ ] **AC9** — Если `authnkit.Module` получил iss/aud из конфига, токен с чужими iss/aud
  отвергается. Токен, подписанный предыдущим секретом (`JWT_PREVIOUS_SECRET`), принимается; новые
  подписываются текущим. Проверка: `correctness-security/run.sh <rev> unit` → E06 PASS;
  `go test -race ./authnkit/` → PASS
- [ ] **AC10** — Атрибут лога регистрируется без `slog` в сигнатуре потребителя. Проверка:
  `go doc ./logkit Attr` → без `slog`; `go test -race ./logkit/` → PASS
- [ ] **AC11** — Тег `v1.3.0` готов:
  - тег аннотированный, с разделами Breaking, Поведение, Изменения, Гейт;
  - каждая строка apidiff `v1.2.0 → v1.3.0` по объёму и каждое изменение envconfig-тегов — в
    аннотации с шагом миграции;
  - каждая ошибка сборки шаблона на v1.3.0 (`replace`) закрыта строкой Breaking;
  - `make gate-clean` и `make test-full` зелёные.

  Проверка: `git tag -l --format='%(contents)' v1.3.0` → четыре раздела;
  `M/kits-audit/api-docs/scripts/run-versions.sh v1.2.0 v1.3.0` → всё покрыто;
  `make gate-clean` → зелёный

## Шаги
### Данные, authn и тестовые хелперы: B4–B8
- [x] **S1** — Инструменты метрик китов создаются от текущего глобального провайдера (KOBS-10):
  logkit, timerkit, auditkit и bruteforcekit (pgstore), storagekit/minio, httpkit
  - Проверка: AC1 (KO09, HO06) → PASS · Закрывает: AC1 · Зависит от: —
  - Сделано: инструменты — в конструкторах (и спан timerkit); KO09, HO06 → PASS;
    `TestInstrumentsAreNotPackageLevel` ловит `var` с `otelkit`/`otel` Meter и Tracer
- [x] **S2** — B7: `testkit/metrictest` — один провайдер на процесс, дельты, проверка коллизий имён.
  Обвязка метрик в тестах китов переведена на него (KTST-06)
  - Проверка: AC1 → выполнен; `make check` → зелёный · Закрывает: AC1 · Зависит от: S1
  - Сделано: `Collect`/`Sub`/`Get`/`Find`/`Value`/`All`, `Collisions`, `PromName`; на нём 17 из
    20 файлов обвязки (кроме `internal/otelkit`, `telemetry`) и рецепты доков; `make check` → зелёный
- [x] **S3** — B4: auditkit берёт `RequestID` и `IP` из ctx, если в записи пусто; `audittest` —
  так же
  - Проверка: AC2 → выполнен · Закрывает: AC2 · Зависит от: —
  - Сделано: `auditkit.Entry.Enrich` — его зовут `pgstore.Store.Log` и `audittest.Recorder.Log`
- [x] **S4** — B5: одноразовые токены в authnkit — генерация и хеш
  - Проверка: AC3 → выполнен · Закрывает: AC3 · Зависит от: —
  - Сделано: `authnkit.NewToken() string` и `HashToken(token) string`, рецепт погашения в доке
- [ ] **S5** — B6: `authnkit/session` и `authnkit/session/pgstore`:
  - выдача, ротация, отзыв цепочки, `RevokeAll`, `Revoke`, абсолютный срок;
  - kit-owned миграция, проверка схемы на старте, очистка через timerkit, метрики;
  - stateless refresh JWT удалён.
  - Проверка: `go test -race ./authnkit/...` с Docker → PASS · Закрывает: AC4 · Зависит от: S2, S4
- [ ] **S6** — B6: фейк `testkit/sessiontest` и общий контрактный набор; набор гоняется по фейку и по
  pgstore
  - Проверка: AC4 → выполнен · Закрывает: AC4 · Зависит от: S5
- [ ] **S7** — B8: `config.Load` и `Source.Apply` без ctx, README без ctx в графе (KAPI-10)
  - Проверка: AC5 → выполнен · Закрывает: AC5 · Зависит от: —

### High базового аудита и кандидаты C
- [ ] **S8** — natskit: бюджет ретраев JetStream переживает типичный сбой (KCOR-01); `stream_seq`
  в dead; в доке — правило «обработчик возвращает ошибку, natskit логирует её сам» (C)
  - Проверка: AC6 → выполнен · Закрывает: AC6 · Зависит от: —
- [ ] **S9** — httpclient: метрики otelhttp выключены, README и тест без коллизий имён (KOBS-01)
  - Проверка: AC7 → выполнен · Закрывает: AC7 · Зависит от: S2
- [ ] **S10** — httpkit, остановка: фаза дренажа с `/ready` 503 (C), дедлайн HTTP из остатка бюджета
  с отменой ctx и `Close` (KFX-08 вместе с KCOR-08), `recover` в пробах `/ready` и
  `healthkit.WithTimeout` (KOBS-14)
  - Проверка: AC8 → выполнен · Закрывает: AC8 · Зависит от: —
- [ ] **S11** — authnkit: iss и aud из конфига `Module`, ротация секрета через предыдущий секрет (C)
  - Проверка: AC9 → выполнен · Закрывает: AC9 · Зависит от: S5
- [ ] **S12** — logkit: `Attr` без slog в сигнатуре (C)
  - Проверка: AC10 → выполнен · Закрывает: AC10 · Зависит от: —

### Релиз
- [ ] **S13** — Релиз:
  - корневые README и CLAUDE.md сверены с API;
  - `make test-full` и `make gate-clean`;
  - apidiff и env-diff против v1.2.0;
  - сборка шаблона на HEAD через `replace` в своём worktree: каждая ошибка сборки — строкой
    Breaking;
  - тег `v1.3.0` локально.

  Push `main` и тега — дирижёр после PASS валидатора партии; затем план удаляется отдельным
  `chore(plan)`.
  - Проверка: AC11 → выполнен · Закрывает: AC11 · Зависит от: S3, S6, S7, S8, S9, S10, S11, S12

## Решения
- **Порядок.** metrictest (S1–S2) — первым, раньше B4–B6: им проверяют метрики сессий (S5) и
  отсутствие коллизий httpclient (S9). Остальное — по replan §6: B4–B8, затем high, затем C.
- **Инструменты метрик** — в конструкторах, от текущего `otel.GetMeterProvider()` через
  `otelkit.Meter`, а не на уровне пакета; трейсер timerkit — так же (та же ловушка у
  `TracerProvider`). Регресс держит статический тест `internal/otelkit`: поведенческий тест
  второго провайдера в пакете противоречит одному провайдеру metrictest.
- **metrictest.** Провайдер ставит импорт пакета (`init`): измерения до первого `Collect` не
  теряются; подменённый провайдер роняет `Collect`. `Get` — ровно эти labels, `Find`/`Value` —
  фильтр; `Sub` оставляет новую серию и с нулём, gauge — текущим значением. `PromName` сверен с
  otlptranslator v1.0.0 на 1292 кейсах вне репо. Переведены и киты вне объёма (один способ);
  свой провайдер — только у `internal/otelkit` и `telemetry`: их тесты проверяют саму связку с
  SDK. Инъекция метра в тестах (`newInstrumentsWithMeter`, `newObserveHookWith`) удалена. Глобальный делегат OTel привязывает пакетный инструмент к
  первому провайдеру процесса: второй его не видит (KO09, HO06). metrictest ставит один провайдер на
  процесс и сравнивает дельты: `otel.SetMeterProvider` глобален, параллельные тесты иначе мешают
  друг другу. Проверка коллизий — своя трансляция имён OTel → Prometheus (подмножество
  `UnderscoreEscapingWithSuffixes`), закреплённая на паре KOBS-01, без новой зависимости.
- **Аудит.** Заполнение — функцией ядра auditkit, её зовут и pgstore, и `audittest`. Ключ IP — из
  `internal/` (он же у httpkit), auditkit не импортирует httpkit. Функция — метод
  `Entry.Enrich(ctx)`: своя реализация `Logger` зовёт его так же, до `Validate`.
- **Токены.** Hex: маска hex ≥ 32 в logkit ловит его по умолчанию, base64url — нет (бэклог
  `logkit-redact-gaps`). SHA-256, а не Argon2id: у токена полная энтропия. Поиск — по хешу в SQL
  владельца таблицы, сравнение в памяти — `subtle`. `NewToken` без ошибки: `crypto/rand.Read`
  с Go 1.24 её не возвращает.
- **Сессии, устройство.**
  - Раскладка: `authnkit/session` — API, конфиг, метрики; `authnkit/session/pgstore` — хранилище,
    миграции, `Module`, очистка; `testkit/sessiontest` — фейк и набор `Run(t, newStore)`, по
    образцу `storagetest`.
  - Refresh — непрозрачный токен из S4 в цепочке с абсолютным сроком. Ротация — условный
    `UPDATE … WHERE used_at IS NULL AND revoked_at IS NULL … RETURNING` и вставка нового токена в
    одной транзакции: из параллельных побеждает один.
  - Повтор использованного токена — отзыв цепочки и `ErrSessionReused`, без окна прощения: клиент
    обновляет последовательно.
- **Сессии, транзакции и API.**
  - `RevokeAll(ctx, subject)` и `Revoke(ctx, token)` идут в транзакции из ctx (pgkit).
  - Схема проверяется на старте — образец для KFX-02 партии 3.
  - Метрики `authn_session_*{result}` — с конечными labels.
  - Конструктор без fx — для CLI (`seed-admin`).
  - Дефолты — срок токена, продлеваемый ротацией, и абсолютный срок не больше прежних 720 ч — в
    `configuration.md`.
  - Stateless refresh (`SignRefresh`/`ParseRefresh`/`JWT_REFRESH_TTL`) удалён: одна механика
    refresh. Его брали только шаблон и тест `oidckit`.
- **config.** `Load(dst, sources...)` и `Source.Apply(dst)` без ctx. Удалённый источник сам
  ограничивает свой I/O. ctx в графе даёт любому конструктору вечный контекст (FX-10).
- **natskit.**
  - Задержка NAK растёт с `NumDelivered` до потолка. Дефолты покрывают не меньше 15 минут:
    failover Postgres, выкатка соседа (E15: сообщение терялось через ≈10 с).
  - Term — только `ErrTerminal` и исчерпанный бюджет.
  - Правило канона: обработчик возвращает ошибку и не логирует её — natskit пишет ERROR с
    `trace_id` и subject (KO20).
- **httpclient.** Метрики otelhttp выключаются через noop `MeterProvider` у транспорта; спаны попытки
  остаются. Длительность попытки видна в спане, повторы считает `http_client_retry_attempts_total`.
  Переименовать метрику кита — breaking и хуже.
- **Остановка httpkit.**
  - Порядок: флаг остановки → `/ready` 503 → пауза `HTTP_SHUTDOWN_DRAIN_DELAY` → `Shutdown` с
    дедлайном `min(HTTP_SHUTDOWN_TIMEOUT, остаток stop-ctx − запас)` → на дедлайне отмена ctx
    хендлеров, `Close` и WARN с числом соединений.
  - `HTTP_SHUTDOWN_TIMEOUT` по умолчанию меньше дефолтного `fx.StopTimeout` (15 с). Пауза по
    умолчанию 0 — совместимо; за балансировщиком 5–10 с, сумма — в доке.
- **JWT.** `JWT_ISSUER` и `JWT_AUDIENCE` из конфига; пустые — не пишутся и не проверяются. Одному
  сервису на своём секрете они не нужны, общий секрет без `aud` небезопасен — это сказано в доке.
  Ротация: подпись текущим секретом, проверка — текущим, затем `JWT_PREVIOUS_SECRET`; `kid` не
  нужен, два HMAC дёшевы.
- **logkit.** `type Attr func(ctx) (any, bool)`, кит оборачивает значение в `slog.AnyValue`.
- **Релиз.** Раздел «Гейт» называет `make gate-clean` (с integration), `make test-full`, apidiff и
  env-diff против v1.2.0 и сборку шаблона через `replace`.

## Handoff
**Шаблон, `fix-reference-code` S9 — что снимается и меняется на bump v1.3.0:**
- **Токены.** `pkg/tokens` → генерация и хеш из authnkit. Погашение — своим условным `UPDATE`.
- **Сессии.** `refresh_sessions`, репозиторий сессий и адаптеры `SignRefresh`/`ParseRefresh` →
  `authnkit/session`:
  - в граф — `session/pgstore.Module`, его миграции — источник `cmd/migrate`;
  - `refresh_sessions` удаляется новой миграцией с `-- infra-handoff:`; при выкатке все входят
    заново один раз;
  - `RevokeAll` — в транзакции блокировки, удаления, смены роли, смены и сброса пароля, в
    `seed-admin`;
  - `CanLogin` проверяется после `Rotate`; `ErrSessionReused` → аудит `refresh_reused`.
- **IP.** `authgate.ClientIP` и `identity.WithIP` → `httpkit.ClientIPFrom(ctx)`; за Traefik —
  `HTTP_TRUST_PROXY_HOPS=1`.
- **Публичные операции.** `publicOperations` и `TestHTTPOperations` → `httpkit.Public(op)` в
  `Register` входа, refresh, logout, регистрации, сброса и подтверждения. Пропуски и дубли — ошибка
  старта кита.
- **Аудит.** Ручные `IP` и `RequestID` в `auditkit.Entry` → убрать.
- **Handler-тесты.** `handlertest.NewAPI` закрыт по умолчанию → `handlertest.WithAuth`; аноним → 401.
- **Логи.** `userIDAttr` → `func(ctx) (any, bool)`, без `slog` и `//nolint:forbidigo`.
- **Конфиг.** `App` (Cookie) можно читать через `config.Load(&cfg, config.Env())` без ctx.
- **OpenAPI.** `openapi.json` перегенерировать: `$schema` снят; `HTTP_DOCS_ENABLED=true` — только в
  dev.
- **Метрики.** Бизнес-метрики (S11) проверяются через `testkit/metrictest`.
- **Тесты.** Лесенка `cmd/migrate/migrate_test.go` может перейти на хелпер testkit (иначе —
  `repair-harness` S7: `testkit.MigrationLadder(t, testkit.WithMigrations(sources.Kits...), …)`).
  `scripts/tools/test.sh:21-22` — testkit без Docker вне `-short` — FAIL, обход
  `TestDockerRequired` (`cmd/migrate/migrate_test.go`) лишний. `events.OutboxFor` —
  `outboxkit.WithSubjects(name+".>")` (бэклог шаблона `publisher-stream-check`).
- **env для INFRA-HANDOFF:**
  - `HTTP_RATE_LIMIT_TRUST_PROXY` → `HTTP_TRUST_PROXY_HOPS`;
  - `JWT_REFRESH_TTL` → `SESSION_*`;
  - `JWT_ISSUER`, `JWT_AUDIENCE`; `JWT_PREVIOUS_SECRET` — при ротации;
  - конечный `OUTBOX_MAX_ATTEMPTS`;
  - `HTTP_SHUTDOWN_TIMEOUT` и `HTTP_SHUTDOWN_DRAIN_DELAY`, бюджет ретраев `NATS_JS_*`.

**Аннотация v1.3.0:** первая половина (гейт, B1–B3) — заготовка в материалах программы
`/home/mm/.cache/stit-audit-program/annotation-v1.3.0.md`; вторая — по шагам этого плана в «Где мы
сейчас». Шлюзам на v0.39.0 — цепочка аннотаций; `config` без ctx → убрать
`fx.Provide(context.Background)`.

## Где мы сейчас
Первая половина партии 2 (`harden-gate-defaults`: гейт, B1–B3) закрыта, её аннотация — в
материалах (Handoff). Сводка аудита — `M/kits-audit/summary.md`. S1–S4 сделаны; следующий шаг — S5
(`authnkit/session`). Хвост для S8: под нагрузкой гейта `TestStreamBus_SubscriptionUnsubscribe`
получил доставку после `Unsubscribe` (1 прогон из 4, отдельно 8 из 8 PASS), а дока обещает «снимает
интерес сразу».

Заготовка аннотации второй половины v1.3.0:
- Поведение — logkit, timerkit, auditkit/pgstore, bruteforcekit/pgstore, storagekit/minio,
  httpkit: инструменты берут глобальный провайдер при сборке объекта, а не первый провайдер
  процесса.
- Изменения — `testkit/metrictest`: проверка записи метрик (провайдер процесса ставит импорт,
  дельты, `Get`/`Find`/`Value`, коллизии имён в Prometheus) — для бизнес-метрик потребителя.
- Поведение — auditkit: пустые `IP` и `RequestID` записи `pgstore.Store` и `audittest.Recorder`
  берут из ctx (`httpkit.ClientIP`, `RequestID`; request-id — и grpckit) → ручные значения в
  `Entry` убрать; изменения — `Entry.Enrich(ctx)` для своей реализации `Logger`.
- Изменения — authnkit: `NewToken()` (256 бит hex) и `HashToken` (SHA-256) → `pkg/tokens`
  шаблона заменить.
