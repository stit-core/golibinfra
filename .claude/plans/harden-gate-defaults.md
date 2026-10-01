---
status: in-progress
parent: go-backend-template:audit-program
blocked-by: —
---

# harden-gate-defaults — партия 2, часть 1: гейт, публичные операции, IP клиента, безопасные дефолты

## Цель
Первая половина партии 2 программы по аудиту (тег v1.3.0): честный гейт golibinfra (integration на
Docker, окружение его не сужает, свой кеш lint, рабочий pre-push, лесенка kit-owned миграций), затем
B1–B3 шаблона с уточнениями аудита: явные публичные операции, IP клиента в ctx с одной настройкой
доверия прокси, безопасные дефолты docs и outbox. Вторая половина и релиз — `harden-authn-release`.

## Контекст
- **Программа.** Метод («исполнитель этапа → валидатор»), политика push и решения владельца —
  `go-backend-template/.claude/plans/audit-program.md`; состав партий — `M/replan-golibinfra.md` §6.
- **Находки** — базовый аудит `M/kits-audit/` (`M` — `/home/mm/.cache/stit-audit-program/`): сводка
  `summary.md` и отчёты углов; эксперименты — `run.sh` углов (тест утверждает желаемое, FAIL —
  дефект есть), тяжёлые прогоны — последовательно.
- **Гейт** (S1–S5): `Makefile`, `scripts/gate-clean.sh`, pre-push, deny и guard `.claude/`, `scripts/envdoc`.
- **Потребитель** — шаблон, `fix-reference-code` S9 (bump v1.3.0): снимает `publicOperations`,
  client-IP middleware и `identity.WithIP`. Полный Handoff шаблону — в `harden-authn-release`.
- **Правила шага:** доки кита — тем же коммитом (`make envdoc-check`); breaking — `!`, `BREAKING CHANGE:`.

## Не входит
- Партия 3 (v1.4.0) — `backlog.md`, триггер «партия 3»; B4–B8, high из D, кандидаты C и релиз
  v1.3.0 — `harden-authn-release`; правки шаблона и шлюзов — по аннотации тега.

## Критерии приёмки
Проверки — в своём worktree на HEAD; `gate_inject.py` — это
`M/kits-audit/tests-gate/gate_inject.py --repo <worktree> --base <rev> --only …`.
- [x] **AC1** — `make check` гоняет unit и integration всех пакетов (testcontainers, `-race`); без
  Docker вне `-short` integration-тест падает, а не пропускается; баг, видимый только integration,
  и битая up-миграция кита краснят `make check` и `make gate-clean`; timing-тест logkit в гейт не
  входит. Проверка: `gate_inject.py --only G25,G26,G30` → все пойманы; `make check` → зелёный,
  итоговая строка называет охват `./...`
  - Подтверждено: на кандидате S1 G25, G26, G30 пойманы; `✓ check clean (./...)`; timing logkit — SKIP
- [x] **AC2** — `PKGS`, `GO`, `GOFLAGS` (и `go env -w`), `A`, флаги `-i`/`-n`/`-k`, `MAKEFLAGS=i` не
  сужают и не глушат гейт: `make check` отказывается запускаться или проверяет всё; `gate-clean` и
  pre-push окружение вызывающего не наследуют. Проверка:
  `gate_inject.py --only G14,G15,G16,G17,G18,G19,G20,G24` → все пойманы
  - Подтверждено: на кандидате S2 все восемь пойманы (G17–G19 — отказ guard'а); `-n -q -t`, `MAKE=`,
    `SHELL=`, `.SHELLFLAGS=`, `MAKEFILES`, `GOFLAGS` в `GOENV` → отказ; `make -k check` → rc=2
- [x] **AC3** — Вердикт lint не зависит от чужого checkout'а: кеш golangci-lint — внутри checkout'а,
  у `gate-clean` — свой холодный. Проверка: сценарий L3 `lint_cache_probe.sh` через `make lint` в
  двух worktree → во втором настоящее нарушение красное
  - Подтверждено: `M/hgd/lint_cache_make_probe.sh <rev>` (L3 через `make lint`) → в W2 красное
- [x] **AC4** — Push проходит гейт: pre-push гоняет `make check` на пушимом коммите при чистом дереве;
  неактивный pre-push роняет `make check` с подсказкой `make hooks`; обходы хуков из сессии Claude
  заблокированы; CLAUDE.md, README, CONTRIBUTING и скиллы `/plan`, `/release` описывают этот гейт.
  Проверка: `gate_inject.py --only G21,G31` → оба пойманы; `make check` при неактивном pre-push → FAIL
  - Подтверждено: на кандидате S3 G21, G31 пойманы; `MAKEFLAGS=i git push` с падающим тестом —
    отказ; `make check` без `make hooks` → FAIL; `M/hgd/guard_cases.py` — 0 расхождений из 84
- [x] **AC5** — Каждый kit-owned источник миграций (auditkit, bruteforcekit, outboxkit с `TableMigrations`)
  проходит в `make check` лесенку up → down до нуля → up со сверкой схемы хелпером testkit, которым потом
  пользуется шаблон. Проверка: `gate_inject.py --only G28` и тот же битый down под `make check` → пойманы
  - Подтверждено: на кандидате S4 G28 и тот же битый down под `make check` пойманы; down 0002
    bruteforcekit без `locked_idx` и outboxkit без снятия колонки → `down 2 must restore …`; `M/hgd/s4/`
- [x] **AC6** — `make envdoc-check` сверяет дефолты тегов с таблицами доков, требует строку таблицы
  на каждый тег, обходит все каталоги с Go-кодом (включая `internal/` и каталоги без README); поле
  без тега в структуре с тегами — ошибка. Проверка: `M/kits-audit/api-docs/scripts/envdoc-inject.sh
  <worktree>` → 7 из 7 ожидаемо красных красные; `gate_inject.py --only G32` → пойман
  - Подтверждено: на кандидате S5 `envdoc-inject.sh` — E1–E6, E8 красные, E0, E7 зелёные; G32 и
    GH04 (дефолт `HTTP_DOCS_ENABLED` перевёрнут в коде) пойманы (`M/hgd/s5/`)
- [x] **AC7** — При схеме безопасности в графе — ошибка старта с именем операции или маршрута:
  операция без `Security` и без `httpkit.Public` (в том числе скрытая и в группе huma), дубль
  METHOD+path (и всегда — маршрут мимо реестра на пути операции, в любом порядке), сырой маршрут
  chi или `Adapter().Handle` мимо реестра без объявления публичным, схема в `Security` вне
  `components`, дубль имени схемы, nil-схема, дубль имени пробы. Проверка:
  `M/kits-audit/httpkit-a/run.sh <rev> security` → H15 PASS; `M/kits-audit/httpkit-b/run.sh <rev>
  exp` → HX05, HX10, HX11 PASS; зонд B1 валидатора B → A1–A3, A1b — ошибка старта
  - Подтверждено: на кандидате S6 H15 — подтесты B1 PASS (ошибка называет операции; подтест
    KSEC-08 — партия 3), HX05, HX10, HX11, HX11b PASS, прочие исходы `exp` — как на v1.2.0;
    `M/hgd2/s6_mutants.py` — 11 из 11 мутантов проверки убиты; фикс B1: `M/hgd2/logs/fix-b1-probe.log`
    — A1–A3, A1b — ошибка старта, `M/hgd2/fixb_mutants.py B1` — 6 из 6
- [x] **AC8** — `handlertest.NewAPI` держит контракт сервера: `HumaAuth` до маршрутов (без
  extractor'а — `500 AUTH_NOT_CONFIGURED`, с `WithAuth` — 401 анониму), та же проверка, что в AC7;
  конфиг и логгер — как у сервера. Проверка: `httpkit-b/run.sh <rev> exp` → в HT01 T14, T22, T23,
  T25 совпадают с `Module`
  - Подтверждено: на кандидате S7 HT01 PASS — T14 401/401, T22 413/413, T23 и T25 закрыты с обеих
    сторон (500 у `NewAPI` без `WithAuth`, 401 и ошибка старта у `Module`), T24 — `handler error` с
    `WithLogger`; `M/hgd2/s7_mutants.py` — 7 из 7 мутантов `NewAPI` убиты
- [x] **AC9** — `httpkit.ClientIPFrom(ctx)` отдаёт IP, который стандартная цепочка кладёт до OTel;
  одна настройка доверия прокси служит лимитеру, ctx и `client.address` спана; клиент не выбирает
  адрес заголовками; IPv6 ограничен по /64 той же функцией `internal/`, что у bruteforcekit; память
  лимитера ограничена; имя server-спана — маршрут и при middleware потребителя с `r.WithContext`.
  Проверка: `httpkit-a/run.sh <rev> security` → H11, H12 PASS; `httpkit-b/run.sh <rev> exp` →
  HO02, HO04 PASS
  - Подтверждено: на кандидате S8 H11, H12 PASS; H11b — +15,7 MiB на 200 000 адресов; HO02 во всех 4
    вариантах и HO04 PASS, сигналы — 0 расхождений; `M/hgd2/s8_mutants.py` — 10 из 10 мутантов убиты
- [x] **AC10** — По умолчанию `/docs`, `/openapi.*` и `/schemas/*` не отдаются; в ответах нет `$schema`
  и `Link` с хостом клиента. Проверка: `httpkit-a/run.sh <rev> contract security` → H08, H14 PASS
  - Подтверждено: на кандидате S9 H08 (docs вкл. и выкл.) и H14 PASS; прочие исходы групп contract,
    security, api, lifecycle — как на v1.2.0, кроме закрытых B1–B3; мутанты S9 — 2 из 2 убиты
- [x] **AC11** — `OUTBOX_MAX_ATTEMPTS` по умолчанию конечен (бюджет ретраев — в доке); переход в dead
  — ERROR `outboxkit: event dead` с instance, id, subject, attempts; выключенный JetStream (и на
  сервере) — ошибка старта outbox; сбой брокера на весь батч пишет `publish_error` и попыток не
  сжигает; payload сверх предела отвергается в `Bus.Publish`. Проверка:
  `M/kits-audit/correctness-security/run.sh <rev> outbox-nats` → E17 PASS;
  `M/kits-audit/fx-observability/run.sh <rev> obs` → KO17 PASS; тесты outboxkit с Docker → PASS
  - Подтверждено: на кандидате S10 E17 и KO17 PASS (прочие исходы `obs` и `compare.py` — как на
    d62b2e6); тесты outboxkit и natskit с Docker — PASS; `M/hgd2/s10_mutants.py` — 28 из 28 убиты;
    фикс B2: сервер без JetStream — старт красный, тики без попыток (`fix-b2-probe.log`), мутанты 4/4

## Шаги
### Гейт
- [x] **S1** — `make check` с integration и обязательным Docker: testkit без Docker вне `-short` —
  FAIL, timing-тест logkit — вне гейта, `gate-clean` наследует (KGATE-01, KTST-01)
  - Проверка: AC1 → выполнен · Закрывает: AC1 · Зависит от: —
  - Сделано: `RequireDocker` без Docker — `t.Fatalf` (RED на старом коде), `GATE_STEPS` с `test-full`
- [x] **S2** — Гейт не зависит от окружения и чужого checkout'а: guard переменных и флагов, охват в
  итоговой строке, кеш lint в checkout'е, `gate-clean` в чистом окружении с холодным кешем (KGATE-02, -04)
  - Проверка: AC2, AC3 → выполнены · Закрывает: AC2, AC3 · Зависит от: S1
  - Сделано: guard `gate_bad`, шаги через `GATE`, кеш lint в `.cache/`, `gate-clean` без make-env
- [x] **S3** — Рабочие хуки: `scripts/git-hooks/pre-push` по образцу шаблона (чистое дерево, пушимый
  коммит = HEAD, окружение гейта вычищено), `make hooks` туда, `make check` требует активный
  pre-push, `.claude/settings.json` с deny и guard обходов, тексты обвязки (KGATE-03, -05, -11)
  - Проверка: AC4 → выполнен · Закрывает: AC4 · Зависит от: S2
  - Сделано: `scripts/git-hooks/pre-push`, `make hooks`, `hooks-check`, deny и `guard-bash.sh`
- [x] **S4** — Хелпер лесенки миграций в testkit и её тест на каждый kit-owned источник (KGATE-06)
  - Проверка: AC5 → выполнен · Закрывает: AC5 · Зависит от: S1
  - Сделано: `testkit.MigrationLadder`, `TestMigrations_Ladder` у каждого kit-owned источника
- [x] **S5** — Строгий `envdoc-check`: дефолты, строки таблиц, все каталоги с Go-кодом, поля без
  тегов (KDOC-04, KGATE-12, дефолты из KGATE-13)
  - Проверка: AC6 → выполнен · Закрывает: AC6 · Зависит от: S1
  - Сделано: `scripts/envdoc` вместо `envdoc-check.sh`, инъекции — `TestCheck`, дефолты в доках

### httpkit и outbox: B1–B3
- [x] **S6** — B1: реестр всех операций huma, пометка `httpkit.Public`, проверки старта из AC7 — с
  сырыми маршрутами chi (бэклог `httpkit-auth-bypass`) и значениями групп (KFX-09 с KFX-07)
  - Проверка: AC7 → выполнен; `go test -race ./httpkit/ ./oidckit/ ./hubkit/...` → PASS ·
    Закрывает: AC7 · Зависит от: S1
  - Сделано: реестр операций, `Public`, `PublicRoutes`, `Server.CheckRoutes` (`ErrRoutes`); схемы и
    пробы — `ErrConfig` в графе; `Public` у `/health`, `/ready`, роутов oidckit и JWKS hubkit; доки
- [x] **S7** — `handlertest.NewAPI` по контракту сервера: `HumaAuth`, `WithAuth`, проверка B1, опции
  конфига и логгера, тест различий с `Module` (KTST-08, KTST-09, паритет из KGATE-13)
  - Проверка: AC8 → выполнен · Закрывает: AC8 · Зависит от: S6
  - Сделано: `NewAPI(t, register, opts...)` — `HumaAuth` до маршрутов, `CheckRoutes`,
    `httpkit.NewConfig()`; `WithAuth`, `WithConfig`, `WithLogger`, `WithSecuritySchemes`; доки
- [x] **S8** — B2: IP клиента в ctx и одна настройка доверия прокси, канонический ключ клиента,
  предел памяти лимитера, `client.address` и имя server-спана (KSEC-09, KSEC-10, KOBS-11, KOBS-13)
  - Проверка: AC9 → выполнен · Закрывает: AC9 · Зависит от: S6
  - Сделано: `ClientIP(log, hops)` до OTel, `ClientIPFrom`, `HTTP_TRUST_PROXY_HOPS`; `internal/clientip`
    у лимитера и bruteforcekit; предел bucket'ов; имя спана и `client.address` — в `Metrics`; доки
- [x] **S9** — B3 в httpkit: docs выключены по умолчанию, `$schema` и `Link` сняты, у `handlertest` —
  тот же дефолт (KSEC-11)
  - Проверка: AC10 → выполнен · Закрывает: AC10 · Зависит от: S7
  - Сделано: `HTTP_DOCS_ENABLED=false`, `CreateHooks` huma сняты, handlertest — `httpkit.NewConfig()`; доки
- [x] **S10** — B3 в outbox: конечный `OUTBOX_MAX_ATTEMPTS`, dead-переход в лог (KOBS-05), выключенный
  JetStream и сбой батча (KAPI-03), subject'ы инстанса без покрывающего стрима — ошибка старта, а не
  строки без PubAck (находка шаблона, `TestBoot`), предел payload (E17), бюджет ретраев в доке (KDOC-09)
  - Проверка: AC11 → выполнен · Закрывает: AC11 · Зависит от: S1
  - Сделано: `OUTBOX_MAX_ATTEMPTS=25` (`-1` — без предела), `OUTBOX_MAX_PAYLOAD_BYTES`, лог dead,
    `ErrUnavailable` без траты попытки, `natskit.StreamChecker` на старте, `WithSubjects`; доки

### Итог
- [ ] **S11** — Итог плана: эксперименты аудита по закрытым находкам на HEAD — PASS (таблица — в «Где
  мы сейчас»), `make gate-clean` зелёный, Handoff перенесён в `harden-authn-release`, план удалён
  - Проверка: `make gate-clean` → зелёный (`./...`) · Закрывает: — · Зависит от: S3, S4, S5, S8, S9, S10

## Решения
- **Состав A.** Кроме high (KGATE-01…-04, KTST-01) и лесенки (KGATE-06) — KGATE-05 (тот же хук), KGATE-11
  (тексты), KDOC-04 с KGATE-12 (дефолты партии 2); KGATE-07…-10 и сигналы KGATE-13 — партия 3.
- **S1, Docker** — `RequireDocker` и `minioFixture` пропускают только под `-short`, без Docker —
  `t.Fatal` без переключателя (skip озеленял `test-full` и `test-int` шаблона, G26); `check` — рецепт
  по `GATE_STEPS`, шаг — свой `make`: первый красный останавливает, `-j` не гонит integration рядом
  с lint. Timing-тест logkit под нагрузкой флакает — opt-in `GOLIBINFRA_TIMING`, `make test-timing`.
- **S2, guard** (как у шаблона) — `-i -n -q -t`, `MAKE`, `SHELL`, `.SHELLFLAGS`, `MAKEFILES` из env или
  командной строки и непустой `go env GOFLAGS` — отказ до первого шага; `PKGS`, `GO`, `A`, `GATE*` до
  шагов не доходят (`override`, под-make); `-k` пропускается: красный шаг всё равно даёт код 2. Кеш
  lint — `override` `$(CURDIR)/.cache/golangci-lint`, `GOLANGCI_DIFF_PROCESSOR_PATCH`/`_CACHEPROG` в
  шаги не идут; `gate-clean` — `env -u` make-переменных, а не `env -i`: нужны `DOCKER_HOST`, прокси, `GOPATH`.
- **S3, хуки** — в `scripts/git-hooks/`, как у шаблона; обход один — `git push --no-verify` руками
  человека, без `SKIP_PREPUSH`. `hooks-check` — первый шаг: неактивный или неисполняемый pre-push —
  FAIL (G31), путь хуков относительный — у worktree свой. `settings.json` — deny обходов,
  разрушительных команд и `*.pem`/`*.key`, allow-листа нет; pre-commit не заводится (KGATE-11).
- **S4, лесенка** — `testkit.MigrationLadder(t, opts...)` (Docker, как `Start*`, принимает
  `pgkit.Source`): каждая версия up → down → up со снимком `pg_catalog`, затем down до нуля и up —
  «всё вниз — всё вверх» не видит down, не вернувший схему своего up (мутация bruteforcekit 0002);
  расширения не сравниваются; откат 0002 `TableMigrations` — в аннотации; hubkit/projection — kit-owned.
- **S5, `envdoc`** — Go-команда `scripts/envdoc` (`go/ast`): вложенные конфиги и поля без тегов grep'ом
  не разобрать. Переменная — envconfig-тег или литерал `os.Getenv`/`LookupEnv` (кроме `allow.txt`),
  строка таблицы — в доках каталога пакета или выше, дефолт — в бэктиках, без `default` — `—` с
  пояснением или нуль; инъекции E0–E8, G32, G01 — `TestCheck`; natskit `Config.Instance` — `ignored`.
- **`gate_inject.py`** — `s_plan` (G11, G20) ломает любой активный план; G21 запускает хук, который
  запустил бы git (`$(git rev-parse --git-path hooks)/pre-push`), со stdin `git push` (N1).
- **N1** — pre-push гоняет `scripts/gate-clean.sh <коммит>` на каждом пушимом коммите: gitignored-файл
  рабочего дерева не прячет поломку, «чистое дерево» и «= HEAD» не нужны, удаление ref'а — без гейта.
- **N2** — `--eval` — строка `gate_bad` (`--eval=` видно в `MAKEFLAGS` до чтения Makefile); второй
  makefile ловит пререквизит `gate-makefiles` у `check` и `gate-clean` (рецепт `-f extra.mk` подменяет,
  а пререквизиты складываются; `$(error)` не глушат `-k`/`SHELL`) — по полному пути (валидатор B:
  `Makefile` другого каталога); переопределение самого `gate-makefiles` — правка гейта, так и в доках.
- **N3** — deny `*--no-veri*` ловит флаг в кавычках и алиасе; guard по пути `git-hooks` или `pre-push`
  пускает только чтение — readers, git без правки дерева, `sed` без `-i`, `bash -n`, запуск хука — и без
  записи перенаправлением (валидатор B: `cp`, `ln`, `truncate`, `>`, `sed -i`); `M/hgd2/fixb_guard.py`.
- **Флак httpclient** (валидатор B) — `record()` до `close(Events())`; тест: `OnEnd` под `sync.Once`, Cleanup — новый провайдер.
- **B1, реестр.** API сервера — `huma.OperationDocumenter`: `DocumentOperation` видит каждую операцию,
  в том числе `Hidden` и из групп (`OnAddOperation` скрытых не видит). Проверка — экспортируемый
  `Server.CheckRoutes()` (`ErrRoutes`): `Module` — на OnStart, `handlertest` — S7, без fx —
  потребитель; extractor (`ErrAuthNotConfigured`) — по тому же реестру, со скрытыми.
- **B1, публичность.** `httpkit.Public(op)` — метка `op.Metadata` (форма `Declare`); пустой `Security`
  не публичен, `Public` с `Security` — ошибка. «`Security` или `Public`», схема из `components` и
  сырые маршруты — при схеме в `components`: без схем всё публично (amoCRM-шлюз). Дубль METHOD+path —
  всегда, `{id}` и `{userID}` — один маршрут. Роуты oidckit и JWKS hubkit — `Public`.
- **B1, сырые маршруты и группы.** `chi.Walk` против реестра, документация huma — разница роутера до
  и после `humachi.New`; объявление — путь в `httpkit.PublicRoutes` (как `Middlewares`; без метода:
  `Mount` — на все методы), лишняя строка — ошибка. Дубль имени схемы, схема без `Name`/`Scheme` —
  `ErrConfig` уже в `fx.New` (HX05 — `app.Err()`); дубль имени пробы — `ErrConfig` `registerHealth`.
- **B1, эксперименты.** H15 (подтесты B1), HX10, HX11 — под «ошибку старта с именем», HX11b — со схемой
  в графе; подтест H15 «AdminKey принимает Bearer» — KSEC-08 (партия 3, `httpkit-auth-edge`), красный.
- **B1, подмена хендлера** (валидатор B). Адаптер реестра ставит операции хендлер типа `opHandler` (по
  METHOD+path: группа документирует копию); маршрут на пути операции с чужим хендлером — ошибка всегда,
  раньше операции — при её `Handle`. Маршрут после проверки — доки: роутер наружу — `*chi.Mux`.
- **S7, handlertest.** Что сервер берёт из графа, `NewAPI` — опциями; без `WithAuth` защищённая операция
  — 500 `AUTH_NOT_CONFIGURED` (fail-closed), а не провал теста. `register` получает API сервера с
  реестром (у обёртки `humatest` нет `OperationDocumenter`); конфиг — `httpkit.NewConfig()`, как у
  сервера. HT01: A получает опциями то, что B — из графа, T24 и T25 сравниваются.
- **B2, доверие прокси.** `HTTP_TRUST_PROXY_HOPS` вместо `HTTP_RATE_LIMIT_TRUST_PROXY`: `0` — адрес
  соединения, `N` — N-я справа запись всех заголовков `X-Forwarded-For`; цепочка короче `N` или не IP —
  адрес соединения, `X-Real-IP` не читается (H12); при `0` и пришедшем XFF — одна строка Warn.
- **B2, IP.** `httpkit.ClientIP(log, hops)` — после `RequestID`, до `OTel`; ключ ctx и свёртка —
  `internal/clientip` (`Canonical`, `Key`): ею ключуют лимитер (/64) и bruteforcekit, auditkit (B4)
  читает тот же ключ; `ClientIPFrom(ctx)` — для bruteforcekit и аудита; `ClientIP(r, trustProxy)` удалена.
- **B2, лимитер и спан.** Предел — константа 100 000 bucket'ов (≈17 MiB), сверх — общий bucket до
  выметания: без env и LRU. Имя server-спана и `client.address` ставит `Metrics` в defer (HO02,
  HO04); форматтер otelhttp оставлен — при `r.Pattern` он зовётся после `Metrics` с тем же именем;
  строка Warn — в `expected-signals-httpkit.json` из доки.
- **B3, `$schema`.** `$schema` и `Link` не ставятся вовсе: `humaConfig.CreateHooks = nil` — единственный
  хук `DefaultConfig` и есть link-трансформер; поле не нужно контракту ошибок, отражает
  `X-Forwarded-Host` клиента, без docs ведёт в 404 (H08). `/schemas` остаётся частью docs. H01b
  (скрытая защищённая операция без extractor'а) правлен под S6: ошибка старта вместо 500.
- **B3, outbox.** Попытка — неудача строки (отказ брокера, «нет стрима»); сбой брокера целиком
  (`ErrUnavailable` транспорта: соединение, JetStream, таймаут `PubAck`, `ctx`; батч без результатов)
  — `publish_error` без `RecordFailures`. `OUTBOX_MAX_ATTEMPTS=25` (≈ 1 ч 24 мин), `0` — `ErrConfig`;
  `-1` — без предела: never-drop биллинга `ai-gateway`, как `NATS_MAX_RECONNECTS=-1`. Dead — ERROR
  после коммита, ключ `err`, как у natskit; логгер — аргумент `New`/`NewWorker`, у модулей — граф.
- **B3, старт и payload.** Выключенный JetStream — ошибка старта, как у `SubscribeOnStart`; на сервере
  (валидатор B) — `CheckStreams` спрашивает `AccountInfo`, транспорт на «нет ответа от стрима» — тоже;
  `WithSubjects` — старт сверяет фильтры со стримами (`natskit.StreamChecker` опционален: фейки без
  него), `Publish` вне них — ошибка; опция необязательна (шаблон — в `events.OutboxFor`), дефолт
  `<name>.>` — магия имени. Предел тела 512 KiB: пробелы JSONB — до 1,5× JSON (1 000 000 в 1 MiB не
  влезал); числа в экспоненте JSONB пишет целиком (`1e+21` ×4,4, валидатор B) — доки: строкой (мерить
  JSONB — запрос к Postgres из `Bus` или предел в pgstore). E17 — big в своей транзакции; zzcs, claimsB,
  контракт сигналов — под API (`orig/`).

## Handoff
При закрытии плана — в Handoff `harden-authn-release`: заготовка аннотации, несделанное и пункты ниже.
- Шаблону после bump (`fix-reference-code` S9): обход `TestDockerRequired` (`cmd/migrate/migrate_test.go`)
  лишний — testkit без Docker сам FAIL; комментарий `scripts/tools/test.sh:21-22` («testkit пропустит
  тесты без Docker») неверен; `TestAppMigrationsRoundtrip` → `testkit.MigrationLadder(t,
  testkit.WithMigrations(sources.Kits...), testkit.WithMigrationsDir(...))` — один способ.
- `events.OutboxFor` — `outboxkit.WithSubjects(name+".>")` (бэклог шаблона `publisher-stream-check`).

## Где мы сейчас
Этап «Гейт» (S1–S5) — PASS валидатора (`M/validation-harden-gate-defaults-A.md`), его N1–N3 и N5
закрыты (N4 — S11). S6–S10 сделаны; по валидатору B (`M/validation-harden-gate-defaults-B.md`) B1, B2,
N1–N6, N10 и тест SSE перепроверки закрыты, N7–N9, N11 — бэклог; дальше перепроверка, S11.

Заготовка аннотации v1.3.0 по сделанному:
- Breaking — httpkit: при схеме в графе операция без `Security` и без `httpkit.Public` (пустой
  `Security` не публичен; и скрытая), схема `Security` вне `components`, маршрут мимо `huma.Register`
  вне `PublicRoutes`, а всегда — дубль METHOD+path, `Public` с `Security`, скрытая операция с
  `Security` без extractor'а, маршрут мимо `huma.Register` на METHOD+path операции — ошибка старта;
  дубль имени схемы или пробы, схема без `Scheme` — ошибка графа → открытые операции —
  `httpkit.Public(op)`, сырые маршруты — `PublicRoutes`, на пути операции — убрать.
- Breaking — testkit/handlertest: `NewAPI` ставит `HumaAuth` до маршрутов и проверяет маршруты
  (`CheckRoutes`) → защищённая операция без `WithAuth` — 500 `AUTH_NOT_CONFIGURED`, свой `HumaAuth`
  в `register` не действует — `handlertest.WithAuth(extract)`; конфиг — `httpkit.NewConfig()` (свой
  — `WithConfig`); `register` получает API сервера, а не `humatest.TestAPI`.
- Breaking — httpkit: `HTTP_RATE_LIMIT_TRUST_PROXY` и `httpkit.ClientIP(r, trustProxy)` удалены →
  `HTTP_TRUST_PROXY_HOPS` (за одним прокси — 1, за CDN → nginx — 2), IP клиента —
  `httpkit.ClientIPFrom(ctx)`, свой middleware IP не нужен; `X-Real-IP` не читается.
- Поведение — httpkit: ключ лимитера — IPv6 по `/64`, bucket'ов не больше 100 000 (сверх — общий);
  имя server-спана — маршрут и за middleware с `r.WithContext`; `client.address` — IP из `ClientIP`;
  `X-Forwarded-For` при `HTTP_TRUST_PROXY_HOPS=0` — одна строка Warn.
- Breaking — httpkit: `HTTP_DOCS_ENABLED` по умолчанию `false` (`/docs`, `/openapi.*`, `/schemas` —
  только с `true`); `$schema` в телах и `Link` в ответах нет, схемы ответов в OpenAPI без `$schema`
  → `openapi.json` перегенерировать, `HTTP_DOCS_ENABLED=true` — только dev; handlertest — тот же дефолт.
- Изменения — httpkit: `Public`, `PublicRoutes`/`WithPublicRoutes`, `Server.CheckRoutes`, `ErrRoutes`,
  `ClientIP(log, hops)`, `ClientIPFrom`; bruteforcekit сворачивает IPv6 той же функцией, что лимитер;
  handlertest: `WithAuth`, `WithConfig`, `WithLogger` (`handler error` в тесте), `WithSecuritySchemes`.
- Breaking — testkit: `RequireDocker` и `Start*` без Docker вне `-short` — `t.Fatalf`, а не skip →
  integration-прогон потребителя без Docker красный; без Docker — только `go test -short`.
- Breaking — outboxkit: `New`, `NewWorker` принимают `logkit.Logger` (`Module`, `Instance` — из графа);
  `OUTBOX_MAX_ATTEMPTS` по умолчанию 25, `0` — ошибка конфига, литерал `MaxAttempts: 0` — 25; JetStream,
  выключенный в конфиге или на сервере, — ошибка старта; JSON больше `OUTBOX_MAX_PAYLOAD_BYTES`
  (512 KiB) — `ErrPayloadTooLarge` → `New(…, cfg, log)`, «без предела» (env `0` и литерал) — `-1`,
  `NATS_JETSTREAM_ENABLED=true` и JetStream на сервере, крупное тело — ссылкой.
- Поведение — outboxkit: сбой брокера целиком попыток не тратит (`publish_error`); dead — ERROR
  `outboxkit: event dead`; откат 0002 `TableMigrations` — no-op (версия 1 уже с `next_attempt_at`),
  у таблицы-зеркала без колонки откат её оставляет (nullable, старому коду не мешает).
- Изменения — outboxkit: `WithSubjects` (стримы — на старте), `ErrSubjectNotDeclared`,
  `ErrPayloadTooLarge`, `ErrUnavailable`, статусы `payload_too_large`, `undeclared_subject`;
  natskit: `StreamChecker` (сервер — `AccountInfo`), `ErrNoStream`, `SubjectMatches` для фильтров.
- Поведение — httpclient: `Events()` SSE закрывается после записи метрики и конца спана стрима.
- Изменения — testkit: `MigrationLadder(t, WithMigrations(...))` — лесенка up → down → up по
  версиям, down до нуля и up со сверкой схемы для своих источников миграций.
- Гейт: `make check` и `make gate-clean` — unit и integration (Docker), лесенка kit-owned миграций;
  pre-push — тот же гейт на каждом пушимом коммите в свежем worktree.
