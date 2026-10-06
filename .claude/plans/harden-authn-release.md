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
- **Потребитель** — шаблон: `fix-reference-code` S9 (сессии, токены, IP, аудит, `config` без ctx) и
  S11 (бизнес-метрики с проверкой записи); что он снимает и меняет на bump — «Handoff».
- **Правила шага:** README и `docs/` кита — тем же коммитом (`make envdoc-check`); breaking — `!` и
  `BREAKING CHANGE:`. Новая зависимость в `go.mod` не нужна ни одному шагу.

## Не входит
- Партия 3 — `backlog.md`, триггер «партия 3». Туда же кандидаты C, которые аудит понизил до minor:
  предел длины пароля Argon2id и retention `audit_log`.
- Правки шаблона и шлюзов — по аннотации тега.
- Push `main` и тега — делает дирижёр после PASS валидатора партии.

## Критерии приёмки
- [x] **AC1** — Инструменты китов пишут в текущий глобальный `MeterProvider`, в том числе в
  тестовый. `testkit/metrictest` проверяет запись метрики (имя, тип, unit, labels, значение) китов и
  кода с переданным `Meter` (параллельно, без глобального провайдера) и коллизии имён в Prometheus.
  Проверка: `go test -race -count=3 ./testkit/metrictest/` → PASS; `M/kits-audit/fx-observability/run.sh <rev>
  obs-provider` → KO09 PASS; `M/kits-audit/httpkit-b/run.sh <rev> exp` → HO06 PASS
  - Подтверждено: metrictest PASS (коллизия KOBS-01, параллельные `NewProvider`); KO09, HO06 → PASS
- [x] **AC2** — Запись аудита без `RequestID` или `IP` получает их из ctx запроса, явные значения не
  перетираются; `audittest` ведёт себя так же. Проверка:
  `go test -race ./auditkit/... ./testkit/audittest/` → PASS
  - Подтверждено: PASS — `TestEntry_Enrich`, pgstore с Docker, `audittest` за middleware httpkit
- [x] **AC3** — authnkit выдаёт одноразовый токен: 256 бит из `crypto/rand`, hex. Для хранения —
  SHA-256 токена. logkit маскирует такой токен по умолчанию. Проверка:
  `go test -race -run Token ./authnkit/ ./logkit/` → PASS
  - Подтверждено: PASS; токен в base64url (мутант) — `TestRedact_OneTimeTokenOfAuthnkit` красный
- [x] **AC4** — `authnkit/session` управляет сессиями refresh: вход выдаёт сессию; обновление
  ротирует токен атомарно (из N параллельных обновлений одним токеном успешно одно); повтор
  использованного токена отзывает всю цепочку; `RevokeAll(subject)` в транзакции из ctx откатывается
  вместе с ней; абсолютный срок не продлевается ротацией; без миграций — ошибка старта с подсказкой,
  схема проходит лесенку; pgstore и фейк проходят один контрактный набор; stateless refresh JWT в
  ките больше нет. Проверка: `go test -race ./authnkit/... ./testkit/sessiontest/` с Docker → PASS;
  `grep -rn "SignRefresh\|ParseRefresh\|JWT_REFRESH_TTL" --include=*.go --include=*.md
  --exclude-dir=plans .` → пусто
  - Подтверждено: PASS, grep пуст; `sessiontest.RunContract` — по фейку и pgstore, его мутанты красные
- [x] **AC5** — `config.Load` и `config.Source` работают без ctx; в доках нет
  `fx.Provide(context.Background)`. Проверка: `go test -race ./config/` → PASS;
  `grep -rn "context" config/` → пусто
  - Подтверждено: PASS, grep пуст; граф README без ctx — `TestModule_AppConfigWithoutCtxInGraph`
- [x] **AC6** — Сообщение JetStream переживает 15-секундный сбой зависимости обработчика при
  дефолтах. `ErrTerminal` уходит в dead сразу. ERROR `jetstream message dead` несёт `stream_seq`.
  Бюджет ретраев по умолчанию назван в доке. Проверка:
  `M/kits-audit/correctness-security/run.sh <rev> nats` → E15 PASS
  - Подтверждено: E15 (5 доставок, обработано 1), E16 PASS; `stream_seq` — `…MaxDeliverDLQ`, бюджет
    ≥ 15 мин — `TestNakDelay_DefaultBudgetCoversFifteenMinutes`, `ErrTerminal` — `…SkipsRetries`
- [x] **AC7** — Метрики otelhttp у клиента не эмитятся; имена метрик объёма после трансляции в
  Prometheus без коллизий. Проверка: `fx-observability/run.sh <rev> obs-unit obs` → KO01 PASS,
  `promname` → 0 коллизий
  - Подтверждено: KO01 PASS; `promname` (KO01 и граф китов) — 0 коллизий, `compare.py` — метрик
    otelhttp нет; мутант без noop-провайдера — `TestMetrics_NoOtelhttpMetricsNoNameCollisions` красный
- [x] **AC8** — Остановка httpkit: `/ready` отвечает 503, пока listener ещё открыт; долгий запрос или
  SSE не съедают бюджет fx — после дедлайна HTTP ctx хендлеров отменён, соединения закрыты, остальные
  OnStop выполняются; panic пробы — проба `down` и ERROR со стеком, процесс жив. Проверка:
  `httpkit-b/run.sh <rev> exp` → HX02 a–c, HX03, HO07 PASS; `M/kits-audit/httpkit-a/run.sh <rev>
  lifecycle` → H31, H32, H33 PASS
  - Подтверждено: HX02 a–c, HX03, HO07, H31–H33 PASS (H33, HX03 — с паузой дренажа, HX02b — под
    решение; оригиналы — `M/exs8/orig/`); прочие исходы `exp` и `lifecycle` — как на базе
- [x] **AC9** — Если `authnkit.Module` получил iss/aud из конфига, токен с чужими iss/aud
  отвергается. Токен, подписанный предыдущим секретом (`JWT_PREVIOUS_SECRET`), принимается; новые
  подписываются текущим. Проверка: `correctness-security/run.sh <rev> unit` → E06 PASS;
  `go test -race ./authnkit/` → PASS
  - Подтверждено: E06 PASS (свой `JWT_AUDIENCE` у сервиса, оригинал — `M/exs8/orig/`); authnkit PASS,
    `TestHS256JWT_PreviousSecret`, `TestModule_AudienceFromEnvIsolatesServices`; 6 мутантов красные
- [x] **AC10** — Атрибут лога регистрируется без `slog` в сигнатуре потребителя. Проверка:
  `go doc ./logkit Attr` → без `slog`; `go test -race ./logkit/` → PASS
  - Подтверждено: `go doc` — `func(ctx context.Context) (any, bool)`, slog нет; logkit PASS, число
    остаётся числом — `TestRegisterAttr_ValueKeepsItsType` (мутант со строкой — красный)
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
  - Сделано: инструменты в конструкторах; KO09, HO06 → PASS; `TestInstrumentsAreNotPackageLevel`
- [x] **S2** — B7: `testkit/metrictest` — провайдер процесса для китов и провайдер теста для
  переданного `Meter`, дельты, проверка коллизий имён. Обвязка метрик в тестах китов — на нём (KTST-06)
  - Проверка: AC1 → выполнен; `make check` → зелёный · Закрывает: AC1 · Зависит от: S1
  - Сделано: metrictest; на нём 17 из 20 файлов обвязки (кроме `internal/otelkit`, `telemetry`)
- [x] **S3** — B4: auditkit и `audittest` берут `RequestID` и `IP` из ctx, если в записи пусто
  - Проверка: AC2 → выполнен · Закрывает: AC2 · Зависит от: —
  - Сделано: `auditkit.Entry.Enrich` — его зовут `pgstore.Store.Log` и `audittest.Recorder.Log`
- [x] **S4** — B5: одноразовые токены в authnkit — генерация и хеш
  - Проверка: AC3 → выполнен · Закрывает: AC3 · Зависит от: —
  - Сделано: `authnkit.NewToken() string` и `HashToken(token) string`, рецепт погашения в доке
- [x] **S5** — B6: `authnkit/session` и `session/pgstore`: выдача, ротация, отзыв цепочки, `RevokeAll`,
  `Revoke`, абсолютный срок; kit-owned миграция, проверка схемы на старте, очистка через timerkit,
  метрики; stateless refresh JWT удалён
  - Проверка: `go test -race ./authnkit/...` с Docker → PASS · Закрывает: AC4 · Зависит от: S2, S4
  - Сделано: PASS; 4 мутанта ротации — красные; лесенка; без миграций — `ErrSchema` на старте
- [x] **S6** — B6: фейк `testkit/sessiontest` и общий контрактный набор — по фейку и по pgstore
  - Проверка: AC4 → выполнен · Закрывает: AC4 · Зависит от: S5
  - Сделано: `sessiontest.New`, `Active`, `RunContract` (10 кейсов) — по фейку и pgstore
- [x] **S7** — B8: `config.Load` и `Source.Apply` без ctx, README без ctx в графе (KAPI-10)
  - Проверка: AC5 → выполнен · Закрывает: AC5 · Зависит от: —
  - Сделано: `Load(dst, sources...)`, `Source.Apply(dst)`; README и тест графа без ctx

### High базового аудита и кандидаты C
- [x] **S8** — natskit: бюджет ретраев JetStream переживает типичный сбой (KCOR-01); `stream_seq`
  в dead; в доке — правило «обработчик возвращает ошибку, natskit логирует её сам» (C)
  - Проверка: AC6 → выполнен · Закрывает: AC6 · Зависит от: —
  - Сделано: NAK `1s·2^(n−1)` до 1m, 21 доставка — 15 мин; доставка после `Unsubscribe` не
    обрабатывается; natskit `-count=2` чист; 5 мутантов (рост, потолок, бюджет, `stream_seq`, флаг)
    красные
- [x] **S9** — httpclient: метрики otelhttp выключены, README и тест без коллизий имён (KOBS-01)
  - Проверка: AC7 → выполнен · Закрывает: AC7 · Зависит от: S2
  - Сделано: noop `MeterProvider` у транспорта otelhttp; тест: только `http_client_*`, коллизий нет
- [x] **S10** — httpkit, остановка: фаза дренажа с `/ready` 503 (C), дедлайн HTTP из остатка бюджета
  с отменой ctx и `Close` (KFX-08 вместе с KCOR-08), `recover` в пробах `/ready` и
  `healthkit.WithTimeout` (KOBS-14); проверка здоровья без shell для distroless-образа — функция
  httpkit для подкоманды бинаря потребителя (`GET /health` по `HTTP_ADDR`, код выхода), шаблон
  снимает свою `cmd/api/healthcheck.go`
  - Проверка: AC8 → выполнен · Закрывает: AC8 · Зависит от: —
  - Сделано: `Server.Stop` — `draining`, пауза, дедлайн с отменой ctx и `Close`; ⅔ бюджета в `OnStop`;
    `healthkit.PanicError`; `Healthcheck`/`CheckHealth`; 8 мутантов — красные
- [x] **S11** — authnkit: iss и aud из конфига `Module`, ротация секрета через предыдущий секрет (C)
  - Проверка: AC9 → выполнен · Закрывает: AC9 · Зависит от: S5
  - Сделано: `JWTConfig.Issuer`/`Audience`/`PreviousSecret`; опции `WithIssuer`/`WithAudience` удалены
- [x] **S12** — logkit: `Attr` без slog в сигнатуре (C)
  - Проверка: AC10 → выполнен · Закрывает: AC10 · Зависит от: —
  - Сделано: `Attr func(ctx) (any, bool)`; httpkit, grpckit, hubkit/sso, oidckit — без slog

### Релиз
- [ ] **S13** — Релиз: корневые README и CLAUDE.md сверены с API; `make test-full` и `make gate-clean`;
  apidiff и env-diff против v1.2.0; сборка шаблона на HEAD через `replace` в своём worktree (каждая
  ошибка сборки — строкой Breaking); тег `v1.3.0` локально. Push `main` и тега — дирижёр после PASS
  валидатора партии; затем план удаляется отдельным `chore(plan)`
  - Проверка: AC11 → выполнен · Закрывает: AC11 · Зависит от: S3, S6, S7, S8, S9, S10, S11, S12

## Решения
- **Инструменты метрик** — в конструкторах через `otelkit.Meter` (и трейсер timerkit): делегат OTel
  привязывает пакетный инструмент к первому провайдеру процесса (KO09, HO06). Регресс держит
  статический страж `internal/otelkit`: код инициализации пакета (`var`, `init()`, `sync.Once*`) — шов,
  функция, метод, значение функции пакета; замыкание конструктора fx — нет; не тест второго
  провайдера — страж видит все пакеты, а не перечень китов.
- **metrictest.** Путь — по источнику `Meter`, у каждого один способ: переданный (бизнес-метрики,
  провайдер telemetry в boot-тесте) — `NewProvider()`, снимок без дельт, параллельно и при `-count`;
  глобальный (киты) — провайдер процесса, его ставит первый `Collect`, не импорт (иначе импорт делал
  пустой проверку графа); записи до него не видны, SDK-провайдер до него и подмена после его роняют.
  `Get` — ровно эти labels, `Find`/`Value` — фильтр; `Sub` оставляет новую серию и с нулём, gauge —
  текущим; повтор — снимки на `NewProvider`, UpDownCounter процесса — labels прогона. Коллизии —
  `UnderscoreEscapingWithSuffixes` с таблицами otlptranslator (вне репо — 0 расхождений), без рядов
  гистограммы. Свой провайдер — у `internal/otelkit` и `telemetry`.
- **Аудит.** `Entry.Enrich(ctx)` ядра auditkit зовут pgstore, `audittest` и свой `Logger` до
  `Validate`. Ключ IP — из `internal/`, auditkit не импортирует httpkit.
- **Токены.** Hex: маска hex ≥ 32 в logkit ловит его, base64url — нет (`logkit-redact-gaps`).
  SHA-256, а не Argon2id: полная энтропия. Поиск — по хешу в SQL, в памяти — `subtle`. `NewToken`
  без ошибки: `crypto/rand.Read` с Go 1.24 её не возвращает.
- **Сессии.** `authnkit/session` — `Store`, `Token`, `Config`, ошибки; `session/pgstore` — хранилище с
  метриками (как auditkit, bruteforcekit), миграции, `Module`, очистка; `testkit/sessiontest`. Ротация
  — один оператор: условный `UPDATE … WHERE used_at IS NULL AND revoked_at IS NULL` и вставка
  следующего токена в `WITH`, срок — `min(now + IdleTTL, срок сессии)`. Повтор токена — отзыв сессии
  и `*ReusedError`, без окна прощения; `Rotate` в транзакции — `ErrInTx`; `Issue`, `Revoke`,
  `RevokeAll` — в транзакции из ctx. Часы приложения (`WithClock`). Старт — версия схемы против
  последней миграции кита (`ErrSchema`); очистка — по абсолютному сроку. Метрики
  `authn_session_*`; `SESSION_IDLE_TTL` 168h, `SESSION_ABSOLUTE_TTL` 720h. Stateless refresh JWT удалён.
- **config.** `Load(dst, sources...)` и `Source.Apply(dst)` без ctx. Удалённый источник сам
  ограничивает свой I/O. ctx в графе даёт любому конструктору вечный контекст (FX-10).
- **natskit.** Задержка NAK — `NATS_JS_NAK_BACKOFF·2^(n−1)` до `NATS_JS_NAK_MAX_BACKOFF` (1m),
  `MaxDeliver` 21: 15 минут — failover Postgres, выкатка соседа (E15: сообщение терялось через ≈10 с);
  после восстановления — не позже минуты; потолок ниже начала — постоянная задержка. Term — только
  `ErrTerminal` и исчерпанный бюджет; dead и dlq-ошибка несут `stream_seq`. Канон: обработчик
  возвращает ошибку и не логирует её — natskit пишет ERROR с `trace_id` и subject (KO20).
  `Unsubscribe` ставит флаг, callback его сверяет: pull-подписку nats.go снимает асинхронно (R5).
  Имена стримов тестов — с номером прогона, `-count=2` чист.
- **httpclient.** Метрики otelhttp выключаются через noop `MeterProvider` у транспорта; спаны попытки
  остаются. Длительность попытки видна в спане, повторы считает `http_client_retry_attempts_total`.
  Переименовать метрику кита — breaking и хуже. otelhttp anthropickit (вне объёма) — бэклог.
- **Остановка httpkit.** `Server.Stop`: флаг → `/ready` 503 → пауза `HTTP_SHUTDOWN_DRAIN_DELAY` →
  `Shutdown` до `HTTP_SHUTDOWN_TIMEOUT` → на дедлайне отмена базового ctx запросов (и захваченных
  соединений), `Close`, WARN с числом запросов; ошибка — только у `Close` (SIGTERM — exit 0). Хукам
  после HTTP — треть остатка stop-ctx (`OnStop` даёт `Stop` ⅔). Флаг — в ctx запросов через
  `BaseContext`: `RegisterHealth` без смены сигнатуры. `HTTP_SHUTDOWN_TIMEOUT` 10s < `fx.StopTimeout`
  15s; пауза по умолчанию 0 — совместимо, за балансировщиком 5–10 с. panic пробы — `*PanicError` из
  `Probe.Run` (одно место для `/ready` и `grpckit`). Эксперименты: H33, HX03 — с паузой; HX02b —
  «ctx отменён, ответа на закрытом пуле клиент не получил» (хендлер без ctx не остановить).
- **JWT.** `JWT_ISSUER` и `JWT_AUDIENCE` из конфига; пустые — не пишутся и не проверяются. Одному
  сервису на своём секрете они не нужны, общий секрет без `aud` небезопасен — это сказано в доке.
  Опции `WithIssuer`/`WithAudience` удалены: один способ, а `Module` знает только конфиг. Ротация:
  подпись текущим секретом, проверка — текущим, затем `JWT_PREVIOUS_SECRET`; `kid` не нужен.
- **logkit.** `type Attr func(ctx) (any, bool)`, кит кладёт значение как `slog.Any`.
- **Релиз.** Раздел «Гейт» называет `make gate-clean` (с integration), `make test-full`, apidiff и
  env-diff против v1.2.0 и сборку шаблона через `replace`.

## Handoff
**Шаблон, `fix-reference-code` S9 — что снимается и меняется на bump v1.3.0:**
- **Токены.** `pkg/tokens` → генерация и хеш из authnkit. Погашение — своим условным `UPDATE`.
- **Сессии.** `refresh_sessions`, репозиторий сессий и адаптеры `SignRefresh`/`ParseRefresh` →
  `authnkit/session`: в граф — `session/pgstore.Module` (и `timerkit.Module`), его миграции —
  источник `cmd/migrate`; `refresh_sessions` удаляется новой миграцией с `-- infra-handoff:` (при
  выкатке все входят заново один раз); `RevokeAll` — в транзакции блокировки, удаления, смены роли,
  смены и сброса пароля, в `seed-admin`; `Rotate` — вне транзакции, `CanLogin` — после него,
  `session.ErrReused` (`*ReusedError`: subject, сессия) → аудит `refresh_reused`; окна прощения нет —
  клиент обновляет сессию последовательно, это — в описании операции refresh в `openapi.json`.
- **IP.** `authgate.ClientIP` и `identity.WithIP` → `httpkit.ClientIPFrom(ctx)`; за Traefik —
  `HTTP_TRUST_PROXY_HOPS=1`.
- **Публичные операции.** `publicOperations` и `TestHTTPOperations` → `httpkit.Public(op)` в
  `Register` входа, refresh, logout, регистрации, сброса и подтверждения. Пропуски и дубли — ошибка
  старта кита.
- **Аудит.** Ручные `IP` и `RequestID` в `auditkit.Entry` → убрать.
- **Handler-тесты.** `handlertest.NewAPI` закрыт по умолчанию → `handlertest.WithAuth`; аноним → 401.
- **Логи.** `userIDAttr` → `func(ctx) (any, bool)`, без `slog` и `//nolint:forbidigo`.
- **Здоровье и остановка.** `cmd/api/healthcheck.go` и его тест → `httpkit.Healthcheck(ctx)` в `main`,
  `httpkit.CheckHealth` в `TestBoot`; `fx.StopTimeout` — HTTP берёт ⅔, комментарий бюджета в `main`.
- **Конфиг.** `App` (Cookie) можно читать через `config.Load(&cfg, config.Env())` без ctx.
- **OpenAPI.** `openapi.json` перегенерировать: `$schema` снят; `HTTP_DOCS_ENABLED=true` — только в
  dev.
- **Метрики.** Хелперы `counted`/`counter` и свой `ManualReader` → `metrictest.NewProvider()`:
  подтесты `auth`, `verification` — `NewMetrics(p.Meter("test"))`, `snap := p.Collect(t)`,
  `snap.Get(t, name, "result", r).Value == 1` и `snap.Value(name) == 1` (исход ровно один);
  `TestBoot` — в `fx.Decorate` запомнить провайдер telemetry (`telemetryMP`) и подменить его
  `p.MeterProvider`, `IsType` → `require.Same(t, telemetryMP, otel.GetMeterProvider())`.
- **Тесты.** Лесенка `cmd/migrate` → `testkit.MigrationLadder(t,
  testkit.WithMigrations(sources.Kits...), …)` (иначе — `repair-harness` S7); testkit без Docker вне
  `-short` — FAIL, обход `TestDockerRequired` лишний; `events.OutboxFor` —
  `outboxkit.WithSubjects(name+".>")` (бэклог `publisher-stream-check`).
- **env для INFRA-HANDOFF:**
  - `HTTP_RATE_LIMIT_TRUST_PROXY` → `HTTP_TRUST_PROXY_HOPS`;
  - `JWT_REFRESH_TTL` → `SESSION_IDLE_TTL`, `SESSION_ABSOLUTE_TTL`, `SESSION_CLEANUP_INTERVAL`;
  - `JWT_ISSUER`, `JWT_AUDIENCE`; `JWT_PREVIOUS_SECRET` — при ротации;
  - конечный `OUTBOX_MAX_ATTEMPTS`;
  - `HTTP_SHUTDOWN_TIMEOUT` и `HTTP_SHUTDOWN_DRAIN_DELAY`; бюджет ретраев — `NATS_JS_MAX_DELIVER` 21,
    `NATS_JS_NAK_MAX_BACKOFF` 1m.

**Аннотация v1.3.0:** первая половина (гейт, B1–B3) — заготовка в материалах программы
`/home/mm/.cache/stit-audit-program/annotation-v1.3.0.md`; вторая — по шагам этого плана в «Где мы
сейчас». Шлюзам на v0.39.0 — цепочка аннотаций; `config` без ctx → убрать
`fx.Provide(context.Background)`.

## Где мы сейчас
Этап S1–S7 принят (перепроверка `cd654a7` — PASS), остатки R1–R5 закрыты (R5 — в S8). Этап S8–S12
сделан: дальше валидатор этапа, затем S13 — релиз. Под решения правлены эксперименты E06, H33, HX03,
HX02b и сборка инвентаря `httpkit-b/run.sh` (оригиналы — `M/exs8/orig/`).

Заготовка аннотации второй половины v1.3.0:
- Поведение — logkit, timerkit, auditkit/pgstore, bruteforcekit/pgstore, storagekit/minio, httpkit:
  инструменты берут глобальный провайдер при сборке объекта, а не первый провайдер процесса.
- Изменения — `testkit/metrictest`: `NewProvider()` — провайдер теста для кода с переданным `Meter`
  (бизнес-метрики, подмена провайдера telemetry в boot-тесте); `Collect` — провайдер процесса китов
  (ставит первый вызов), дельты `Sub`, `Get`/`Find`/`Value`, коллизии имён в Prometheus.
- Поведение — auditkit: пустые `IP` и `RequestID` записи `pgstore` и `audittest` берут из ctx
  (httpkit; request-id — и grpckit) → ручные значения убрать, тест `Entry` целиком под ctx запроса
  видит их; своему `Logger` — `Entry.Enrich(ctx)`.
- Изменения — authnkit: `NewToken()` (256 бит hex) и `HashToken` (SHA-256) → вместо `pkg/tokens`.
- Breaking — authnkit: `SignRefresh`, `ParseRefresh`, `TokenTypeRefresh`, `DefaultRefreshTTL`,
  `JWTConfig.RefreshTTL` (`JWT_REFRESH_TTL`) удалены → refresh — `authnkit/session`: в граф
  `session/pgstore.Module` (с `timerkit.Module`) и `session.NewConfig`, `pgstore.Migrations` — в
  мигратор (без них `ErrSchema` на старте), env `SESSION_IDLE_TTL`, `SESSION_ABSOLUTE_TTL`,
  `SESSION_CLEANUP_INTERVAL`; старые refresh-JWT недействительны — вход заново один раз.
- Изменения — `authnkit/session`: `Store` (`Rotate` вне транзакции, `RevokeAll` в транзакции из
  ctx), `Token`, `ReusedError`; `session/pgstore`: `NewStore`, `WithClock`, `CheckSchema`,
  `Cleanup`, `Module`, `authn_session_*`; `testkit/sessiontest`: фейк `New`/`Active`, `RunContract`.
- Breaking — config: `Load(ctx, dst, …)` → `Load(dst, …)`, `Source.Apply(ctx, dst)` → `Apply(dst)`
  → убрать ctx и `fx.Provide(context.Background)`; свой `Source` с I/O ограничивает время сам.
- Поведение — natskit: задержка NAK растёт от `NATS_JS_NAK_BACKOFF` вдвое до `NATS_JS_NAK_MAX_BACKOFF`
  (1m), `NATS_JS_MAX_DELIVER` по умолчанию 21 — ретраи 15 минут, а не ≈10 с; ERROR `jetstream
  message dead` несёт `stream_seq`; доставку после `Unsubscribe` обработчик не получает.
- Изменения — natskit: `JetStreamConfig.NakMaxBackoff` (`NATS_JS_NAK_MAX_BACKOFF`).
- Поведение — httpclient: метрик otelhttp (`http.client.request.duration`, `.body.size`) нет — в
  Prometheus первая сливалась с `http_client_request_duration_seconds` кита → запросы — на неё.
- Поведение — httpkit: с начала остановки `/ready` — 503 `draining`; пауза `HTTP_SHUTDOWN_DRAIN_DELAY`
  (новый, 0); `HTTP_SHUTDOWN_TIMEOUT` по умолчанию 10s, а не 15s; на дедлайне ctx хендлеров отменён,
  соединения закрыты (WARN); HTTP берёт не больше ⅔ остатка `fx.StopTimeout`; panic пробы — `down` и
  ERROR → за балансировщиком пауза 5–10 с, сумма бюджетов — в `fx.StopTimeout`.
- Изменения — httpkit: `Healthcheck(ctx)`, `CheckHealth(ctx, addr)` → подкоманда `api healthcheck`;
  healthkit: `PanicError` — panic проверки в `Run` и `WithTimeout`.
- Breaking — authnkit: `WithIssuer`, `WithAudience` удалены → `JWTConfig.Issuer`, `Audience`
  (`JWT_ISSUER`, `JWT_AUDIENCE` через запятую); сервисам на общем `JWT_SECRET` — свой `JWT_AUDIENCE`.
- Изменения — authnkit: ротация секрета — `JWTConfig.PreviousSecret` (`JWT_PREVIOUS_SECRET`).
- Breaking — logkit: `Attr` — `func(ctx) (any, bool)`, а не `(slog.Value, bool)` → значение без
  обёртки `slog.*Value`, `//nolint:forbidigo` у регистрации не нужен.
