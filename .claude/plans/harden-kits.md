---
status: in-progress
parent: go-backend-template:fix-reference-code
blocked-by: —
---

# harden-kits — фиксы китов по аудитам шаблона и релиз v1.2.0

## Цель
Закрыть в китах, которые импортирует `go-backend-template`, дефекты из аудита шаблона и fx-аудита:
жизненный цикл и fail-closed в `httpkit`, общий контракт ошибок API, явная обязательность
`timerkit`, миграции outbox-инстансов в графе, строгий `outboxtest`, `logkit.FxLogger` и
маскирование по умолчанию, атомарный `bruteforcekit`, помощник подписки `natskit`, предел
параллельности Argon2id. Итог — аннотированный тег `v1.2.0` в origin вместе с `main`.

## Контекст
- Находки: аудит шаблона (`M/audit-report.html`: SEC-02, SEC-05, SEC-09, TST-01, COD-05, COD-06,
  догфудер D02), fx-аудит (`M/fx-audit.md`: FX-01, FX-03, FX-04, FX-05, FX-12), аудит китов
  (`M/kits-audit/`: KDOC-01/06/07, KSEC-01/02), валидаторы этапов (`M/validation-harden-kits-{A,B}.md`:
  N-, R-замечания); `M` — `/home/mm/.cache/stit-audit-program/`.
- Программа, метод (исполнитель этапа → независимый валидатор) и политика push —
  `go-backend-template/.claude/plans/audit-program.md`: push `main` и тега — после PASS
  валидатора партии и `make gate-clean`.
- Объём — киты, которые импортирует шаблон (logkit, pgkit, httpkit, clockkit, authnkit,
  testkit/{pgtest,handlertest,authntest,audittest,outboxtest}, outboxkit(+pgstore),
  auditkit(+pgstore), natskit, config, bizmetric, bruteforcekit(+pgstore), timerkit, healthkit,
  flagkit, cachekit); прочие правятся, только если их ломает общий механизм.
- Потребители: `go-backend-template` на `v1.1.0` (ждёт релиз строками своего `backlog.md`
  golibinfra-httpkit-fx, -timerkit-dep, -fxlogger, -outbox-migrations), `ai-gateway-template` и
  `amocrm-gateway-template` на `v0.39.0`.
- fx v1.24: Invoke'и дочерних `fx.Module` идут раньше Invoke'ов уровня, куда они включены; хуки —
  в порядке добавления, OnStop — в обратном; хук, добавленный во время Start, не запускается.
  huma v2.39: `huma.NewError` — process-global, схема ошибок — из `NewError(0, "")`, Transformer'ы
  видят тело до записи статуса, OnAddOperation идёт после `defineErrors`.

## Не входит
- Правки в `go-backend-template` и шаблонах-шлюзах: миграция — по аннотации тега.
- Перенос I/O конструкторов `pgkit`/`natskit` в OnStart — см. «Решения».

## Критерии приёмки
- [x] **AC1** — HTTP-сервер `httpkit.Module` начинает слушать после OnStart всех fx-модулей и гасится до их OnStop при любом месте `httpkit.Module` среди опций `fx.New`; запрос в полёте на остановке завершается при живых ресурсах. Проверка: `go test -short -race -run 'TestModule_Lifecycle' ./httpkit/` → PASS
  - Подтверждено: `go test -short -race -run 'TestModule_Lifecycle' ./httpkit/` → PASS (`Module` первым, в середине и последним: хук сервера — последний в OnStart и первый в OnStop, запрос в полёте на остановке — 204 при открытом ресурсе); на прежнем коде (хук в конструкторе) краснеют «первым» и «в середине»
- [x] **AC2** — Операция с `Security` без `httpkit.AuthExtractor` в графе — ошибка старта с именами операций; `HumaAuth` без extractor'а отвечает на защищённую операцию `500 AUTH_NOT_CONFIGURED`, а не пропускает. Проверка: `go test -short -race -run 'TestModule_Auth|TestHumaAuth' ./httpkit/` → PASS
  - Подтверждено: `go test -short -race -run 'TestModule_Auth|TestHumaAuth' ./httpkit/` → PASS (старт — `ErrAuthNotConfigured` с именами защищённых операций, публичные не названы; с extractor'ом граф стартует и отвечает 401; скрытая защищённая операция и `HumaAuth(log, nil)` — `500 AUTH_NOT_CONFIGURED`); мутанты «nil → пропуск» и «без проверки старта» краснеют
- [x] **AC3** — Порядок middleware потребителя задаёт приложение одним значением `httpkit.Middlewares`; value group с перемешиваемым порядком нет. Проверка: `go test -short -race -run TestModule_Middlewares ./httpkit/` → PASS; `grep -rn MiddlewareGroup --include=*.go .` → пусто
  - Подтверждено: `go test -short -race -run TestModule_Middlewares ./httpkit/` → PASS (пять middleware в порядке слайса на каждом запросе, внутри цепочки кита: request-id в ctx, panic ловит `Recovery`); `grep -rn MiddlewareGroup --include=*.go .` → пусто
- [x] **AC4** — Любой ответ-ошибка сервера `httpkit` (сопоставленная и несопоставленная ошибка хендлера, валидация, auth, неизвестный маршрут, метод, лимит, panic) — `application/problem+json` с `code` и `request_id`, равным `X-Request-ID` и полю лога; 422 несёт `errors[]{location,code,message}`; 5xx без текста причины; 429 с `Retry-After`. Проверка: `go test -short -race -run TestErrorContract ./httpkit/` → PASS
  - Подтверждено: `go test -short -race -run TestErrorContract ./httpkit/` → PASS (12 классов на живом `Module`: `request_id` = `X-Request-ID` = строке access-log, у 5xx и panic — и строке причины; 422 и битый JSON — поля без эха пароля; 5xx без `detail`; `Retry-After` у 429 хендлера и лимитера — 1, у 503 с `RetryAfter()` в причине — 30; 405 с `Allow`); мутанты «без request_id», «5xx с текстом», «405 chi», «5xx без лога», «429 без Retry-After» краснеют
- [x] **AC5** — `httpkit.ErrorMap` сопоставляет доменную ошибку (`errors.Is` или предикат) со статусом, `code`, опц. полем и `Retry-After` из `RetryAfter()`; `Unmapped` перечисляет несопоставленные; несопоставленная даёт 500 и строку `handler error` с `operation` в логе; `Declare` кладёт статусы и коды в `openapi.json`. Проверка: `go test -short -race -run 'TestErrorMap' ./httpkit/` → PASS
  - Подтверждено: `go test -short -race -run 'TestErrorMap' ./httpkit/` → PASS (sentinel сквозь обёртку, предикат, поле в `errors[]`, `Retry-After` 90 из `RetryAfter()` и 1 у 429 без неё, `Unmapped`, 500 и `handler error` с `operation`, описания ответов в OpenAPI, сломанная и затенённая таблица — panic); шесть мутантов краснеют; шаблон в своём worktree через `replace` собирается после механической миграции, его handler-тесты зелёные, коды `Declare` — в его `openapi.json`
- [x] **AC6** — `timerkit.Provide` без `timerkit.Module` в графе — ошибка сборки (`fx.ValidateApp`), в том числе у `outboxkit.Module`/`Instance` и `bruteforcekit/pgstore.Module`. Проверка: `go test -short -race -run 'RequiresTimerkit|RequiresModule' ./timerkit/ ./outboxkit/... ./bruteforcekit/...` → PASS
  - Подтверждено: `go test -short -race -run 'RequiresTimerkit|RequiresModule' ./timerkit/ ./outboxkit/... ./bruteforcekit/...` → PASS на `a4030bf`
- [x] **AC7** — `outboxkit.Instance(name, table)` кладёт схему таблицы в группу `pgkit.migrations`: `pgkit.MigrateModule` накатывает её без ручного списка. Проверка: `go test -short -race -run TestInstance_ProvidesMigrations ./outboxkit/` → PASS; `make test-full` (pgstore) → PASS
  - Подтверждено: `go test -short -race -run TestInstance_ProvidesMigrations ./outboxkit/` → PASS; `make test-full PKGS=./outboxkit/...` → PASS (`TestFactoryModule_InstanceOnCustomTable` заводит таблицу через `pgkit.MigrateModule`); мутант «Instance без группы» краснеет в обоих
- [x] **AC8** — `outboxtest.Recorder.Publish` вне транзакции возвращает `outboxkit.ErrNoTx` и событие не копит (мутант D02 «Publish вне транзакции» краснеет). Проверка: `go test -short -race ./testkit/outboxtest/` → PASS
  - Подтверждено: `go test -short -race ./testkit/outboxtest/` → PASS (`TestRecorder_PublishRequiresTx`); мутант D02 на ветке догфудера шаблона (`note/usecase.go`: `Publish(ctx, …)` вместо `txCtx`) — на v1.1.0 выжил, на этом golibinfra через `replace` в своём worktree — `TestCreate` FAIL
- [x] **AC9** — `logkit.FxLogger` пишет события fx в структурный лог: причина падения старта — JSON-строка уровня ERROR, рутина — DEBUG. Проверка: `go test -short -race -run TestFxLogger ./logkit/` → PASS
  - Подтверждено: `go test -short -race -run TestFxLogger ./logkit/` → PASS (конструктор, `OnStart`, паника под `fx.RecoverFromPanics` — ERROR с причиной; рутина — DEBUG, при `info` лог пуст); мутанты «рутина на INFO» и `fx.NopLogger` краснеют
- [x] **AC10** — `NewRedactConfig` без env включает маскирование; значения под ключами `token`/`password`/`secret`/`authorization`/`cookie`/… и hex-цепочки ≥32 символов маскируются, числа не трогаются. Проверка: `go test -short -race -run 'TestRedact' ./logkit/` → PASS; `make envdoc-check` → OK
  - Подтверждено: `go test -short -race -run 'TestRedact' ./logkit/` → PASS (`TestRedact_EnabledByDefault`, `…SecretKeysMaskWholeValue`, `…NonSecretKeysStay`, `…SecretKeyValueKinds`, `…HexChains`); `make envdoc-check` → OK; мутанты «pre-filter без hex» и `minRedactHex=33` краснеют
- [x] **AC11** — `bruteforcekit`: subject нормализуется (trim, нижний регистр), IPv6 по умолчанию сворачивается до /64, `Guard.Attempt` атомарен: из N параллельных попыток пары проходят не больше `MaxFailures`. Проверка: `go test -short -race ./bruteforcekit/... ./testkit/bruteforcetest/` → PASS; `make test-full` → `TestStore_ConcurrentAttempts` PASS
  - Подтверждено: `go test -short -race ./bruteforcekit/... ./testkit/bruteforcetest/` → PASS; `make test-full` → `TestStore_ConcurrentAttempts` PASS (из 50 попыток проходят ровно `MaxFailures` = 5; `-count=3` стабилен); мутанты «upsert без `WHERE`» и «попытка сверх бюджета проходит» краснеют
- [x] **AC12** — `natskit.SubscribeOnStart` подписывает в OnStart, дренит в OnStop; ошибка подписки (включая `ErrJetStreamDisabled`) валит старт. Проверка: `go test -short -race -run TestSubscribeOnStart ./natskit/` → PASS
  - Подтверждено: `go test -short -race -run TestSubscribeOnStart ./natskit/` → PASS (порядок: старт зависимости → подписка → drain → остановка зависимости; `ErrJetStreamDisabled` настоящей выключенной шины валит старт); мутанты «ошибка подписки проглочена» и «без drain» краснеют
- [x] **AC13** — `Argon2idHasher` считает одновременно не больше `max(1, GOMAXPROCS/Threads)` хэшей; ожидание очереди прерывается ctx. Проверка: `go test -short -race -run TestArgon2id_Concurrency ./authnkit/` → PASS
  - Подтверждено: `go test -short -race -run TestArgon2id_Concurrency ./authnkit/` → PASS (из 3×предела одновременных `Hash` считается ровно предел; отмена ctx прерывает ожидание `Hash`/`Verify`/`VerifyDummy`); мутанты «без семафора» и «ожидание без ctx» краснеют
- [ ] **AC14** — README и доки затронутых китов описывают текущее поведение, `make gate-clean` и `make test-full` зелёные, аннотированный тег `v1.2.0` с разделами `Breaking`/`Поведение`/`Изменения`/`Гейт` в origin вместе с `main`. Проверка: `make gate-clean` → зелёный; `git tag -l --format='%(contents)' v1.2.0` → четыре раздела; `git ls-remote --tags origin v1.2.0` → есть; `git status -sb` → синхронно с origin

## Шаги
### Инфраструктурные киты
- [x] **S1** — `timerkit.Provide` требует `timerkit.Module` (маркер модуля в графе); fx-тесты и доки зависимых китов
  - Проверка: `go test -short -race ./timerkit/ ./outboxkit/... ./bruteforcekit/... ./hubkit/...` → PASS · Закрывает: AC6 · Сделано: `a4030bf`, `make check` зелёный
- [x] **S2** — `outboxkit.RepositoryFactory` — интерфейс `Repository`+`Migrations`; `Instance` кладёт миграции в `pgkit.migrations`
  - Проверка: `go test -short -race ./outboxkit/...` → PASS · Закрывает: AC7 · Зависит от: S1 · Сделано: `887527a`, `make test-full PKGS=./outboxkit/...` → PASS
- [x] **S3** — `outboxtest.Recorder` требует транзакцию (`pgkit.InTx`)
  - Проверка: `go test -short -race ./testkit/outboxtest/` → PASS · Закрывает: AC8 · Сделано: `16949a0`, `make check` зелёный
- [x] **S4** — `logkit.FxLogger`
  - Проверка: `go test -short -race ./logkit/` → PASS · Закрывает: AC9 · Сделано: `5333bee`, `make check` зелёный
- [x] **S5** — `logkit`: маскирование по ключу и hex-токенов, redact включён по умолчанию
  - Проверка: `go test -short -race ./logkit/`, `make envdoc-check` → PASS/OK · Закрывает: AC10 · Сделано: `73900ca`, `make check` зелёный
- [x] **S6** — `bruteforcekit`: нормализация subject, IPv6 /64 по умолчанию
  - Проверка: `go test -short -race ./bruteforcekit/...` → PASS · Закрывает: AC11 · Сделано: `2d0aeaf`, `make test-full` bruteforcekit → PASS
- [x] **S7** — `bruteforcekit`: атомарный `Guard.Attempt` вместо `Check`+`RecordFailure`; фейки `bruteforcetest`
  - Проверка: `go test -race ./bruteforcekit/... ./testkit/bruteforcetest/` (Docker) → PASS · Закрывает: AC11 · Зависит от: S6 · Сделано: `2b8e39a`, `make check` зелёный
- [x] **S8** — `authnkit`: предел параллельности Argon2id
  - Проверка: `go test -short -race ./authnkit/` → PASS · Закрывает: AC13 · Сделано: `a42b333`, `make check` зелёный
- [x] **S9** — `natskit.SubscribeOnStart`
  - Проверка: `go test -short -race ./natskit/` → PASS · Закрывает: AC12 · Сделано: `37da068`, `make test-full` зелёный
### httpkit
- [x] **S10** — Хук HTTP-сервера в Invoke уровня `httpkit.Module`: старт последним, остановка первой
  - Проверка: `go test -short -race ./httpkit/` → PASS · Закрывает: AC1 · Сделано: `832904e`, `make check` зелёный
- [x] **S11** — Fail-closed auth: ошибка старта и `HumaAuth` без extractor'а
  - Проверка: `go test -short -race ./httpkit/ ./oidckit/ ./hubkit/...` → PASS · Закрывает: AC2 · Зависит от: S10 · Сделано: `5c3646e`, `make check` зелёный
- [x] **S12** — `httpkit.Middlewares` вместо группы `httpkit.middleware`
  - Проверка: `go test -short -race ./httpkit/` → PASS · Закрывает: AC3 · Зависит от: S10 · Сделано: `f5f4111`, `make check` зелёный
- [x] **S13** — Контракт ошибок: problem+json с `code`/`request_id`/`errors[]`, `Retry-After`, 404/405 маршрутизатора; `handlertest`
  - Проверка: `go test -short -race ./httpkit/ ./testkit/handlertest/ ./oidckit/ ./cachekit/` → PASS · Закрывает: AC4 · Зависит от: S11 · Сделано: `c1f84e8`, `make check` зелёный
- [x] **S14** — `ErrorMap` (`Map`/`Unmapped`/`Declare`) вместо `MapErr`/`MapIs`/`MapAs`, коды в OpenAPI
  - Проверка: `go test -short -race ./httpkit/` → PASS · Закрывает: AC5 · Зависит от: S13 · Сделано: `007d79a`, `make check` зелёный
### Релиз
- [ ] **S15** — Корневой README/CLAUDE.md сверены, `make test-full`, `make gate-clean`, тег `v1.2.0` локально; push `main` и тега — после PASS валидатора партии
  - Проверка: `make gate-clean` → зелёный, `make test-full` → PASS · Закрывает: AC14 · Зависит от: S1, S2, S3, S4, S5, S6, S7, S8, S9, S10, S11, S12, S13, S14

## Решения
- Все находки подтверждены чтением кода, кроме FX-12 «I/O конструкторов» (ниже).
- FX-01: хук сервера вешает `fx.Invoke(serve)` рядом с `fx.Module("httpkit", …)`, а не конструктор
  — fx выполняет Invoke'и всех `fx.Module` раньше, поэтому место `Module` среди модулей не важно
  (прототип P1 требовал ставить его последним). Условие в доке: `Module` — на верхнем уровне
  `fx.New`, корневой `fx.Invoke`, который строит зависимости, — до него или внутри `fx.Module`.
- FX-03: проверку «`Security` без extractor'а» делает OnStart сервера по `OpenAPI().Paths` — итог
  после хуков потребителя, `Server` состояния не копит; скрытую операцию OpenAPI не видит — её
  закрывает `HumaAuth(log, nil)`: `500 AUTH_NOT_CONFIGURED` и строка `auth not configured`.
- FX-12 (middleware, N6): одно необязательное значение `httpkit.Middlewares` / `httpkit.HumaConfigs`
  в порядке слайса вместо групп — порядок зависимых правок явный по построению, номера не нужны;
  стражей старых групп нет: их берёт только шаблон, через константы — bump падает при компиляции.
- Контракт ошибок: `huma.NewError`/`NewErrorWithContext` подменены в `init()` — одна форма во всём
  процессе, включая `humatest`; `request_id` и строка причины 5xx — Transformer сервера (его
  логгер, без глобального указателя); `type`/`instance` удалены, 5xx кита — без `detail` (`detail`
  своего 5xx — выбор потребителя).
  `Retry-After` ставит пишущий ответ (Transformer, chi, `HumaAuth`) из `RetryAfter()` причины, у 429
  — не меньше секунды; заголовок хендлера (`huma.ErrorWithHeaders`) точнее и сохраняется (N2).
- Эхо ввода: у ошибок полей нет `value`, код — `INVALID`; хвост сообщений huma, которые цитируют
  ввод, режется до базового текста — форматы по константам `huma/validation` (N3) и
  `invalid value: ` декодера параметра. Текст декодера тела (синтаксис JSON, `UnmarshalText`/
  `UnmarshalJSON` типа поля без формата в схеме) идёт без метки и объясняет ошибку с location
  `body` — обещание названо честно: пароль в строковом поле проверяет схема, а не декодер.
- `ErrorMap` — одна таблица на домен/операцию (`Map`, `Declare`, `Unmapped`), `MapErr`/`MapIs`/
  `MapAs` удалены — один способ; `NewErrorMap` отвергает сломанное и затенённое правило panic'ом при
  сборке; коды — в `op.Metadata` и описание ответа (`Conflict: CODE_A, CODE_B`). Тот же хук объявляет
  401 `UNAUTHORIZED` у операции с `Security` (N7; 503 `AUTH_UNAVAILABLE` отдаёт только extractor —
  не объявляется); хук — последний, после правок потребителя: их `Security` тоже даёт 401, а
  `openapi.json` из `make gen` строится без старта.
- N8: `Idempotency` берёт логгер параметром, как `Recovery`/`AccessLog`, — забыть нельзя; отказ
  store — Warn с `op`, 5xx `APIError` extractor'а — Error `auth failed`: у каждого 5xx кита есть
  строка причины, контракт-тест требует её в логгере графа.
- FX-04: `timerkit.Provide` требует неэкспортируемый маркер модуля (`fx.Supply` его не подложит) —
  job без запускающего модуля невозможна у любого кита; сырой тег группы маркер обходит — контракт
  «только `Provide`» в README timerkit (бэклог).
- FX-12 (outbox, S2): `RepositoryFactory` — интерфейс со схемой таблицы: `outboxkit` не может
  импортировать `pgstore` (цикл); ручной `ProvideMigrations(TableMigrations(t))` рядом с `Instance`
  падает громко (`duplicate source name`) — дедупликации нет.
- `fix(outboxkit)` вне шагов: `EnsurePartitions` рядом с параллельным создателем той же партиции
  (хук и keeper-job на старте, реплики) терпит только `42P07` — блокировка родителя сериализует
  создателей, `unique_violation` каталога для `PARTITION OF` недостижим.
- S3: `outboxtest.Recorder` всегда требует транзакцию (как `OUTBOX_REQUIRE_TX=true`), решая это той
  же `pgkit.InTx`, что pgstore; лазейку `pgkit.WithTestTx` на ctx usecase'а дёшево не закрыть (один
  маркер у фейка и ручной пометки) — контракт в `outboxkit/docs/testing.md` (бэклог).
- S4: `FxLogger` — переменная `fx.Option` (как `fx.NopLogger`) над `*slog.Logger` графа, рутина —
  DEBUG (`fxevent.SlogLogger` пишет INFO); невалидный `LOG_*` не даёт собрать сам логгер — причина
  уходит текстом в stderr.
- SEC-02, KDOC-01: маскирование включено всегда, умолчание — в коде: выключатель `Disabled`
  (`LOG_REDACT_DISABLED`), потому что у `bool` безопасен только нулевой `false`; нулевой
  `RedactConfig{}`, граф без него и `New` без `WithRedact` маскируют, прежний
  `LOG_REDACT_ENABLED=false` игнорируется — ошибка в безопасную сторону; регрессии KSEC-01 держат
  три пути: граф из одного `NewConfig`, `logkit.New` без опций, логгер ошибок telemetry.
- S5: ключ режется на слова (разделители, camelCase, цифры); секрет — слово словаря или его
  окончание (`accesstoken`; `input_tokens` — не секрет), `api`/`private` + `key`; код — слово `pin`,
  `pincode` или на `otp` где угодно в ключе: прячется и число, и счётчик рядом (`otp_attempts`) —
  смысл соседних слов правило не угадывает. Те же правила — у ключей `map` (`http.Header`,
  `url.Values`, вложенные, в срезах; копия — только при секрете); поля структур не разбираются
  (путь — `LogValuer`); hex-цепочка ≥32 без `\b`. Прочие пропуски — в бэклоге.
- Обход ключей (R1, R2, KSEC-02): пройденные map помнятся (`seen`, `memo`) — цикл не роняет процесс
  (предел глубины дал бы экспоненциальный обход общих поддеревьев). Копия кодируется в тот же JSON:
  указатели не разыменовываются (адресуемость для `MarshalJSON`), типы с собственной кодировкой не
  разбираются, неJSON-значение — только `!ERROR:<ошибка кодирования>` во всех sink'ах; указатель на
  контейнер не разбирается — польза мала, риск адресуемости велик (бэклог).
- Цена обхода: `map` и срезы map обходятся всегда (pre-filter по JSON дороже на ключах-триггерах
  без секрета), ключ без подстроки из `keyTriggers` на слова не режется (property-тест),
  `selfEncoding` кэшируется по типу; тест держит потолок ×3 (замер — не выше ×2.66).
- SEC-05, S6, S7: попытка учитывается до проверки секрета (`Attempt`), успех (`RecordSuccess`)
  сбрасывает окно — гонка «прочитал, потом записал» и забытый учёт невозможны; попытка на пороге
  проходит и ставит блокировку, upsert с `WHERE` под блокировкой строку не пишет (снятую между
  чтениями — переучитывает, ≤3 раз); `Attempt` в транзакции — `ErrInTx` (откат снимал учёт). Subject
  — `ToLower(TrimSpace)`, фейки, кроме `Counting`, ключ только проверяют; строки со старым ключом
  теряются не дольше `LockDuration`, не мигрируем; метрика `bruteforce_checks_total` сохранила имя.
- SEC-09, S8: предел — семафор `max(1, GOMAXPROCS/Threads)` без настройки (lanes не больше ядер),
  общий для `Hash`/`Verify`/`VerifyDummy` — иначе время ответа различало бы пути; отмена ctx в
  очереди — ошибка, у `VerifyDummy` — пропуск; ожидание входит в гистограммы; шов теста —
  неэкспортируемое поле-функция `derive` (из-за него тип несравним — строка аннотации).
- COD-06, S9: `SubscribeOnStart(lc, subscribe)` принимает любую подписку, `subscribe` получает ctx
  хука; выключенный JetStream — ошибка старта. Вызов из чужого `OnStart` fx молча теряет — узнать
  изнутри нельзя, поэтому строка в doc-comment и `usage.md`.
- FX-12 «I/O конструкторов» отброшен: Ping пула ограничен `PG_CONNECT_TIMEOUT`, `nats.Connect` —
  `NATS_CONNECT_TIMEOUT`, вызовы JetStream API — 5s nats.go; перенос в OnStart не убирает I/O из
  сборки графа и ломает подписку в `fx.Invoke` — несоразмерно.
- Задеты неиспользуемые шаблоном киты: `testkit/bruteforcetest` (интерфейс `Guard`), тесты
  `hubkit/sync` (`timerkit.Module`), `oidckit`/`hubkit/sso` (контракт ошибок).

## Открытые вопросы
—

## Handoff
- `go-backend-template` при bump: `beforeHTTP` не нужен, но корневой `fx.Invoke(registerHTTP)` —
  до `httpkit.Module` или внутри `fx.Module` (иначе зависимость с хуком, которую первыми берут
  хендлеры, стартует после HTTP); boot-тест — `httpkit.serve` последний в OnStart и первый в
  OnStop целиком; client-IP — `httpkit.Middlewares`; `HumaConfigGroup` → `httpkit.HumaConfigs`;
  `MapErr` → `ErrorMap.Map` + `Declare`; `LoginGuard` — `Attempt` до проверки пароля +
  `RecordSuccess`; subscriber — `natskit.SubscribeOnStart`; `NATS_JETSTREAM_ENABLED=true` в
  `.env.example`; `logkit.FxLogger` + `fx.RecoverFromPanics()` в `main`; `openapi.json`
  перегенерировать (схема `APIError`, 401 защищённых операций); логи маскируются по умолчанию.
- Шлюзы (`v0.39.0`): цепочка аннотаций тегов до `v1.2.0`.

## Где мы сейчас
- S1–S14 закрыты (хеши — в «Шагах»), вне шагов — `dd6eed6`. Валидаторы этапов — PASS: S1–S9
  (`M/validation-harden-kits-A.md`, фиксы `cb32246`…`d62b2e6`), S10–S14 с кругом перед релизом
  (`M/validation-harden-kits-B.md`, фиксы `80fb648`…`49e0ed3`); остаток перепроверки 49e0ed3 —
  эхо декодера параметра `55c39b4`, 401 из хука потребителя `a070e1c`, мутант F11 `5243883`.
- Следующий шаг — S15: корневые README/CLAUDE.md, `make test-full`, `make gate-clean`, тег
  `v1.2.0` локально; затем валидатор партии, push `main` и тега. Перед коммитом — `make check`
  (govulncheck зависает на сети — перезапуск).
- Заготовка аннотации `v1.2.0` (в теге — строки «- кит: что изменилось → как мигрировать»):
  - Breaking:
    - logkit (KDOC-01): `RedactConfig.Enabled` и `LOG_REDACT_ENABLED` удалены → выключатель
      `RedactConfig{Disabled: true}` / `LOG_REDACT_DISABLED=true`, `RedactConfig{Enabled: true}` →
      `RedactConfig{}`; тест, сверяющий сырые значения в логе, — `WithRedact(RedactConfig{Disabled: true})`.
    - httpkit (S11): `Security` без `AuthExtractor` в графе — ошибка старта `ErrAuthNotConfigured`
      с именами операций, `HumaAuth(log, nil)` — `500 AUTH_NOT_CONFIGURED` → провайдите extractor
      или объявите публичную операцию пустым `Security: []map[string][]string{}`.
    - httpkit (S12): группа `httpkit.middleware` и `MiddlewareGroup` удалены → одно значение
      `httpkit.Middlewares` (порядок слайса, первый — внешний); значение с тегом старой группы
      никто не читает — middleware молча выпадет из цепочки.
    - httpkit (N6): группа `httpkit.huma_config` и `HumaConfigGroup` удалены →
      `fx.Supply(httpkit.HumaConfigs{f})` вместо `fx.Supply(fx.Annotated{Group: …, Target: f})`.
    - httpkit (N8): `Idempotency(cfg, store, opts...)` → `Idempotency(cfg, store, log, opts...)`.
    - httpkit (S13): `APIError` без `Type`/`Instance`, с `RequestID` и `Errors []FieldError`
      (значение больше не сравнимо `==`); ошибки huma во всём процессе — `APIError`, `ErrorModel` и
      эхо `value` клиент не получает → тесты с разбором `ErrorModel` — на `APIError`,
      `openapi.json` перегенерировать.
    - httpkit (S14): `MapErr`/`MapIs`/`MapAs`/`ErrorMapping` удалены →
      `httpkit.NewErrorMap(httpkit.ErrorRule{Err|Match, Status, Code, Detail, Field})`: `m.Map(err)` в
      хендлере, `huma.Register(api, m.Declare(op), h)`, `m.Unmapped(sentinel…)` пуст в тесте
      полноты; сломанная таблица и частное правило под общим — panic.
    - testkit/handlertest (S13): `APIError`/`APIErrorLocation` удалены → `httpkit.APIError`
      (`DecodeAPIError` отдаёт его), `Errors[]` — `httpkit.FieldError` без `Value`;
      `AssertAPIError` проверяет ещё problem+json и `request_id` = `X-Request-ID`.
    - timerkit (S1): `timerkit.Provide` и киты с job'ами (outboxkit `Module`/`Instance`,
      `outboxkit/pgstore`, `bruteforcekit/pgstore`, `hubkit/sync`) требуют `timerkit.Module` и
      `logkit.Logger` в графе, в том числе тестовом → добавьте их; без модуля —
      `missing type: timerkit.scheduler`.
    - outboxkit (S2): `RepositoryFactory` — интерфейс `Repository(table)`/`Migrations(table)` →
      своя фабрика реализует оба метода; ручной `pgkit.ProvideMigrations(pgstore.TableMigrations(t))`
      рядом с `Instance` убрать, иначе `duplicate source name`.
    - testkit/outboxtest (S3): `Recorder.Publish` вне транзакции → `outboxkit.ErrNoTx` → usecase в
      тесте — через фейк Transactor (`pgtest.Identity`/`pgtest.Recorder`); `pgkit.WithTestTx` —
      только для прямого вызова `Publish`, иначе публикация мимо транзакции снова проходит.
    - bruteforcekit (S7): `Guard` и `pgstore.Store` — `Attempt` + `RecordSuccess`,
      `Check`/`RecordFailure` удалены → `Attempt` до проверки пароля и вне транзакции (в
      `WithTx` — `ErrInTx`), `*LockState` → отказ, успех — `RecordSuccess` (можно в транзакции);
      `LockState.FailedCount` — попытки окна без успеха.
    - testkit/bruteforcetest (S7, KDOC-06): у `Permissive`, `Locked`, `Recorder`, `Counting` —
      `Attempt` вместо `Check`/`RecordFailure`; `Recorder.Failures()`/`SuccessAttempts()` удалены,
      `Successes()` → `[]Attempt` (subject — поле `Subject`), `Attempts()` — каждый вызов `Attempt`.
    - authnkit (S8): `Argon2idHasher` больше не сравним `==` → держите `*Argon2idHasher` из
      `NewArgon2idHasher`.
  - Поведение:
    - httpkit (S10): HTTP слушает после OnStart всех `fx.Module` и гасится до их OnStop; `Module` —
      на верхнем уровне `fx.New`, корневой `fx.Invoke`, который строит зависимости (в том числе
      регистрирует роуты), — до него или внутри `fx.Module`, иначе хук зависимости встанет после HTTP.
    - httpkit (S13): 404/405 — problem+json `NOT_FOUND`/`METHOD_NOT_ALLOWED` (с `Allow`); 422 —
      `VALIDATION_FAILED`, поля `INVALID` без эха ввода (хвост форматов и `invalid value` параметра
      режется, текст декодера тела — как есть); 5xx кита без `detail` (500 panic, 503
      `AUTH_UNAVAILABLE`, `IDEMPOTENCY_UNAVAILABLE`); `Retry-After` — из `RetryAfter()` причины, свой
      заголовок хендлера сохраняется; `handler error` — с `operation` и `code`, в логгер сервера.
    - httpkit (N8): отказ store идемпотентности — Warn `idempotency store failed`, 5xx `APIError`
      extractor'а — Error `auth failed`.
    - testkit/handlertest (S13): `NewAPI` — API сервера `httpkit` (404/405 по контракту, тело до
      1 MiB, `request_id` в ответах).
    - timerkit (S1): повторный `timerkit.Module` — ошибка старта `already provided`.
    - outboxkit (S2): с `pgkit.MigrateModule` таблицы инстансов накатываются на старте сами;
      `EnsurePartitions` не валит старт, когда ту же партицию создаёт другой процесс.
    - logkit (S5, KDOC-01): маскирование включено всегда, в том числе без `NewRedactConfig` и у
      `New` без `WithRedact`; значение под ключом-секретом (token, password, secret, authorization,
      cookie, …, и по окончанию слова — `accesstoken`), в том числе у ключей `map` (`http.Header`,
      `url.Values`, `map[string]any`), — целиком, ключ со словом `pin`/`pincode`/`…otp` — и число,
      hex-цепочки ≥32 — маской; выключить — `LOG_REDACT_DISABLED=true`; значение без JSON-формы
      (`*http.Request`, структура с `func`) во всех sink'ах — `!ERROR:<ошибка>`.
    - bruteforcekit (S6): subject — в нижнем регистре без пробелов по краям,
      `BRUTEFORCE_IPV6_PREFIX` по умолчанию `64` (полный адрес — `128`); блокировки строк со старым
      ключом теряются не дольше `BRUTEFORCE_LOCK_DURATION`.
    - authnkit (S8): `Argon2idHasher` считает не больше `max(1, GOMAXPROCS/Threads)` хэшей
      одновременно, лишние ждут; отмена ctx в очереди — ошибка с `ctx.Err()`.
  - Изменения:
    - logkit (S4): `logkit.FxLogger` — события fx в структурный лог.
    - natskit (S9): `SubscribeOnStart` — подписка в `OnStart`, drain в `OnStop`, ошибка подписки
      (и `ErrJetStreamDisabled`) валит старт.
    - outboxkit (S2): `pgstore.Module` и `FactoryModule` сочетаются в одном графе.
    - httpkit (S14, N7): `ErrorMap.Declare` — статусы и коды ошибок в `openapi.json`; операция с
      `Security` (и выставленным хуком потребителя) объявляет `401 UNAUTHORIZED` сама — спецификацию
      перегенерировать.
    - Тулинг: `make plan-check` (формат планов) — часть `make check`.
