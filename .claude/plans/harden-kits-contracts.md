---
status: in-progress
parent: go-backend-template:audit-program
blocked-by: —
---

# harden-kits-contracts — партия 3, часть 1: конфиг и формы API, данные, httpkit, секреты, дефолты

## Цель
Первая половина партии 3 — тега v1.4.0, после которого каждый угол аудита китов ≥ 9 (выше minor ничего, эксперименты
чисты): medium и minor углов 1–3 — ENV, конфиги, формы API, pgkit, outbox, идемпотентность, httpkit, секреты, дефолты.

## Контекст
- **Программа** — `go-backend-template/.claude/plans/audit-program.md`; находки — `M/kits-audit/summary.md` («Как
  повторить аудит») и отчёты углов (`M` — `/home/mm/.cache/stit-audit-program/`).
- **Состав партии 3:** 27 medium и 36 minor аудита, кандидаты C, N1–N5 `M/validation-release-v1.3.0.md`, 255 байт шаблона.
- **Эксперименты** — `run.sh` углов в своём worktree: FAIL — дефект; правка под API без смены смысла (оригинал рядом).
- **Потребитель** — шаблон: bump v1.4.0 в `repair-harness` S7 (Handoff — во втором плане).
- **Правила шага:** доки и киты вне объёма под API — тем же коммитом; breaking — `!` и строка аннотации; deps — нет.

## Не входит
- Вторая половина партии 3 — `harden-tests-archkit-release` (fx, сигналы, фейки, мутанты, `archkit`, гейт, релиз).
- Хвосты — `backlog.md`; правки шаблона и шлюзов — по аннотации тега; push — дирижёр после PASS валидатора партии.

## Критерии приёмки
`<rev>` — проверяемый коммит, `<wt>` — worktree на нём; скрипты — от `M/kits-audit/`.
- [x] **AC1** — ENV одним способом: `KEY=` — как незаданная в каждом ките и в `config.Env`; ошибка разбора —
  `ErrConfig`, ключ и префикс кита; `<P>_HTTPCLIENT_BREAKER_*` читается, теневых имён нет; выключение — явным
  значением. Проверка: `api-docs/scripts/run-go-experiments.sh <rev> envempty hcprefix` → U4, U5;
  `api-docs/claims-A-exp/run.sh <wt>` (`parseerr`), `api-docs/scripts/claimsB/run.sh <wt>` → U6 21/21 с
  `ErrConfig`; `httpkit-a/run.sh <rev> api` → U01 PASS, U02 — пустое = дефолт
  - Подтверждено: дерево S1 (`M/exc1/logs/`) — U4 87/87, U5, U6 (13/13, 11/11, `TestKitConfigs_*`), U01; U02 — 31 тег
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
  - Подтверждено: дерево S8 (`M/exc2/logs/s8-*.log`) — семь PASS; фикс B1 — T2–T5, T8 валидатора чисты (`M/hkc3e`)
- [x] **AC8** — Край httpkit: операцию открывает только её схема (Bearer не открывает `AdminKey`); extractor —
  один раз на запрос и при отказе бэкенда; `Security` со схемой вне `components` — ошибка старта и без схем в
  графе; trace-context и baggage клиента без явного доверия не принимаются; `ip:port` в XFF даёт ip; тест кладёт
  IP клиента в ctx без middleware. Проверка: `httpkit-a/run.sh <rev> security` → H15, H18 PASS; `httpkit-b/run.sh
  <rev> exp` → HX07 PASS
  - Подтверждено: дерево S9 (`M/exc2/logs/s9-*.log`) — H15, H18 PASS, HX07 — 1 вызов (503); httpkit-a — без FAIL
- [x] **AC9** — Секреты: пароль и токен из URL и DSN не попадают в ошибки, логи и спаны natskit, cachekit,
  storagekit, telemetry и httpclient; маски logkit по умолчанию прячут base64url-токены и ссылки с ними, пароль
  DSN, коды вне словаря, поля структур и map под указателем в `Any`, JSON в `RawMessage` и `[]byte`, `otpauth`;
  счётчики и код ошибки `code` открыты. Проверка: `correctness-security/run.sh <rev> unit` → E02, E03, E04 PASS;
  `TestVal_RedactBypassMatrix` (`M/val-kits-A/experiments/logkit_zz_val_exp_test.go`) → `leaked=false`, кроме
  ключей из «Решений»; `api-docs/scripts/claims-C/run.sh <wt>` → urlleak без секретов
  - Подтверждено: E02–E04 PASS, urlleak чист; матрица — `leaked=false`, кроме голого `code` (`M/hkc3e/logs/s1[01]-*`)
- [x] **AC10** — bruteforcekit: проверок пароля одного subject за окно не больше бюджета субъекта при любом числе
  адресов; `CLEANUP_RETENTION` меньше `LOCK_DURATION` — `ErrConfig`; пауза отказа — по часам хранилища; ветка
  переучёта `Attempt` покрыта тестом; subject длиннее 255 байт учитывается. Проверка: `correctness-security/run.sh
  <rev> pg` → E11 PASS; `go test -race ./bruteforcekit/... ./testkit/bruteforcetest/` с Docker → PASS
  - Подтверждено: E11 PASS — 10 и 10 проверок с 20 адресов (`M/hkc3e/logs/s12-e11.log`); test-full PASS
- [x] **AC11** — Входы и дефолты: endpoint S3 без схемы — TLS; токен OTLP по `http://` не на loopback без явного
  разрешения — ошибка старта; пароль длиннее предела отвергается до Argon2id; NUL, не-UTF-8 и `Metadata` сверх
  предела в записи аудита — `ErrInvalidEntry`; retention `audit_log` — job кита по явной настройке; отзыв сессии
  называет её владельца (аудит выхода с автором). Проверка: `correctness-security/run.sh <rev> unit pg` → E12
  PASS, E08 — по «Решениям», E05 — отказ длинного пароля; `go test -race ./authnkit/... ./testkit/sessiontest/` с
  Docker → PASS
  - Подтверждено: дерево S13 (`M/hkc3e/logs/{unit,pg}-acf3865.log`) — E12 (3 × `ErrInvalidEntry`), E09–E14 PASS; E08: S3
    `tls=true`, токен к `http://collector` — `ErrConfig`, loopback и `ALLOW` — нет; E05: > 1 KiB — отказ; test-full PASS

## Шаги
### Конфиг и формы API
- [x] **S1** — Один ридер ENV в `internal/` — для всех `New*Config` и `config.Env` (KAPI-02, -04, KDOC-03)
  - Проверка: AC1 → выполнен · Закрывает: AC1 · Зависит от: —
  - Сделано: `69e5146`; AC1 → PASS; мутанты `M/exc1/mut_s1.py` 14/15 (M15 эквивалентен); B1 — `fix(envconf)`
- [x] **S2** — Конструкторы зовут `Validate`, одна семантика нулей и опций (KAPI-01, -05, -08, KCOR-12)
  - Проверка: AC2 → выполнен · Закрывает: AC2 · Зависит от: S1
  - Сделано: `51fc02d`; AC2 → PASS; мутанты `M/exc1/mut_s2.py` 18/18; эксперименты правлены под API
- [x] **S3** — Одна форма API, опции httpclient значением (KAPI-06, -07, KTST-07, KFX-01, -10; N5 валидатора v1.3.0)
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
- [x] **S9** — Край httpkit (KSEC-08, KFX-11, KSEC-12; N7–N9, N11, f1-07 валидатора B `harden-gate-defaults`)
  - Проверка: AC8 → выполнен · Закрывает: AC8 · Зависит от: S8
  - Сделано: AC8 → PASS; мутанты `M/exc2/mut_s9.py` 12/12; под API: zzka2 (`bearerAuth`, H15, H18, H40, H52), zzhb

### Секреты и дефолты
- [x] **S10** — URL и DSN без секретов в ошибках, логах и спанах, `SECURITY.md` к факту (KDOC-02, KSEC-03)
  - Проверка: `correctness-security/run.sh <rev> unit` → E03, E04 PASS; claims-C urlleak — чисто ·
    Закрывает: AC9 · Зависит от: S1
  - Сделано: E03, E04 PASS, urlleak и claimsB redact чисты (`M/hkc3e/logs/s10-*`); мутанты `M/hkc3e/mut_s10.py` 14/14
- [x] **S11** — Маски logkit (бэклог `logkit-redact-gaps`, `logkit-redact-containers`, `-otpauth`)
  - Проверка: AC9 → выполнен · Закрывает: AC9 · Зависит от: S10
  - Сделано: AC9 → PASS; мутанты `M/hkc3e/mut_s11.py` 18/18; цена записи без секрета — ×1.7–2.6 (потолок ×3)
- [x] **S12** — bruteforcekit (KSEC-04, KSEC-07, бэклог `bruteforcekit-*`, заметки шаблона про 255 байт и NUL)
  - Проверка: AC10 → выполнен · Закрывает: AC10 · Зависит от: S4
  - Сделано: AC10 → PASS; мутанты `M/hkc3e/mut_s12.py` 17/17; E11 правлен под API (бюджет 10 — его граница)
- [x] **S13** — Входы и дефолты: S3, OTLP, пароль, аудит, retention (KSEC-05, KCOR-06, C; N4 v1.3.0 — `Revoke`)
  - Проверка: AC11 → выполнен · Закрывает: AC11 · Зависит от: S4
  - Сделано: AC11 → PASS; мутанты `M/hkc3e/mut_s13.py` 38/38; под решение: E05 (отказ > 1 KiB), E08 (без `UseSSL`, OTLP)

### Итог
- [ ] **S14** — Итог: эксперименты AC1–AC11 на HEAD — PASS (таблица — в «Где мы сейчас»), `make
  gate-clean` зелёный; Handoff и заготовка аннотации — в `harden-tests-archkit-release`; план удалён
  - Проверка: `make gate-clean` → зелёный · Закрывает: — · Зависит от: S6, S9, S11, S12, S13

## Решения
- **ENV и конфиг.** `internal/envconf`: `KEY=` — как незаданная (U4), пустое — `-`; `<P>_<TAG>`, вложенная — без тега
  (U5, B1), вторая секция — `EnvPrefixed`; поле без тега — ошибка; целые — десятичные. `Validate` — проверка, дефолты
  — в тегах, ноль — значение доки или `ErrConfig` («без предела» — `-1`), формат — и у выключенного; nil-опции — no-op.
- **Формы.** Группа — `<Thing>Group` = `<kit>.<thing>`, `Provide<Thing>` с проверкой типа, значение — `Supply<Thing>`;
  конструктор — `(cfg, зависимости…, log, opts…)`; опции httpclient — `Options` (KX50); job'ы со схемой — после `Migrated`.
- **pgkit.** `lock_timeout` мигратора = `PG_MIGRATE_LOCK_TIMEOUT` (5 с), у lock'а `MigrateUpAll` — только дедлайн;
  дедлайн — cancel request до конца прогона (E09; `GracefulStop` — гонка, `M/exc2/logs/s4-gracefulstop-race.log`).
  `OnStop` пула — 2/3 остатка (KX65); `ErrNestedTxStricter`; `CheckSchemaOnStart` — после `Migrated` (KX62).
- **outbox.** `created_at` — `clock_timestamp()` (E10); предел — по JSONB тем же `INSERT`; trace-context — колонка 0003,
  producer-спан — ребёнок со ссылкой на тик (KO13); label `subject` — только `WithMetricSubject` (KO11, KO21).
- **Идемпотентность и кэш.** `SetNX`, `GetDel`, `Compare*` — без повторов (E18); `Set` — CAS по токену (E07), набор —
  `idempotencytest`. Предел — отказ: `WithMaxKeys`, `MemoryStore` — 100 000, httpclient `MaxHosts` 1024 (`-1`), сверх — `other`.
- **Контракт HTTP.** Один `MaxBytesReader`, лимит операции и скрытой — хуком (H17); не хранятся 408/409/425/429 (H16) и
  анонимы; `WWW-Authenticate` — хук по `components`; HEAD — свой middleware; CORS — `Idempotency-Key`. Дедлайн
  `HTTP_REQUEST_TIMEOUT` (25s < `WriteTimeout` < `ReserveTTL`, `-1s` — выкл.) над `Idempotency` (H35): целый 503 с
  `Connection: close` и в гонке с таймером, запись после него — `len(b), nil` (B1: huma паникует на ошибке); без
  дедлайна — только GET-поток (N5); panic после заголовка — Flush и `http.ErrAbortHandler` (H06).
- **Край httpkit.** Extractor — `SecuritySchemeEntry.Extract` (без него — `ErrAuthNotConfigured`); операцию открывает
  альтернатива целиком, `{}` — последней (N2), из одних `{}` — `ErrRoutes`; отказ — 503 бэкенда, иначе `APIError`
  extractor'а, иначе 401; `AuthExtractor`, `WithAuth` удалены (H15), схема вне `components` — `ErrRoutes` (N8);
  `Idempotency` — после `HumaAuth` (HX07); trace-context — ссылкой, без baggage (H18); XFF — адрес, `ParseAddrPort` (N9).
- **Секреты в URL.** `internal/redact.URL`: userinfo и значения query — `xxxxx` (маркер pgx), фрагмент снят, путь
  остаётся; без схемы — userinfo до последнего `@`; `ParseError` без ввода. httpclient — свой спан попытки и
  `*url.Error` с очищенным URL (E03, E04); `STORAGE_PUBLIC_BASE_URL` с кредами — `ErrConfig`; OTLP-заголовки — по ключу.
- **Маски logkit.** Слова ключа: `signature` — суффиксом, `pass`, `sid`, `refresh`, `tokens` — словом; код — `passcode`
  и `code` после квалификатора (голый `code` — код ошибки httpkit, открыт), счётчик (`otp_attempts`) — не код. Ключи в
  `Any` — по его JSON (поля, указатели, `MarshalJSON`, `RawMessage`/`[]byte`). Маски значений: base64url ≥ 20 с обоими
  регистрами и цифрой, пароль userinfo, `ключ=значение` с ключом-секретом (DSN, query, `otpauth`).
- **bruteforcekit.** Бюджет субъекта по всем адресам (E11) — строка `(subject, '')` той же таблицы, `SUBJECT_MAX_FAILURES`
  20 (`-1` — выкл.), `SUBJECT_LOCK_DURATION` 1m; один оператор (`FOR UPDATE` строки субъекта, upsert пары и субъекта),
  успех снимает оба; `LockState.RetryAfter` по часам базы и `Scope`; > 255 байт — `SHA256:<hex>`, NUL и не-UTF-8 —
  `ErrInvalidKey` (`nul-guard-key`); точка теста `lockSeen`.
- **Входы и дефолты.** S3 без схемы — TLS, plaintext — `http://`; `STORAGE_S3_USE_SSL` удалён (bool-дефолт `true`
  нарушил бы «ноль литерала — дефолт доки»). `OTEL_TOKEN`/`Authorization` к `http://` не на loopback — `ErrConfig` и у
  выключенного, без `OTEL_ALLOW_INSECURE_TOKEN` (как grpcclient). Пароль > 1 KiB в Argon2id не идёт: `Hash` — отказ,
  `Verify` — после `VerifyDummy`: длина не выдаёт логин таймингом (E05: гигиена). auditkit: строки — UTF-8 без NUL;
  `Metadata` — по JSON (`MetadataJSON`, его пишет pgstore): ≤ 64 КиБ, без `\u0000`, UTF-8; не-UTF-8 строки — U+FFFD
  от `encoding/json`, запись не теряется. Retention — opt-in `RetentionModule` (`AUDIT_RETENTION_DAYS` без дефолта, [1,
  36500], раз в час, батч 5000). `Revoke` → `(subject, err)`: владелец — у вызова, закрывшего сессию, иначе `""`. Рядом:
  255 у auditkit и `session` — предел кита (KDOC-09); README auditkit — дедлайн миграций по S4; фикстура MinIO — `http://`.

## Handoff
При закрытии — в Handoff `harden-tests-archkit-release`. Что меняет потребитель на bump v1.4.0:
- **S1–S3.** `KEY=` — дефолт, пустое — `-`; App на `config.Env` — имена тегов; места — в Handoff второго плана.
- **pgkit.** `cmd/migrate`: `MigrateUpAll`/`MigrateDown(ctx, dsn, cfg.LockTimeout, …)`; хранилища без миграций —
  ошибка старта (`cmd/migrate` шаблона их накатывает); `PG_MIGRATE_LOCK_TIMEOUT` (5 с).
- **outbox.** Миграция 0003 (своя `SchemaSQL` — `trace_context JSONB`), метрики без `subject`, предел тела — JSONB.
- **httpkit.** `IdempotencyStore.Set` с токеном; extractor — у схемы (`authgate/module.go`); `handlertest.WithAuth` →
  `WithSecuritySchemes`: `handler_test.go` auth и user, `scripts/new-domain.sh:662`, `rules/tests-pipeline.md:47`,
  doc `identitytest.go:47`; `make gen` (`REQUEST_TIMEOUT` в `openapi.json`); `Security` из одних `{}` — `Public`.
- **bruteforcekit.** `limiter_test.go`: литерал `Config` — `Subject*`, `CleanupRetention` ≥ `LockDuration`; Retry-After
  — `lock.RetryAfter`; обход длинного subject в адаптере не нужен, NUL — `ErrInvalidKey` (его маппинг — уже есть).
- **logkit.** Маски шире (S11): тест, который ищет в логе `password=…`, base64url-токен или пароль URL, — на маркер.
- **S13.** `Revoke` → `(subject, err)`: `internal/domain/auth/usecase.go:199,213` (Logout — аудит `logout` с `ActorID:
  subject`, если не `""`), обёртки `usecase_test.go:235,240`; DTO пароля — `maxLength:"1024"`: `auth/delivery/http/dto.go:8,
  38,39`, `user/delivery/http/dto.go:11`, `verification/delivery/http/dto.go:12`; ввод в поля аудита — UTF-8 без NUL.
- **env для INFRA-HANDOFF:** `PG_MIGRATE_LOCK_TIMEOUT`, `HTTPCLIENT_MAX_HOSTS`, `HTTP_REQUEST_TIMEOUT` (меньше
  `HTTP_WRITE_TIMEOUT` и `HTTP_IDEMPOTENCY_RESERVE_TTL`, иначе старт падает), `HTTP_TRUST_TRACE_CONTEXT`, S12, S13;
  `HTTP_CORS_HEADERS` + `Idempotency-Key` (свой список — дописать); `STORAGE_S3_USE_SSL` удалён (plaintext — `http://`);
  токен OTLP к `http://` не на loopback — https или `OTEL_ALLOW_INSECURE_TOKEN=true`; `AUDIT_RETENTION_DAYS`; Redis — 2× пул.

## Где мы сейчас
Этапы 1–2 приняты (фикс B — `3dea79c`), S10–S13 сделаны — дальше валидатор этапа 3, затем S14. Заготовка аннотации:
- **Breaking.** natskit: `NewConfigPrefixed` → `NewInstanceConfig`, `HealthProbe` → `ProvideProbe`, `NewBus(cfg, conn,
  log) (Bus, error)`, `NewStreamBus(cfg, conn, log)`. config: App — именами тегов (`POSTGRES_PG_DSN` → `PG_DSN`), поле
  без тега — ошибка `Load` (тег или `ignored:"true"`), тег на вложенной структуре — ошибка (был приставкой,
  `DB_PG_DSN`: снять тег, имена — `PG_*`; вторая секция того же кита — `ignored:"true"` и свой `config.Load(&a.X,
  config.EnvPrefixed("x"))`, незаданная `X_*` — из голой), поле `map`, `[]byte`, `url.URL` или типа только с
  `UnmarshalBinary`/`Set` — ошибка `Load` (строка с разбором в `Validate` или тип с `UnmarshalText`), нет
  `*envconfig.ParseError`. httpkit: `NewServer(cfg, log, opts…) (*Server, error)` ставит цепочку кита и `HumaAuth`
  (`WithSecuritySchemes`, `WithIdempotencyStore`), `WithLogger` удалён; `ClientIP(hops, log)`, `RegisterHealth(api,
  timeout, log, probes…)`, схема — `ProvideSecurityScheme(ctor)`; handlertest — с цепочкой кита. healthkit, timerkit:
  `Group` → `ProbesGroup`/`JobsGroup`, `Provide` → `ProvideProbe`/`ProvideJob`. pgkit: `ProvideMigrations` →
  `SupplyMigrations`; `MigrateUp/Down/UpAll(ctx, dsn, lockTimeout, …)` (> 0, команда миграций — `cfg.LockTimeout`);
  `Module` берёт `logkit.Logger`; литерал `MigrateRunnerConfig` без `LockTimeout` — `ErrConfig`; вложенный
  `WithTxOptions` строже внешней — `ErrNestedTxStricter`. authnkit/session/pgstore: `ErrSchema`, `Store.CheckSchema` →
  `pgkit.ErrSchema`, `pgkit.CheckSchema`. outboxkit: `New(name, cfg, …)`, `NewWorker(instance, cfg, …)` с ошибкой,
  `Default{BatchSize,TickTimeout,MaxAttempts}` удалены, `Repository.Insert(ctx, subject, payload, maxBytes)` — тело
  мерит хранилище (своё — по своему представлению), сверх — `ErrPayloadTooLarge`; `pgstore.SchemaSQL` — с колонкой
  `trace_context JSONB` (своя копия схемы её добавляет). minio: `NewStorage(cfg, client, urls, log)` с ошибкой.
  authnkit: `DefaultAccessTTL`, `DefaultLeeway` удалены, `Argon2idParams`/`NewArgon2idParams` →
  `Argon2idConfig`/`NewArgon2idConfig`; `pgstore.NewStore(cfg, db…)` у bruteforcekit и сессий. httpclient: `Validate`
  — value-приёмник, группа опций → значение `httpclient.Options`, литерал `Config` без `MaxHosts` — `ErrConfig`
  (`1024` или `-1`). storagekit: `NewKey` → `string`, `MemoryStorage.Bytes`/`Keys` удалены (`Get`). telemetry:
  `Headers.Decode` — без `ErrConfig`. cachekit: `Cache.CompareAndSet` (свой `Cache` — реализовать),
  `IdempotencyStore.Set(ctx, key, token, record, ttl) (bool, error)`. httpkit: `IdempotencyStore.Set(ctx, key, token,
  record, ttl) (stored bool, err error)` — запись только поверх резерва этого токена (свой store —
  `testkit/idempotencytest.RunContract`), литерал `Config` без `RequestTimeout` — `ErrConfig` (`HTTP_REQUEST_TIMEOUT`
  25s меньше `HTTP_WRITE_TIMEOUT`, `RESERVE_TTL` больше; `-1s` — выкл.). httpkit (S9): extractor —
  `SecuritySchemeEntry.Extract` (поле-функция: `SecuritySchemeEntry` несравним — `==` и ключ map не компилируются),
  `AuthExtractor` в графе и `WithAuthExtractor` удалены, `HumaAuth(schemes, log)`, `Idempotency(cfg, store, log)`
  (`IdempotencyOption` и `WithAuth` удалены), `OTel(trust, quiet…)`; схема `Security` вне `components` и `Security` из
  одних `{}` — ошибка старта (`{}` — `httpkit.Public`); идемпотентность — только у операций huma: маршрут мимо huma с
  `Idempotency-Key` исполняется каждый раз — операция huma или обёртка `httpkit.Idempotency(cfg, store, log)`;
  handlertest: `WithAuth` → `WithSecuritySchemes` со схемой с `Extract`. oidckit, hubkit/sso: `Module` не кладёт
  `httpkit.AuthExtractor` — схемы с extractor'ом (hubkit — cookie своего контура). bruteforcekit (S12): литерал
  `Config` без `SubjectMaxFailures` (`20` или `SubjectBudgetOff`) и `SubjectLockDuration`, а `CleanupRetention` меньше
  `LockDuration` — `ErrConfig`. storagekit (S13): `Config.UseSSL` (`STORAGE_S3_USE_SSL`) удалён — endpoint без схемы —
  TLS, plaintext — `http://host:port`. telemetry: `OTEL_TOKEN` или `Authorization` к `http://` не на loopback — `ErrConfig`
  (https или `OTEL_ALLOW_INSECURE_TOKEN=true`). authnkit/session: `Store.Revoke(ctx, refresh) (subject string, err error)`
  — свой `Store` и обёртки отдают владельца закрытой этим вызовом сессии (`""` — токен неизвестен или закрыт раньше).
- **Поведение.** `KEY=` во всех китах и `config.Env` — дефолт; пустую строку или список задаёт `-`
  (`PG_MIGRATIONS_PATH=-`, `HTTP_HSTS=-`, `HTTP_QUIET_PATHS=-`); `<P>_HTTPCLIENT_BREAKER_*` читается, теневых имён
  нет; списки — без пустых, из одних разделителей (`JWT_AUDIENCE=,`) — `ErrConfig`, целые — десятичные; ошибка ENV —
  `<kit>.ErrConfig` с переменной (и у `tzkit.NewZone`). Литерал с нулями — `ErrConfig`: httpclient (таймаут, ретрай,
  SSE; `MaxRedirects 0` — без редиректов), natskit (таймауты, drain, потолки; JetStream — у включённого;
  `NakMaxBackoff 0` — постоянная задержка), `JWTConfig.AccessTTL`; `Leeway 0` — без допуска. Формат telemetry и
  JetStream — и у выключенного; невалидный `Config` у `storagekit.Module` и тик outbox ≥
  `PG_IDLE_IN_TX_SESSION_TIMEOUT` — ошибка графа. nil-опции ничего не меняют; hook'и `WithBeforeAttempt` складываются,
  второй `WithTransport` — `ErrConfig`; проба natskit — `natskit` или `natskit:<name>`; cleanup bruteforcekit и
  сессий, drain outbox — после миграций; префиксы ошибок `<kit>/<sub>:`, воркер outbox — `outboxkit:`, `telemetry:
  shutdown`, лог ошибок SDK — `telemetry: otel sdk error`. Хранилища китов (аудит, bruteforce, сессии, outbox и его
  инстансы) без своих миграций — ошибка старта `pgkit.ErrSchema`; дедлайн миграций прерывает идущий оператор,
  блокировку он ждёт ≤ `PG_MIGRATE_LOCK_TIMEOUT` (отказ — `dirty`); outbox `ordered` — по моменту вставки, а не началу
  транзакции; предел тела — по JSONB-тексту (числа в экспоненте у предела — `ErrPayloadTooLarge`); пул с занятым
  соединением на остановке — WARN и `nil` через 2/3 бюджета. Метрики outbox — без label `subject` (шаблон —
  `WithMetricSubject` инстанса), backlog-gauge'и — серия на инстанс (у пустой очереди — 0); producer-спан natskit без
  шаблона — `natskit.publish`, `destination.template` — только шаблон; доставка события outbox — в трассе записавшего
  его запроса (ссылка на тик). cachekit: `SetNX`, `GetDel`, `CompareAndDelete` не повторяются — на сбое связи ошибка
  вместо ложного «нет» (`REDIS_MAX_RETRIES` — `Get`, `Set`, `Delete`, теги, `Ping`), соединений — до
  2×`REDIS_POOL_SIZE`; `MemoryCache` и `httpkit.MemoryStore` — до 100 000 ключей (новый сверх — ошибка, без
  вытеснения); ответ запроса дольше `HTTP_IDEMPOTENCY_RESERVE_TTL` не сохраняется (WARN, `skipped`); httpclient:
  breaker и label `host` — у первых 1024 хостов (`HTTPCLIENT_MAX_HOSTS`), остальные — без breaker'а, `host="other"`.
  httpkit: тело сверх лимита в huma — 413, лимит операции выше общего поднимает транспортный, скрытая — по тем же
  хукам `OnAddOperation`; 408/409/425/429 и запросы без credentials идемпотентность не хранит; 401 — с
  `WWW-Authenticate`; HEAD на GET-маршрутах; ответ, не начатый к `HTTP_REQUEST_TIMEOUT`, — `503 REQUEST_TIMEOUT` целым
  ответом с `Connection: close`, ответ хендлера после него отбрасывается (идемпотентность его хранит); без дедлайна —
  только GET-поток (SSE, `Upgrade`), POST-поток обрывается; `openapi.json` — `REQUEST_TIMEOUT` в описании `code`
  (`make gen`); CORS пускает `Idempotency-Key`; panic после заголовка обрывает соединение; операцию открывает только
  альтернатива её схем целиком, `{}` — без credentials, последней (отказ бэкенда — 503); идемпотентность — после
  `HumaAuth`; trace-context клиента — ссылкой на новый корень, baggage — с `HTTP_TRUST_TRACE_CONTEXT`; `ip:port` в
  X-Forwarded-For — его IP. Секреты URL (S10): в ошибках, логах и спанах natskit, cachekit, storagekit, telemetry и
  httpclient userinfo и значения query — `xxxxx`, фрагмента нет, ошибка разбора URL и `OTEL_EXPORTER_OTLP_HEADERS` не
  цитирует ввод; `url.full` спанов httpclient — с именами параметров query; спан попытки httpclient — свой (`HTTP
  <METHOD>` до заголовков ответа), не otelhttp; `STORAGE_PUBLIC_BASE_URL` с кредами — `ErrConfig`. logkit (S11):
  секрет по ключу — и `pass`, `sid`, `refresh`, `signature`, `tokens`, `passcode`, `code` после
  `verification`/`auth`/`sms`…; ключ на слово-счётчик (`otp_attempts`) — не код; ключи-секреты ищутся в полях
  структур, за указателями, в выводе `MarshalJSON` и в JSON из `RawMessage`/`[]byte`; маски значений — base64url-токен
  ≥ 20, пароль userinfo URL, пара `ключ=значение` с ключом-секретом. bruteforcekit (S12): бюджет субъекта — 20 попыток
  со всех адресов, блокировка 1m (`LockState.Scope`), успех снимает его; `Retry-After` — `LockState.RetryAfter` по
  часам базы; subject длиннее 255 байт — ключ по SHA-256, с NUL или не в UTF-8 — `ErrInvalidKey`;
  `bruteforce_locks_total{scope}`. Входы (S13): пароль длиннее 1024 байт — `ErrPasswordTooLong` у `Hash` и `Verify`
  (`Verify` — за время `VerifyDummy`; DTO — `maxLength`); аудит: NUL и не-UTF-8 в строках, `Metadata` > 64 КиБ JSON или с
  `\u0000` — `ErrInvalidEntry` до базы (`result=invalid`), не-UTF-8 строки `Metadata` — U+FFFD; аудит-`Module` даёт и `*Store`.
- **Изменения.** `httpkit.ProvideSecurityScheme`, `grpckit.ProvideService`, `httpclient.Options`; без envconfig;
  `pgkit.CheckSchema`, `CheckSchemaOnStart`, `ErrSchema`, `ErrNestedTxStricter`, `outboxkit.SchemaChecker`,
  `outboxkit.WithMetricSubject`, `Message.TraceParent`, `OutMsg.MetricSubject`/`TraceParent`, миграция outbox 0003;
  `natskit.WithTraceParent`, `StreamMsg.TraceParent`; `testkit/idempotencytest`, `Cache.CompareAndSet`,
  `cachekit.WithMaxKeys`, `ErrMemoryFull`, `HTTPCLIENT_MAX_HOSTS`; `HTTP_REQUEST_TIMEOUT`, код `REQUEST_TIMEOUT`;
  `HTTP_TRUST_TRACE_CONTEXT`, `httpkit.ContextWithClientIP`. `bruteforcekit.LockScope`,
  `LockState.RetryAfter`/`Scope`, `SubjectBudgetOff`, `BRUTEFORCE_SUBJECT_*`. `OTEL_ALLOW_INSECURE_TOKEN`,
  `authnkit.ErrPasswordTooLong`/`MaxPasswordLen`, `auditkit.MaxMetadataBytes`/`ErrConfig`, `Entry.MetadataJSON`;
  `auditkit/pgstore.RetentionModule`/`RetentionConfig`/`NewRetentionConfig`/`RetentionJob`/`MaxRetentionDays`,
  `Store.DeleteExpired`, `AUDIT_RETENTION_DAYS`, `audit_log_retention_deleted_total` — срок хранения аудита (opt-in).
