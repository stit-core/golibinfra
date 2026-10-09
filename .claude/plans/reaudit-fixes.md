---
status: in-progress
parent: —
blocked-by: —
---

# reaudit-fixes — находки повторного аудита v1.4.0: каждый угол не ниже девяти

## Цель
Закрыть находки повторного аудита тега `v1.4.0` выше minor и дешёвые minor и выпустить версию,
на которой у каждого из восьми углов выше minor ничего нет, а пробы аудиторов чисты.

## Контекст
- Отчёты аудиторов и их пробы — вне репо, `M` = `/home/mm/.cache/stit-audit-program/`:
  `M/reaudit/golibinfra-correctness-security.md` (пробы `M/reaudit/corsec-probes/zzra/`),
  `golibinfra-api-docs.md` (`M/reaudit/ra-api-docs/`), `golibinfra-fx-observability.md`
  (`M/reaudit/fxobs/ra-run.sh <rev>`), `golibinfra-tests-gate.md`. ID находок — оттуда.
- Журнал хода для продолжателя — `M/fixround1/progress.md`.
- Версия — по `README.md § Версионирование`: правки без смены сигнатур, дефолтов, имён метрик и
  envconfig-тегов — патч `v1.4.1`; смена наблюдаемого поведения — в «Поведении» аннотации.
- Резерв бюджета остановки («две трети остатка») уже держат `httpkit/module.go`,
  `pgkit/module.go` (`stopShare`) и `telemetry/module.go`; маски логов — `logkit/redact.go`.

## Не входит
- Breaking-правки: инверсия bool с `default:"true"` (RAPI-01), переименования RAPI-05 — бэклог.
- Киты вне объёма аудита (hubkit, oidckit, amocrmkit, anthropickit, grpckit, grpcclient).
- Push и bump в шаблоне — после валидатора, у дирижёра.

## Критерии приёмки
- [x] **AC1** — угол 1 (API): nil-логгер в функциях httpkit — `Nop`, тексты `Validate` называют
  переменную, отказ каталога миграций называет каталог. Проверка: `go test -short ./httpkit/ ./cachekit/ ./authnkit/ ./pgkit/` → PASS
  - Подтверждено: `TestNilLoggerIsNop`, `TestKitConfigs_ValidateErrorNamesVariable`, `TestMigrateModule_MissingAppDirNamesTheVariable` → PASS (до правок — красные, логи `M/fixround1/logs/red-s7-*.log`)
- [ ] **AC2** — угол 2 (корректность): NUL в ошибке транспорта не стопорит тик outbox, отказ DLQ на
  последней доставке не теряет сообщение. Проверка: пробы RA-C05, RA-C07 в worktree HEAD → PASS
- [ ] **AC3** — угол 3 (безопасность): идемпотентность дедуплицирует только запрос с личностью,
  один scope не занимает store, заголовок клиента не снимает дедлайн операции. Проверка: пробы RA-S10, RA-S10b, RA-S10c, RA-S06, RA-S01 в worktree HEAD → PASS
- [ ] **AC4** — угол 4 (fx): обработчик шины и job в полёте оставляют бюджет остановки хукам
  после себя, `Stop` — `nil`. Проверка: `M/reaudit/fxobs/ra-run.sh HEAD fx fx-docker` → RX01, RX02 PASS
- [ ] **AC5** — угол 5 (тесты): scope идемпотентности по субъекту и `CleanupJob.Run` закреплены
  тестами, контрактные наборы testkit проваливают сломанную реализацию, фейки не расходятся с
  боевыми молча, фикстуры живут под `-count>1`. Проверка: мутанты аудитора hk2-06, sess-11, tk3-01…03 (`M/reaudit/tg/scripts/rmutate.py`) → убиты; `go test -race -count=3 ./tzkit/` → PASS
- [ ] **AC6** — угол 6 (observability): занятый пул не стопорит экспорт метрик, выключенный SDK не
  меняет решение о сэмплировании, текст ошибки в спане — под масками, рукопожатие Redis — без
  `result=error`. Проверка: `M/reaudit/fxobs/ra-run.sh HEAD tel tel-docker obs-unit obs` → RT02, RT02b, RT05, RO2 PASS, RO3 — маски
- [ ] **AC7** — угол 7 (доки): каждый полный граф `fx.New` в доках объёма собирается как написан и
  закреплён тестом. Проверка: `go test -short -run 'TestDocGraphs' .` → PASS
- [ ] **AC8** — угол 8 (гейт): ни одна из 34 форм запуска аудитора не даёт `✓ check clean` на
  красном дереве — и через pre-push; envdoc видит анонимную структуру с тегом, `//nolint`
  невключённого линтера — отказ, проводку guard держит тест. Проверка: `go test ./scripts/...` → PASS; `M/reaudit/tg/scripts/gate_forge.sh` на HEAD → честных 34 из 34
- [ ] **AC9** — релиз готов: гейт на чистом дереве зелёный, аннотация и Handoff шаблону лежат в
  `M/`, тег стоит локально. Проверка: `make gate-clean` → `✓ check clean (./...)`; `git tag -l 'v1.*' | tail -1`

## Шаги
### Medium
- [x] **S1** — httpkit: идемпотентность только с личностью (субъект или проверенные схемой
  credentials), бюджет ключей на scope, тест «два субъекта — один ключ» (RSEC-01, KSEC-14, RTST-01)
  - Проверка: `go test -short -race ./httpkit/ ./testkit/...` → PASS · Закрывает: AC3, AC5
  - Сделано: scope — субъект или отпечаток credentials по месту схемы (`HumaAuth`), запрос без личности — `skipped` и без записи в store; бюджет 10 000 ответов на scope за окно TTL; `go test -short -race ./httpkit/ ./testkit/...` → PASS
- [x] **S2** — httpkit: поток определяет операция, а не заголовок запроса (RSEC-05, KSEC-15)
  - Проверка: `go test -short -race ./httpkit/` → PASS · Закрывает: AC3
  - Сделано: операции huma без ответа `text/event-stream` дедлайн ставит её хендлер и при `Accept: text/event-stream` или `Upgrade` клиента; `TestModule_RequestDeadline` до правки красный (204 через 600 мс), после — PASS
- [x] **S3** — natskit, timerkit: ожидающие хуки оставляют треть бюджета остановки, ctx
  обработчиков подписки отменяется на исчерпании (RFX-01, RFX-02)
  - Проверка: `go test -race ./natskit/ ./timerkit/` → PASS · Закрывает: AC4
  - Сделано: `stopbudget.Share` — одна формула у HTTP, gRPC, пула, подписки и шины natskit, job'ов timerkit и telemetry; у подписки свой ctx обработчиков; `TestModule_HandlerInFlightLeavesStopBudget` (3 варианта) и `TestModule_StuckJobLeavesStopBudget` до правки красные, после — PASS
- [x] **S4** — outboxkit: снимок backlog'а — вне пути сбора метрик (ROBS-01)
  - Проверка: `go test -race ./outboxkit/...` → PASS · Закрывает: AC6
  - Сделано: первый снимок — при регистрации, следующие заказывает сбор и берёт фоновая горутина под сроком 5 с; callback отдаёт последний; `TestRegisterBacklogGauges_SnapshotIsOutsideCollection` до правки красный (сбор ждал хранилище 3 с), после — PASS; `go test -race ./outboxkit/...` → PASS
- [x] **S5** — telemetry: выключенный SDK не трогает trace-context (ROBS-02)
  - Проверка: `go test -short -race ./telemetry/` → PASS · Закрывает: AC6
  - Сделано: при `Disabled` провайдер трасс процесса — no-op (`trace/noop`); `TestInitDisabled_LeavesTraceContextToNeighbours` до правки красный (`…-00` и новый корень), после — PASS

### На границе medium
- [x] **S6** — logkit, natskit, timerkit: текст ошибки потребителя в спане — под масками логгера (ROBS-03)
  - Проверка: `go test -race ./logkit/ ./natskit/ ./timerkit/ ./internal/spanerr/` → PASS · Закрывает: AC6
  - Сделано: `internal/spanerr.Record` — exception-событие и статус с текстом под масками логгера (`MaskText` у логгеров logkit, чужой логгер — дефолтные маски); `TestConsumerSpan_HandlerErrorTextIsMasked`, `TestSpan_JobErrorTextIsMasked`, `TestRecord_MasksErrorTextLikeTheLogger`; до правки e-mail и Bearer-токен в спане открыты
- [x] **S7** — httpkit, cachekit, authnkit, pgkit: nil-логгер — `Nop`; тексты ошибок `Validate` и
  каталога миграций (RAPI-02, RAPI-03, RAPI-04)
  - Проверка: `go test -short -race ./httpkit/ ./cachekit/ ./authnkit/ ./pgkit/ ./internal/envconf/` → PASS · Закрывает: AC1
  - Сделано: шесть функций httpkit берут nil-логгер как `Nop` (`TestNilLoggerIsNop`, до правки — panic); ошибка `Validate` у cachekit, httpkit, authnkit и amocrmkit называет переменную — держит `TestKitConfigs_ValidateErrorNamesVariable` (до правки 49 значений без имени переменной); отказ каталога миграций называет каталог и `PG_MIGRATIONS_PATH`

### Minor
- [x] **S8** — outboxkit, natskit: NUL в тексте ошибки транспорта, отказ DLQ на последней
  доставке (RCOR-03, RCOR-05)
  - Проверка: `go test -race ./outboxkit/... ./natskit/` → PASS · Закрывает: AC2
  - Сделано: `truncErr` и `pgstore.RecordFailures` убирают NUL и битые руны (`TestWorker_BinaryErrorTextDoesNotStallTheTick`, до правки — откат тика `22021`); с DLQ предел доставок консьюмера на сервере — `2 × MaxDeliver` (`TestStreamBus_DLQFailureOnLastDeliveryIsRetried`, до правки копия в оживший DLQ не доходила)
- [x] **S9** — logkit, cachekit: маски ключей на оборванном JSON, рукопожатие go-redis без
  `result=error` (RSEC-02, ROBS-04)
  - Проверка: `go test -race ./logkit/ ./cachekit/` → PASS · Закрывает: AC3, AC6
  - Сделано: `maskJSONKeys` маскирует до места обрыва, `[]byte` — тем же сканером, что строка (`TestRedact_TruncatedJSONStaysMasked`, до правки 26 подтестов красные); `MaintNotificationsConfig{Mode: disabled}` у обоих клиентов (`TestRedisCache_HandshakeHasNoErrorSignals`, до правки `cmd=client,result=error` = 2)
- [ ] **S10** — остальные дешёвые minor четырёх отчётов: правка в несколько строк — здесь,
  остальное — строкой в бэклог
  - Проверка: `make test-full` → PASS · Закрывает: AC2, AC3, AC6
- [ ] **S11** — гейт: самозащита `make check` без условия на `MAKECMDGOALS`, pre-push с окружением
  без make-переменных, 34 формы аудитора — в тесте; envdoc и анонимная структура, `//nolint`
  невключённых линтеров, ложные отказы guard на имени ветки, проводка guard в `settings.json`
  (RGATE-01…05)
  - Проверка: `go test ./scripts/...` → PASS; `M/reaudit/tg/scripts/gate_forge.sh` → зелёного вердикта на красном дереве нет · Закрывает: AC8
- [ ] **S15** — тесты: scope идемпотентности по субъекту на `Module`, `CleanupJob.Run` исполняется,
  негативные самотесты контрактных наборов testkit, фикстуры testkit под `-count>1`
  (RTST-01, RTST-02, RTST-04, RTST-05)
  - Проверка: `go test -race -count=3 ./tzkit/` → PASS; мутанты hk2-06, sess-11, tk3-01…03 → убиты · Закрывает: AC5
- [ ] **S16** — natskit и фейки testkit: один durable с двумя фильтрами — отказ, а не молчаливая
  потеря сообщений; расхождения фейков с боевыми выровнены или названы в доках (RTST-03)
  - Проверка: `go test -race ./natskit/ ./testkit/...` → PASS; зонд `M/reaudit/tg/probes/fakes2_probe_test.go` → неназванных расхождений нет · Закрывает: AC5

### Доки
- [ ] **S12** — графы `fx.New` доков собираются и закреплены тестом; логгер в графе — `logkit.Module`;
  префикс ошибок auditkit, скоуп authnkit в SECURITY.md, рецепт входа под блокировкой строки
  пользователя, сверка имён Breaking в `/release` (RDOC-01…05, RCOR-01)
  - Проверка: `go test -short -run 'TestDocGraphs|TestDocs' .` → PASS · Закрывает: AC7

### Релиз
- [ ] **S13** — пробы и группы экспериментов аудиторов на HEAD чисты; бэклог дополнен (FX-12,
  RAPI-01, отложенные minor)
  - Проверка: `ra-run.sh HEAD`, пробы `zzra` в worktree → FAIL только у отложенных в бэклог · Закрывает: AC2, AC3, AC4, AC6 · Зависит от: S1, S2, S3, S4, S5, S6, S7, S8, S9, S10, S11, S12, S15, S16
- [ ] **S14** — релиз: `make tools`, `make test-full`, `make gate-clean`, `make release-diff
  PREV=v1.4.0`, аннотация и Handoff шаблону в `M/`, тег локально
  - Проверка: `make gate-clean` → `✓ check clean (./...)` · Закрывает: AC9 · Зависит от: S13

## Решения
- Личность запроса для идемпотентности — субъект или credentials, проверенные схемой прошедшей
  альтернативы (значение по месту схемы в OpenAPI), а не заголовки `Authorization` и `Cookie`
  целиком: заголовок без проверки и служебная cookie рядом с проверенной клиента не различают.
- `OnStop` timerkit и drain подписки natskit на исчерпании своей доли бюджета возвращают
  `nil` и пишут WARN (как пул pgkit): ошибка остановки дала бы код выхода 1 сервису, у
  которого один job или обработчик не свернулся; прежняя ошибка `jobs still running` снята.
- Формула «две трети остатка» вынесена в `internal/stopbudget` и поставлена всем ждущим
  хукам, включая gRPC (кит вне объёма, правка в две строки): три копии формулы и были причиной,
  по которой правка KFX-08 не дошла до natskit и timerkit.
- Снимок backlog'а outbox обновляется по сигналу сбора, а не своим тикером: без
  `MeterProvider` callback не зовётся, и тикер слал бы запросы в БД ни для кого. Цена — значение
  gauge'ей отстаёт на интервал экспорта; первый снимок берётся синхронно при регистрации, чтобы
  серии были сразу после старта. После сбоя снимка серий нет (как до правки), устаревший
  снимок за «очередь пуста» не выдаётся.
- Выключенный SDK ставит процессу no-op-провайдер трасс, а не sampler `ParentBased`: no-op
  несёт входящий контекст как есть и не порождает корень с `sampled=0`. Поле
  `Providers.TracerProvider` остаётся SDK-провайдером без экспорта (тип и «не nil» — контракт
  `Init`), в глобальные он не ставится. Цена: `trace_id` в логах выключенного сервиса есть
  только у запросов с входящим контекстом — строка «Поведения» и Handoff шаблону.
- Маска текста для спана — метод `MaskText` конкретных логгеров logkit, который кит находит
  утверждением интерфейса в `internal/spanerr`, а не новый экспорт logkit: правила масок графа
  (свой набор, выключатель) есть только у логгера, а экспортируемая функция сделала бы релиз
  minor'ом. Под масками — только текст потребителя (обработчик шины, job); ошибки драйверов и
  самого кита (NATS, Redis, транспорт httpclient) идут как есть.
- С DLQ серверный предел доставок — вдвое выше клиентского, а не снят: без предела сообщение,
  которое роняет процесс до `Term`, переотправлялось бы вечно; запас в `MaxDeliver` доставок
  даёт DLQ время ожить, а его исчерпание лог называет прямо.
- Поток, которому не ставится дедлайн запроса, у операции huma определяет её описание (ответ
  `text/event-stream`), у маршрута мимо huma — по-прежнему заголовок запроса: описания у него
  нет, а пометка маршрута — новый экспорт (бэклог `httpkit-raw-route-stream-mark`).
- Бюджет ключей на scope — константа кита (10 000 ответов за окно TTL, счёт в памяти процесса),
  сверх него — отказ store этому scope: настройка появится с потребителем, которому её мало
  (бэклог `httpkit-idempotency-budget-knob`); новая переменная сделала бы релиз minor'ом.
- Версия — патч `v1.4.1`: новых env, экспортируемых символов и имён метрик правки не вводят —
  предел ключей на scope и срок снимка backlog'а — константы кита, маска текста для спана —
  метод неэкспортируемого типа логгера.

## Handoff
—

## Где мы сейчас
S1–S9 закрыты. Дирижёр переслал окончательный отчёт аудитора тестов и гейта: RGATE-01 и RTST-01
— medium, добавлены RTST-03…05 и RGATE-04, RGATE-05 — шаги S11 (гейт), S15 (тесты), S16 (natskit и
фейки). Следующий шаг — S11.
