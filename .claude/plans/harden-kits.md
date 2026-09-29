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
- Аудит шаблона (`M/audit-report.html`: SEC-02, SEC-05, SEC-09, TST-01, COD-05, COD-06, дневник
  догфудера D02) и fx-аудит (`M/fx-audit.md`: FX-01, FX-03, FX-04, FX-05, FX-12; прототип P1 —
  `M/fxexp/zz_httpfix_proto.go.txt`); `M` — материалы программы `/home/mm/.cache/stit-audit-program/`.
- Программа, метод (исполнитель этапа → независимый валидатор) и политика push —
  `go-backend-template/.claude/plans/audit-program.md`: push `main` и тега — после PASS
  валидатора партии и `make gate-clean`.
- Объём — только киты, которые импортирует шаблон (logkit, pgkit, httpkit, clockkit, authnkit,
  testkit/{pgtest,handlertest,authntest,audittest,outboxtest}, outboxkit(+pgstore),
  auditkit(+pgstore), natskit, config, bizmetric, bruteforcekit(+pgstore), timerkit, healthkit,
  flagkit, cachekit). Неиспользуемые киты правятся, только если их ломает общий механизм.
- Потребители: `go-backend-template` на `v1.1.0` (обновится сам следующим этапом),
  `ai-gateway-template` и `amocrm-gateway-template` на `v0.39.0`. Шаблон ждёт этот релиз
  строками `backlog.md` (golibinfra-httpkit-fx, -timerkit-dep, -fxlogger, -outbox-migrations).
- fx v1.24: Invoke'и дочерних `fx.Module` выполняются раньше Invoke'ов уровня, куда они
  включены (`module.go:313`); хуки идут в порядке добавления, OnStop — в обратном; хук,
  добавленный во время Start, не запускается (`internal/lifecycle/lifecycle.go:205`).
- huma v2.39: `huma.NewError`/`NewErrorWithContext` — process-global; `defineErrors` берёт схему
  ошибок из `NewError(0, "")`; Transformer'ы видят каждое тело ответа до записи статуса;
  `HeadersError` даёт заголовки ответа-ошибки; OnAddOperation идёт после `defineErrors`.

## Не входит
- Правки в `go-backend-template` и шаблонах-шлюзах: миграция — по аннотации тега.
- Перенос I/O конструкторов `pgkit`/`natskit` в OnStart — см. «Решения».

## Критерии приёмки
- [ ] **AC1** — HTTP-сервер `httpkit.Module` начинает слушать после OnStart всех fx-модулей и гасится до их OnStop при любом месте `httpkit.Module` среди опций `fx.New`; запрос в полёте на остановке завершается при живых ресурсах. Проверка: `go test -short -race -run 'TestModule_Lifecycle' ./httpkit/` → PASS
- [ ] **AC2** — Операция с `Security` без `httpkit.AuthExtractor` в графе — ошибка старта с именами операций; `HumaAuth` без extractor'а отвечает на защищённую операцию `500 AUTH_NOT_CONFIGURED`, а не пропускает. Проверка: `go test -short -race -run 'TestModule_Auth|TestHumaAuth' ./httpkit/` → PASS
- [ ] **AC3** — Порядок middleware потребителя задаёт приложение одним значением `httpkit.Middlewares`; value group с перемешиваемым порядком нет. Проверка: `go test -short -race -run TestModule_Middlewares ./httpkit/` → PASS; `grep -rn MiddlewareGroup --include=*.go .` → пусто
- [ ] **AC4** — Любой ответ-ошибка сервера `httpkit` (сопоставленная и несопоставленная ошибка хендлера, валидация, auth, неизвестный маршрут, метод, лимит, panic) — `application/problem+json` с `code` и `request_id`, равным `X-Request-ID` и полю лога; 422 несёт `errors[]{location,code,message}`; 5xx без текста причины; 429 с `Retry-After`. Проверка: `go test -short -race -run TestErrorContract ./httpkit/` → PASS
- [ ] **AC5** — `httpkit.ErrorMap` сопоставляет доменную ошибку (`errors.Is` или предикат) со статусом, `code`, опц. полем и `Retry-After` из `RetryAfter()`; `Unmapped` перечисляет несопоставленные; несопоставленная даёт 500 и строку `handler error` с `operation` в логе; `Declare` кладёт статусы и коды в `openapi.json`. Проверка: `go test -short -race -run 'TestErrorMap' ./httpkit/` → PASS
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
  - Проверка: `go test -short -race ./timerkit/ ./outboxkit/... ./bruteforcekit/... ./hubkit/...` → PASS · Закрывает: AC6
  - Сделано: `a4030bf` (`make check` зелёный)
- [x] **S2** — `outboxkit.RepositoryFactory` — интерфейс `Repository`+`Migrations`; `Instance` кладёт миграции в `pgkit.migrations`
  - Проверка: `go test -short -race ./outboxkit/...` → PASS · Закрывает: AC7 · Зависит от: S1
  - Сделано: `feat(outboxkit)!` — `go test -short -race ./outboxkit/...` → PASS, `make test-full PKGS=./outboxkit/...` → PASS, `make check` зелёный
- [x] **S3** — `outboxtest.Recorder` требует транзакцию (`pgkit.InTx`)
  - Проверка: `go test -short -race ./testkit/outboxtest/` → PASS · Закрывает: AC8
  - Сделано: `feat(outboxtest)!` — `go test -short -race ./testkit/outboxtest/` → PASS, `make check` зелёный
- [x] **S4** — `logkit.FxLogger`
  - Проверка: `go test -short -race ./logkit/` → PASS · Закрывает: AC9
  - Сделано: `feat(logkit)` — `go test -short -race ./logkit/` → PASS, `make check` зелёный
- [x] **S5** — `logkit`: маскирование по ключу и hex-токенов, redact включён по умолчанию
  - Проверка: `go test -short -race ./logkit/`, `make envdoc-check` → PASS/OK · Закрывает: AC10
  - Сделано: `feat(logkit)` — `go test -short -race ./logkit/` → PASS, `make envdoc-check` → OK, `make check` зелёный
- [x] **S6** — `bruteforcekit`: нормализация subject, IPv6 /64 по умолчанию
  - Проверка: `go test -short -race ./bruteforcekit/...` → PASS · Закрывает: AC11
  - Сделано: `feat(bruteforcekit)` — `go test -short -race ./bruteforcekit/... ./testkit/bruteforcetest/` → PASS, `make test-full PKGS='./bruteforcekit/... ./testkit/bruteforcetest/'` → PASS, `make check` зелёный
- [x] **S7** — `bruteforcekit`: атомарный `Guard.Attempt` вместо `Check`+`RecordFailure`; фейки `bruteforcetest`
  - Проверка: `go test -race ./bruteforcekit/... ./testkit/bruteforcetest/` (Docker) → PASS · Закрывает: AC11 · Зависит от: S6
  - Сделано: `feat(bruteforcekit)!` — `go test -race ./bruteforcekit/... ./testkit/bruteforcetest/` (Docker) → PASS, `make check` зелёный; в `make test-full` bruteforcekit PASS, красный только флак `outboxkit/pgstore` `TestModule_EnsuresPartitionsOnStart` (гонка создания партиции, чинится отдельным `fix(outboxkit)`)
- [x] **S8** — `authnkit`: предел параллельности Argon2id
  - Проверка: `go test -short -race ./authnkit/` → PASS · Закрывает: AC13
  - Сделано: `feat(authnkit)` — `go test -short -race ./authnkit/` → PASS, `make check` зелёный
- [x] **S9** — `natskit.SubscribeOnStart`
  - Проверка: `go test -short -race ./natskit/` → PASS · Закрывает: AC12
  - Сделано: `feat(natskit)` — `go test -short -race ./natskit/` → PASS, `make check` зелёный, `make test-full` зелёный
### httpkit
- [ ] **S10** — Хук HTTP-сервера в Invoke уровня `httpkit.Module`: старт последним, остановка первой
  - Проверка: `go test -short -race ./httpkit/` → PASS · Закрывает: AC1
- [ ] **S11** — Fail-closed auth: ошибка старта и `HumaAuth` без extractor'а
  - Проверка: `go test -short -race ./httpkit/ ./oidckit/ ./hubkit/...` → PASS · Закрывает: AC2 · Зависит от: S10
- [ ] **S12** — `httpkit.Middlewares` вместо группы `httpkit.middleware`
  - Проверка: `go test -short -race ./httpkit/` → PASS · Закрывает: AC3 · Зависит от: S10
- [ ] **S13** — Контракт ошибок: problem+json с `code`/`request_id`/`errors[]`, `Retry-After`, 404/405 маршрутизатора; `handlertest`
  - Проверка: `go test -short -race ./httpkit/ ./testkit/handlertest/ ./oidckit/ ./cachekit/` → PASS · Закрывает: AC4 · Зависит от: S11
- [ ] **S14** — `ErrorMap` (`Map`/`Unmapped`/`Declare`) вместо `MapErr`/`MapIs`/`MapAs`, коды в OpenAPI
  - Проверка: `go test -short -race ./httpkit/` → PASS · Закрывает: AC5 · Зависит от: S13
### Релиз
- [ ] **S15** — Корневой README/CLAUDE.md сверены, `make test-full`, `make gate-clean`, тег `v1.2.0` локально; push `main` и тега — после PASS валидатора партии
  - Проверка: `make gate-clean` → зелёный, `make test-full` → PASS · Закрывает: AC14 · Зависит от: S1, S2, S3, S4, S5, S6, S7, S8, S9, S10, S11, S12, S13, S14

## Решения
- Все пункты подтверждены чтением кода, кроме FX-12 «I/O конструкторов под таймаутом» (ниже).
- FX-01: хук сервера регистрирует `fx.Invoke` уровня, куда включён `httpkit.Module`
  (`fx.Options(fx.Module("httpkit", …), fx.Invoke(serve))`), конструктор хуков не добавляет. fx
  выполняет Invoke'и всех `fx.Module` раньше, поэтому место `httpkit.Module` среди модулей не
  важно; прототип P1 (Invoke внутри модуля) требовал ставить модуль последним. Ограничение в
  доке: `httpkit.Module` — на верхнем уровне `fx.New`, корневой `fx.Invoke` с хуками — до него.
- FX-03: проверка — в OnStart сервера по операциям, собранным OnAddOperation; `HumaAuth(nil)`
  отвечает 500 на защищённую операцию (скрытые операции OnAddOperation не видит).
- FX-12 (middleware): одно необязательное значение `httpkit.Middlewares` в порядке слайса вместо
  группы — порядок явный по построению, сортировка и номера не нужны; группа удаляется.
- Контракт ошибок: `huma.NewError`/`NewErrorWithContext` подменяются в `init()` пакета — форма
  ошибок одна во всём процессе, включая `humatest` в тестах потребителя; `request_id` и лог
  причины 5xx — Transformer сервера (логгер сервера, без глобального указателя); поле ошибки из
  huma получает код `INVALID`, эхо `value` убрано (пароль не возвращается клиенту);
  `Retry-After` — из метода `RetryAfter()` в цепочке ошибки, у 429 не меньше секунды.
- `ErrorMap` — одна таблица на домен/операцию: `Map` в хендлере, `Declare` для OpenAPI (коды —
  в описание ответа статуса через OnAddOperation), `Unmapped` для теста полноты;
  `MapErr`/`MapIs`/`MapAs` удалены — один канонический способ.
- FX-04 обобщён: маркер требует `timerkit.Provide`, поэтому job без запускающего модуля
  невозможна у любого кита (outboxkit, bruteforcekit/pgstore, hubkit/sync).
- FX-12 (outbox): `RepositoryFactory` — интерфейс со схемой таблицы: `outboxkit` не может
  импортировать `pgstore` (цикл), а фабрика знает и хранилище, и его миграции.
- `outboxtest.Recorder` всегда требует транзакцию — как дефолт `OUTBOX_REQUIRE_TX=true`; фейк
  без переключателя.
- SEC-02: redact включается дефолтом `LOG_REDACT_ENABLED=true` (нулевой `RedactConfig{}` —
  выключен, как и был); ключи сравниваются по словам (`input_tokens` — не секрет), маскируются
  строки, группы и структуры под ключом, числа нет; hex-цепочка ≥32 (токены, SHA-1/256).
- SEC-05: попытка учитывается до проверки секрета (`Attempt`), успех сбрасывает окно — гонка
  «прочитал, потом записал» и забытый `RecordFailure` невозможны; upsert с `WHERE` не пишет
  строку при активной блокировке.
- SEC-09 подтверждён кодом: 64 МиБ на хэш без предела, логин анонимен; предел — семафор
  `max(1, GOMAXPROCS/Threads)` без настройки (lanes не больше ядер, пропускная способность та же).
- COD-06: `SubscribeOnStart(lc, subscribe)` принимает любую подписку; выключенный JetStream —
  ошибка старта, а не молчаливый пропуск.
- FX-12 «I/O конструкторов» отброшен: Ping пула ограничен `PG_CONNECT_TIMEOUT` (10s; `0` —
  документированный opt-out), `nats.Connect` — `NATS_CONNECT_TIMEOUT`, каждый вызов JetStream API
  в `ensureStreams` — 5s по умолчанию nats.go, зависший вызов обрывает старт ошибкой. Перенос в
  OnStart не убирает I/O из сборки графа (pgxpool прогревает `MinConns` в фоне) и для natskit
  ломает поддержанную подписку в `fx.Invoke` — несоразмерно.
- S2: одноимённые источники `MigrateUpAll` отвергает (`duplicate source name`), поэтому ручной
  `ProvideMigrations(TableMigrations(t))` рядом с `Instance` падает громко — дедупликации нет;
  юнит-тест группы — на фейковой фабрике (реальная регистрирует gauge'и), связку с pgstore держит
  integration через `MigrateModule`; `pgstore.Module` и `FactoryModule` сочетаются.
- S3: ошибка фейка — та же обёртка, что у pgstore; фейк и хранилище решают «в транзакции ли»
  одной функцией `pgkit.InTx`.
- S4: `FxLogger` — переменная `fx.Option` (как `fx.NopLogger`), берёт `*slog.Logger` графа и
  ставит рутине DEBUG (по умолчанию `fxevent.SlogLogger` пишет INFO); невалидный `LOG_*` не даёт
  собрать сам логгер событий — причина уходит текстом в stderr (README).
- S5 и неблок. 1: hex-маска `[0-9A-Fa-f]{32,}` без `\b`, pre-filter «32 hex подряд» совпадает с
  ней точно. Ключ режется на слова (разделители, camelCase, цифры); секрет — слово из словаря
  целиком или его окончание (`accesstoken`, `totp`), `api`/`private` + `key`; коды `otp`/`pin` —
  слово целиком, прячется и число; те же правила — у ключей `map` со строковыми ключами
  (`http.Header`, `url.Values`, `map[string]any`, вложенные; копия — только при секрете). Поля
  структур не разбираются (путь — `LogValuer`-группа). Граф без `RedactConfig` не маскирует
  (`Module` ENV не читает). Цена без маркеров — около ×2 (потолок теста ×3). Остальные пропуски
  (`pass`, `sid`, `code`, base64url, DSN) — в бэклоге.
- S6: `NormalizeKey` — `ToLower(TrimSpace)` до проверок пустоты и длины; фейки кроме `Counting`
  ключ только проверяют. Строки со старым ключом новым не находятся — блокировка теряется не
  дольше `LockDuration`, данные не мигрируем (эфемерные счётчики).
- S7 и B1: `Guard` — `Attempt` + `RecordSuccess`. Попытка на пороге проходит и ставит
  блокировку, под блокировкой `WHERE` строку не пишет, счётчик выше порога блокируется сразу;
  под блокировкой её читает второй запрос, снятую между ними попытку переучитываем (не больше 3
  раз; эту ветку тест не воспроизводит). `Attempt` в транзакции — `ErrInTx` (pgstore и фейки):
  откат логина снимал учёт; `RecordSuccess` в транзакции допустим (fail-closed). Метрика
  `bruteforce_checks_total` сохранила имя; у `Recorder` — `Attempts()`/`Successes()`.
- `fix(outboxkit)` вне шагов: `EnsurePartitions` падала `42P07` рядом с параллельным создателем
  той же партиции (хук и keeper-job на старте, реплики) — флак `make test-full`, проверки AC7.
  «Уже есть» — не ошибка; регрессия детерминирована открытой транзакцией.
- S8: слоты — канал ёмкостью `max(1, GOMAXPROCS/Threads)`, общие для `Hash`/`Verify`/`VerifyDummy`
  (иначе время ответа различало бы пути); отмена ctx в очереди — ошибка, у `VerifyDummy` —
  пропуск; ожидание входит в гистограммы; шов теста — неэкспортируемое поле `derive`.
- S9 и неблок. 8: `subscribe` получает ctx хука — natskit берёт его только на регистрацию (тест
  проверяет, что это ctx хука); `OnStop` дренит, отмену зависших обработчиков делает `Close`
  модуля. Вызов из чужого `OnStart` fx молча теряет (хук, добавленный во время старта, не
  запускается) — узнать об этом изнутри нельзя, поэтому строка в doc-comment и `usage.md`.
- B2: аннотация — шаг миграции S1, у S3 только фейк Transactor; лазейку `pgkit.WithTestTx` в
  `outboxtest` дёшево не закрыть (один маркер pgkit у фейка и у ручной пометки) — контракт в
  `outboxkit/docs/testing.md`; критерий разделов аннотации — в README § Версионирование.
- Неблок. 7: маркер модуля — неэкспортируемый `timerkit.scheduler`, `fx.Supply` его не подложит;
  сырой тег группы проверку обходит — контракт «только `Provide`» в README timerkit.
- Задеты неиспользуемые шаблоном киты: `testkit/bruteforcetest` (интерфейс `Guard`),
  тесты `hubkit/sync` (требование `timerkit.Module`), при необходимости `oidckit`/`hubkit/sso`
  (контракт ошибок `httpkit`).

## Открытые вопросы
—

## Handoff
- `go-backend-template` при bump: `beforeHTTP` не нужен; client-IP — `httpkit.Middlewares`;
  `MapErr` → `ErrorMap.Map` + `Declare`; `LoginGuard` — `Attempt` до проверки пароля +
  `RecordSuccess`; subscriber — `natskit.SubscribeOnStart`; `NATS_JETSTREAM_ENABLED=true` в
  `.env.example`; `logkit.FxLogger` + `fx.RecoverFromPanics()` в `main`; `openapi.json`
  перегенерировать (схема `APIError`); логи маскируются по умолчанию.
- Шлюзы (`v0.39.0`): цепочка аннотаций тегов до `v1.2.0`.

## Где мы сейчас
- Сделано: план (`bc726e3`), S1 (`a4030bf`, `feat(timerkit)!`) — маркер модуля в графе;
  S2 (`887527a`, `feat(outboxkit)!`) — `RepositoryFactory` стал интерфейсом (`Repository`,
  `Migrations`), `Instance` кладёт `pgkit.Source` таблицы в `pgkit.MigrationsGroup`;
  S3 (`16949a0`, `feat(outboxtest)!`) — `Recorder.Publish` вне `pgkit.InTx` → `outboxkit.ErrNoTx`;
  S4 (`5333bee`, `feat(logkit)`) — `logkit.FxLogger`: события fx через `*slog.Logger` графа;
  S5 (`73900ca`, `feat(logkit)`) — redact по умолчанию, значение под ключом-секретом, hex ≥32;
  S6 (`2d0aeaf`, `feat(bruteforcekit)`) — subject в нижнем регистре без пробелов, IPv6 /64;
  S7 (`2b8e39a`, `feat(bruteforcekit)!`) — `Guard.Attempt` до проверки секрета + `RecordSuccess`;
  `fix(outboxkit)` (`dd6eed6`) — `EnsurePartitions` рядом с параллельным создателем партиции;
  S8 (`a42b333`, `feat(authnkit)`) — Argon2id в слотах `max(1, GOMAXPROCS/Threads)`, ожидание по
  ctx; S9 (`feat(natskit)`) — `natskit.SubscribeOnStart`. Этап «Инфраструктурные киты» закрыт.
- Осталось: S10–S15 по «Шагам», дизайн каждого — в «Решениях». Тега нет; ветка не запушена.
- Валидатор этапа S1–S9 (`M/validation-harden-kits-A.md`) — FAIL: B1, B2 и неблокирующие.
  Фиксы — отдельными `fix(...)`: B1 (`cb32246`, `ErrInTx`), B2 (`e885daa`, аннотация S1/S3) и
  маскирование по ключу (`e1d35da`), маркер timerkit (`f0a8be8`), natskit (`fix(natskit)`)
  сделаны; дальше outboxkit (партиции, тест `Module`+`FactoryModule`); затем перепроверка
  валидатором, потом S10.
- Для аннотации тега копить по шагам:
  - Breaking:
    - timerkit (S1): `timerkit.Provide` и киты с job'ами (outboxkit `Module`/`Instance`,
      `outboxkit/pgstore`, `bruteforcekit/pgstore`, `hubkit/sync`) требуют в графе
      `timerkit.Module` и `logkit.Logger` — добавьте их, в том числе в тестовые графы
      (`fx.ValidateApp`/`fxtest`); без модуля — `missing type: timerkit.scheduler`.
    - outboxkit (S2): `RepositoryFactory` — интерфейс `Repository(table)`/`Migrations(table)`
      вместо func-типа, своя фабрика реализует оба метода; `Instance` сам регистрирует схему
      таблицы — ручной `pgkit.ProvideMigrations(pgstore.TableMigrations(t))` рядом с ним убрать,
      иначе `MigrateModule` падает `duplicate source name`.
    - testkit/outboxtest (S3): `Recorder.Publish` вне транзакции → `outboxkit.ErrNoTx`; usecase
      в тесте — только через фейк Transactor (`pgtest.Identity`/`pgtest.Recorder`);
      `pgkit.WithTestTx` — лишь для прямого вызова `Publish`, не для ctx usecase'а, иначе
      публикация мимо транзакции снова проходит.
    - bruteforcekit (S7): `Guard` — `Attempt` + `RecordSuccess`, `Check`/`RecordFailure`
      удалены: `Attempt` до проверки пароля и вне транзакции (в `Transactor.WithTx` —
      `bruteforcekit.ErrInTx`), `*LockState` → отказ, неудача ничего не требует, успех —
      `RecordSuccess` (можно в транзакции); `LockState.FailedCount` — попытки окна без успеха;
      `bruteforcetest.Recorder` — `Attempts()`/`Successes()`.
  - Поведение:
    - timerkit (S1): повторный `timerkit.Module` — ошибка старта `already provided`.
    - outboxkit (S2): с `pgkit.MigrateModule` таблицы инстансов накатываются на старте сами.
    - outboxkit/pgstore (fix): `EnsurePartitions` не валит старт, когда ту же партицию создаёт
      другой процесс.
    - logkit (S5): `LOG_REDACT_ENABLED` по умолчанию `true`; значение под ключом-секретом
      (token, password, secret, authorization, cookie, …; и по окончанию слова — `accesstoken`),
      в том числе у ключей `map` (`http.Header`, `url.Values`, `map[string]any`), маскируется
      целиком, под `otp`/`pin` — и число, hex-цепочки ≥32 — маской; выключить —
      `LOG_REDACT_ENABLED=false`; граф без `logkit.NewRedactConfig` не маскирует — добавьте его.
    - bruteforcekit (S6): subject — в нижнем регистре без пробелов по краям,
      `BRUTEFORCE_IPV6_PREFIX` по умолчанию `64` (полный адрес — `128`); блокировки строк со
      старым ключом теряются не дольше `BRUTEFORCE_LOCK_DURATION`.
    - authnkit (S8): `Argon2idHasher` считает не больше `max(1, GOMAXPROCS/Threads)` хэшей
      одновременно, лишние ждут; отмена ctx в очереди — ошибка `Hash`/`Verify` с `ctx.Err()`.
  - Изменения:
    - outboxkit (S2): `pgstore.Module` и `FactoryModule` сочетаются в одном графе.
    - logkit (S4): `logkit.FxLogger` — события fx в структурный лог.
    - natskit (S9): `SubscribeOnStart` — подписка в `OnStart`, drain в `OnStop`, ошибка подписки
      (и `ErrJetStreamDisabled`) валит старт.
- Перед коммитом — `make check` (govulncheck однажды завис на сети — перезапуск помог).
