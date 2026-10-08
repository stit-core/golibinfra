---
status: in-progress
parent: go-backend-template:audit-program
blocked-by: —
---

# harden-tests-archkit-release — партия 3, часть 2: fx, сигналы, тесты, archkit, гейт, релиз v1.4.0

## Цель
Вторая половина партии 3 и тег v1.4.0, после которого каждый угол аудита китов ≥ 9 (выше minor ничего, эксперименты чисты): fx и
конвенции сигналов, фейки и сила тестов, сигналы httpkit в гейте; `archkit` — общие правила раскладки из `internal/archtest`
шаблона (решение владельца: узко); гейт, обвязка, `/plan` с шаблоном, доки; повтор экспериментов и релиз для `repair-harness` S7.

## Контекст
- **Порядок.** После `harden-kits-contracts` (его Handoff и аннотация — здесь); программа — `audit-program.md` шаблона; находки и
  «Как повторить аудит» — `M/kits-audit/summary.md` (`M` = `/home/mm/.cache/stit-audit-program/`).
- **archkit.** Источник — archtest шаблона `e2f3dd2`, потребитель — `repair-harness` S7 и AC9; материалы — `M/archkit/README.md`.
- **Обвязка.** `/plan` шаблона после `repair-harness` S1 разошёлся с копией здесь (маршрут, накопитель handoff, pre-commit).
- **Гейт сейчас:** матрица `M/kits-audit/tests-gate/gate_inject.py` — не пойман G04 (KGATE-08); pre-push зовёт
  `scripts/gate-clean.sh` рабочего дерева; `commit-msg` нет; `/release` ищет breaking `grep '!:'`; версии тулов не сверяются.
- **Правила шага:** доки кита — тем же коммитом; breaking — `!`, `BREAKING CHANGE:`, строка аннотации; без новых зависимостей.

## Не входит
- Правила обвязки шаблона (HTTP-типы, таблицы ошибок, subject'ы, `Trusted`, `fx.As`, тесты без утверждений, проводка доменов,
  проза, `//nolint`, `.template`) — остаются в его archtest и едут git-способом: `archkit` узкий.
- Сырой тег группы timerkit (`backlog.md`), копии `/plan` в других репо, правки шаблона (его `repair-harness` по Handoff), push.

## Критерии приёмки
`<rev>` — проверяемый коммит, `<wt>` — worktree на нём; скрипты — от `M/kits-audit/`.
- [x] **AC1** — Жизненный цикл: flagkit, natskit, cachekit, tzkit проверяют конфиг и связь на старте и без потребителя; `Stop` при
  недоступном коллекторе — без ошибки и кода 1; ошибка конфига telemetry и строки go-redis — JSON-записи logkit. Проверка:
  `fx-observability/run.sh <rev> fx` → KX40, KX67 (a, d) PASS, KX66 — `Stop` без ошибки
  - Подтверждено: дерево S1 — KX40–43, KX67a, d PASS (stderr 0 байт), KX66 `err=<nil>`, KX67f exit 0; fx-docker — как в S14
- [x] **AC2** — Одна конвенция сигналов, как в доках (semconv БД, спан otelpgx без SQL, ключ `error`, `not_found`, TOTP `replay`,
  anthropickit без метрик otelhttp). Проверка: `fx-observability/run.sh <rev> obs-unit obs` → KO15, KO16, KO18 PASS, `compare.py`
  — 0 расхождений с доками, `promname` — 0 коллизий; `api-docs/claims-A-exp/run.sh <wt>` (`authn`) → `ok:1 replay:1`
  - Подтверждено: дерево S2 — KO01–08, KO11–21 PASS (KO15: 67 спанов `query <операция>`), compare 0, promname 0; `ok:1 replay:1`
- [x] **AC3** — Фейки testkit ведут себя как боевые: расхождения зонда — только задокументированные (база — 26 из 50);
  `pgkit.WithTestTx` на ctx usecase'а не пропускает `Publish` мимо транзакции. Проверка: `tests-gate/run-all.sh <rev> probes` →
  расхождений вне доков нет; `api-docs/claims-A-exp/run.sh <wt>` (`fakes`, `logtest`, `clock`) → по докам
  - Подтверждено: дерево S4 — зонд 50 SAME из 50; `fakes`, `logtest`, `clock` — по докам (`cap` канала таймера 1 — в доке); C2 ✓
- [x] **AC4** — Тесты убивают мутантов базового аудита; регрессия таймаута healthkit — падение теста, а не зависание; тест метрик
  amocrmkit ловит смену типа инструмента; страж пакетных инструментов — по `CLAUDE.md`. Проверка: `tests-gate/run-all.sh <rev>
  mutants`, `summarize.py` → выжили только эквивалентные (с причиной); V-G2, V-J1b, P-G4, P-G5 (`M/val-rel130/mut/`) → красные
  - Подтверждено: дерево S5 — 139 мутантов: гейт 109, integration 30, выживших 0; V-G2, V-J1b, P-G4, P-G5, N11a — красные
- [x] **AC5** — Контракт сигналов httpkit и паритет handlertest держит `make check`; ответ известного маршрута до роутинга — с его
  маршрутом, `unmatched` — только неизвестному. Проверка: `httpkit-b/run.sh <rev> exp mut gate` → HO01 PASS, `compare.py` и
  `compare_logs.py` — 0, выжили только эквивалентные мутанты (hk-ctr-05 убит), GH01, GH03, GH04, GH06, GH08 пойманы
  - Подтверждено: дерево S6 — HO01, HT01 PASS, `compare.py`, `compare_logs.py` — 0; мутанты 63: убиты все; GH01–GH08 пойманы
- [x] **AC6** — `archkit.Check` одной точкой входа держит раскладку любого сервиса на golibinfra — семь правил («Решения»);
  module path — из `go.mod`; настройки и известные нарушения (с ID находки, устаревшая запись — FAIL) — у потребителя. Проверка:
  `go test -race ./archkit/...` → PASS: нарушение и мутант «правило снято» у каждого правила — красные, чистая фикстура — зелёная
  - Подтверждено: PASS; мутанты `M/archkit/mut.py`: «правило снято» — 7 из 7 красные, всего 199 из 204, 5 эквивалентны
- [x] **AC7** — Шаблон берёт archkit без потерь: в его worktree через `replace` чистое дерево зелёное, инъекции 1b, 3b, 13, M,
  ADP b, c, g, h, i, k — FAIL из archkit, копия с module path форка — правила действуют. Проверка: кейсы `M/s12/inj.py`,
  `M/valb2/inj.py` в worktree шаблона → как сказано, протокол — `M/archkit/README.md`
  - Подтверждено: шаблон с проводкой — сборка и `internal/archtest` зелёные, 10 инъекций → FAIL `TestLayout`; 144 кейса
    валидатора B: 8 строже источника, «красный → зелёный» — одно его ложное срабатывание; форк и форк после prune зелёные
- [ ] **AC8** — Гейт держит запреты и свой конфиг: `fmt.Print*`, `slog.Default`, `http.DefaultClient` и `:latest` в Go-коде
  краснят `make check` (G01); линтер, снятый из `enable` (G04), и чужая версия golangci-lint или govulncheck — отказ; коммит не по
  Conventional Commits не создаётся; `/release` берёт несовместимое из apidiff, env- и metric-diff. Проверка:
  `tests-gate/run-all.sh <rev> gate` → обязательные отказы пойманы, контроли зелёные; `git commit -m "update stuff"` → отказ хука
- [ ] **AC9** — Push проверяет то, что пушит: правка `scripts/gate-clean.sh` в рабочем дереве гейт push не выключает; guard не
  отказывает на чтении хуков. Проверка: `printf 'exit 0' > scripts/gate-clean.sh` без коммита, push HEAD с падающим тестом в bare
  → отказ; `M/hgd/guard_cases.py`, `M/val-hgd-B/guard_b.py` и `find -name pre-push`, `awk`/`sort`/`cp` из хука → 0 расхождений
- [ ] **AC10** — Доки совпадают с кодом: статика — 0 расхождений; ✗ ручной сверки закрыты; P7 HX01 назван в доке порядка хуков.
  Проверка: `api-docs/scripts/run-static.sh <rev>` → D3, D4, D6, D10 — 0, D7 — ERR только от заглушек; `httpkit-a/static.sh <rev>`
  → 0; пересчёт `api-docs/claims-*.md` и `httpkit-a/claims.md` → ✗ нет
- [ ] **AC11** — Скилл `/plan` — одна копия с шаблоном. Проверка: `diff` `.claude/skills/plan/SKILL.md` и
  `references/plan-template.md` с копиями шаблона → только строка про pre-commit, которую шаблон снимает по Handoff;
  `make plan-check` → OK
- [ ] **AC12** — Все эксперименты «Как повторить аудит» на кандидате тега — «чисто» по критериям `M/kits-audit/summary.md`;
  открытых находок «п3» выше minor нет. Проверка: прогоны всех групп шести каталогов → таблица исходов в «Где мы сейчас»
- [ ] **AC13** — Тег `v1.4.0` готов: аннотированный, с разделами Breaking, Поведение, Изменения, Гейт; каждая строка apidiff
  `v1.3.0 → v1.4.0` по объёму и каждое изменение envconfig-тегов, дефолтов и имён метрик — в аннотации с шагом миграции; каждая
  ошибка сборки шаблона на HEAD (`replace`) закрыта строкой Breaking; `make gate-clean` и `make test-full` зелёные. Проверка:
  `git tag -l --format='%(contents)' v1.4.0` → четыре раздела; `api-docs/scripts/run-versions.sh v1.3.0 v1.4.0`,
  `httpkit-a/versions.sh v1.3.0 v1.4.0` → всё покрыто; `make gate-clean` → зелёный

## Шаги
### Жизненный цикл, сигналы, тесты
- [x] **S1** — fail-fast без потребителя, best-effort telemetry, `FxLogger` без OTel, go-redis в logkit (KFX-03/05/06, KOBS-08)
  - Проверка: AC1 → выполнен · Закрывает: AC1 · Сделано: `M/tsa/mut_s1.py` 19/19; эксперименты под API — `*.p2s1`
- [x] **S2** — Сигналы: semconv БД, `error`, `not_found`, TOTP, otelhttp anthropickit (KOBS-06/07/09, KDOC-05, класс KOBS-01)
  - Проверка: AC2 → выполнен · Закрывает: AC2 · Сделано: `mut_s2.py` 12/12; контракт — `M/tsa/tools/gen_contract.py`; `*.p2s2`
- [x] **S3** — Фейки natstest, outboxtest, audittest, pgtest (откат), `WithTestTx` (KTST-03/04, KDOC-08, `outboxtest-withtesttx`)
  - Проверка: зонд фейков — 0 DIVERGE вне доков · Закрывает: AC3 · Сделано: `fakes` — по докам; `mut_s3.py` 24/24; `*.p2s3`
- [x] **S4** — Фейки логов, хешей, кэша, часов: logtest, authntest, `MemoryCache` и контракт `Cache`, clockkit (KTST-05, KDOC-08)
  - Проверка: AC3 → выполнен · Закрывает: AC3 · Сделано: `mut_s4.py` 41/41; зонд — под указатель хэшера (`*.p2s4`)
- [x] **S5** — Мутанты базового аудита убиты, таймаут healthkit с дедлайном, тип метрик amocrmkit, страж пакетных инструментов,
  первое значение `JWT_AUDIENCE` (KTST-02, R4 валидатора A `harden-authn-release`, N1 и N2 валидатора v1.3.0)
  - Проверка: AC4 → выполнен · Закрывает: AC4 · Сделано: healthkit-01 — не эквивалент: тест усилен; `mut_s5.py` 30/30; `*.p2s5`
- [x] **S6** — Сигналы httpkit в гейте: контракт `Module` по доке, `unmatched`, мутанты, handlertest (KGATE-13, KTST-11, KOBS-12)
  - Проверка: AC5 → выполнен · Закрывает: AC5 · Сделано: HO01 и 22 якоря мутантов под API (`*.p2s6`); локальные мутанты 9/9
### archkit
- [x] **S7** — `archkit` через `/add-kit`: правила, настройки потребителя, фикстуры, README, строки в корневом README и CLAUDE.md
  - Проверка: AC6 → выполнен · Закрывает: AC6 · Сделано: `0e30261`, фиксы `704fa67`…`08d75be` — таблица в `M/archkit/README.md`
- [x] **S8** — archkit на шаблоне: проводка и инъекции в его worktree, правки archkit по итогу, Handoff — по проверенной проводке
  - Проверка: AC7 → выполнен · Закрывает: AC7 · Сделано: `850f9c3`; проводка — `M/archkit/template-wiring.patch` (`wire.py`)
### Гейт, обвязка, доки
- [ ] **S9** — Запреты и конфиг гейта машиной: forbidigo, contextcheck после пробы, `:latest`, эталон линтеров, версии тулов, пути
  гейта в «Сначала спросить» (KGATE-07, KGATE-08, KGATE-10)
  - Проверка: `tests-gate/run-all.sh <rev> gate` → G01, G04 пойманы · Закрывает: AC8 · Зависит от: —
- [ ] **S10** — Хуки и релиз: pre-push из пушимого коммита, `commit-msg`, `make release-diff` для `/release`, guard без ложных
  отказов (KGATE-08, KGATE-09, бэклог `gate-hook-paths`)
  - Проверка: AC8, AC9 → выполнены · Закрывает: AC8, AC9 · Зависит от: S9
- [ ] **S11** — Скилл `/plan` и маршрут: одна копия с шаблоном, «Точка входа» CLAUDE.md — таблица «Маршрут задачи», специфика репо
  — в `references/repo.md`
  - Проверка: AC11 → выполнен · Закрывает: AC11 · Зависит от: —
- [ ] **S12** — Доки к коду: остаток KDOC-07, KDOC-09 без бюджета outbox, KDOC-10, KDOC-11, P7 HX01, корневые README и CLAUDE.md —
  к API партии; ✗ ручной сверки пересчитаны
  - Проверка: AC10 → выполнен · Закрывает: AC10 · Зависит от: S1, S2, S6, S8, S10
### Релиз
- [ ] **S13** — Повтор всех экспериментов «Как повторить аудит» на кандидате тега — агентом, не писавшим код партии; таблица
  исходов — в «Где мы сейчас»
  - Проверка: AC12 → выполнен · Закрывает: AC12 · Зависит от: S5, S11, S12
- [ ] **S14** — Релиз: `make test-full` и `make gate-clean`; `make release-diff PREV=v1.3.0`; сборка шаблона на HEAD через
  `replace` в своём worktree (каждая ошибка — строкой Breaking); тег `v1.4.0` локально с разделами Breaking, Поведение, Изменения,
  Гейт. Push `main` и тега — дирижёр после PASS валидатора партии; затем план удаляется отдельным `chore(plan)`
  - Проверка: AC13 → выполнен · Закрывает: AC13 · Зависит от: S13

## Решения
- **Фиксы валидатора A.** `LOG_SILENT`: логгеры без OTel-sink (события fx, ошибки telemetry) пишут ERROR в stderr —
  `logkit.WithStderrFallback`; общий `Silent` не трогали: им глушат тесты.
- **archkit.** Кит без `Module`, конфига и сигналов: `archkit.Check(t, opts…)`, корень и module path — из `go.mod` (stdlib);
  правила — семь проверок archtest шаблона (`TestInternalLayout`, `TestImportBoundaries`, `TestDomainRootIsTransportAgnostic`,
  `TestPureLayersHaveNoIO`, `TestNoWallClockInDomains`, `TestNoCrossDomainIdentityClosure`, `TestQueriesUseOwnTables`), настройки
  и известные нарушения — опции. Доверие корня домена пакету с I/O — отдельной `WithUncheckedPackages`, не `WithPurePackages`:
  одна опция на оба смысла снимала проверку `pkg/policy` (B2 валидатора B); запись про запас — FAIL, и о пакете, которого корень
  домена не импортирует (R3: иначе исключение включается с первым импортом, без решения). Правила читают то, что берёт сборка, —
  и пакет по импорту из `testdata`, каталога на `.` или `_`, вложенного модуля, файл-ссылку (B1); неимпортированный код таких
  каталогов правила слоёв не читают: в сборке его нет. Каталоги `WithInternalDirs` — над доменами: ai-gateway с `internal/gen` —
  перенос в `pkg/` или известные нарушения. Против источника: строже — в заготовке аннотации, точнее и пределы —
  `M/archkit/README.md`. Новое или более строгое правило — minor со строкой Breaking.
- **Этапы в ветках.** archkit (S7–S8, `p3-archkit`) и гейт (S9–S11, `p3-gate`) шли параллельно в ветках (дирижёр): план правился
  при слиянии, а не коммитами шагов, — шаблон и релиз не ждали; коммиты archkit сверх двух шагов — фиксы по отчёту валидатора B.
- **Гейт.** forbidigo — `fmt.Print*`, `print`/`println`, `log.*`, `slog.Default` и функции пакета `slog`, `http.DefaultClient`,
  `http.Get/Post/Head`; contextcheck — если проба без ложных, иначе причина здесь; `:latest` — проверка Go-кода шагом гейта.
  Эталон линтеров и форматтеров — файл в `scripts/`, его сверка с `golangci-lint linters --json` — в шаге `lint` (G04); версии —
  против пинов `Makefile`, ставит `make tools`; apidiff — пиненный через `make tools`, не в `go.mod`. Пути гейта (`Makefile`,
  `scripts/`, `.golangci.yml`, `.claude/settings.json`, `.claude/hooks/`) — в «Сначала спросить».
- **Хуки.** pre-push берёт `scripts/gate-clean.sh` из пушимого коммита: правка в дереве гейт не выключает, правка в коммите видна
  в диффе. `commit-msg` — по образцу шаблона, `hooks-check` — оба хука. `make release-diff PREV=…` — apidiff по пакетам, diff
  env-тегов и дефолтов (envdoc) и имён метрик; `/release` требует строку аннотации на каждую несовместимость.
- **`/plan`.** Тело SKILL.md — общее: нужен ли план, решает таблица «Маршрут задачи» CLAUDE.md («Точка входа» golibinfra
  становится ею), «в какой гейт и хук входит проверка — там же», накопитель handoff — «из профиля»; профиль golibinfra: pre-commit
  и накопителя нет, Handoff — в аннотацию. `validate-plan.sh` — без накопителя: он нужен релизу образов шаблона. Сверка — с копией
  шаблона на момент шага.
- **Эксперименты и релиз.** S13 — агент, не писавший код партии; контракты сигналов (`expected-signals*.json`) — заново из доков
  HEAD; инструменты — с поправками N4 валидатора A `harden-gate-defaults` (L3 через `make lint`, G21 по тексту отказа); правка
  эксперимента под API — со смыслом, оригинал рядом. «Гейт» аннотации: `make gate-clean`, `make test-full`, `release-diff` против
  v1.3.0, сборка шаблона через `replace` (и archkit на нём: инъекции AC7), `go test -race ./archkit/`, эксперименты S13.

## Handoff
**Шаблон, `repair-harness` S7 — bump v1.4.0** (места — по сборке шаблона `e2f3dd2` через `replace`; на bump — сверить сборкой):
- **Конфиг и формы API.** App (`internal/platform/config/config.go:45`) и `cmd/seed-admin:66` — на
  `config.Load(&cfg, config.Env())` и `authnkit.NewArgon2idConfig()` (`KEY=` и имена по тегам — как у китов, теневое
  `COOKIE_COOKIE_SECURE` уходит, `APP_ENV=` — «required»: правится `TestModule_EmptyEnvIsNotDev`); `cmd/api/main.go`:
  `NewArgon2idConfig`, `healthkit.ProvideProbe(pgkit.Probe)`, `natskit.ProvideProbe("")`; `cmd/seed-admin` и его `main_test.go`
  (тег `integration`): `authnkit.Argon2idConfig`, `sessionpg.NewStore(cfg, db)`; `authgate/module.go` —
  `httpkit.ProvideSecurityScheme`, `module_test.go:46` — `Argon2idConfig`; `identitytest`, `identity_test` —
  `httpkit.ClientIP(0, log)`; `authgate/jwt_test.go`, `auth/infra/jwt/jwt_test.go` — `JWTConfig` с `AccessTTL`, без
  `DefaultAccessTTL`/`DefaultLeeway`; `timerkit.ProvideJob`/`JobsGroup` — `rules/events-and-messaging.md`,
  `docs/reference/architecture.md`, `docs/stack/_templates/runtime.md`.
- **pgkit и outbox.** `cmd/migrate`: `MigrateUpAll`/`MigrateDown(ctx, dsn, cfg.LockTimeout, …)`; хранилища без миграций — ошибка
  старта. Миграция outbox 0003 (`trace_context JSONB` своей `SchemaSQL`), метрики без `subject`, предел — JSONB;
  `events/outbox_test.go:22` — `Insert(…, maxBytes)`, `:49` — `outboxkit.NewConfig`.
- **httpkit.** `IdempotencyStore.Set` с токеном; extractor — у схемы (`authgate/module.go`); `WithAuth` → `WithSecuritySchemes`
  (`handler_test.go` auth и user, `new-domain.sh:662`, `tests-pipeline.md:47`, `identitytest.go:47`); `make gen`; `{}` — `Public`.
- **bruteforcekit.** `limiter_test.go`: литерал `Config` — `Subject*`, `CleanupRetention` ≥ `LockDuration`; Retry-After —
  `lock.RetryAfter`; длинный subject — ключ SHA-256, NUL — `ErrInvalidKey`: `guard_test.go:40` — случай с NUL вместо 255.
- **auth и logkit.** `Revoke` → `(subject, err)`: `internal/domain/auth/usecase.go:199,213` (Logout — аудит `logout` с
  `ActorID: subject`, если не `""`), обёртки `usecase_test.go:235,240`, `TestLogout` (`:674`) — одна запись аудита с автором; DTO
  пароля — `maxLength:"1024"`: `auth/delivery/http/dto.go:8,38,39`, `user/…/dto.go:11`, `verification/…/dto.go:12`. Маски шире:
  тест, который ищет в логе `password=…`, base64url-токен или пароль URL, — на маркер.
- **env для INFRA-HANDOFF:** `PG_MIGRATE_LOCK_TIMEOUT` (5 с), `HTTPCLIENT_MAX_HOSTS`, `HTTP_REQUEST_TIMEOUT`,
  `HTTP_TRUST_TRACE_CONTEXT`, `BRUTEFORCE_SUBJECT_MAX_FAILURES` (20), `BRUTEFORCE_SUBJECT_LOCK_DURATION` (1m); старт падает, если
  `HTTP_REQUEST_TIMEOUT` ≥ `HTTP_WRITE_TIMEOUT` или `RESERVE_TTL`, а `BRUTEFORCE_CLEANUP_RETENTION` < `LOCK_DURATION` или
  `SUBJECT_LOCK_DURATION`; `HTTP_CORS_HEADERS` + `Idempotency-Key`; `STORAGE_S3_USE_SSL` удалён; токен OTLP к `http://` не на
  loopback — https или `OTEL_ALLOW_INSECURE_TOKEN=true` (и при выключенном SDK); `AUDIT_RETENTION_DAYS`; Redis — 2× пул.
- **archkit.** Проводка — `M/archkit/template-wiring.patch` (проверена на `e2f3dd2`; после `git apply` — `git add` новых файлов,
  иначе `TestTemplatePaths` красный): семь проверок («Решения») и их хелперы уходят из archtest, `imports_test.go` →
  `generated_test.go`, правила обвязки остаются. `layout_test.go` — файл шаблона (`TestLayout` зовёт `archkit.Check(t,
  layout...)`), `layout_settings_test.go` — файл форка под `:(exclude)` в `.template/paths`: настройки прежних проверок и
  исключение корня `internal/platform/identity` (`identity.IPFrom` берёт IP из httpkit); без него пакет не компилируется, первый
  перенос — `git show <тег>:internal/archtest/layout_settings_test.go` — дописать в `/upgrade`. Проза с именами ушедших тестов и
  пометка archtest у перенесённых инвариантов — на `TestLayout` и archkit: `CLAUDE.md` § archtest,
  `rules/domain-architecture.md:53`, `architecture.md:18,147,170`, `domain-mapping.md:99`, `triage-failure/SKILL.md:90`,
  `scaffold/references/port.md:157`, package-doc; новое в правилах — «строже» из аннотации. Сборка через `replace`: `go build
  ./...` — `M/archkit/template-bump-compile.patch` (5 файлов), тесты 12 пакетов (11 без `integration`) — по пунктам выше.
- **Логи, тесты, telemetry.** Ключ ошибки — `error` в каноне и коде; `cmd/api/boot_test.go`: хуки — `fx.WithLogger` теста с
  `hookLog`, не `teeLog`. testkit: `logtest` — с масками (`logmail_test.go:39-42` — проверять поля, не подстроку);
  `natstest.New(natstest.WithStream("user"))` — `verification/delivery/nats/contract_test.go:52`, `handler_test.go:90`;
  `outboxtest.Last`/`Published` — `json.RawMessage` (`json.Unmarshal` в тип события): `user/usecase_test.go:145,383,510`,
  `verification/usecase_test.go:193,301`; хэшер — `&authntest.DeterministicHasher{}`: `user/trusted_test.go:25,83`,
  `usecase_test.go` (user `:64`, verification `:87`, auth `:108`), `module/adapters_test.go` (verification `:29`, auth `:26`),
  `verification/delivery/nats/contract_test.go:39`; свои `brokenHasher`, `countingHasher`, `failingAudit` снять
  (`testkit-failing-fakes`): `HashErr`, `VerifyCalls()`, `VerifyDummyCalls()`, `audittest.Recorder{LogErr: …}`.
- **`/plan`.** Строка «Формат проверяет …» SKILL.md — из golibinfra, pre-commit — в `references/repo.md` шаблона: копии совпадают.

## Где мы сейчас
S1–S8 закрыты: S1–S6 — у валидатора; archkit (S7–S8) принят валидатором B, перебазирован на первый этап и перепроверен на
шаблоне. S9–S11 — в ветке `p3-gate` (план — при слиянии); дальше S12, S13, S14. S13: эксперименты под API (`M/exs8/orig/*.v130`,
`*.s12`, `*.p2s*`), H15 `[{}]` не адаптирован, сигналы — из доков HEAD, KX64 — FX-12, 255 KDOC-09 — в доках.

Заготовка аннотации v1.4.0 (первая половина — `harden-kits-contracts`; вторая — по шагам):
- **Breaking.** config: App — именами тегов (`POSTGRES_PG_DSN` → `PG_DSN`); ошибка `Load`: поле без тега (тег или
  `ignored:"true"`), тег на вложенной структуре (снять, имена — `PG_*`, не `DB_PG_DSN`; вторая секция кита — `ignored:"true"` и
  свой `config.Load(&a.X, config.EnvPrefixed("x"))`, незаданная `X_*` — из голой), `map`, `[]byte`, `url.URL` или тип только с
  `UnmarshalBinary`/`Set` (строка с разбором в `Validate` или тип с `UnmarshalText`); `*envconfig.ParseError` нет. natskit:
  `NewConfigPrefixed` → `NewInstanceConfig`, `HealthProbe` → `ProvideProbe`, `NewBus(cfg, conn, log) (Bus, error)`,
  `NewStreamBus(cfg, conn, log)`. healthkit, timerkit: `Group` → `ProbesGroup`/`JobsGroup`, `Provide` →
  `ProvideProbe`/`ProvideJob`. pgkit: `ProvideMigrations` → `SupplyMigrations`; `MigrateUp/Down/UpAll(ctx, dsn, lockTimeout, …)` (>
  0, команда миграций — `cfg.LockTimeout`); `Module` берёт `logkit.Logger`; литерал `MigrateRunnerConfig` без `LockTimeout` —
  `ErrConfig`; вложенный `WithTxOptions` строже внешнего — `ErrNestedTxStricter`. authnkit: `DefaultAccessTTL`, `DefaultLeeway`
  удалены, `Argon2idParams`/`NewArgon2idParams` → `Argon2idConfig`/`NewArgon2idConfig`; пароль длиннее 1024 байт —
  `ErrPasswordTooLong` у `Hash` и `Verify` (DTO — `maxLength:"1024"`);
  `session.Store.Revoke(ctx, refresh) (subject string, err error)` — свой `Store` и обёртки отдают владельца закрытой этим вызовом
  сессии (`""` — неизвестна или закрыта раньше); `session/pgstore`: `NewStore(cfg, db…)`, `ErrSchema` и `Store.CheckSchema` →
  `pgkit.ErrSchema`, `pgkit.CheckSchema`. bruteforcekit: `pgstore.NewStore(cfg, db…)`; литерал `Config` без `SubjectMaxFailures`
  (`20` или `SubjectBudgetOff`) и `SubjectLockDuration`, а `CleanupRetention` меньше `LockDuration` — `ErrConfig`. outboxkit:
  `New(name, cfg, …)`, `NewWorker(instance, cfg, …)` — с ошибкой, `Default{BatchSize,TickTimeout,MaxAttempts}` удалены;
  `Repository.Insert(ctx, subject, payload, maxBytes)` — тело мерит хранилище (своё — по своему представлению), сверх —
  `ErrPayloadTooLarge`; `pgstore.SchemaSQL` — с колонкой `trace_context JSONB` (своя копия схемы — добавить). cachekit:
  `Cache.CompareAndSet` (свой `Cache` — реализовать), `IdempotencyStore.Set(ctx, key, token, record, ttl) (bool, error)`,
  `NewRedisCache(cfg, log)`, `Module` берёт `logkit.Logger`. httpclient: `Validate` — value-приёмник, группа опций → значение
  `httpclient.Options`, литерал `Config` без `MaxHosts` — `ErrConfig` (`1024` или `-1`). storagekit: `NewKey` → `string`,
  `MemoryStorage.Bytes`/`Keys` удалены (`Get`); `Config.UseSSL` (`STORAGE_S3_USE_SSL`) удалён — endpoint без схемы — TLS, plaintext
  — `http://host:port`; `minio.NewStorage(cfg, client, urls, log)` — с ошибкой. telemetry: `Headers.Decode` — без `ErrConfig`;
  `OTEL_TOKEN` или `Authorization` к `http://` не на loopback — `ErrConfig`, и при `OTEL_SDK_DISABLED=true` (https или
  `OTEL_ALLOW_INSECURE_TOKEN=true`). auditkit: NUL, не-UTF-8 и суррогат без пары в строках и `Metadata` (и ключах), `Metadata` > 64
  КиБ JSON — `ErrInvalidEntry` до базы (`result=invalid`): ввод пользователя — проверить до `Log`. httpkit:
  `NewServer(cfg, log, opts…) (*Server, error)` — с цепочкой кита и `HumaAuth` (`WithSecuritySchemes`, `WithIdempotencyStore`;
  `WithLogger` удалён); `ClientIP(hops, log)`, `RegisterHealth(api, timeout, log, probes…)`, `HumaAuth(schemes, log)`,
  `Idempotency(cfg, store, log)`, `OTel(trust, quiet…)`; схема — `ProvideSecurityScheme(ctor)`, extractor — её поле `Extract`
  (`SecuritySchemeEntry` несравним: `==` и ключ map не компилируются); удалены `AuthExtractor` в графе, `WithAuthExtractor`,
  `IdempotencyOption`, `WithAuth`; схема `Security` вне `components` и `Security` из одних `{}` — ошибка старта (`{}` —
  `httpkit.Public`); идемпотентность — только у операций huma (маршрут мимо huma с `Idempotency-Key` исполняется каждый раз —
  операция huma или обёртка `httpkit.Idempotency`); `IdempotencyStore.Set(ctx, key, token, record, ttl) (stored bool, err error)` —
  только поверх резерва этого токена (свой store — `testkit/idempotencytest.RunContract`); литерал `Config` без `RequestTimeout` —
  `ErrConfig` (`HTTP_REQUEST_TIMEOUT` 25s, меньше `HTTP_WRITE_TIMEOUT` и `RESERVE_TTL`; `-1s` — выкл.); handlertest — с цепочкой
  кита, `WithAuth` → `WithSecuritySchemes` (схема с `Extract`); outboxtest: `Event.Payload`, `Published`, `Last` —
  `json.RawMessage` (сравнение — `json.Unmarshal`), `Publish` — в транзакции фейка pgtest (метка `pgkit.WithTestTx` — `ErrNoTx`);
  natstest: стримы объявляет тест (`WithStream`), без них stream-операции — ошибка. oidckit, hubkit/sso: `Module` не кладёт
  `httpkit.AuthExtractor` — схемы с extractor'ом (hubkit — cookie своего контура). authntest: хэшер — `&DeterministicHasher{}`.
  logkit: `FxLogger` — логгер из `logkit.Config` графа, не из `*slog.Logger` (тест с логгером графа — свой `fx.WithLogger`).
- **Поведение.** ENV: `KEY=` — дефолт (киты и `config.Env`), пустое — `-` (`PG_MIGRATIONS_PATH=-`, `HTTP_HSTS=-`,
  `HTTP_QUIET_PATHS=-`); `<P>_HTTPCLIENT_BREAKER_*` читается, теневых имён нет; список из одних разделителей (`JWT_AUDIENCE=,`) —
  `ErrConfig`; целые — десятичные; ошибка — `<kit>.ErrConfig` с переменной (и у `tzkit.NewZone`). Литерал с нулями — `ErrConfig`
  (httpclient: таймаут, ретрай, SSE, `MaxRedirects 0` — без редиректов; natskit: таймауты, drain, потолки, JetStream у
  включённого, `NakMaxBackoff 0` — постоянная задержка; `JWTConfig.AccessTTL`, `Leeway 0` — без допуска); формат telemetry и
  JetStream — и у выключенного; невалидный `Config` у `storagekit.Module` и тик outbox ≥ `PG_IDLE_IN_TX_SESSION_TIMEOUT` — ошибка
  графа; nil-опции — без эффекта; hook'и `WithBeforeAttempt` складываются, второй `WithTransport` — `ErrConfig`. Проба natskit —
  `natskit` или `natskit:<name>`; cleanup bruteforcekit и сессий, drain outbox — после миграций; префиксы ошибок — `<kit>/<sub>:`
  (воркер outbox — `outboxkit:`, `telemetry: shutdown`, `telemetry: otel sdk error`). pgkit: хранилища китов (аудит, bruteforce,
  сессии, outbox и инстансы) без своих миграций — `pgkit.ErrSchema` на старте; дедлайн миграций прерывает идущий оператор,
  блокировку он ждёт ≤ `PG_MIGRATE_LOCK_TIMEOUT` (отказ — `dirty`); занятое соединение на остановке пула — WARN и `nil` через 2/3
  бюджета. outbox: `ordered` — по моменту вставки, а не началу транзакции; предел тела — по JSONB-тексту; метрики — без label
  `subject` (шаблон — `WithMetricSubject` инстанса), backlog-gauge'и — серия на инстанс (пустая очередь — 0); producer-спан
  natskit без шаблона — `natskit.publish`, `destination.template` — только шаблон; доставка события — в трассе записавшего его
  запроса. cachekit: `SetNX`, `GetDel`, `CompareAndDelete` без повторов — на сбое связи ошибка вместо ложного «нет»
  (`REDIS_MAX_RETRIES` — `Get`, `Set`, `Delete`, теги, `Ping`), соединений — до 2×`REDIS_POOL_SIZE`; `MemoryCache` и
  `httpkit.MemoryStore` — до 100 000 ключей (новый сверх — ошибка, без вытеснения). httpclient: breaker и label `host` — у первых
  1024 хостов (`HTTPCLIENT_MAX_HOSTS`), прочие — `host="other"` без breaker'а. httpkit: тело сверх лимита в huma — 413 (лимит
  операции выше общего поднимает транспортный, и у скрытой); идемпотентность не хранит 408/409/425/429, запросы без credentials и
  ответ дольше `HTTP_IDEMPOTENCY_RESERVE_TTL` (WARN, `skipped`); 401 — с `WWW-Authenticate`; HEAD на GET-маршрутах; ответ, не
  начатый к `HTTP_REQUEST_TIMEOUT`, — целый `503 REQUEST_TIMEOUT` с `Connection: close`, ответ хендлера после него отбрасывается
  (идемпотентность его хранит); без дедлайна — только GET-поток (SSE, `Upgrade`), POST-поток обрывается; `REQUEST_TIMEOUT` — в
  описании `code` `openapi.json` (`make gen`); CORS пускает `Idempotency-Key` (свой `HTTP_CORS_HEADERS` — дописать); panic после
  заголовка обрывает соединение; операцию открывает альтернатива её схем целиком, `{}` — без credentials, последней (отказ бэкенда
  — 503); идемпотентность — после `HumaAuth`; trace-context клиента — ссылкой на новый корень, baggage — с
  `HTTP_TRUST_TRACE_CONTEXT`; `ip:port` в X-Forwarded-For — его IP. Секреты URL (natskit, cachekit, storagekit, telemetry): в
  ошибках, логах и спанах — `xxxxx` от схемы до последнего `@` (и при `#`, `?`, `/` в пароле); у httpclient — userinfo, путь до
  последнего `@` и значения query; разбор URL и `OTEL_EXPORTER_OTLP_HEADERS` не цитируется; спан попытки httpclient — свой, не
  otelhttp; креды в `STORAGE_PUBLIC_BASE_URL` и `@` после хоста в `NATS_URL`, публичном URL и OTLP — `ErrConfig`. logkit:
  ключи-секреты — и `pass`, `pw`, `creds`, `sid`, `refresh`, `signature`, `tokens`, `secrets`, `api_keys`, `code_verifier`,
  `passcode`, `code` после `verification`/`auth`/`sms`… (`otp_attempts` — счётчик, не код) — и в полях структур, за указателями, в
  `MarshalJSON`, в JSON из `RawMessage`/`[]byte` и внутри строк; маски значений — base64url-токен ≥ 20, пароль userinfo URL, пара
  `ключ = значение` с ключом-секретом (и `password='…'`). bruteforcekit: бюджет субъекта — 20 попыток со всех адресов, блокировка
  1m (`LockState.Scope`); успех снимает её и обнуляет строку субъекта (её уберёт cleanup, занятые попыткой строки он пропускает);
  `Retry-After` — `LockState.RetryAfter` по часам базы; subject длиннее 255 байт — ключ SHA-256, с NUL или не в UTF-8 —
  `ErrInvalidKey`; `bruteforce_locks_total{scope}`. Входы: `Verify` пароля длиннее 1024 байт — за время `VerifyDummy`;
  `auditkit/pgstore.Module` даёт и `*Store`. Старт: flagkit, natskit, cachekit, tzkit строят тип и без потребителя (мусор
  `FEATURE_FLAGS`, пустая `APP_TZ`, нет NATS, Redis — отказ); `Stop` telemetry без коллектора — ERROR `telemetry: shutdown`;
  `FxLogger` без OTel (`LOG_SILENT` — ERROR его и логгера ошибок telemetry в stderr); go-redis — WARN `redis`, текст мостов —
  `detail`. Сигналы: ключ ошибки логов — `error` (запросы по `err` — переписать); cachekit — `db.system.name`, `db.operation.name`;
  спан otelpgx — `query <операция>`; нет ключа у `Get`/`Stat` — `result="not_found"`; TOTP — запись на вызов; otelhttp у
  anthropickit без метрик. natskit: пустой токен, wildcard в публикации и `>` не последним в фильтре — `nats.ErrBadSubject` сразу;
  фейки testkit — правила шины, JSONB и откат транзакции pgtest. `logtest` — логгер `logkit.New` в память (маски, типы JSON);
  authntest: хеш без `hash:` — `ErrInvalidHash`; `MemoryCache`: членство в теге переживает ключ (до `InvalidateByTag` или конца
  набора); таймер фейка clockkit: `Stop`/`Reset` забирают неполученный момент. httpkit: у 429, preflight и 405 `path`, `route`,
  `http.route` — маршрут; `unmatched` — только путь без него, без `http.route`.
- **Изменения.** Новое API, env и метрики — по `make release-diff PREV=v1.3.0` (apidiff, env, метрики); сверх них — ридер ENV без
  envconfig, миграция outbox 0003, `testkit/idempotencytest`, срок хранения аудита (opt-in: `auditkit/pgstore.RetentionModule`,
  `AUDIT_RETENTION_DAYS`, `audit_log_retention_deleted_total`). `archkit` — правила раскладки сервиса из его теста
  (`archkit.Check` с опциями). Строже прежнего archtest шаблона: корень домена не видит пакет `internal/platform/*` и обёртку над
  ним в `pkg/` с транспортом, драйвером или I/O (исключение — запись `WithUncheckedPackages`); identity-closure ловится с лишним
  параметром и кодом после `return`; dot-импорт `time` — и рядом с другим dot-импортом; пакет в корне модуля закрыт домену, `pkg`
  и platform; пакет в каталоге на точку и в каталоге-ссылке читается по импорту; Go-файл в каталоге на точку прямо под
  `internal/` — нарушение правила каталогов. Закрывается правкой или записью `WithKnownViolations` с ID находки.
