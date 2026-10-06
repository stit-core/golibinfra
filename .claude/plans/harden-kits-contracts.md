---
status: in-progress
parent: go-backend-template:audit-program
blocked-by: —
---

# harden-kits-contracts — партия 3, часть 1: конфиг и формы API, данные, httpkit, секреты, дефолты

## Цель
Первая половина партии 3 — тега v1.4.0, после которого каждый угол аудита китов ≥ 9 (выше minor
ничего, эксперименты чисты): medium и minor углов 1–3 — ридер ENV, конфиги, форма API, pgkit, outbox,
идемпотентность, httpkit, секреты, bruteforcekit, дефолты. Вторая половина и релиз — второй план.

## Контекст
- **Программа** — `go-backend-template/.claude/plans/audit-program.md`; находки — `M/kits-audit/summary.md` («Как
  повторить аудит») и отчёты углов (`M` — `/home/mm/.cache/stit-audit-program/`).
- **Состав партии 3:** 27 medium и 36 minor аудита, кандидаты C (длина пароля, retention `audit_log`), N1–N5
  `M/validation-release-v1.3.0.md`, заметка шаблона про subject длиннее 255 байт.
- **Эксперименты** — `run.sh` углов в своём worktree на ревизии: FAIL — дефект есть; не собирается на новом API —
  правка под API без смены смысла (оригинал — `M/exs8/orig/*.v130`); тяжёлые — по одному.
- **Потребитель** — шаблон: bump v1.4.0 в `repair-harness` S7 (Handoff — во втором плане).
- **Правила шага:** README и `docs/` кита — тем же коммитом; breaking — `!`, `BREAKING CHANGE:` и строка заготовки
  аннотации; новых зависимостей нет; киты вне объёма правятся под API тем же коммитом.
- **Код:** extractor — `httpkit/middleware.go`, `redactURL` — `natskit/conn.go`.

## Не входит
- Вторая половина партии 3 — `harden-tests-archkit-release`: fx (KFX-03, -05, -06, KOBS-08), сигналы
  (и httpkit в гейте), фейки, мутанты, `archkit`, гейт, обвязка, доки, повтор экспериментов, релиз.
- Маршрут после проверки старта httpkit и сырой тег группы timerkit — `backlog.md`; правки шаблона и
  шлюзов — по аннотации тега; push — дирижёр после PASS валидатора партии.

## Критерии приёмки
`<rev>` — проверяемый коммит, `<wt>` — worktree на нём; скрипты — от `M/kits-audit/`.
- [x] **AC1** — ENV одним способом: `KEY=` — как незаданная в каждом ките и в `config.Env`; ошибка разбора —
  `ErrConfig`, ключ и префикс кита; `<P>_HTTPCLIENT_BREAKER_*` читается, теневых имён нет; выключение — явным
  значением. Проверка: `api-docs/scripts/run-go-experiments.sh <rev> envempty hcprefix` → U4, U5;
  `api-docs/claims-A-exp/run.sh <wt>` (`parseerr`), `api-docs/scripts/claimsB/run.sh <wt>` → U6 21/21 с
  `ErrConfig`; `httpkit-a/run.sh <rev> api` → U01 PASS, U02 — пустое = дефолт
  - Подтверждено: дерево S1 (`M/exc1/logs/`) — U4 87/87, U5, U6 (13/13, 11/11, `TestKitConfigs_*`), U01; U02 — 31
    тег
- [x] **AC2** — Невалидный конфиг не проходит ни один конструктор (`outboxkit.New`, `NewWorker`,
  `storagekit.Module`, `httpkit.NewServer` — с ошибкой); у `Validate`, нулей и nil-опций одна семантика; `BindAPI`
  без гонки; тик outbox дольше простоя транзакции пула — ошибка старта. Проверка:
  `api-docs/scripts/run-go-experiments.sh <rev> literal` → U3 16/16; `httpkit-a/run.sh <rev> security lifecycle` →
  H20, H41 PASS; `api-docs/scripts/claims-C/run.sh <wt>` → K2, T1 — отказ, C1 — без panic
  - Подтверждено: дерево S2 (`M/exc1/logs/`) — U3 16/16; H20, H41 PASS; K2, T1 — отказ, C1 — без panic
- [x] **AC3** — Одна форма API (группы, хелперы, пробы, логгер, порядок параметров — по «Решениям»); опции
  httpclient — в заданном порядке; ошибка графа httpkit называет функцию кита; job'ы со схемой — после миграций;
  test-хелперы в китах — только исключения. Проверка: `fx-observability/run.sh <rev> fx fx-docker` → KX50, KX61,
  KX61b PASS; `httpkit-b/run.sh <rev> exp` → в HX04 (a), (b) есть `httpkit.`; таблица единообразия `api-docs.md` →
  без ✗ KAPI-06, KAPI-07
  - Подтверждено: дерево S3 (`M/exc1/logs`) — KX50 40/40, KX61, KX61b PASS; HX04 — `*httpkit.Server`; ✗ нет
- [x] **AC4** — pgkit и схемы китов: миграция под чужой блокировкой укладывается в дедлайн и держит читателей не
  дольше `lock_timeout`; `OnStop` пула уважает ctx; вложенная транзакция строже внешней — ошибка; хранилище кита
  без своих миграций не стартует и называет источник. Проверка: `correctness-security/run.sh <rev> pg` → E09 PASS;
  `fx-observability/run.sh <rev> fx-docker` → KX62, KX65 PASS
  - Подтверждено: дерево S4 (`M/exc2/logs/s4-*.log`) — E09 PASS (2,01 с, читатель 1,3 с), KX62, KX65 PASS
- [x] **AC5** — outbox: `ordered` публикует события агрегата в порядке его писателей; payload, который в JSONB
  больше предела, отвергается при вставке; потребитель события — в трассе запроса или со ссылкой на неё; число
  серий метрик outbox и имён producer-спанов не растёт с id в subject; новая миграция кита проходит лесенку.
  Проверка: `correctness-security/run.sh <rev> pg outbox-nats` → E10, E17 PASS; `fx-observability/run.sh <rev>
  obs` → KO11–KO14, KO21 PASS; `go test -race ./outboxkit/... ./natskit/...` с Docker → PASS
  - Подтверждено: дерево S6 (`M/exc2/logs/s6-obs.log`) — E10, E17, KO11–KO14 (KO13 21/21), KO21 PASS; лесенка
- [x] **AC6** — Идемпотентность и кэш: `GetDel`, `SetNX` и резерв не теряют атомарности при сбое связи; запись
  идемпотентности ставит только владелец резерва; один контрактный набор проходят `httpkit.MemoryStore`, cachekit
  на памяти и на Redis; `MemoryCache` и breaker'ы httpclient ограничены. Проверка: `correctness-security/run.sh
  <rev> unit redis` → E07, E18 PASS; `go test -race ./testkit/... ./cachekit/... ./httpkit/...` с Docker → PASS
  - Подтверждено: дерево S7 (`M/exc2/logs/s7-*.log`) — E07, E18 (3/3: ошибка, не «нет») PASS; test-full PASS
- [x] **AC7** — Контракт HTTP: тело сверх лимита — 413 и при поднятом лимите операции, и у скрытой; ответ
  «повторите позже» не воспроизводится из хранилища идемпотентности; 401 — с `WWW-Authenticate`; HEAD на
  GET-маршрутах; анонимы не делят scope ключа; долгий хендлер — дедлайн ctx и 503 problem+json, не EOF; CORS
  пускает заголовок идемпотентности; panic после заголовка обрывает ответ. Проверка: `httpkit-a/run.sh <rev>
  contract security lifecycle` → H04, H06, H07, H10, H16, H17, H35 PASS
  - Подтверждено: дерево S8 (`M/exc2/logs/s8-*.log`) — семь PASS (H35: 503 через 200 мс, ctx с дедлайном)
- [ ] **AC8** — Край httpkit: операцию открывает только её схема (Bearer не открывает `AdminKey`); extractor —
  один раз на запрос и при отказе бэкенда; `Security` со схемой вне `components` — ошибка старта и без схем в
  графе; trace-context и baggage клиента без явного доверия не принимаются; `ip:port` в XFF даёт ip; тест кладёт
  IP клиента в ctx без middleware. Проверка: `httpkit-a/run.sh <rev> security` → H15, H18 PASS; `httpkit-b/run.sh
  <rev> exp` → HX07 PASS
- [ ] **AC9** — Секреты: пароль и токен из URL и DSN не попадают в ошибки, логи и спаны natskit, cachekit,
  storagekit, telemetry и httpclient; маски logkit по умолчанию прячут base64url-токены и ссылки с ними, пароль
  DSN, коды вне словаря, поля структур и map под указателем в `Any`, JSON в `RawMessage` и `[]byte`, `otpauth`;
  счётчики и код ошибки `code` открыты. Проверка: `correctness-security/run.sh <rev> unit` → E02, E03, E04 PASS;
  `TestVal_RedactBypassMatrix` (`M/val-kits-A/experiments/logkit_zz_val_exp_test.go`) → `leaked=false`, кроме
  ключей из «Решений»; `api-docs/scripts/claims-C/run.sh <wt>` → urlleak без секретов
- [ ] **AC10** — bruteforcekit: проверок пароля одного subject за окно не больше бюджета субъекта при любом числе
  адресов; `CLEANUP_RETENTION` меньше `LOCK_DURATION` — `ErrConfig`; пауза отказа — по часам хранилища; ветка
  переучёта `Attempt` покрыта тестом; subject длиннее 255 байт учитывается. Проверка: `correctness-security/run.sh
  <rev> pg` → E11 PASS; `go test -race ./bruteforcekit/... ./testkit/bruteforcetest/` с Docker → PASS
- [ ] **AC11** — Входы и дефолты: endpoint S3 без схемы — TLS; токен OTLP по `http://` не на loopback без явного
  разрешения — ошибка старта; пароль длиннее предела отвергается до Argon2id; NUL, не-UTF-8 и `Metadata` сверх
  предела в записи аудита — `ErrInvalidEntry`; retention `audit_log` — job кита по явной настройке; отзыв сессии
  называет её владельца (аудит выхода с автором). Проверка: `correctness-security/run.sh <rev> unit pg` → E12
  PASS, E08 — по «Решениям», E05 — отказ длинного пароля; `go test -race ./authnkit/... ./testkit/sessiontest/` с
  Docker → PASS

## Шаги
### Конфиг и формы API
- [x] **S1** — Один ридер ENV в `internal/` — для всех `New*Config` и `config.Env` (KAPI-02, -04, KDOC-03)
  - Проверка: AC1 → выполнен · Закрывает: AC1 · Зависит от: —
  - Сделано: `69e5146`; AC1 → PASS; мутанты `M/exc1/mut_s1.py` 14/15 (M15 эквивалентен); B1 — `fix(envconf)`
- [x] **S2** — Конструкторы зовут `Validate`, одна семантика нулей и опций (KAPI-01, -05, -08, KCOR-12)
  - Проверка: AC2 → выполнен · Закрывает: AC2 · Зависит от: S1
  - Сделано: `51fc02d`; AC2 → PASS; мутанты `M/exc1/mut_s2.py` 18/18; эксперименты правлены под API
- [x] **S3** — Одна форма API, опции httpclient значением (KAPI-06, KAPI-07, KTST-07, KFX-01, KFX-10;
  N5 валидатора v1.3.0 — перечень хранилищ `pgstore` в README § Принципы)
  - Проверка: AC3 → выполнен · Закрывает: AC3 · Зависит от: S2
  - Сделано: `4508a58`; AC3 → PASS; мутанты `M/exc1/mut_s3.py` 23/23 (M7 заменён M7b); эксперименты под API

### Данные и httpkit
- [x] **S4** — pgkit: мигратор, пул, вложенные транзакции, схема китов на старте (KCOR-03, -07, KFX-02, -04)
  - Проверка: AC4 → выполнен · Закрывает: AC4 · Зависит от: S1
  - Сделано: `ae45272`; AC4 → PASS; мутанты `M/exc2/mut_s4.py` 25/25; эксперименты правлены под API
- [x] **S5** — outbox: `ordered` по моменту вставки, предел payload по JSONB (KCOR-02, `outbox-payload-jsonb`)
  - Проверка: E10 PASS; тест предела с числами в экспоненте → отказ `Publish` · Закрывает: AC5 · Зависит от: S4
  - Сделано: E10, E17 PASS; `TestPublish_RejectsPayloadOverJSONBLimit`; мутанты `M/exc2/mut_s5.py` 7/7
- [x] **S6** — Трасса и кардинальность outbox и natskit, миграция кита (KOBS-02, KOBS-03, KOBS-04)
  - Проверка: AC5 → выполнен · Закрывает: AC5 · Зависит от: S5
  - Сделано: AC5 → PASS; мутанты `M/exc2/mut_s6.py` 15/15; obs-эксперимент правлен под API (`MaxAttempts -1`)
- [x] **S7** — Атомарность cachekit, контракт идемпотентности, пределы (KCOR-04, -05, KTST-10, KSEC-06)
  - Проверка: AC6 → выполнен · Закрывает: AC6 · Зависит от: S3
  - Сделано: AC6 → PASS; мутанты `M/exc2/mut_s7.py` 28/28; под API: E07 — `Set` с токеном, E03/E04/E22 — `MaxHosts`
- [x] **S8** — Контракт HTTP httpkit (KCOR-09, KCOR-10, KCOR-11, KSEC-13, KSEC-14, KSEC-15, KAPI-09)
  - Проверка: AC7 → выполнен · Закрывает: AC7 · Зависит от: S7
  - Сделано: AC7 → PASS; мутанты `M/exc2/mut_s8.py` 27/27; под API: H16, `downStore` (S7), H35 — дедлайн
- [ ] **S9** — Край httpkit (KSEC-08, KFX-11, KSEC-12; N7, N8, N9, N11, f1-07 валидатора B
  `harden-gate-defaults` — тесты и строки доки; N4 валидатора v1.3.0 — IP клиента в ctx для тестов)
  - Проверка: AC8 → выполнен · Закрывает: AC8 · Зависит от: S8

### Секреты и дефолты
- [ ] **S10** — URL и DSN без секретов в ошибках, логах и спанах, `SECURITY.md` к факту (KDOC-02, KSEC-03)
  - Проверка: `correctness-security/run.sh <rev> unit` → E03, E04 PASS; claims-C urlleak — чисто ·
    Закрывает: AC9 · Зависит от: S1
- [ ] **S11** — Маски logkit (бэклог `logkit-redact-gaps`, `logkit-redact-containers`, `-otpauth`)
  - Проверка: AC9 → выполнен · Закрывает: AC9 · Зависит от: S10
- [ ] **S12** — bruteforcekit (KSEC-04, KSEC-07, бэклог `bruteforcekit-*`, заметка шаблона про 255 байт)
  - Проверка: AC10 → выполнен · Закрывает: AC10 · Зависит от: S4
- [ ] **S13** — Входы и дефолты: S3, OTLP, пароль, аудит и его retention (KSEC-05, KCOR-06, кандидаты C;
  N4 валидатора v1.3.0 — `session.Revoke` называет владельца)
  - Проверка: AC11 → выполнен · Закрывает: AC11 · Зависит от: S4

### Итог
- [ ] **S14** — Итог: эксперименты AC1–AC11 на HEAD — PASS (таблица — в «Где мы сейчас»), `make
  gate-clean` зелёный; Handoff и заготовка аннотации — в `harden-tests-archkit-release`; план удалён
  - Проверка: `make gate-clean` → зелёный · Закрывает: — · Зависит от: S6, S9, S11, S12, S13

## Решения
- **ENV.** Ридер — `internal/envconf` (35 конструкторов и `config.Env`): `KEY=` — как незаданная (U4), пустое —
  `-`, список из разделителей — ошибка; имя `<P>_<TAG>`, голый `<TAG>` — где кит обещает; вложенная структура —
  без тега, с тем же префиксом (U5; тег — ошибка, B1), вторая секция — свой `Load` с `EnvPrefixed`; тип вне ридера
  (`map`, `[]byte`, `Set`, `UnmarshalBinary`) и поле без тега и `ignored` — ошибка (канон — строка с `Validate`
  или `UnmarshalText`); целые — десятичные.
- **Конфиг.** `Validate` — только проверка, value-приёмник; дефолты — в тегах; ноль — значение из доки или
  `ErrConfig` («без предела» у новых — `-1`); формат — и у выключенного; nil-опции игнорируются. тик outbox против
  простоя транзакции — при `pgkit.PoolConfig` в графе; ноль, уходящий в драйвер, — его значение из доки;
  `RedactConfig{}` — безопасный дефолт.
- **Формы.** Группа — `<Thing>Group` = `<kit>.<thing>` и `Provide<Thing>(ctor, anns…)` с проверкой типа, значение
  — `Supply<Thing>`; проба инстанса — `natskit.ProvideProbe(name)`. Конструктор — `(cfg, зависимости…, log,
  opts…)`, функция над объектом берёт его первым; логгер — параметр (исключения — в CLAUDE.md). Опции httpclient —
  значение `Options` (KX50); `registerHealth` — `fx.In` (KFX-10); job'ы со схемой — после optional
  `pgkit.Migrated` (KX61b).
- **pgkit.** Мигратор — `lock_timeout` = `PG_MIGRATE_LOCK_TIMEOUT` (5 с, ноль — `ErrConfig`) параметром
  `MigrateUp/Down/UpAll`, у advisory-lock — только дедлайн; дедлайн — cancel request серверу до конца прогона
  (`StatementTimeout` не видит ctx, `GracefulStop` — гонка флага `M/exc2/logs/s4-gracefulstop-race.log`, обрыв
  сокета ALTER не снимает — E09), упавшая миграция — `dirty`. `OnStop` пула — 2/3 остатка, затем WARN и `nil`
  (KX65), логгер — из графа. Вложенный строже внешней (и `ReadOnly` в `ReadWrite`) — `ErrNestedTxStricter`.
  `CheckSchemaOnStart(src)` — у хранилищ после optional `Migrated`, у outbox — перед партициями, `Instance` —
  `outboxkit.SchemaChecker` (KX62); фейк `pgtest` — паритет у S3 второго плана.
- **outbox: порядок и предел.** `created_at` — `clock_timestamp()` вставки: писатели, сериализованные блокировкой
  агрегата, вставляют после коммита соседа (E10); ключ партиций и `ORDER BY` те же. Предел — по JSONB-тексту тем
  же `INSERT` (числа в экспоненте раздувают JSONB ×4, N1 валидатора B): мерит хранилище, не `Bus` — параметр
  `Repository.Insert(…, maxBytes)` (у инстанса свой, фабрика его не знает).
- **outbox: трасса и кардинальность.** Строка хранит W3C trace-context `Publish` (колонка `trace_context` миграции
  0003, без baggage); producer-спан — ребёнок контекста со ссылкой на тик (`natskit.WithTraceParent`), строка без
  контекста — в трассе тика (KO13). Label `subject` — только шаблоном `WithMetricSubject(fn)` инстанса (как
  `WithSubjects`), по умолчанию — серия на инстанс (KO11, KO21; gauge'и — самое старое и сумма). Producer-спан
  natskit — шаблон или `natskit.publish` (стрим по subject без запроса к серверу не узнать); `outbox.instance` не
  нужен — его называет ссылка на тик.
- **Идемпотентность и кэш.** `SetNX`, `GetDel`, `CompareAndDelete`, `CompareAndSet` — вторым клиентом без повторов
  (свой пул: до 2×`REDIS_POOL_SIZE`): повтор видит эффект первой попытки (E18); `Get`, `Set`, `Delete`, теги,
  `Ping` — с повторами, их повтор даёт то же состояние. `IdempotencyStore.Set(…, token, …) (stored, err)` —
  compare-and-set по токену (E07), без sentinel (cachekit не импортирует httpkit); `!stored` — WARN и `skipped`.
  Набор — `testkit/idempotencytest`. Предел — отказ, не вытеснение (резерв и lock не пропадают молча):
  `MemoryCache.WithMaxKeys` (100 000, `ErrMemoryFull`); `httpkit.MemoryStore` — тот же класс KSEC-06: 100 000 без
  опции (свой — cachekit); httpclient `MaxHosts` (1024, `-1` — без предела) — один реестр breaker'ов и label
  `host`, сверх — `other`.
- **Контракт HTTP.** Лимит тела — один `MaxBytesReader`, лимит фиксируется на первом чтении: huma-middleware
  ставит `MaxBodyBytes` операции (поднятый хуком — выше общего), скрытой реестр зовёт те же `OnAddOperation`;
  `*http.MaxBytesError` в причине ошибки huma — 413 (H17); до S9 тело с ключом идемпотентности читается до
  операции — по общему лимиту. Не хранятся 408, 409, 425, 429: «не выполнено», повтор исполняет заново (H16); без
  субъекта и credentials — без дедупликации. `WWW-Authenticate` — хук кита по `components` (http — схема,
  oauth2/oidc — `Bearer`, прочие — имя), без хука — имена; HEAD — свой middleware, `Allow` — с HEAD. Дедлайн —
  `HTTP_REQUEST_TIMEOUT` 25s < `WriteTimeout`, `ReserveTTL` строго больше, отрицательный — выкл. (`-1`
  длительностью не читается): 503 из таймера с Flush под мьютексом writer'а, над `Idempotency` — хранит ответ
  срезанного хендлера; SSE (`Accept`) и upgrade — без дедлайна (H35). CORS — `Idempotency-Key` в дефолте. panic
  после заголовка — Flush и `http.ErrAbortHandler`: без Flush клиент видит EOF до заголовков (H06).
- **Край httpkit.** Extractor — в `SecuritySchemeEntry`; операция открыта, если прошла одна из её альтернатив
  целиком; общий `AuthExtractor` графа удалён — один способ (H15). Идемпотентность — после маршрута и `HumaAuth`:
  знает операцию и субъект, extractor — один вызов (HX07), replay получает маршрут (KOBS-12). Входящий
  trace-context — ссылкой на новый корень, baggage отбрасывается; доверие — одна настройка, выключена (H18). XFF —
  `netip.ParseAddrPort`, затем адрес.
- **Секреты в URL.** Один `redactURL` в `internal/`: пароль userinfo, значения query, URL без схемы
  (`user:pass@host`), пароль keyword-DSN. httpclient: атрибуты попытки — на свой спан с очищенным URL,
  `*url.Error` — с очищенным URL (E03, E04); путь остаётся, README: секрет — не в пути.
- **Маски logkit.** Схема слов ключа (`classifyWords`) остаётся. Словарь секретов дополняют `pass`, `sid`,
  `signature`, `refresh`, `tokens`; `code` — слово-код (прячет и числа) только с квалификатором (`passcode`,
  `verification_code`, `auth_code`…); голый `code` — код ошибки httpkit (`httpkit/server.go:350`), открыт; ключ,
  который кончается словом-счётчиком (`otp_attempts`), — не код. Значения — маски base64url-токенов (порог — без
  ложных `TestVal_RedactHexFalsePositives`), пароля DSN, `otpauth://…secret=`; контейнеры — map под указателем,
  поля структур, JSON из `RawMessage`/`[]byte`.
- **bruteforcekit.** Второй бюджет — на subject по всем адресам (E11: 100 проверок с 20 адресов при
  `MaxFailures=5`): превышение — `LockState` с областью «субъект» и коротким сроком, потребитель отвечает как на
  блокировку пары. Пара блокируется строго, субъект — коротко: держать чужой аккаунт закрытым можно лишь
  непрерывным потоком неудач. Ноль — `ErrConfig`, выключено — `-1`; E11 правится под API бюджетом 10 (его
  граница). `LockState` несёт остаток по часам хранилища. subject длиннее предела — ключ по SHA-256, а не
  `ErrInvalidKey` (заметка шаблона: обход в `auth/infra/bruteforce/guard.go:24`); предел — свой, не btree
  (KDOC-09).
- **Входы и дефолты.** Endpoint S3 без схемы — TLS, plaintext — явно. `OTEL_TOKEN` или `Authorization` к `http://`
  не на loopback — ошибка старта без явного разрешения, как у grpcclient. Пароль длиннее 1 KiB — ошибка
  `Hash`/`Verify` до Argon2id (E05: не DoS, гигиена). auditkit: UTF-8 без NUL в строках и значениях `Metadata`,
  предел размера `Metadata` (E12); retention — job pgstore по явной настройке, по умолчанию аудит не удаляется.

## Handoff
При закрытии — в Handoff `harden-tests-archkit-release`. Что меняет потребитель на bump v1.4.0:
- **S1–S3.** `KEY=` — дефолт, пустое — `-`; App на `config.Env` — имена тегов; места — в Handoff второго плана.
- **pgkit.** `cmd/migrate`: `MigrateUpAll`/`MigrateDown(ctx, dsn, cfg.LockTimeout, …)`; хранилища без миграций —
  ошибка старта (`cmd/migrate` шаблона их накатывает); `PG_MIGRATE_LOCK_TIMEOUT` (5 с).
- **outbox.** Миграция 0003 (мигратор берёт её сам; своя копия `SchemaSQL` — колонка `trace_context JSONB`);
  метрики без `subject` (дашборды — `WithMetricSubject`); предел тела — по JSONB.
- **httpkit.** `IdempotencyStore.Set` с токеном; extractor — у схемы (`authgate` шаблона); дедлайн запроса;
  408/409/425/429 не воспроизводятся; trace-заголовки клиента — по настройке доверия.
- **bruteforcekit.** Бюджет субъекта, `CLEANUP_RETENTION` ≥ `LOCK_DURATION`, `Retry-After` — из `LockState`; обход
  длинного subject в адаптере не нужен.
- **authnkit и auditkit.** Пароль длиннее 1 KiB — ошибка (DTO — свой `maxLength`); NUL и не-UTF-8 в аудите —
  `ErrInvalidEntry`; retention — env; владелец сессии из `Revoke` — автор аудита выхода.
- **env для INFRA-HANDOFF:** новые переменные S4, S7 (`HTTPCLIENT_MAX_HOSTS`), S8 (`HTTP_REQUEST_TIMEOUT` <
  `HTTP_WRITE_TIMEOUT`), S9, S12, S13; `STORAGE_S3_USE_SSL`; OTLP вне loopback; Redis — 2× пул.

## Где мы сейчас
Этап 1 принят (`M/validation-harden-kits-contracts-A.md`); этап 2: S4–S8 закрыты (AC4–AC7), дальше S9.

Заготовка аннотации v1.4.0 (по шагам):
- **Breaking.** natskit: `NewConfigPrefixed` → `NewInstanceConfig`, `HealthProbe` → `ProvideProbe`, `NewBus(cfg,
  conn, log) (Bus, error)`, `NewStreamBus(cfg, conn, log)`. config: App — именами тегов (`POSTGRES_PG_DSN` →
  `PG_DSN`), поле без тега — ошибка `Load` (тег или `ignored:"true"`), тег на вложенной структуре — ошибка (был
  приставкой, `DB_PG_DSN`: снять тег, имена — `PG_*`; вторая секция того же кита — `ignored:"true"` и свой
  `config.Load(&a.X, config.EnvPrefixed("x"))`, незаданная `X_*` — из голой), поле `map`, `[]byte`, `url.URL` или
  типа только с `UnmarshalBinary`/`Set` — ошибка `Load` (строка с разбором в `Validate` или тип с `UnmarshalText`),
  нет `*envconfig.ParseError`. httpkit: `NewServer(cfg, log, opts…) (*Server, error)` ставит цепочку кита и
  `HumaAuth` (`WithAuthExtractor`, `WithIdempotencyStore`), `WithLogger` удалён; `ClientIP(hops, log)`,
  `HumaAuth(extract, log)`, `RegisterHealth(api, timeout, log, probes…)`, схема — `ProvideSecurityScheme(ctor)`;
  handlertest — с цепочкой кита. healthkit, timerkit: `Group` → `ProbesGroup`/`JobsGroup`, `Provide` →
  `ProvideProbe`/`ProvideJob`. pgkit: `ProvideMigrations` → `SupplyMigrations`; `MigrateUp/Down/UpAll(ctx, dsn,
  lockTimeout, …)` (> 0, команда миграций — `cfg.LockTimeout`); `Module` берёт `logkit.Logger`; литерал
  `MigrateRunnerConfig` без `LockTimeout` — `ErrConfig`; вложенный `WithTxOptions` строже внешней —
  `ErrNestedTxStricter`. authnkit/session/pgstore: `ErrSchema`, `Store.CheckSchema` → `pgkit.ErrSchema`,
  `pgkit.CheckSchema`. outboxkit: `New(name, cfg, …)`, `NewWorker(instance, cfg, …)` с ошибкой,
  `Default{BatchSize,TickTimeout,MaxAttempts}` удалены, `Repository.Insert(ctx, subject, payload, maxBytes)` — тело
  мерит хранилище (своё — по своему представлению), сверх — `ErrPayloadTooLarge`; `pgstore.SchemaSQL` — с колонкой
  `trace_context JSONB` (своя копия схемы её добавляет). minio: `NewStorage(cfg, client, urls, log)` с ошибкой.
  authnkit: `DefaultAccessTTL`, `DefaultLeeway` удалены, `Argon2idParams`/`NewArgon2idParams` →
  `Argon2idConfig`/`NewArgon2idConfig`; `pgstore.NewStore(cfg, db…)` у bruteforcekit и сессий. httpclient:
  `Validate` — value-приёмник, группа опций → значение `httpclient.Options`, литерал `Config` без `MaxHosts` —
  `ErrConfig` (`1024` или `-1`). storagekit: `NewKey` → `string`, `MemoryStorage.Bytes`/`Keys` удалены (`Get`).
  telemetry: `Headers.Decode` — без `ErrConfig`. cachekit: `Cache.CompareAndSet` (свой `Cache` — реализовать),
  `IdempotencyStore.Set(ctx, key, token, record, ttl) (bool, error)`. httpkit: `IdempotencyStore.Set(ctx, key,
  token, record, ttl) (stored bool, err error)` — запись только поверх резерва этого токена (свой store —
  `testkit/idempotencytest.RunContract`), литерал `Config` без `RequestTimeout` — `ErrConfig`
  (`HTTP_REQUEST_TIMEOUT` 25s меньше `HTTP_WRITE_TIMEOUT`, `RESERVE_TTL` больше; `-1s` — выкл.).
- **Поведение.** `KEY=` во всех китах и `config.Env` — дефолт; пустую строку или список задаёт `-`
  (`PG_MIGRATIONS_PATH=-`, `HTTP_HSTS=-`, `HTTP_QUIET_PATHS=-`); `<P>_HTTPCLIENT_BREAKER_*` читается, теневых имён
  нет; списки — без пустых, из одних разделителей (`JWT_AUDIENCE=,`) — `ErrConfig`, целые — десятичные; ошибка ENV —
  `<kit>.ErrConfig` с переменной (и у `tzkit.NewZone`). Литерал с нулями — `ErrConfig`: httpclient (таймаут, ретрай,
  SSE; `MaxRedirects 0` — без редиректов), natskit (таймауты, drain, потолки; JetStream — у включённого;
  `NakMaxBackoff 0` — постоянная задержка), `JWTConfig.AccessTTL`; `Leeway 0` — без допуска. Формат telemetry и
  JetStream — и у выключенного; невалидный `Config` у `storagekit.Module` и тик outbox ≥
  `PG_IDLE_IN_TX_SESSION_TIMEOUT` — ошибка графа. nil-опции ничего не меняют; hook'и `WithBeforeAttempt`
  складываются, второй `WithTransport` — `ErrConfig`; проба natskit — `natskit` или `natskit:<name>`; cleanup
  bruteforcekit и сессий, drain outbox — после миграций; префиксы ошибок `<kit>/<sub>:`, воркер outbox —
  `outboxkit:`, `telemetry: shutdown`, лог ошибок SDK — `telemetry: otel sdk error`. Хранилища китов (аудит,
  bruteforce, сессии, outbox и его инстансы) без своих миграций — ошибка старта `pgkit.ErrSchema`; дедлайн миграций
  прерывает идущий оператор, блокировку он ждёт ≤ `PG_MIGRATE_LOCK_TIMEOUT` (отказ — `dirty`); outbox `ordered` — по
  моменту вставки, а не началу транзакции; предел тела — по JSONB-тексту (числа в экспоненте у предела —
  `ErrPayloadTooLarge`); пул с занятым соединением на остановке — WARN и `nil` через 2/3 бюджета. Метрики outbox —
  без label `subject` (шаблон — `WithMetricSubject` инстанса), backlog-gauge'и — серия на инстанс; producer-спан
  natskit без шаблона — `natskit.publish`, `destination.template` — только шаблон; доставка события outbox — в
  трассе записавшего его запроса (ссылка на тик). cachekit: `SetNX`, `GetDel`, `CompareAndDelete` не повторяются —
  на сбое связи ошибка вместо ложного «нет» (`REDIS_MAX_RETRIES` — `Get`, `Set`, `Delete`, теги, `Ping`), соединений
  — до 2×`REDIS_POOL_SIZE`; `MemoryCache` и `httpkit.MemoryStore` — до 100 000 ключей (новый сверх — ошибка, без
  вытеснения); ответ запроса дольше `HTTP_IDEMPOTENCY_RESERVE_TTL` не сохраняется (WARN, `skipped`); httpclient:
  breaker и label `host` — у первых 1024 хостов (`HTTPCLIENT_MAX_HOSTS`), остальные — без breaker'а, `host="other"`.
  httpkit: тело сверх лимита в huma — 413, лимит операции выше общего поднимает транспортный, скрытая — по тем же
  хукам `OnAddOperation`; 408/409/425/429 и запросы без credentials идемпотентность не хранит; 401 — с
  `WWW-Authenticate`; HEAD на GET-маршрутах; ответ, не начатый к `HTTP_REQUEST_TIMEOUT`, — `503 REQUEST_TIMEOUT`
  (SSE — без дедлайна); CORS пускает `Idempotency-Key`; panic после заголовка обрывает соединение.
- **Изменения.** `httpkit.ProvideSecurityScheme`, `grpckit.ProvideService`, `httpclient.Options`; без envconfig;
  `pgkit.CheckSchema`, `CheckSchemaOnStart`, `ErrSchema`, `ErrNestedTxStricter`, `outboxkit.SchemaChecker`,
  `outboxkit.WithMetricSubject`, `Message.TraceParent`, `OutMsg.MetricSubject`/`TraceParent`, миграция outbox 0003;
  `natskit.WithTraceParent`, `StreamMsg.TraceParent`; `testkit/idempotencytest`, `Cache.CompareAndSet`,
  `cachekit.WithMaxKeys`, `ErrMemoryFull`, `HTTPCLIENT_MAX_HOSTS`; `HTTP_REQUEST_TIMEOUT`, код `REQUEST_TIMEOUT`.
