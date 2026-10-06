---
status: blocked
parent: go-backend-template:audit-program
blocked-by: harden-kits-contracts — первая половина партии 3 идёт раньше (после тега v1.3.0)
---

# harden-tests-archkit-release — партия 3, часть 2: fx, сигналы, тесты, archkit, гейт, релиз v1.4.0

## Цель
Вторая половина партии 3 и тег v1.4.0, после которого каждый угол аудита китов ≥ 9 (выше minor
ничего, эксперименты чисты). Здесь — fx и конвенции сигналов, фейки и сила тестов, сигналы httpkit
в гейте; `archkit` — общие правила раскладки из `internal/archtest` шаблона (решение владельца:
узко); гейт и обвязка, единый скилл `/plan` с шаблоном, доки; повтор всех экспериментов аудита и
релиз, на который шаблон переходит в `repair-harness` S7.

## Контекст
- **Порядок.** После `harden-kits-contracts`: его Handoff и заготовка аннотации переносятся сюда
  при его закрытии. Программа, метод и push — `go-backend-template/.claude/plans/audit-program.md`;
  находки и «Как повторить аудит» — `M/kits-audit/summary.md` и отчёты углов
  (`M` — `/home/mm/.cache/stit-audit-program/`).
- **archkit — источник.** `go-backend-template/internal/archtest/` (`archtest_test.go`,
  `imports_test.go`, `crossdomain_test.go`; обвязка шаблона — `harness_test.go`, `template_test.go`),
  allowlist — `internal/testkit/allowlist`. Потребитель — `repair-harness` S7 и AC9 (инъекции 1b,
  3b, 13, M, ADP b, c, g, h, i, k — `M/s12-harden-gate.md`, кейсы `M/s12/inj.py`,
  `M/valb2/inj.py`). Раскладку `internal/domain/<d>` делят три шаблона; у ai-gateway свои каталоги
  `internal/` (`gen`, `jobs`). Ограничения — `M/replan-golibinfra.md` §5: только общее для
  раскладки, одна точка входа, module path из `go.mod`, ужесточение — minor с шагом миграции.
- **Обвязка.** Скилл `/plan` шаблона после `repair-harness` S1 разошёлся с копией здесь
  (маршрут — таблица CLAUDE.md, накопитель handoff, строка про pre-commit); остальные репо
  экосистемы держат прежний канон — вне программы.
- **Гейт сейчас:** матрица `M/kits-audit/tests-gate/gate_inject.py` — не пойман G04 (KGATE-08);
  pre-push зовёт `scripts/gate-clean.sh` рабочего дерева; `commit-msg` нет; `/release` ищет
  breaking `grep '!:'`; версии тулов не сверяются.
- **Правила шага:** README и `docs/` кита — тем же коммитом (`make envdoc-check`); breaking — `!`,
  `BREAKING CHANGE:` и строка заготовки аннотации; новых зависимостей в `go.mod` нет; новый кит —
  через `/add-kit`.

## Не входит
- Первая половина партии 3 — `harden-kits-contracts`.
- Правила обвязки шаблона (HTTP-типы, таблицы ошибок, subject'ы, `Trusted`, `fx.As`, тесты без
  утверждений, проводка доменов, проза, `//nolint`, `.template`) — остаются в его archtest и едут
  git-способом: `archkit` узкий.
- Сырой тег группы timerkit — не правило раскладки, `backlog.md`; копии `/plan` в остальных репо
  экосистемы; правки шаблона — его `repair-harness` по Handoff; push — дирижёр.

## Критерии приёмки
`<rev>` — проверяемый коммит, `<wt>` — worktree на нём; скрипты — от `M/kits-audit/`.
- [ ] **AC1** — Жизненный цикл: flagkit, natskit, cachekit, tzkit проверяют конфиг и связь на
  старте и без потребителя; `Stop` при недоступном коллекторе — без ошибки и кода 1; ошибка конфига
  telemetry и строки go-redis — JSON-записи logkit. Проверка: `fx-observability/run.sh <rev> fx` →
  KX40, KX67 (a, d) PASS, KX66 — `Stop` без ошибки
- [ ] **AC2** — Одна конвенция сигналов: спаны БД — `db.system.name`, имя спана otelpgx — без текста
  SQL; ключ ошибки в логах — `error` во всех китах; `Stat` отсутствующего ключа — `not_found`;
  повтор кода TOTP — `replay`, не `ok`; метрик otelhttp у anthropickit нет; сигналы совпадают с
  доками. Проверка: `fx-observability/run.sh <rev> obs-unit obs` → KO15, KO16, KO18 PASS,
  `compare.py` — 0 расхождений с контрактом из доков HEAD, `promname` — 0 коллизий;
  `api-docs/claims-A-exp/run.sh <wt>` (`authn`) → `ok:1 replay:1`
- [ ] **AC3** — Фейки testkit ведут себя как боевые: расхождения зонда — только задокументированные
  (база — 26 из 50); `pgkit.WithTestTx` на ctx usecase'а не пропускает `Publish` мимо транзакции.
  Проверка: `tests-gate/run-all.sh <rev> probes` → расхождений вне доков нет;
  `api-docs/claims-A-exp/run.sh <wt>` (`fakes`, `logtest`, `clock`) → по докам
- [ ] **AC4** — Тесты убивают мутантов базового аудита: выжили только эквивалентные (с причиной);
  регрессия таймаута healthkit — падение теста, а не зависание пакета; тест метрик amocrmkit ловит
  смену типа инструмента. Проверка: `tests-gate/run-all.sh <rev> mutants`, `summarize.py` → выжили
  только эквивалентные
- [ ] **AC5** — Контракт сигналов httpkit и паритет handlertest держит `make check`; ответ известного
  маршрута до роутинга — с его маршрутом, `unmatched` — только неизвестному. Проверка:
  `httpkit-b/run.sh <rev> exp mut gate` → HO01 PASS, `compare.py` и `compare_logs.py` — 0,
  выжили только эквивалентные мутанты (hk-ctr-05 убит), GH01, GH03, GH04, GH06, GH08 пойманы
- [ ] **AC6** — `archkit.Check` одной точкой входа держит раскладку любого сервиса на golibinfra:
  импорты между доменами и слоями (моки, sqlc-типы, свои подпакеты корня), каталоги `internal/`,
  чистые слои, корень домена без транспорта и I/O (и через `pkg/`, `internal/platform/`), часы,
  identity-closure между доменами, свои таблицы; module path — из `go.mod`; настройки и известные
  нарушения (с ID находки, устаревшая запись — FAIL) — у потребителя. Проверка: `go test -race
  ./archkit/...` → PASS: у каждого правила фикстура-нарушение красная, чистая — зелёная; мутант
  «правило снято» на каждое правило — красный
- [ ] **AC7** — Шаблон берёт archkit без потерь: в его worktree через `replace` чистое дерево
  зелёное, инъекции 1b, 3b, 13, M, ADP b, c, g, h, i, k — FAIL из archkit, копия с module path
  форка — правила действуют. Проверка: кейсы `M/s12/inj.py`, `M/valb2/inj.py` в worktree шаблона →
  как сказано, протокол — в «Где мы сейчас»
- [ ] **AC8** — Гейт держит запреты и свой конфиг: `fmt.Print*`, `slog.Default`, `http.DefaultClient`
  и `:latest` в Go-коде краснят `make check` (G01); линтер, снятый из `enable` (G04), и чужая версия
  golangci-lint или govulncheck — отказ; коммит не по Conventional Commits не создаётся; `/release`
  берёт несовместимое из apidiff, env- и metric-diff. Проверка: `tests-gate/run-all.sh <rev> gate` →
  все обязательные отказы пойманы, контроли зелёные; `git commit -m "update stuff"` → отказ хука
- [ ] **AC9** — Push проверяет то, что пушит: правка `scripts/gate-clean.sh` в рабочем дереве гейт
  push не выключает; guard не отказывает на чтении хуков. Проверка: `printf 'exit 0' >
  scripts/gate-clean.sh` без коммита, push HEAD с падающим тестом в bare → отказ;
  `M/hgd/guard_cases.py`, `M/val-hgd-B/guard_b.py` и `find -name pre-push`, `awk`/`sort`/`cp` из
  хука → 0 расхождений
- [ ] **AC10** — Доки совпадают с кодом: статика — 0 расхождений; ✗ ручной сверки закрыты; P7 HX01
  назван в доке порядка хуков. Проверка: `api-docs/scripts/run-static.sh <rev>` → D3, D4, D6, D10 —
  0, D7 — ERR только от заглушек; `httpkit-a/static.sh <rev>` → 0; пересчёт `api-docs/claims-*.md`
  и `httpkit-a/claims.md` → ✗ нет
- [ ] **AC11** — Скилл `/plan` — одна копия с шаблоном. Проверка: `diff` `.claude/skills/plan/SKILL.md`
  и `references/plan-template.md` с копиями шаблона → только строка про pre-commit, которую шаблон
  снимает по Handoff; `make plan-check` → OK
- [ ] **AC12** — Все эксперименты «Как повторить аудит» на кандидате тега — «чисто» по критериям
  `M/kits-audit/summary.md`; находок со статусом «п3» выше minor открытых нет. Проверка: прогоны
  всех групп шести каталогов углов → таблица исходов в «Где мы сейчас»
- [ ] **AC13** — Тег `v1.4.0` готов:
  - аннотированный, с разделами Breaking, Поведение, Изменения, Гейт;
  - каждая строка apidiff `v1.3.0 → v1.4.0` по объёму и каждое изменение envconfig-тегов, дефолтов
    и имён метрик — в аннотации с шагом миграции;
  - каждая ошибка сборки шаблона на HEAD (`replace`) закрыта строкой Breaking;
  - `make gate-clean` и `make test-full` зелёные.

  Проверка: `git tag -l --format='%(contents)' v1.4.0` → четыре раздела;
  `api-docs/scripts/run-versions.sh v1.3.0 v1.4.0`, `httpkit-a/versions.sh v1.3.0 v1.4.0` → всё
  покрыто; `make gate-clean` → зелёный

## Шаги
### Жизненный цикл, сигналы, тесты
- [ ] **S1** — fx: fail-fast модулей без потребителя, best-effort экспорт telemetry, `FxLogger` без
  OTel-sink, go-redis через logkit (KFX-03, KFX-05, KFX-06, KOBS-08)
  - Проверка: AC1 → выполнен · Закрывает: AC1 · Зависит от: —
- [ ] **S2** — Конвенции сигналов: semconv БД, ключ `error`, `not_found`, TOTP, otelhttp anthropickit
  (KOBS-06, KOBS-07, KOBS-09, KDOC-05, класс KOBS-01)
  - Проверка: AC2 → выполнен · Закрывает: AC2 · Зависит от: —
- [ ] **S3** — Фейки данных и шины: `natstest`, `outboxtest`, `audittest`, `pgtest` с откатом,
  маркер `WithTestTx` (KTST-03, KTST-04, KDOC-08, бэклог `outboxtest-withtesttx`)
  - Проверка: зонд по этим фейкам → без расхождений вне доков · Закрывает: AC3 · Зависит от: —
- [ ] **S4** — Фейки логов, хешей, кэша и часов: `logtest`, `authntest`, `MemoryCache` и контракт
  `Cache`, таймеры clockkit (KTST-05, KDOC-08)
  - Проверка: AC3 → выполнен · Закрывает: AC3 · Зависит от: S3
- [ ] **S5** — Выжившие мутанты базового аудита убиты, таймаут healthkit с дедлайном, тип метрик
  amocrmkit (KTST-02, R4 валидатора A `harden-authn-release`)
  - Проверка: AC4 → выполнен · Закрывает: AC4 · Зависит от: S4
- [ ] **S6** — Сигналы httpkit в гейте: контракт сигналов `Module` против доки, `unmatched`, мутанты
  httpkit и handlertest (KGATE-13, KTST-11, KOBS-12)
  - Проверка: AC5 → выполнен · Закрывает: AC5 · Зависит от: S2

### archkit
- [ ] **S7** — `archkit` через `/add-kit`: правила, настройки потребителя, фикстуры, README, строки в
  корневом README и CLAUDE.md
  - Проверка: AC6 → выполнен · Закрывает: AC6 · Зависит от: закрытый `fix-reference-code` шаблона
- [ ] **S8** — archkit на шаблоне: проводка и инъекции в его worktree, правки archkit по итогу,
  Handoff — по проверенной проводке
  - Проверка: AC7 → выполнен · Закрывает: AC7 · Зависит от: S7

### Гейт, обвязка, доки
- [ ] **S9** — Запреты и конфиг гейта машиной: forbidigo, contextcheck после пробы, `:latest`,
  эталон линтеров, версии тулов, пути гейта в «Сначала спросить» (KGATE-07, KGATE-08, KGATE-10)
  - Проверка: `tests-gate/run-all.sh <rev> gate` → G01, G04 пойманы · Закрывает: AC8 · Зависит от: —
- [ ] **S10** — Хуки и релиз: pre-push из пушимого коммита, `commit-msg`, `make release-diff` для
  `/release`, guard без ложных отказов (KGATE-08, KGATE-09, бэклог `gate-hook-paths`)
  - Проверка: AC8, AC9 → выполнены · Закрывает: AC8, AC9 · Зависит от: S9
- [ ] **S11** — Скилл `/plan` и маршрут: одна копия с шаблоном, «Точка входа» CLAUDE.md — таблица
  «Маршрут задачи», специфика репо — в `references/repo.md`
  - Проверка: AC11 → выполнен · Закрывает: AC11 · Зависит от: —
- [ ] **S12** — Доки к коду: остаток KDOC-07, KDOC-09 без бюджета outbox, KDOC-10, KDOC-11, P7 HX01,
  корневые README и CLAUDE.md — к API партии; ✗ ручной сверки пересчитаны
  - Проверка: AC10 → выполнен · Закрывает: AC10 · Зависит от: S1, S2, S6, S8, S10

### Релиз
- [ ] **S13** — Повтор всех экспериментов «Как повторить аудит» на кандидате тега — агентом, не
  писавшим код партии; таблица исходов — в «Где мы сейчас»
  - Проверка: AC12 → выполнен · Закрывает: AC12 · Зависит от: S5, S11, S12
- [ ] **S14** — Релиз: `make test-full` и `make gate-clean`; `make release-diff PREV=v1.3.0`; сборка
  шаблона на HEAD через `replace` в своём worktree (каждая ошибка — строкой Breaking); тег `v1.4.0`
  локально с разделами Breaking, Поведение, Изменения, Гейт. Push `main` и тега — дирижёр после
  PASS валидатора партии; затем план удаляется отдельным `chore(plan)`
  - Проверка: AC13 → выполнен · Закрывает: AC13 · Зависит от: S13

## Решения
- **fx.** `fx.Invoke` потребителя типа — в модулях, чьи доки обещают fail-fast (flagkit, natskit,
  cachekit, tzkit; KX40). Ошибки финального экспорта telemetry пишет её error-логгер, `Shutdown` —
  nil: телеметрия best-effort, а код 1 у Job без коллектора хуже (KX66). `FxLogger` — свой логгер из
  `logkit.Config` и `RedactConfig` без OTel-sink, как error-логгер telemetry (KX67a). go-redis — мост
  в logkit (WARN) по образцу `internal/grpclogkit`, ставится один раз (KX67d).
- **Сигналы.** cachekit — semconv версии natskit и httpclient (`db.system.name`,
  `db.operation.name`), старые ключи не дублируются; otelpgx — имя `query <операция>`, текст — в
  `db.query.text`; дока pgkit — к факту, KO15 — под неё. Ключ ошибки — `error`: его пишет
  `fxevent.SlogLogger` у `FxLogger` (natskit и telemetry меняются, канон шаблона — Handoff).
  Проверка TOTP без метрики — внутренняя функция, итог пишут `Verify` и `VerifyOnce`. anthropickit —
  noop `MeterProvider` у otelhttp, как httpclient.
- **Фейки.** Фейки зовут проверки боевых китов (natskit — subject, durable, DLQ-петля; payload и
  `Metadata` — `json.Marshal`, `\u0000`, UTF-8) и хранят `json.RawMessage`. `pgtest.Identity` —
  отменяемые записи в ctx, при ошибке `fn` фейки их выкидывают. Маркер транзакции фейка и
  `pgkit.WithTestTx` — разные: `outboxtest` принимает транзакцию кода под тестом, ручная пометка на
  ctx usecase'а — ошибка `Publish` (D02 шаблона). `logtest` — боевая цепочка logkit с захватом
  записей (`slog.Record`, маски). Контракт `Cache` — `testkit/cachetest.RunContract` по памяти и
  Redis. Оставленное различие — строкой в доке фейка.
- **Сигналы httpkit в гейте.** Тест на живом `Module`, без Docker: инвентарь метрик (labels,
  значения, unit), спанов и строк access-log (уровень по статусу, `route`, `duration_ms`) против
  таблиц `httpkit/docs/observability.md` — тест читает доку, как `envdoc` читает env-таблицы (GH06);
  коллизии — `metrictest`. Пустой `RoutePattern` — `Routes.Find`, у unmatched `http.route` нет
  (HO01). Эквивалентный мутант — с причиной здесь.
- **archkit.** Кит без `Module`, конфига и сигналов, как ptrkit: `archkit.Check(t, opts…)`; корень и
  module path — из ближайшего `go.mod` (stdlib, без `golang.org/x/mod`). Правила — семь проверок
  archtest шаблона: `TestInternalLayout`, `TestImportBoundaries`, `TestDomainRootIsTransportAgnostic`,
  `TestPureLayersHaveNoIO`, `TestNoWallClockInDomains`, `TestNoCrossDomainIdentityClosure`,
  `TestQueriesUseOwnTables`. Корень домена видит пакет `pkg/` или `internal/platform/`, только если
  тот без I/O насквозь — пункт `repair-harness` S7. У потребителя — внешние пакеты корня, пакеты
  модуля для чистых слоёв, свои каталоги `internal/`, известные нарушения с ID находки (устаревшая
  запись — FAIL, как `allowlist.List`). Новое или более строгое правило — minor со строкой Breaking.
- **archkit на шаблоне** — потребительский эксперимент программы (§2 replan): правила проверяются на
  живом шаблоне и на копии с module path форка; инъекции AC9 `repair-harness` — его критерий.
- **Гейт.** forbidigo — `fmt.Print*`, `print`/`println`, `log.*`, `slog.Default` и функции пакета
  `slog`, `http.DefaultClient`, `http.Get/Post/Head`; contextcheck — если проба без ложных, иначе
  причина здесь; `:latest` — проверка Go-кода шагом гейта. Эталон линтеров и форматтеров — файл в
  `scripts/`, его сверка с `golangci-lint linters --json` — в шаге `lint` (G04); версии — против
  пинов `Makefile`, ставит `make tools`; apidiff — пиненный через `make tools`, не в `go.mod`. Пути
  гейта (`Makefile`, `scripts/`, `.golangci.yml`, `.claude/settings.json`, `.claude/hooks/`) — в
  «Сначала спросить».
- **Хуки.** pre-push берёт `scripts/gate-clean.sh` из пушимого коммита: правка в дереве гейт не
  выключает, правка в коммите видна в диффе. `commit-msg` — по образцу шаблона, `hooks-check` — оба
  хука. `make release-diff PREV=…` — apidiff по пакетам, diff env-тегов и дефолтов (envdoc) и имён
  метрик; `/release` требует строку аннотации на каждую несовместимость.
- **`/plan`.** Тело SKILL.md — общее: нужен ли план, решает таблица «Маршрут задачи» CLAUDE.md
  («Точка входа» golibinfra становится ею), «в какой гейт и хук входит проверка — там же», накопитель
  handoff — «из профиля»; профиль golibinfra: pre-commit и накопителя нет, Handoff — в аннотацию.
  `validate-plan.sh` — без накопителя: он нужен релизу образов шаблона. Сверка — с копией шаблона
  на момент шага.
- **Эксперименты и релиз.** S13 — агент, не писавший код партии; контракты сигналов
  (`expected-signals*.json`) — заново из доков HEAD; инструменты — с поправками N4 валидатора A
  `harden-gate-defaults` (L3 через `make lint`, G21 по тексту отказа); правка эксперимента под API
  — со смыслом, оригинал рядом. «Гейт» аннотации: `make gate-clean`, `make test-full`,
  `release-diff` против v1.3.0, сборка шаблона через `replace`, эксперименты S13.

## Handoff
**Шаблон, `repair-harness` S7 — bump v1.4.0** (плюс Handoff `harden-kits-contracts` при его закрытии):
- **archkit.** Семь проверок `internal/archtest` (список — «Решения») → один тест с
  `archkit.Check(t, …)`: внешние пакеты корня — нынешний `rootLibs`, чистым слоям —
  `internal/platform/identity` и `pkg/policy`, каталоги `internal/` — `testkit` и `archtest`,
  известных нарушений нет. Правила обвязки остаются в archtest («Не входит»); пометка archtest у
  перенесённых инвариантов в правилах и package-doc — на archkit; «корень домена не дотягивается до
  I/O через `internal/platform/*`» — правило archkit.
- **Лесенка** `cmd/migrate` — `testkit.MigrationLadder` (с v1.3.0), если её не взял
  `fix-reference-code` S9.
- **Логи.** Ключ ошибки — `error`, не `err`, в каноне и коде.
- **Тесты.** `outboxtest`: ручная `pgkit.WithTestTx` на ctx usecase'а — ошибка, транзакцию открывает
  `pgtest`; `natstest` проверяет стримы — объявить их в тесте; `logtest` — с масками.
- **`/plan`.** Строка «Формат проверяет …» SKILL.md — из golibinfra, pre-commit — в
  `references/repo.md` шаблона: копии совпадают.
- **telemetry.** `Stop` без коллектора — без ошибки и кода 1.

**Аннотация v1.4.0:** первая половина — из `harden-kits-contracts`, вторая — по шагам в «Где мы
сейчас».

## Где мы сейчас
План заведён; шаги не начаты — после `harden-kits-contracts`. S7 — после закрытия
`fix-reference-code` шаблона (его правила раскладки к этому моменту стабильны): перед шагом сверить
`git log -- internal/archtest` шаблона. Строки бэклога «партия 3», появившиеся после коммита плана,
— в шаги. Следующий шаг — S1.
