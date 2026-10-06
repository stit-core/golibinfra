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
- **Программа** — `go-backend-template/.claude/plans/audit-program.md`; находки — `M/kits-audit/summary.md`
  («Как повторить аудит») и отчёты углов (`M` — `/home/mm/.cache/stit-audit-program/`).
- **Состав партии 3:** 27 medium и 36 minor аудита, кандидаты C (длина пароля, retention `audit_log`),
  N1–N5 `M/validation-release-v1.3.0.md`, заметка шаблона про subject длиннее 255 байт.
- **Эксперименты** — `run.sh` углов в своём worktree на ревизии: FAIL — дефект есть; не собирается
  на новом API — правка под API без смены смысла (оригинал — `M/exs8/orig/*.v130`); тяжёлые — по одному.
- **Потребитель** — шаблон: bump v1.4.0 в `repair-harness` S7 (Handoff — во втором плане).
- **Правила шага:** README и `docs/` кита — тем же коммитом; breaking — `!`, `BREAKING CHANGE:` и строка
  заготовки аннотации; новых зависимостей нет; киты вне объёма правятся под API тем же коммитом.
- **Код:** outbox — `outboxkit/pgstore/store.go`, схема — `pgkit/schema.go`, ретраи —
  `cachekit/cache.go`, extractor — `httpkit/middleware.go`, `redactURL` — `natskit/conn.go`.

## Не входит
- Вторая половина партии 3 — `harden-tests-archkit-release`: fx (KFX-03, -05, -06, KOBS-08), сигналы
  (и httpkit в гейте), фейки, мутанты, `archkit`, гейт, обвязка, доки, повтор экспериментов, релиз.
- Маршрут после проверки старта httpkit и сырой тег группы timerkit — `backlog.md`; правки шаблона и
  шлюзов — по аннотации тега; push — дирижёр после PASS валидатора партии.

## Критерии приёмки
`<rev>` — проверяемый коммит, `<wt>` — worktree на нём; скрипты — от `M/kits-audit/`.
- [x] **AC1** — ENV читается одним способом: `KEY=` — как незаданная переменная в каждом ките и в
  `config.Env`; ошибка разбора несёт `ErrConfig`, ключ и префикс кита; `<P>_HTTPCLIENT_BREAKER_*`
  читается, теневых имён нет; выключение — явным значением, не пустой строкой. Проверка:
  `api-docs/scripts/run-go-experiments.sh <rev> envempty hcprefix` → U4 одна семантика, U5 читается;
  `api-docs/claims-A-exp/run.sh <wt>` (`parseerr`), `api-docs/scripts/claimsB/run.sh <wt>` → U6 21
  из 21 с `ErrConfig`; `httpkit-a/run.sh <rev> api` → U01 PASS, U02 — пустое = дефолт
  - Подтверждено: дерево S1 (`M/exc1/logs/`) — U4 87/87; U5 читается, теневых нет; U6 — 13/13, 11/11,
    прочие — `TestKitConfigs_*`; U01 PASS, U02 — 31 тег — дефолт
- [x] **AC2** — Невалидный конфиг не проходит ни один конструктор (`outboxkit.New`, `NewWorker`,
  `storagekit.Module`, `httpkit.NewServer` — с ошибкой), молча подставленных дефолтов нет; у
  `Validate`, нулей и nil-опций одна семантика; `BindAPI` без гонки; тик outbox дольше таймаута
  простоя транзакции пула — ошибка старта. Проверка: `api-docs/scripts/run-go-experiments.sh <rev>
  literal` → U3 16 из 16; `httpkit-a/run.sh <rev> security lifecycle` → H20, H41 PASS;
  `api-docs/scripts/claims-C/run.sh <wt>` → K2, T1 — отказ, C1 — без panic
  - Подтверждено: дерево S2 (`M/exc1/logs/`) — U3 16/16; H20, H41 PASS; K2, T1 — отказ, C1 — без panic
- [x] **AC3** — Одна форма API: константы групп, хелперы регистрации, пробы, логгер и порядок
  параметров — по правилу «Решений»; обе опции httpclient срабатывают в заданном порядке; ошибка
  графа httpkit называет функцию кита; job'ы со схемой стартуют после миграций; test-хелперы внутри
  китов — только исключения правила; README § Принципы — хранилища `pgstore` без неполного перечня.
  Проверка: `fx-observability/run.sh <rev> fx fx-docker` → KX50, KX61, KX61b PASS;
  `httpkit-b/run.sh <rev> exp` → в HX04 (a), (b) есть `httpkit.`; таблица единообразия
  `api-docs.md` пересчитана → без ✗ KAPI-06, KAPI-07
  - Подтверждено: дерево S3 (`M/exc1/logs`) — KX50 40/40, KX61, KX61b PASS; HX04 — `*httpkit.Server`; ✗ нет
- [x] **AC4** — pgkit и схемы китов: миграция под чужой блокировкой укладывается в дедлайн и держит
  читателей не дольше `lock_timeout`; `OnStop` пула уважает ctx; вложенная транзакция строже
  внешней — ошибка; хранилище кита без своих миграций не стартует и называет источник. Проверка:
  `correctness-security/run.sh <rev> pg` → E09 PASS; `fx-observability/run.sh <rev> fx-docker` →
  KX62, KX65 PASS
  - Подтверждено: дерево S4 (`M/exc2/logs/s4-*.log`) — E09 PASS: возврат 2,01 с, читатель 1,3 с;
    KX62 PASS: старт — `pgkit.ErrSchema` с источником; KX65 PASS: Stop 2 с, `nil`, хук после пула
- [ ] **AC5** — outbox: `ordered` публикует события агрегата в порядке его писателей; payload, который
  в JSONB больше предела, отвергается при вставке; потребитель события — в трассе запроса или со
  ссылкой на неё; число серий метрик outbox и имён producer-спанов не растёт с id в subject; новая
  миграция кита проходит лесенку. Проверка: `correctness-security/run.sh <rev> pg outbox-nats` → E10,
  E17 PASS; `fx-observability/run.sh <rev> obs` → KO11–KO14, KO21 PASS; `go test -race
  ./outboxkit/... ./natskit/...` с Docker → PASS
- [ ] **AC6** — Идемпотентность и кэш: `GetDel`, `SetNX` и резерв не теряют атомарности при сбое
  связи; запись идемпотентности ставит только владелец резерва; один контрактный набор проходят
  `httpkit.MemoryStore`, cachekit на памяти и на Redis; `MemoryCache` и breaker'ы httpclient
  ограничены. Проверка: `correctness-security/run.sh <rev> unit redis` → E07, E18 PASS;
  `go test -race ./testkit/... ./cachekit/... ./httpkit/...` с Docker → PASS
- [ ] **AC7** — Контракт HTTP: тело сверх лимита — 413 и при поднятом лимите операции, и у скрытой;
  ответ «повторите позже» не воспроизводится из хранилища идемпотентности; 401 — с
  `WWW-Authenticate`; HEAD на GET-маршрутах; анонимы не делят scope ключа; долгий хендлер — дедлайн
  ctx и 503 problem+json, не EOF; CORS пускает заголовок идемпотентности; panic после заголовка
  обрывает ответ. Проверка: `httpkit-a/run.sh <rev> contract security lifecycle` → H04, H06, H07,
  H10, H16, H17, H35 PASS
- [ ] **AC8** — Край httpkit: операцию открывает только её схема (Bearer не открывает `AdminKey`);
  extractor — один раз на запрос и при отказе бэкенда; `Security` со схемой вне `components` —
  ошибка старта и без схем в графе; trace-context и baggage клиента без явного доверия не
  принимаются; `ip:port` в XFF даёт ip; тест кладёт IP клиента в ctx без middleware. Проверка:
  `httpkit-a/run.sh <rev> security` → H15, H18 PASS; `httpkit-b/run.sh <rev> exp` → HX07 PASS
- [ ] **AC9** — Секреты: пароль и токен из URL и DSN не попадают в ошибки, логи и спаны natskit,
  cachekit, storagekit, telemetry и httpclient; маски logkit по умолчанию прячут base64url-токены и
  ссылки с ними, пароль DSN, коды вне словаря, поля структур и map под указателем в `Any`, JSON в
  `RawMessage` и `[]byte`, `otpauth`; счётчики и код ошибки `code` открыты. Проверка:
  `correctness-security/run.sh <rev> unit` → E02, E03, E04 PASS; `TestVal_RedactBypassMatrix`
  (`M/val-kits-A/experiments/logkit_zz_val_exp_test.go`) → `leaked=false`, кроме ключей из
  «Решений»; `api-docs/scripts/claims-C/run.sh <wt>` → urlleak без секретов
- [ ] **AC10** — bruteforcekit: проверок пароля одного subject за окно не больше бюджета субъекта при
  любом числе адресов; `CLEANUP_RETENTION` меньше `LOCK_DURATION` — `ErrConfig`; пауза отказа — по
  часам хранилища; ветка переучёта `Attempt` покрыта тестом; subject длиннее 255 байт учитывается.
  Проверка: `correctness-security/run.sh <rev> pg` → E11 PASS; `go test -race ./bruteforcekit/...
  ./testkit/bruteforcetest/` с Docker → PASS
- [ ] **AC11** — Входы и дефолты: endpoint S3 без схемы — TLS; токен OTLP по `http://` не на loopback
  без явного разрешения — ошибка старта; пароль длиннее предела отвергается до Argon2id; NUL,
  не-UTF-8 и `Metadata` сверх предела в записи аудита — `ErrInvalidEntry`; retention `audit_log` —
  job кита по явной настройке; отзыв сессии называет её владельца (аудит выхода с автором).
  Проверка: `correctness-security/run.sh <rev> unit pg` → E12 PASS, E08 — по «Решениям», E05 —
  отказ длинного пароля; `go test -race ./authnkit/... ./testkit/sessiontest/` с Docker → PASS

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
  - Проверка: `correctness-security/run.sh <rev> pg` → E10 PASS; тест предела с числами в экспоненте
    → отказ `Publish` · Закрывает: AC5 · Зависит от: S4
  - Сделано: E10 PASS (`[paid shipped]`), E17 PASS; `TestPublish_RejectsPayloadOverJSONBLimit`; мутанты
    `M/exc2/mut_s5.py` 7/7; тест «порядок по началу транзакции» удалён — решение обратное
- [ ] **S6** — Трасса и кардинальность outbox и natskit, миграция кита (KOBS-02, KOBS-03, KOBS-04)
  - Проверка: AC5 → выполнен · Закрывает: AC5 · Зависит от: S5
- [ ] **S7** — Атомарность cachekit, контракт идемпотентности, пределы (KCOR-04, -05, KTST-10, KSEC-06)
  - Проверка: AC6 → выполнен · Закрывает: AC6 · Зависит от: S3
- [ ] **S8** — Контракт HTTP httpkit (KCOR-09, KCOR-10, KCOR-11, KSEC-13, KSEC-14, KSEC-15, KAPI-09)
  - Проверка: AC7 → выполнен · Закрывает: AC7 · Зависит от: S7
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
- **ENV.** Ридер — `internal/envconf` у 35 конструкторов и `config.Env`, без envconfig. `KEY=` — как
  незаданная (U4: `PG_MIGRATIONS_PATH=` выключал миграции); пустое — `-`, список из одних разделителей —
  ошибка. Имя — `<P>_<TAG>`, голый `<TAG>` — где кит обещает; вложенная структура — без тега, с тем же
  префиксом (U5), тег на ней — ошибка (B1: секция молча пустела); вторая секция кита — свой `Load` с
  `EnvPrefixed` (подсказка в ошибке). Тип вне ридера (`map`, `[]byte`, `Set`, `UnmarshalBinary` —
  `url.URL`) — ошибка по типу: канон — строка с `Validate` или `UnmarshalText`. Поле без тега и `ignored`
  — ошибка; целые — десятичные.
- **Конфиг.** `Validate` — только проверка, value-приёмник; дефолты — в тегах; ноль — значение из доки
  или `ErrConfig`, не дефолт кита («без предела» у новых настроек — `-1`); формат — и у выключенного,
  обязательное — у включённого; nil-опции игнорируются; `withDefaults` outbox удалён. `NewServer(cfg,
  log, …) (*Server, error)` — сборка `Module` (цепочка кита, `HumaAuth` в `BindAPI`), handlertest — на
  нём (H51). Тик outbox против таймаута простоя транзакции — в `Module` и `Instance` при
  `pgkit.PoolConfig` в графе. Ноль, уходящий в драйвер (pgxpool, go-redis), — его значение из доки;
  `RedactConfig{}` — безопасный дефолт; киты вне объёма `withDefaults` сохраняют.
- **Формы.** Группа — `<Thing>Group` = `<kit>.<thing>` и `Provide<Thing>(ctor, anns…)` с проверкой типа
  (`healthkit.ProvideProbe`, `timerkit.ProvideJob`, `httpkit.ProvideSecurityScheme`,
  `grpckit.ProvideService`), значение — `Supply<Thing>` (`pgkit.SupplyMigrations`); проба инстанса —
  `natskit.ProvideProbe(name)`. Конструктор — `(cfg, зависимости…, log, opts…)`, функция над объектом
  берёт его первым; логгер — параметр, у handlertest — опция, `WithErrorLogger` telemetry — handler SDK.
  Опции httpclient — значение `Options`: hook'и складываются, второй транспорт — `ErrConfig` (KX50);
  `registerHealth` — `fx.In` (KFX-10); job'ы со схемой — после optional `pgkit.Migrated` (KX61b).
  Test-хелперы в ките — `MemoryCache`, `MemoryStorage`, `httpkit.MemoryStore`, фейки `clockkit`,
  `pgkit.WithTestTx`. hubkit — как есть (вне объёма).
- **pgkit.** Мигратор — `lock_timeout` = `PG_MIGRATE_LOCK_TIMEOUT` (5 с, ноль — `ErrConfig`), параметр
  `lockTimeout` у `MigrateUp/Down/UpAll` (команда миграций — семантика модуля); у advisory-lock — только
  дедлайн. Дедлайн — cancel request серверу с повтором до конца прогона: `StatementTimeout` драйвера не
  видит ctx, `GracefulStop` golang-migrate — гонка флага (`M/exc2/logs/s4-gracefulstop-race.log`), обрыв
  сокета ALTER не снимает (E09); упавшая миграция — `dirty`. `OnStop` пула ждёт 2/3 остатка, затем WARN и
  `nil` (KX65, правило httpkit), логгер — из графа. Вложенный строже внешней — `ErrNestedTxStricter`, у
  режима — `ReadOnly` в `ReadWrite`. `CheckSchema` — в pgkit, `CheckSchemaOnStart(src)` у хранилищ после
  optional `Migrated`, outbox — перед партициями, `Instance` — `outboxkit.SchemaChecker` (KX62). Фейк
  `pgtest` опции игнорирует — паритет у S3 второго плана.
- **outbox: порядок и предел.** `created_at` — `clock_timestamp()` вставки: при писателях,
  сериализованных блокировкой агрегата, вставка идёт после коммита соседа (E10); ключ партиций и `ORDER
  BY` те же. Предел — по представлению хранилища тем же `INSERT` (`octet_length` JSONB-текста): числа в
  экспоненте раздувают JSONB ×4 (N1 валидатора B). Мерит хранилище, не `Bus`: предел — параметр
  `Repository.Insert(…, maxBytes)` (у инстанса свой предел, фабрика его не знает); дефолт тот же.
- **outbox: трасса и кардинальность.** Строка хранит W3C-контекст запроса (колонка новой миграции
  обоих источников); producer-спан публикации — ребёнок этого контекста со ссылкой на тик, строки
  без контекста — как сейчас (KO13). Label `subject` метрик outbox и backlog-запроса — только через
  `WithMetricSubject(func)` инстанса, по умолчанию — `instance` (KO11, KO21); в доке — «subject —
  тип события, id — в payload». Producer-спан natskit — по шаблону, без него — `natskit.publish`
  или `natskit.publish <stream>`; outbox — атрибут `outbox.instance`, не `destination.template`.
- **Идемпотентность и кэш.** `SetNX`, `GetDel`, скрипты и резерв — клиентом без ретраев, `Get`,
  `Set`, `Delete` — с ретраями (E18). `IdempotencyStore.Set` — compare-and-set по токену резерва
  (E07): breaking интерфейса httpkit и обеих реализаций. Набор — `testkit/idempotencytest.RunContract`
  (имя — как у `storagetest`). `MemoryCache` — предел числа ключей; breaker'ы httpclient —
  ограниченный набор хостов, README: хосты — конечное множество.
- **Контракт HTTP.** `*http.MaxBytesError` в пути huma — 413, лимит операции выше общего поднимает и
  chi-лимит, скрытым — тот же лимит (H17). Не хранятся 408, 409, 425, 429: «не выполнено», повтор с
  ключом исполняет заново (H16). `WWW-Authenticate` — по схемам операции; HEAD — у GET-маршрутов,
  `/health` и `/ready` тоже. Без credentials — без дедупликации. Дедлайн запроса — настройка меньше
  `HTTP_WRITE_TIMEOUT`, включена по умолчанию (потоки SSE — без него), по ней — 503 problem+json;
  `ReserveTTL` меньше дедлайна — `ErrConfig` (H35). CORS по умолчанию пускает заголовок
  идемпотентности. panic после заголовка — `http.ErrAbortHandler` (H06).
- **Край httpkit.** Extractor — в `SecuritySchemeEntry`; операция открыта, если прошла одна из её
  альтернатив целиком; общий `AuthExtractor` графа удалён — один способ (H15). Идемпотентность —
  после маршрута и `HumaAuth`: знает операцию и субъект, extractor — один вызов (HX07), replay
  получает маршрут (KOBS-12). Входящий trace-context — ссылкой на новый корень, baggage
  отбрасывается; доверие — одна настройка, выключена (H18). XFF — `netip.ParseAddrPort`, затем адрес.
- **Секреты в URL.** Один `redactURL` в `internal/`: пароль userinfo, значения query, URL без схемы
  (`user:pass@host`), пароль keyword-DSN. httpclient: атрибуты попытки — на свой спан с очищенным
  URL, `*url.Error` — с очищенным URL (E03, E04); путь остаётся, README: секрет — не в пути.
- **Маски logkit.** Схема слов ключа (`classifyWords`) остаётся. Словарь секретов дополняют
  `pass`, `sid`, `signature`, `refresh`, `tokens`; `code` — слово-код (прячет и числа) только с
  квалификатором (`passcode`, `verification_code`, `auth_code`…); голый `code` — код ошибки httpkit
  (`httpkit/server.go:350`), открыт; ключ, который кончается словом-счётчиком (`otp_attempts`), —
  не код. Значения — маски base64url-токенов (порог — без ложных `TestVal_RedactHexFalsePositives`),
  пароля DSN, `otpauth://…secret=`; контейнеры — map под указателем, поля структур, JSON из
  `RawMessage`/`[]byte`.
- **bruteforcekit.** Второй бюджет — на subject по всем адресам (E11: 100 проверок с 20 адресов при
  `MaxFailures=5`): превышение — `LockState` с областью «субъект» и коротким сроком, потребитель
  отвечает как на блокировку пары. Пара блокируется строго, субъект — коротко: держать чужой
  аккаунт закрытым можно лишь непрерывным потоком неудач. Ноль — `ErrConfig`, выключено — `-1`; E11
  правится под API бюджетом 10 (его граница). `LockState` несёт остаток по часам хранилища.
  subject длиннее предела — ключ по SHA-256, а не `ErrInvalidKey` (заметка шаблона: обход в
  `auth/infra/bruteforce/guard.go:24`); предел — свой, не btree (KDOC-09).
- **Входы и дефолты.** Endpoint S3 без схемы — TLS, plaintext — явно. `OTEL_TOKEN` или
  `Authorization` к `http://` не на loopback — ошибка старта без явного разрешения, как у grpcclient.
  Пароль длиннее 1 KiB — ошибка `Hash`/`Verify` до Argon2id (E05: не DoS, гигиена). auditkit: UTF-8
  без NUL в строках и значениях `Metadata`, предел размера `Metadata` (E12); retention — job pgstore
  по явной настройке, по умолчанию аудит не удаляется.

## Handoff
При закрытии — в Handoff `harden-tests-archkit-release`. Что меняет потребитель на bump v1.4.0:
- **ENV, конструкторы и формы (S1–S3).** `KEY=` — дефолт, пустое — `-`; App на `config.Env` — имена
  тегов, без тега на вложенной структуре; правки шаблона по местам — уже в Handoff второго плана.
- **pgkit.** `cmd/migrate`: `MigrateUpAll`/`MigrateDown(ctx, dsn, cfg.LockTimeout, …)`; хранилища без
  миграций — ошибка старта (`cmd/migrate` шаблона их накатывает); `PG_MIGRATE_LOCK_TIMEOUT` (5 с).
- **outbox.** Новая миграция кита (мигратор берёт её сам); метрики без `subject` (`WithMetricSubject`);
  предел тела — по JSONB (событие с числами в экспоненте у предела теперь `ErrPayloadTooLarge`).
- **httpkit.** `IdempotencyStore.Set` с токеном; extractor — у схемы (`authgate` шаблона); дедлайн
  запроса; 408/409/425/429 не воспроизводятся; trace-заголовки клиента — по настройке доверия.
- **bruteforcekit.** Бюджет субъекта, `CLEANUP_RETENTION` ≥ `LOCK_DURATION`, `Retry-After` — из
  `LockState`; обход длинного subject в адаптере не нужен.
- **authnkit и auditkit.** Пароль длиннее 1 KiB — ошибка (DTO — свой `maxLength`); NUL и не-UTF-8
  в аудите — `ErrInvalidEntry`; retention — env; владелец сессии из `Revoke` — автор аудита выхода.
- **env для INFRA-HANDOFF:** новые переменные S4, S8, S9, S12, S13; `STORAGE_S3_USE_SSL`; OTLP вне loopback.

## Где мы сейчас
Этап 1 (S1–S3) принят валидатором A (`M/validation-harden-kits-contracts-A.md`), мелочи перепроверки —
`fix(envconf)!`; этап 2: S4 (AC4), S5 закрыты, дальше S6.

Заготовка аннотации v1.4.0 (по шагам):
- **Breaking.** natskit: `NewConfigPrefixed` → `NewInstanceConfig`, `HealthProbe` → `ProvideProbe`,
  `NewBus(cfg, conn, log) (Bus, error)`, `NewStreamBus(cfg, conn, log)`. config: App — именами тегов
  (`POSTGRES_PG_DSN` → `PG_DSN`), поле без тега — ошибка `Load` (тег или `ignored:"true"`), тег на вложенной
  структуре — ошибка (был приставкой, `DB_PG_DSN`: снять тег, имена — `PG_*`; вторая секция того же кита —
  `ignored:"true"` и свой `config.Load(&a.X, config.EnvPrefixed("x"))`, незаданная `X_*` — из голой), поле
  `map`, `[]byte`, `url.URL` или типа только с `UnmarshalBinary`/`Set` — ошибка `Load` (строка с разбором в
  `Validate` или тип с `UnmarshalText`), нет `*envconfig.ParseError`. httpkit: `NewServer(cfg, log, opts…)
  (*Server, error)` ставит цепочку кита и `HumaAuth` (`WithAuthExtractor`, `WithIdempotencyStore`),
  `WithLogger` удалён; `ClientIP(hops, log)`, `HumaAuth(extract, log)`, `RegisterHealth(api, timeout, log,
  probes…)`, схема — `ProvideSecurityScheme(ctor)`; handlertest — с цепочкой кита. healthkit, timerkit:
  `Group` → `ProbesGroup`/`JobsGroup`, `Provide` → `ProvideProbe`/`ProvideJob`. pgkit: `ProvideMigrations` →
  `SupplyMigrations`; `MigrateUp/Down/UpAll(ctx, dsn, lockTimeout, …)` (> 0, команда миграций —
  `cfg.LockTimeout`); `Module` берёт `logkit.Logger`; литерал `MigrateRunnerConfig` без `LockTimeout` —
  `ErrConfig`; вложенный `WithTxOptions` строже внешней — `ErrNestedTxStricter`. authnkit/session/pgstore:
  `ErrSchema`, `Store.CheckSchema` → `pgkit.ErrSchema`, `pgkit.CheckSchema`. outboxkit: `New(name, cfg, …)`,
  `NewWorker(instance, cfg, …)` с ошибкой, `Default{BatchSize,TickTimeout,MaxAttempts}` удалены,
  `Repository.Insert(ctx, subject, payload, maxBytes)` — тело мерит хранилище (своё — по своему
  представлению), сверх — `ErrPayloadTooLarge`. minio: `NewStorage(cfg, client, urls, log)` с ошибкой.
  authnkit: `DefaultAccessTTL`, `DefaultLeeway` удалены, `Argon2idParams`/`NewArgon2idParams` →
  `Argon2idConfig`/`NewArgon2idConfig`; `pgstore.NewStore(cfg, db…)` у bruteforcekit и сессий. httpclient:
  `Validate` — value-приёмник, группа опций → значение `httpclient.Options`. storagekit: `NewKey` →
  `string`, `MemoryStorage.Bytes`/`Keys` удалены (`Get`). telemetry: `Headers.Decode` — без `ErrConfig`.
- **Поведение.** `KEY=` во всех китах и `config.Env` — дефолт; пустую строку или список задаёт `-`
  (`PG_MIGRATIONS_PATH=-`, `HTTP_HSTS=-`, `HTTP_QUIET_PATHS=-`); `<P>_HTTPCLIENT_BREAKER_*` читается,
  теневых имён нет; списки — без пустых, из одних разделителей (`JWT_AUDIENCE=,`) — `ErrConfig`, целые —
  десятичные; ошибка ENV — `<kit>.ErrConfig` с переменной (и у `tzkit.NewZone`). Литерал с нулями —
  `ErrConfig`: httpclient (таймаут, ретрай, SSE; `MaxRedirects 0` — без редиректов), natskit (таймауты,
  drain, потолки; JetStream — у включённого; `NakMaxBackoff 0` — постоянная задержка),
  `JWTConfig.AccessTTL`; `Leeway 0` — без допуска. Формат telemetry и JetStream — и у выключенного;
  невалидный `Config` у `storagekit.Module` и тик outbox ≥ `PG_IDLE_IN_TX_SESSION_TIMEOUT` — ошибка графа.
  nil-опции ничего не меняют; hook'и `WithBeforeAttempt` складываются, второй `WithTransport` — `ErrConfig`;
  проба natskit — `natskit` или `natskit:<name>`; cleanup bruteforcekit и сессий, drain outbox — после
  миграций; префиксы ошибок `<kit>/<sub>:`, воркер outbox — `outboxkit:`, `telemetry: shutdown`, лог ошибок
  SDK — `telemetry: otel sdk error`. Хранилища китов (аудит, bruteforce, сессии, outbox и его инстансы) без
  своих миграций — ошибка старта `pgkit.ErrSchema`; дедлайн миграций прерывает идущий оператор, блокировку
  он ждёт ≤ `PG_MIGRATE_LOCK_TIMEOUT` (отказ — `dirty`); outbox `ordered` — по моменту вставки, а не началу
  транзакции; предел тела — по JSONB-тексту (числа в экспоненте у предела — `ErrPayloadTooLarge`); пул с
  занятым соединением на остановке — WARN и `nil` через 2/3 бюджета.
- **Изменения.** `httpkit.ProvideSecurityScheme`, `grpckit.ProvideService`, `httpclient.Options`; без envconfig;
  `pgkit.CheckSchema`, `CheckSchemaOnStart`, `ErrSchema`, `ErrNestedTxStricter`, `outboxkit.SchemaChecker`.
