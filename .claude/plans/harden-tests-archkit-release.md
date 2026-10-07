---
status: in-progress
parent: go-backend-template:audit-program
blocked-by: —
---

# harden-tests-archkit-release — партия 3, часть 2: fx, сигналы, тесты, archkit, гейт, релиз v1.4.0

## Цель
Вторая половина партии 3 и тег v1.4.0, после которого каждый угол аудита китов ≥ 9 (выше minor ничего, эксперименты чисты). Здесь
— fx и конвенции сигналов, фейки и сила тестов, сигналы httpkit в гейте; `archkit` — общие правила раскладки из
`internal/archtest` шаблона (решение владельца: узко); гейт и обвязка, единый скилл `/plan` с шаблоном, доки; повтор всех
экспериментов аудита и релиз, на который шаблон переходит в `repair-harness` S7.

## Контекст
- **Порядок.** После `harden-kits-contracts` (закрыт; его Handoff и заготовка аннотации — здесь). Программа, метод и push —
  `go-backend-template/.claude/plans/audit-program.md`; находки и «Как повторить аудит» — `M/kits-audit/summary.md` и отчёты углов
  (`M` — `/home/mm/.cache/stit-audit-program/`).
- **archkit — источник.** `go-backend-template/internal/archtest/` (`archtest_test.go`, `imports_test.go`, `crossdomain_test.go`;
  обвязка шаблона — `harness_test.go`, `template_test.go`), allowlist — `internal/testkit/allowlist`. Потребитель —
  `repair-harness` S7 и AC9 (инъекции — AC7, разбор — `M/s12-harden-gate.md`). Раскладку `internal/domain/<d>` делят три шаблона;
  у ai-gateway свои каталоги `internal/` (`gen`, `jobs`). Ограничения — `M/replan-golibinfra.md` §5: только общее для раскладки,
  одна точка входа, module path из `go.mod`, ужесточение — minor с шагом миграции.
- **Обвязка.** Скилл `/plan` шаблона после `repair-harness` S1 разошёлся с копией здесь (маршрут — таблица CLAUDE.md, накопитель
  handoff, строка про pre-commit); остальные репо экосистемы держат прежний канон — вне программы.
- **Гейт сейчас:** матрица `M/kits-audit/tests-gate/gate_inject.py` — не пойман G04 (KGATE-08); pre-push зовёт
  `scripts/gate-clean.sh` рабочего дерева; `commit-msg` нет; `/release` ищет breaking `grep '!:'`; версии тулов не сверяются.
- **Правила шага:** README и `docs/` кита — тем же коммитом (`make envdoc-check`); breaking — `!`, `BREAKING CHANGE:` и строка
  заготовки аннотации; новых зависимостей в `go.mod` нет; новый кит — через `/add-kit`.

## Не входит
- Правила обвязки шаблона (HTTP-типы, таблицы ошибок, subject'ы, `Trusted`, `fx.As`, тесты без утверждений, проводка доменов,
  проза, `//nolint`, `.template`) — остаются в его archtest и едут git-способом: `archkit` узкий.
- Сырой тег группы timerkit — не правило раскладки, `backlog.md`; копии `/plan` в остальных репо экосистемы; правки шаблона — его
  `repair-harness` по Handoff; push — дирижёр.

## Критерии приёмки
`<rev>` — проверяемый коммит, `<wt>` — worktree на нём; скрипты — от `M/kits-audit/`.
- [ ] **AC1** — Жизненный цикл: flagkit, natskit, cachekit, tzkit проверяют конфиг и связь на старте и без потребителя; `Stop` при
  недоступном коллекторе — без ошибки и кода 1; ошибка конфига telemetry и строки go-redis — JSON-записи logkit. Проверка:
  `fx-observability/run.sh <rev> fx` → KX40, KX67 (a, d) PASS, KX66 — `Stop` без ошибки
- [ ] **AC2** — Одна конвенция сигналов: спаны БД — `db.system.name`, имя спана otelpgx — без текста SQL; ключ ошибки в логах —
  `error` во всех китах; `Stat` отсутствующего ключа — `not_found`; повтор кода TOTP — `replay`, не `ok`; метрик otelhttp у
  anthropickit нет; сигналы совпадают с доками. Проверка: `fx-observability/run.sh <rev> obs-unit obs` → KO15, KO16, KO18 PASS,
  `compare.py` — 0 расхождений с контрактом из доков HEAD, `promname` — 0 коллизий; `api-docs/claims-A-exp/run.sh <wt>` (`authn`)
  → `ok:1 replay:1`
- [ ] **AC3** — Фейки testkit ведут себя как боевые: расхождения зонда — только задокументированные (база — 26 из 50);
  `pgkit.WithTestTx` на ctx usecase'а не пропускает `Publish` мимо транзакции. Проверка: `tests-gate/run-all.sh <rev> probes` →
  расхождений вне доков нет; `api-docs/claims-A-exp/run.sh <wt>` (`fakes`, `logtest`, `clock`) → по докам
- [ ] **AC4** — Тесты убивают мутантов базового аудита: выжили только эквивалентные (с причиной); регрессия таймаута healthkit —
  падение теста, а не зависание пакета; тест метрик amocrmkit ловит смену типа инструмента; страж пакетных инструментов ловит то,
  что обещает `CLAUDE.md`. Проверка: `tests-gate/run-all.sh <rev> mutants`, `summarize.py` → выжили только эквивалентные; мутанты
  V-G2, V-J1b и зонды P-G4, P-G5 (`M/val-rel130/mut/`) → красные
- [ ] **AC5** — Контракт сигналов httpkit и паритет handlertest держит `make check`; ответ известного маршрута до роутинга — с его
  маршрутом, `unmatched` — только неизвестному. Проверка: `httpkit-b/run.sh <rev> exp mut gate` → HO01 PASS, `compare.py` и
  `compare_logs.py` — 0, выжили только эквивалентные мутанты (hk-ctr-05 убит), GH01, GH03, GH04, GH06, GH08 пойманы
- [ ] **AC6** — `archkit.Check` одной точкой входа держит раскладку любого сервиса на golibinfra: импорты между доменами и слоями
  (моки, sqlc-типы, свои подпакеты корня), каталоги `internal/`, чистые слои, корень домена без транспорта и I/O (и через `pkg/`,
  `internal/platform/`), часы, identity-closure между доменами, свои таблицы; module path — из `go.mod`; настройки и известные
  нарушения (с ID находки, устаревшая запись — FAIL) — у потребителя. Проверка: `go test -race ./archkit/...` → PASS: у каждого
  правила фикстура-нарушение красная, чистая — зелёная; мутант «правило снято» на каждое правило — красный
- [ ] **AC7** — Шаблон берёт archkit без потерь: в его worktree через `replace` чистое дерево зелёное, инъекции 1b, 3b, 13, M, ADP
  b, c, g, h, i, k — FAIL из archkit, копия с module path форка — правила действуют. Проверка: кейсы `M/s12/inj.py`,
  `M/valb2/inj.py` в worktree шаблона → как сказано, протокол — в «Где мы сейчас»
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
- [ ] **S1** — fx: fail-fast модулей без потребителя, best-effort экспорт telemetry, `FxLogger` без OTel-sink, go-redis через
  logkit (KFX-03, KFX-05, KFX-06, KOBS-08)
  - Проверка: AC1 → выполнен · Закрывает: AC1 · Зависит от: —
- [ ] **S2** — Конвенции сигналов: semconv БД, ключ `error`, `not_found`, TOTP, otelhttp anthropickit (KOBS-06, KOBS-07, KOBS-09,
  KDOC-05, класс KOBS-01)
  - Проверка: AC2 → выполнен · Закрывает: AC2 · Зависит от: —
- [ ] **S3** — Фейки данных и шины: `natstest`, `outboxtest`, `audittest`, `pgtest` с откатом, маркер `WithTestTx` (KTST-03,
  KTST-04, KDOC-08, бэклог `outboxtest-withtesttx`)
  - Проверка: зонд по этим фейкам → без расхождений вне доков · Закрывает: AC3 · Зависит от: —
- [ ] **S4** — Фейки логов, хешей, кэша и часов: `logtest`, `authntest`, `MemoryCache` и контракт `Cache`, таймеры clockkit
  (KTST-05, KDOC-08)
  - Проверка: AC3 → выполнен · Закрывает: AC3 · Зависит от: S3
- [ ] **S5** — Выжившие мутанты базового аудита убиты, таймаут healthkit с дедлайном, тип метрик amocrmkit (KTST-02, R4 валидатора
  A `harden-authn-release`; N1, N2 валидатора v1.3.0 — страж: `var f = func…` в `init()`, замыкание через хелпер, generic с двумя
  параметрами; первое значение `JWT_AUDIENCE`)
  - Проверка: AC4 → выполнен · Закрывает: AC4 · Зависит от: S4
- [ ] **S6** — Сигналы httpkit в гейте: контракт сигналов `Module` против доки, `unmatched`, мутанты httpkit и handlertest
  (KGATE-13, KTST-11, KOBS-12)
  - Проверка: AC5 → выполнен · Закрывает: AC5 · Зависит от: S2
### archkit
- [ ] **S7** — `archkit` через `/add-kit`: правила, настройки потребителя, фикстуры, README, строки в корневом README и CLAUDE.md
  - Проверка: AC6 → выполнен · Закрывает: AC6 · Зависит от: закрытый `fix-reference-code` шаблона
- [ ] **S8** — archkit на шаблоне: проводка и инъекции в его worktree, правки archkit по итогу, Handoff — по проверенной проводке
  - Проверка: AC7 → выполнен · Закрывает: AC7 · Зависит от: S7
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
- **fx.** `fx.Invoke` потребителя типа — в модулях, чьи доки обещают fail-fast (flagkit, natskit, cachekit, tzkit; KX40). Ошибки
  финального экспорта telemetry пишет её error-логгер, `Shutdown` — nil: телеметрия best-effort, а код 1 у Job без коллектора хуже
  (KX66). `FxLogger` — свой логгер из `logkit.Config` и `RedactConfig` без OTel-sink, как error-логгер telemetry (KX67a). go-redis
  — мост в logkit (WARN) по образцу `internal/grpclogkit`, ставится один раз (KX67d).
- **Сигналы.** cachekit — semconv версии natskit и httpclient (`db.system.name`, `db.operation.name`), старые ключи не
  дублируются; otelpgx — имя `query <операция>`, текст — в `db.query.text`; дока pgkit — к факту, KO15 — под неё. Ключ ошибки —
  `error`: его пишет `fxevent.SlogLogger` у `FxLogger` (natskit и telemetry меняются, канон шаблона — Handoff). Проверка TOTP без
  метрики — внутренняя, итог пишут `Verify` и `VerifyOnce`. anthropickit — noop `MeterProvider` у otelhttp, как httpclient.
- **Фейки.** Фейки зовут проверки боевых китов (natskit — subject, durable, DLQ-петля; payload и `Metadata` — `json.Marshal`,
  `\u0000`, UTF-8) и хранят `json.RawMessage`. `pgtest.Identity` — отменяемые записи в ctx, при ошибке `fn` фейки их выкидывают.
  Маркер транзакции фейка и `pgkit.WithTestTx` — разные: `outboxtest` принимает транзакцию кода под тестом, ручная пометка на ctx
  usecase'а — ошибка `Publish` (D02 шаблона). `logtest` — боевая цепочка logkit с захватом записей (`slog.Record`, маски).
  Контракт `Cache` — `testkit/cachetest.RunContract` по памяти и Redis. Оставленное различие — строкой в доке фейка.
- **Сигналы httpkit в гейте.** Тест на живом `Module`, без Docker: инвентарь метрик (labels, значения, unit), спанов и строк
  access-log (уровень по статусу, `route`, `duration_ms`) против таблиц `httpkit/docs/observability.md` — тест читает доку, как
  `envdoc` читает env-таблицы (GH06); коллизии — `metrictest`. Пустой `RoutePattern` — `Routes.Find`, у unmatched `http.route` нет
  (HO01). Эквивалентный мутант — с причиной здесь.
- **archkit.** Кит без `Module`, конфига и сигналов, как ptrkit: `archkit.Check(t, opts…)`; корень и module path — из ближайшего
  `go.mod` (stdlib, без `golang.org/x/mod`). Правила — семь проверок archtest шаблона: `TestInternalLayout`,
  `TestImportBoundaries`, `TestDomainRootIsTransportAgnostic`, `TestPureLayersHaveNoIO`, `TestNoWallClockInDomains`,
  `TestNoCrossDomainIdentityClosure`, `TestQueriesUseOwnTables`. Корень домена видит пакет `pkg/` или `internal/platform/`, только
  если тот без I/O насквозь — пункт `repair-harness` S7. У потребителя — внешние пакеты корня, пакеты модуля для чистых слоёв,
  свои каталоги `internal/`, известные нарушения с ID находки (устаревшая запись — FAIL, как `allowlist.List`). Новое или более
  строгое правило — minor со строкой Breaking.
- **archkit на шаблоне** — потребительский эксперимент программы (§2 replan): правила проверяются на живом шаблоне и на копии с
  module path форка; инъекции AC9 `repair-harness` — его критерий.
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
  v1.3.0, сборка шаблона через `replace`, эксперименты S13.

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
- **archkit.** Семь проверок `internal/archtest` (список — «Решения») → один тест `archkit.Check(t, …)`: внешние пакеты корня —
  нынешний `rootLibs`, чистым слоям — `internal/platform/identity` и `pkg/policy`, каталоги `internal/` — `testkit` и `archtest`,
  известных нарушений нет; правила обвязки остаются в archtest («Не входит»), пометка archtest у перенесённых инвариантов в
  правилах и package-doc — на archkit, «корень домена не дотягивается до I/O через `internal/platform/*`» — правило archkit.
- **Логи, тесты, telemetry.** Ключ ошибки — `error`, не `err`, в каноне и коде; `outboxtest`: ручная `pgkit.WithTestTx` на ctx
  usecase'а — ошибка, транзакцию открывает `pgtest`; `natstest` проверяет стримы — объявить их в тесте; `logtest` — с масками;
  `Stop` telemetry без коллектора — без ошибки и кода 1.
- **`/plan`.** Строка «Формат проверяет …» SKILL.md — из golibinfra, pre-commit — в `references/repo.md` шаблона: копии совпадают.

## Где мы сейчас
Первая половина партии 3 закрыта (`harden-kits-contracts`: S14 — эксперименты AC1–AC11 PASS); `fix-reference-code` шаблона закрыт
— перед S7 сверить `git log -- internal/archtest` шаблона. Для S13: эксперименты правлены под API партии, оригиналы —
`M/exs8/orig/*.v130`, `*.s12`; подтест H15 про `[{}]` не адаптирован (старт — `ErrRoutes` по решению); `expected-signals.json` —
из доков HEAD; KX64 — FX-12. KDOC-09: 255 у auditkit и сессий — уже в доках. Следующий шаг — S1, перед ним отдельным `fix` —
мелочи перепроверки C: граница масок (заголовок строкой в msg, `Cookie: …`, — только атрибутом) — в `logkit/README.md`; `@` в пути
`STORAGE_PUBLIC_BASE_URL` и OTLP endpoint — по экранированному пути (`%40` — не `ErrConfig`), свой текст ошибки.

Заготовка аннотации v1.4.0 (первая половина — `harden-kits-contracts`; вторая — по шагам):
- **Breaking.** config: App — именами тегов (`POSTGRES_PG_DSN` → `PG_DSN`); ошибка `Load`: поле без тега (тег или
  `ignored:"true"`), тег на вложенной структуре (снять, имена — `PG_*`, не `DB_PG_DSN`; вторая секция кита — `ignored:"true"` и
  свой `config.Load(&a.X, config.EnvPrefixed("x"))`, незаданная `X_*` — из голой), `map`, `[]byte`, `url.URL` или тип только с
  `UnmarshalBinary`/`Set` (строка с разбором в `Validate` или тип с `UnmarshalText`); `*envconfig.ParseError` нет. natskit:
  `NewConfigPrefixed` → `NewInstanceConfig`, `HealthProbe` → `ProvideProbe`, `NewBus(cfg, conn, log) (Bus, error)`,
  `NewStreamBus(cfg, conn, log)`. healthkit, timerkit: `Group` → `ProbesGroup`/`JobsGroup`, `Provide` →
  `ProvideProbe`/`ProvideJob`. pgkit: `ProvideMigrations` → `SupplyMigrations`; `MigrateUp/Down/UpAll(ctx, dsn, lockTimeout, …)`
  (> 0, команда миграций — `cfg.LockTimeout`); `Module` берёт `logkit.Logger`; литерал `MigrateRunnerConfig` без `LockTimeout` —
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
  `Cache.CompareAndSet` (свой `Cache` — реализовать), `IdempotencyStore.Set(ctx, key, token, record, ttl) (bool, error)`.
  httpclient: `Validate` — value-приёмник, группа опций → значение `httpclient.Options`, литерал `Config` без `MaxHosts` —
  `ErrConfig` (`1024` или `-1`). storagekit: `NewKey` → `string`, `MemoryStorage.Bytes`/`Keys` удалены (`Get`); `Config.UseSSL`
  (`STORAGE_S3_USE_SSL`) удалён — endpoint без схемы — TLS, plaintext — `http://host:port`;
  `minio.NewStorage(cfg, client, urls, log)` — с ошибкой. telemetry: `Headers.Decode` — без `ErrConfig`; `OTEL_TOKEN` или
  `Authorization` к `http://` не на loopback — `ErrConfig`, и при `OTEL_SDK_DISABLED=true` (https или
  `OTEL_ALLOW_INSECURE_TOKEN=true`). auditkit: NUL, не-UTF-8 и суррогат без пары в строках и `Metadata` (и ключах), `Metadata` >
  64 КиБ JSON — `ErrInvalidEntry` до базы (`result=invalid`): ввод пользователя — проверить до `Log`. httpkit:
  `NewServer(cfg, log, opts…) (*Server, error)` — с цепочкой кита и `HumaAuth` (`WithSecuritySchemes`, `WithIdempotencyStore`;
  `WithLogger` удалён); `ClientIP(hops, log)`, `RegisterHealth(api, timeout, log, probes…)`, `HumaAuth(schemes, log)`,
  `Idempotency(cfg, store, log)`, `OTel(trust, quiet…)`; схема — `ProvideSecurityScheme(ctor)`, extractor — её поле `Extract`
  (`SecuritySchemeEntry` несравним: `==` и ключ map не компилируются); удалены `AuthExtractor` в графе, `WithAuthExtractor`,
  `IdempotencyOption`, `WithAuth`; схема `Security` вне `components` и `Security` из одних `{}` — ошибка старта (`{}` —
  `httpkit.Public`); идемпотентность — только у операций huma (маршрут мимо huma с `Idempotency-Key` исполняется каждый раз —
  операция huma или обёртка `httpkit.Idempotency`); `IdempotencyStore.Set(ctx, key, token, record, ttl) (stored bool, err error)`
  — только поверх резерва этого токена (свой store — `testkit/idempotencytest.RunContract`); литерал `Config` без `RequestTimeout`
  — `ErrConfig` (`HTTP_REQUEST_TIMEOUT` 25s, меньше `HTTP_WRITE_TIMEOUT` и `RESERVE_TTL`; `-1s` — выкл.); handlertest — с цепочкой
  кита, `WithAuth` → `WithSecuritySchemes` (схема с `Extract`). oidckit, hubkit/sso: `Module` не кладёт `httpkit.AuthExtractor` —
  схемы с extractor'ом (hubkit — cookie своего контура).
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
  `auditkit/pgstore.Module` даёт и `*Store`.
- **Изменения.** Новое API, env и метрики — по `make release-diff PREV=v1.3.0` (apidiff, env, метрики); сверх них — ридер ENV без
  envconfig, миграция outbox 0003, `testkit/idempotencytest`, срок хранения аудита (opt-in: `auditkit/pgstore.RetentionModule`,
  `AUDIT_RETENTION_DAYS`, `audit_log_retention_deleted_total`).
