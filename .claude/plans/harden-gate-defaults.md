---
status: in-progress
parent: go-backend-template:audit-program
blocked-by: —
---

# harden-gate-defaults — партия 2, часть 1: гейт, публичные операции, IP клиента, безопасные дефолты

## Цель
Первая половина партии 2 программы исправлений по аудиту (тег v1.3.0). Сначала гейт golibinfra
становится честным: integration на Docker в `make check`, окружение его не сужает, кеш линтера —
свой у каждого checkout'а, pre-push работает, kit-owned миграции проходят лесенку. Затем — пункты
шаблона B1–B3 с уточнениями аудита: явные публичные операции, IP клиента в ctx с одной настройкой
доверия прокси, безопасные дефолты docs и outbox. Вторая половина и релиз — `harden-authn-release`.

## Контекст
- **Программа.** Метод («исполнитель этапа → валидатор»), политика push и решения владельца —
  `go-backend-template/.claude/plans/audit-program.md`; состав партий — `M/replan-golibinfra.md` §6.
- **Находки** — базовый аудит `M/kits-audit/` (`M` — `/home/mm/.cache/stit-audit-program/`):
  сводка `summary.md` (оценки, все находки со статусом, повтор экспериментов) и отчёты углов
  `tests-gate.md` (KGATE, KTST), `httpkit-a.md`, `httpkit-b.md`, `correctness-security.md`,
  `fx-observability.md`, `api-docs.md`. Эксперименты — `run.sh` углов: тест утверждает желаемое,
  FAIL — дефект есть; тяжёлые прогоны — последовательно.
- **Гейт.** `make check` — шаги `GATE_STEPS` с integration и guard `gate_bad` (`Makefile`),
  `scripts/gate-clean.sh`, `scripts/git-hooks/pre-push`, `.claude/settings.json` с
  `.claude/hooks/guard-bash.sh`, лесенка `testkit.MigrationLadder`, `scripts/envdoc` (S1–S5).
  Образец — гейт шаблона (`Makefile`, `scripts/git-hooks/`, `scripts/tools/`).
- **httpkit на v1.2.0.** Старт проверяет только `Security` без extractor'а, по `OpenAPI().Paths` —
  скрытых операций не видит (`httpkit/module.go:124-178`). `ClientIP` и
  `HTTP_RATE_LIMIT_TRUST_PROXY` служат только лимитеру (`httpkit/ratelimit.go:13-104`).
  `HTTP_DOCS_ENABLED` по умолчанию `true` (`httpkit/server.go:39`). У outbox
  `OUTBOX_MAX_ATTEMPTS=0` — без предела (`outboxkit/config.go:41`).
- **Потребитель** — шаблон, `fix-reference-code` S9 (bump v1.3.0): снимает `publicOperations`
  (`cmd/api/api_test.go:74-115`), client-IP middleware (`internal/platform/authgate/clientip.go`) и
  `identity.WithIP`. Полный Handoff шаблону — в `harden-authn-release`.
- **Правила шага:** README и `docs/` кита — тем же коммитом (`make envdoc-check`); breaking — `!` и
  `BREAKING CHANGE:`.

## Не входит
- Партия 3 (v1.4.0): `archkit`, medium и minor вне этих планов, доки — `backlog.md`, триггер
  «партия 3».
- B4–B8, high из D, кандидаты C и релиз v1.3.0 — `harden-authn-release`.
- Правки шаблона и шлюзов — по аннотации тега.

## Критерии приёмки
Проверки — в своём worktree на HEAD; `gate_inject.py` — это
`M/kits-audit/tests-gate/gate_inject.py --repo <worktree> --base <rev> --only …`.
- [x] **AC1** — `make check` гоняет unit и integration всех пакетов (testcontainers, `-race`); без
  Docker вне `-short` integration-тест падает, а не пропускается; баг, видимый только integration,
  и битая up-миграция кита краснят `make check` и `make gate-clean`; timing-тест logkit в гейт не
  входит. Проверка: `gate_inject.py --only G25,G26,G30` → все пойманы; `make check` → зелёный,
  итоговая строка называет охват `./...`
  - Подтверждено: на кандидате S1 G25, G26 (`Docker is unavailable` — FAIL), G30 пойманы;
    `make check` → `✓ check clean (./...)`; `go test -run TestRedact_Cost -v ./logkit/` → SKIP
- [x] **AC2** — `PKGS`, `GO`, `GOFLAGS` (и `go env -w`), `A`, флаги `-i`/`-n`/`-k`, `MAKEFLAGS=i` не
  сужают и не глушат гейт: `make check` отказывается запускаться или проверяет всё; `gate-clean` и
  pre-push окружение вызывающего не наследуют. Проверка:
  `gate_inject.py --only G14,G15,G16,G17,G18,G19,G20,G24` → все пойманы
  - Подтверждено: на кандидате S2 все восемь пойманы (G17–G19 — отказ guard'а); вручную `-n`,
    `-q`, `-t`, `MAKE=`, `SHELL=`, `.SHELLFLAGS=`, `MAKEFILES`, `GOFLAGS` в `GOENV` → отказ;
    `make -k check`, `MAKEFLAGS=i scripts/gate-clean.sh` с нарушением → rc=2 (`M/hgd/s2/`)
- [x] **AC3** — Вердикт lint не зависит от чужого checkout'а: кеш golangci-lint — внутри checkout'а,
  у `gate-clean` — свой холодный. Проверка: сценарий L3 `lint_cache_probe.sh` через `make lint` в
  двух worktree → во втором настоящее нарушение красное
  - Подтверждено: `M/hgd/lint_cache_make_probe.sh <rev>` (L3 через `make lint`, общий кеш в
    окружении) → в W2 нарушение красное; `GOLANGCI_DIFF_PROCESSOR_PATCH` lint не глушит
- [x] **AC4** — Push проходит гейт: pre-push гоняет `make check` на пушимом коммите при чистом
  дереве; неактивный pre-push роняет `make check` с подсказкой `make hooks`; обходы хуков из сессии
  Claude заблокированы; CLAUDE.md, README, CONTRIBUTING и скиллы `/plan`, `/release` описывают этот
  гейт. Проверка: `gate_inject.py --only G21,G31` → оба пойманы; `make check` при неактивном
  pre-push → FAIL
  - Подтверждено: на кандидате S3 G21 и G31 пойманы; чистый зелёный HEAD пушится, не-HEAD ref и
    `MAKEFLAGS=i git push` с падающим тестом — отказ; `make check` без `make hooks` → FAIL с
    подсказкой; `M/hgd/guard_cases.py` — 84 случая, 0 расхождений (`M/hgd/s3/`)
- [x] **AC5** — Каждый kit-owned источник миграций (auditkit, bruteforcekit, outboxkit с
  `TableMigrations`) проходит в `make check` лесенку up → down до нуля → up со сверкой схемы
  хелпером testkit, которым потом пользуется шаблон. Проверка: `gate_inject.py --only G28` и тот же
  битый down под `make check` → пойманы
  - Подтверждено: на кандидате S4 G28 и тот же битый down под `make check` пойманы
    (`TestMigrations_Ladder` bruteforcekit: `down 2: … does not exist`); down 0002 bruteforcekit
    без возврата `locked_idx` и outboxkit без снятия колонки → `down 2 must restore …`; лесенка
    зелёная у auditkit, bruteforcekit, outboxkit (оба источника), hubkit/projection; `M/hgd/s4/`
- [x] **AC6** — `make envdoc-check` сверяет дефолты тегов с таблицами доков, требует строку таблицы
  на каждый тег, обходит все каталоги с Go-кодом (включая `internal/` и каталоги без README); поле
  без тега в структуре с тегами — ошибка. Проверка:
  `M/kits-audit/api-docs/scripts/envdoc-inject.sh <worktree>` → 7 из 7 ожидаемо красных красные;
  `gate_inject.py --only G32` → пойман
  - Подтверждено: на кандидате S5 `envdoc-inject.sh` — E1–E6, E8 красные, E0, E7 зелёные; G32
    пойман; GH04 (дефолт `HTTP_DOCS_ENABLED` перевёрнут в коде) — красный; 239 переменных
    сходятся с доками (`M/hgd/s5/`)
- [ ] **AC7** — При схеме безопасности в графе — ошибка старта с именем операции или маршрута:
  операция без `Security` и без `httpkit.Public` (в том числе скрытая и в группе huma), дубль
  METHOD+path, сырой маршрут chi или `Adapter().Handle` мимо реестра без объявления публичным,
  схема в `Security` вне `components`, дубль имени схемы, nil-схема, дубль имени пробы. Проверка:
  `M/kits-audit/httpkit-a/run.sh <rev> security` → H15 PASS;
  `M/kits-audit/httpkit-b/run.sh <rev> exp` → HX05, HX10, HX11 PASS
- [ ] **AC8** — `handlertest.NewAPI` держит контракт сервера: `HumaAuth` до маршрутов (без
  extractor'а — `500 AUTH_NOT_CONFIGURED`, с `WithAuth` — 401 анониму), та же проверка, что в AC7;
  конфиг и логгер — как у сервера. Проверка: `httpkit-b/run.sh <rev> exp` → в HT01 T14, T22, T23,
  T25 совпадают с `Module`
- [ ] **AC9** — `httpkit.ClientIPFrom(ctx)` отдаёт IP, который стандартная цепочка кладёт до OTel;
  одна настройка доверия прокси служит лимитеру, ctx и `client.address` спана; клиент не выбирает
  адрес заголовками; IPv6 ограничен по /64 той же функцией `internal/`, что у bruteforcekit; память
  лимитера ограничена; имя server-спана — маршрут и при middleware потребителя с `r.WithContext`.
  Проверка: `httpkit-a/run.sh <rev> security` → H11, H12 PASS; `httpkit-b/run.sh <rev> exp` →
  HO02, HO04 PASS
- [ ] **AC10** — По умолчанию `/docs`, `/openapi.*` и `/schemas/*` не отдаются; в ответах нет
  `$schema` и `Link` с хостом из заголовков клиента. Проверка:
  `httpkit-a/run.sh <rev> contract security` → H08, H14 PASS
- [ ] **AC11** — `OUTBOX_MAX_ATTEMPTS` по умолчанию конечен (бюджет ретраев — в доке); переход в dead
  — ERROR `outboxkit: event dead` с instance, id, subject, attempts; выключенный JetStream — ошибка
  старта outbox; сбой брокера на весь батч пишет `publish_error` и попыток не сжигает; payload
  сверх предела отвергается в `Bus.Publish`. Проверка:
  `M/kits-audit/correctness-security/run.sh <rev> outbox-nats` → E17 PASS;
  `M/kits-audit/fx-observability/run.sh <rev> obs` → KO17 PASS; тесты outboxkit с Docker → PASS

## Шаги
### Гейт
- [x] **S1** — `make check` с integration и обязательным Docker: testkit без Docker вне `-short` —
  FAIL, timing-тест logkit — вне гейта, `gate-clean` наследует (KGATE-01, KTST-01)
  - Проверка: AC1 → выполнен · Закрывает: AC1 · Зависит от: —
  - Сделано: `RequireDocker` — `t.Fatalf` без Docker (регрессия RED на старом коде); `check` —
    шаги `GATE_STEPS` с `test-full`; `make test-timing`; тексты обвязки и китов. AC1 → выполнен
- [x] **S2** — Гейт не зависит от окружения и чужого checkout'а: guard переменных и флагов, охват в
  итоговой строке, кеш golangci-lint в checkout'е, `gate-clean` в чистом окружении с холодным кешем
  (KGATE-02, KGATE-04)
  - Проверка: AC2, AC3 → выполнены · Закрывает: AC2, AC3 · Зависит от: S1
  - Сделано: guard `gate_bad`, шаги через `GATE` с `PKGS=./... GO=go A=`, кеш lint в `.cache/`,
    `gate-clean` без make-окружения вызывающего; тексты обвязки. AC2, AC3 → выполнены
- [x] **S3** — Рабочие хуки: `scripts/git-hooks/pre-push` по образцу шаблона (чистое дерево, пушимый
  коммит = HEAD, окружение гейта вычищено), `make hooks` туда, `make check` требует активный
  pre-push, `.claude/settings.json` с deny и guard обходов, тексты обвязки (KGATE-03, KGATE-05,
  KGATE-11)
  - Проверка: AC4 → выполнен · Закрывает: AC4 · Зависит от: S2
  - Сделано: `scripts/git-hooks/pre-push` (чистое дерево, refs = HEAD), `make hooks`, шаг
    `hooks-check`; `.claude/settings.json` с `guard-bash.sh`; тексты обвязки. AC4 → выполнен
- [x] **S4** — Хелпер лесенки миграций в testkit и её тест на каждый kit-owned источник (KGATE-06)
  - Проверка: AC5 → выполнен · Закрывает: AC5 · Зависит от: S1
  - Сделано: `testkit.MigrationLadder` с тестами отказов, `TestMigrations_Ladder` у каждого
    kit-owned источника; откат 0002 `TableMigrations` — no-op; тексты. AC5 → выполнен
- [x] **S5** — Строгий `envdoc-check`: дефолты, строки таблиц, все каталоги с Go-кодом, поля без
  тегов (KDOC-04, KGATE-12, дефолты из KGATE-13)
  - Проверка: AC6 → выполнен · Закрывает: AC6 · Зависит от: S1
  - Сделано: `scripts/envdoc` (Go, `go/ast`) вместо `envdoc-check.sh`, инъекции — тест
    `TestCheck`; ячейки дефолтов приведены к канону; natskit `Config.Instance` —
    `ignored:"true"`; тексты. AC6 → выполнен

### httpkit и outbox: B1–B3
- [ ] **S6** — B1: реестр всех операций huma, пометка `httpkit.Public`, проверки старта из AC7 — с
  сырыми маршрутами chi (бэклог `httpkit-auth-bypass`) и значениями групп (KFX-09 с KFX-07)
  - Проверка: AC7 → выполнен; `go test -race ./httpkit/ ./oidckit/ ./hubkit/...` → PASS ·
    Закрывает: AC7 · Зависит от: S1
- [ ] **S7** — `handlertest.NewAPI` по контракту сервера: `HumaAuth`, `WithAuth`, проверка B1, опции
  конфига и логгера, тест различий с `Module` (KTST-08, KTST-09, паритет из KGATE-13)
  - Проверка: AC8 → выполнен · Закрывает: AC8 · Зависит от: S6
- [ ] **S8** — B2: IP клиента в ctx и одна настройка доверия прокси, канонический ключ клиента,
  предел памяти лимитера, `client.address` и имя server-спана (KSEC-09, KSEC-10, KOBS-11, KOBS-13)
  - Проверка: AC9 → выполнен · Закрывает: AC9 · Зависит от: S6
- [ ] **S9** — B3 в httpkit: docs выключены по умолчанию, `$schema` и `Link` сняты, у `handlertest` —
  тот же дефолт (KSEC-11)
  - Проверка: AC10 → выполнен · Закрывает: AC10 · Зависит от: S7
- [ ] **S10** — B3 в outbox: конечный `OUTBOX_MAX_ATTEMPTS`, dead-переход в лог (KOBS-05), выключенный
  JetStream и сбой батча (KAPI-03), subject'ы инстанса без покрывающего стрима — ошибка старта, а
  не строки без PubAck (находка шаблона, его `TestBoot`), предел payload (E17), бюджет ретраев в
  доке (KDOC-09, outbox)
  - Проверка: AC11 → выполнен · Закрывает: AC11 · Зависит от: S1

### Итог
- [ ] **S11** — Итог плана: эксперименты аудита по закрытым находкам на HEAD — PASS (таблица — в «Где
  мы сейчас»), `make gate-clean` зелёный, Handoff перенесён в `harden-authn-release`, план удалён
  - Проверка: `make gate-clean` → зелёный, охват `./...` · Закрывает: — · Зависит от: S3, S4, S5, S8,
    S9, S10

## Решения
- **Состав A.** Кроме high (KGATE-01…-04, KTST-01) и плановой лесенки (KGATE-06) в A взяты KGATE-05
  (тот же хук), KGATE-11 (тексты того же гейта) и KDOC-04 с KGATE-12 (аудит рекомендовал их в A:
  партия 2 меняет дефолты). KGATE-07…-10 и сигналы KGATE-13 — партия 3.
- **Docker.** `RequireDocker` пропускает только под `-short`; недоступный Docker — `t.Fatal`, без
  переключателя: skip делал зелёными и `test-full`, и `test-int` шаблона (G26). `make test` —
  быстрый `-short`, `make check` и `test-full` — полный прогон. Timing-тест
  `TestRedact_CostOfRecordWithoutSecrets` — отдельной целью: под нагрузкой соседних агентов флакает.
- **Гейт и окружение** — как у шаблона: `gate_bad`, под-make с фиксированными `PKGS=./... GO=go A=`,
  пустые `GOFLAGS`/`MAKEFLAGS`. Кеш линтера — `$(CURDIR)/.cache/golangci-lint` (в `.gitignore`).
- **Хуки** — в `scripts/git-hooks/`: так устроен шаблон, и рабочий клон уже указывает туда.
  Неактивный pre-push — FAIL `make check`: пуш без гейта никто не видел (G31).
- **Лесенка** — хелпер testkit (имя — на выбор, например `testkit/migratetest.Ladder(t, sources...)`):
  Postgres из testkit, up → снимок схемы из `pg_catalog` → down до нуля (остаются таблицы версий) →
  up → снимок совпал. Принимает `pgkit.Source`, поэтому годится шаблону.
- **`envdoc-check`** — скрипт или Go-тест в гейте, на выбор, без новых зависимостей; инъекции E1–E8
  и G32 — его тесты.
- **B1, реестр.** Реестр — `huma.OperationDocumenter` на API кита: huma зовёт `DocumentOperation`
  для каждой операции, в том числе `Hidden` и из групп (`huma.go:813-817`, `group.go:101-111`), а
  `OnAddOperation` скрытых не видит (`huma.go:816`). Проверка — одна функция для старта `Module` и
  `handlertest.NewAPI`.
- **B1, публичность.** Пометка — `httpkit.Public(op)` в `op.Metadata`; пустой `Security` публичным
  не считается — один способ. Правило действует, когда в графе есть схема: без схем все операции
  публичны (как у amoCRM-шлюза). Сырые маршруты — `chi.Walk` против реестра: маршрут вне реестра и
  вне маршрутов кита — ошибка старта, если не объявлен публичным (`httpkit.PublicRoutes` или форма на
  выбор исполнителя). Дубль METHOD+path — с нормализацией параметров (`{id}` и `{userID}` для chi —
  один маршрут).
- **B2, доверие прокси.** Одна настройка `HTTP_TRUST_PROXY_HOPS` вместо булевой
  `HTTP_RATE_LIMIT_TRUST_PROXY`: `0` — адрес соединения, `N` — N-я справа запись `X-Forwarded-For`,
  `X-Real-IP` не читается. Булева не говорит, какой заголовок ставит прокси и сколько ступеней
  (H12: XFF клиента побеждал `X-Real-IP` прокси; за CDN → nginx все клиенты делили ключ edge).
- **B2, IP.** IP — в ctx рядом с RequestID, до OTel, в канонической форме (`netip`, IPv4-mapped →
  IPv4). Ключ лимитера и bruteforcekit — одна свёртка IPv6 до /64 в `internal/` (H11: 100 из 100
  при `Burst=2`). При `HOPS=0` и пришедшем XFF — один WARN на процесс: за Traefik без настройки все
  клиенты делят bucket и ключ антибрутфорса (H12 INFO). Имя server-спана ставит `Metrics` по
  маршруту chi — от `r.Pattern` объекта otelhttp не зависит (HO02).
- **B3, `$schema`.** `$schema` и `Link` не ставятся вовсе (link-трансформер huma снят): поле не нужно
  контракту ошибок, отражает `X-Forwarded-Host` клиента, при выключенных docs ведёт в 404 (H08);
  `openapi.json` потребителей меняется — Handoff.
- **B3, outbox.** Попытка — неудача публикации строки; сбой брокера на весь батч (соединение,
  JetStream выключен в рантайме) пишет `publish_error` и попыток не сжигает — иначе простой брокера
  дольше бюджета массово уводит очередь в dead. Выключенный JetStream — ошибка старта, как у
  `SubscribeOnStart`. Предел payload по умолчанию — 1 MiB (`max_payload` NATS), превышение — ошибка
  `Bus.Publish` в транзакции домена (E17: 2 MiB держали ordered-очередь). Бюджет ретраев по
  умолчанию — не меньше часа, пересчёт `configuration.md` (KDOC-09).
- **S1** — `check` — рецепт по списку `GATE_STEPS`, шаг — свой `make`: первый красный
  останавливает прогон, `-j` не гонит integration рядом с lint. Timing-тест — opt-in
  `GOLIBINFRA_TIMING` в самом тесте и `make test-timing`: vet и lint видят тест, Makefile не связан
  с его именем. `minioFixture` без контейнера вне `-short` — `t.Fatal`, не skip (правка рядом).
- **S2, guard** — у `check` и `gate-clean`: `-i -n -q -t`, `MAKE`, `SHELL`, `.SHELLFLAGS`,
  `MAKEFILES` из env или командной строки и непустой `go env GOFLAGS` — отказ до первого шага;
  `PKGS`, `GO`, `A` и свои `GATE*` не отказывают, а не доходят до шагов (`override` и аргументы
  под-make). `-k` пропускается: первый красный шаг всё равно останавливает `check` с кодом 2.
- **S2, кеш lint** — `override`, а не `?=`: значение из окружения снова сделало бы кеш общим.
  `GOLANGCI_DIFF_PROCESSOR_PATCH` (оставляет в отчёте только замечания патча) и
  `GOLANGCI_LINT_CACHEPROG` (чужой кеш) в шаги не экспортируются.
- **S2, `gate-clean`** — `env -u MAKEFLAGS -u MFLAGS -u GNUMAKEFLAGS -u MAKEFILES`, как pre-push
  шаблона, а не `env -i`: прогону нужны `DOCKER_HOST`, прокси и `GOPATH`, а сужающее гейт отсекает
  guard или под-make.
- **`gate_inject.py`** — G11 и G20 ломали удалённый `harden-kits.md`; `s_plan` берёт любой активный
  план, смысл инъекции тот же. G21 запускает хук, который запустил бы git
  (`$(git rev-parse --git-path hooks)/pre-push`). Правки — в материалах аудита.
- **S3** — без `SKIP_PREPUSH`: обход один, `git push --no-verify` руками человека (шаблон убирает
  приглашение к второму — `repair-harness` S1); guard — копия шаблона без этой ветки.
  `hooks-check` — первый шаг: git берёт хуки из `scripts/git-hooks` этого checkout'а (путь в общем
  конфиге относительный — у worktree свой хук), `pre-push` исполняемый. `settings.json` — deny
  `--no-verify` и запрещённых программой разрушительных команд, `*.pem`/`*.key`; allow-листа нет.
  pre-commit не заводится: строка `/plan` про него исправлена (KGATE-11).
- **S4, лесенка по версиям** — `testkit.MigrationLadder(t, opts...)` в корне testkit (Docker, как
  `Start*`; сабпакеты — без Docker): каждая версия up → down → up со снимком `pg_catalog`, затем
  down до нуля и up. «Всё вниз — всё вверх» не видит down, не вернувший схему своего up, если
  следующий down сносит таблицу (мутация bruteforcekit 0002), а шаблон держит лесенку по версиям
  (`cmd/migrate/migrate_test.go`) — хелпер не слабее. Источники и образ — опциями `Start*`;
  расширения не сравниваются, как у шаблона.
- **S5, `envdoc`** — Go-команда `scripts/envdoc` (`go run` в `make envdoc-check`, `go/ast`, без
  зависимостей): вложенные конфиги и поля без тегов grep'ом не разобрать. Переменная —
  envconfig-тег или литерал `os.Getenv`/`LookupEnv` (кроме стандартных из `allow.txt`), её строка
  таблицы — в доках каталога пакета или выше. Ячейка дефолта — значение в бэктиках, без
  `default`-тега — `—` с пояснением или нулевое значение (`""`, `false`, `0`); «пусто», `nil`,
  «нет, обязательна» приведены к канону. Инъекции E0–E8, G32, G01 — тест `TestCheck` на копии
  репо в памяти. natskit `Config.Instance` — `ignored:"true"` (дока: «из ENV не читается»).
- **S4, `TableMigrations`** — откат 0002 стал no-op: версия 1 — уже вся `SchemaSQL` с
  `next_attempt_at`, снятие колонки не возвращало схему версии 1 (`down 2 must restore`); у
  зеркала без колонки откат её оставляет. hubkit/projection — тоже kit-owned источник, лесенка и
  ему (правки рядом).
- **N1** — pre-push гоняет `scripts/gate-clean.sh <коммит>` на каждом пушимом коммите: gitignored-файл
  рабочего дерева не прячет поломку, «чистое дерево» и «= HEAD» не нужны, удаление ref'а гейт не
  гоняет (`M/hgd2/n1_probe.sh`); G21 `gate_inject.py` подаёт хуку stdin `git push`.
- **N2** — `--eval` — строка `gate_bad` (`--eval=` видно в `MAKEFLAGS` до чтения Makefile); второй
  makefile ловит пререквизит `gate-makefiles` у `check` и `gate-clean`: рецепт `-f extra.mk` подменяет,
  а пререквизиты правил складываются, `$(error)` не глушат `-k`/`SHELL` (`M/hgd2/n2_probe.sh`).

## Handoff
При закрытии плана — в Handoff `harden-authn-release`. Строки аннотации v1.3.0:
- testkit: без Docker вне `-short` — `t.Fatal`, не skip → без Docker integration гоняется только с
  `-short`; хелпер лесенки миграций — для своих источников.
- outboxkit: откат 0002 `TableMigrations` — no-op (версия 1 уже с `next_attempt_at`).
- httpkit: при схеме в графе публичная операция — `httpkit.Public(op)`, пустой `Security` не
  публичен; сырой маршрут chi объявляется публичным; дубль METHOD+path, схема вне `components`,
  дубли имён схем и проб — ошибка старта.
- testkit/handlertest: `NewAPI` закрыт по умолчанию → защищённые операции через `WithAuth`, конфиг и
  логгер — опциями.
- httpkit: `HTTP_RATE_LIMIT_TRUST_PROXY` удалён → `HTTP_TRUST_PROXY_HOPS` (за одним прокси — 1); IP
  клиента — `httpkit.ClientIPFrom(ctx)`, свой middleware не нужен.
- httpkit: `HTTP_DOCS_ENABLED` по умолчанию `false`; `$schema` и `Link` в ответах нет →
  `openapi.json` перегенерировать.
- outboxkit: `OUTBOX_MAX_ATTEMPTS` конечен (после бюджета — dead, вернуть — `ReviveDead`);
  выключенный JetStream — ошибка старта; payload сверх предела — ошибка `Publish`.

## Где мы сейчас
Этап «Гейт» (S1–S5) закрыт: `make check` гоняет integration (Docker обязателен, ≈1,5 мин), всегда
на `./...`, окружение его не сужает, кеш lint — в `.cache/` checkout'а; push проходит pre-push
(`make hooks`); лесенка держит kit-owned миграции; `envdoc-check` сверяет имена и дефолты. Дальше —
валидатор этапа, затем S6 (B1: реестр операций и `httpkit.Public`). Эксперименты этапа — `M/hgd/`,
правки `gate_inject.py` — «Решения».

Заготовка аннотации v1.3.0 по сделанному:
- Breaking — testkit: `RequireDocker` и `Start*` без Docker вне `-short` обрывают тест
  (`t.Fatalf`), а не пропускают → без Docker гонять только `go test -short`; integration-гейт
  потребителя без Docker красный.
- Поведение — outboxkit: откат 0002 `TableMigrations` — no-op (версия 1 уже с `next_attempt_at`).
- Изменения — testkit: `MigrationLadder(t, WithMigrations(...))` — лесенка up → down → up по
  версиям, down до нуля и up со сверкой схемы для своих источников миграций.
- Гейт: `make gate-clean` — unit и integration (Docker), лесенка kit-owned миграций.
