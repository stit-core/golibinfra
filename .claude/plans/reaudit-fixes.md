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
- Версия — `v1.5.0` (§ Решения): две смены поведения требуют правки кода потребителя — по
  `README.md § Версионирование` это `Breaking`, а breaking идёт minor'ом.
- Отчёт валидатора круга — `M/validation-reaudit-fixes-1.md` (блокирующие B1–B3, неблокирующие
  N1–N9; его пробы и прототипы — `M/val-fr1/`); журнал доводки — `M/fixround1/progress-2.md`.
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
- [x] **AC2** — угол 2 (корректность): NUL в ошибке транспорта не стопорит тик outbox, отказ DLQ на
  последней доставке не теряет сообщение. Проверка: пробы RA-C05, RA-C07 в worktree HEAD → PASS
  - Подтверждено: RA-C05, RA-C07 (и RA-C03, RA-C12) на HEAD → PASS (`M/fixround1/logs/zzra-*.log`)
- [x] **AC3** — угол 3 (безопасность): идемпотентность дедуплицирует только запрос с личностью,
  один scope не занимает store, заголовок клиента не снимает дедлайн операции. Проверка: пробы RA-S10, RA-S10b, RA-S10c, RA-S06, RA-S01 в worktree HEAD → PASS
  - Подтверждено: RA-S10, RA-S10b, RA-S10c, RA-S06, RA-S01 на HEAD → PASS
- [x] **AC4** — угол 4 (fx): обработчик шины и job в полёте оставляют бюджет остановки хукам
  после себя, `Stop` — `nil`. Проверка: `M/reaudit/fxobs/ra-run.sh HEAD fx fx-docker` → RX01, RX02 PASS
  - Подтверждено: RX01a, RX01b, RX02 ×4, RT01 на HEAD → PASS (`M/fixround1/logs/fxobs/`)
- [x] **AC5** — угол 5 (тесты): scope идемпотентности по субъекту и `CleanupJob.Run` закреплены
  тестами, контрактные наборы testkit проваливают сломанную реализацию, фейки не расходятся с
  боевыми молча, фикстуры живут под `-count>1`. Проверка: мутанты аудитора hk2-06, sess-11, tk3-01…03 (`M/reaudit/tg/scripts/rmutate.py`) → убиты; `go test -race -count=3 ./tzkit/` → PASS
  - Подтверждено: мутанты аудитора руками — hk2-06 (по смыслу), sess-11, tk3-01…03 убиты (`M/fixround1/logs/mutants-*.log`); `go test -race -count=3 ./tzkit/` → ok; зонд фейков на HEAD — DIVERGE 23, все названы в доках (`M/fixround1/logs/fakes2-probe.log`)
- [x] **AC6** — угол 6 (observability): занятый пул не стопорит экспорт метрик, выключенный SDK не
  меняет решение о сэмплировании, текст ошибки в спане — под масками, рукопожатие Redis — без
  `result=error`. Проверка: `M/reaudit/fxobs/ra-run.sh HEAD tel tel-docker obs-unit obs` → RT02, RT02b, RT05, RO2 PASS, RO3 — маски
  - Подтверждено: RT02, RT02b, RT05, RO2 на HEAD → PASS; RO3 — в статусе и событии спана `[REDACTED]`; сверка таблиц доков с прогоном — 0 расхождений
- [x] **AC7** — угол 7 (доки): графы `fx.New` доков, которые не собирались как написаны,
  собираются и закреплены тестом. Проверка: `go test -short -run 'TestDocGraphs' .` → PASS
  - Подтверждено: `TestDocGraphs` (4 графа: блок доки — тело функции теста байт в байт, `fx.ValidateApp`) → PASS; остальные 22 графа доков аудитор собрал на `v1.4.0`, правки круга их не трогали, кроме трёх быстрых стартов с `logkit.Module` (RDOC-02)
- [x] **AC8** — угол 8 (гейт): ни одна из 34 форм запуска аудитора не даёт `✓ check clean` на
  красном дереве — и через pre-push; envdoc видит анонимную структуру с тегом, `//nolint`
  невключённого линтера — отказ, проводку guard держит тест. Проверка: `go test ./scripts/...` → PASS; `M/reaudit/tg/scripts/gate_forge.sh` на HEAD → честных 34 из 34
  - Подтверждено: `gate_forge.sh` на HEAD — 34 формы из 34 честные (`M/fixround1/logs/forge/`); инъекции RG31, RG54, RG55 пойманы (`M/fixround1/logs/inject/`); `go test ./scripts/...` → PASS
- [ ] **AC10** — блокирующие валидатора закрыты: недоверенный trace-context не идёт дальше OTel
  при любом провайдере трасс, окружение оболочки не подделывает вердикт гейта и не обходит
  pre-push, сообщение, роняющее процесс, с DLQ останавливается на `MaxDeliver`. Проверка: пробы валидатора `otelearly`, `oteluntrusted`, `prepush`, `TestV_DLQ_1` (`M/val-fr1/probes/`) в worktree HEAD → PASS
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
  - Сделано: `truncErr` и `pgstore.RecordFailures` убирают NUL и битые руны (`TestWorker_BinaryErrorTextDoesNotStallTheTick`, до правки — откат тика `22021`); с DLQ предела доставок у консьюмера на сервере нет (`TestStreamBus_DLQFailureOnLastDeliveryIsRetried`, до правки копия в оживший DLQ не доходила)
- [x] **S9** — logkit, cachekit: маски ключей на оборванном JSON, рукопожатие go-redis без
  `result=error` (RSEC-02, ROBS-04)
  - Проверка: `go test -race ./logkit/ ./cachekit/` → PASS · Закрывает: AC3, AC6
  - Сделано: `maskJSONKeys` маскирует до места обрыва, `[]byte` — тем же сканером, что строка (`TestRedact_TruncatedJSONStaysMasked`, до правки 26 подтестов красные); `MaintNotificationsConfig{Mode: disabled}` у обоих клиентов (`TestRedisCache_HandshakeHasNoErrorSignals`, до правки `cmd=client,result=error` = 2)
- [x] **S10** — остальные minor четырёх отчётов: правка в несколько строк и без риска — здесь,
  остальное — строкой в бэклог с ID находки и причиной; FX-12 и уточнение RAPI-01 — там же
  - Проверка: `make test-full` → PASS; `make plan-check` → OK · Закрывает: AC2, AC3, AC6
  - Сделано: RCOR-02 (отзыв при повторе токена — мимо отмены ctx), RCOR-06 (одновременный старт с `CREATE_BUCKET`), ROBS-06 (subject сообщения в логе Core-обработчика), RAPI-05 (sentinel у `lockTimeout`) — с тестами, до правок красными (`M/fixround1/logs/red-s10-minors.log`); строки доков — RSEC-06, RSEC-09, RCOR-04; в бэклог — FX-12, RAPI-01, RAPI-05 (остаток), RSEC-03, RSEC-04, RSEC-07, RSEC-08, RCOR-04 (код), RFX-03, ROBS-05, ROBS-07, ROBS-08 и два остатка своих решений
- [x] **S11** — гейт: самозащита `make check` без условия на `MAKECMDGOALS`, pre-push с окружением
  без make-переменных, 34 формы аудитора — в тесте; envdoc и анонимная структура, `//nolint`
  невключённых линтеров, проводка guard в `settings.json` (RGATE-01, RGATE-02, RGATE-03, RGATE-05)
  - Проверка: `go test ./scripts/...` → PASS; `M/reaudit/tg/scripts/gate_forge.sh` → зелёного вердикта на красном дереве нет · Закрывает: AC8
  - Сделано: `MAKECMDGOALS` и `MAKEFILE_LIST` от вызывающего — отказ make для любой цели, отказы `check` и `gate-clean` стоят ещё и первой строкой рецепта; pre-push снимает из окружения все `MAKE*`; `TestMakeCheckCannotBeForged` держит 34 формы аудитора и алиас цели (F09 на прежнем Makefile — `✓ check clean`), `TestPrePushGatesPushedCommit` — окружение пушащего; envdoc читает теги анонимных структур; 30 директив `//nolint` невключённых линтеров сняты, `scripts/lint.sh` такие отвергает; `TestSettingsWireTheGuard`
- [x] **S15** — тесты: scope идемпотентности по субъекту на `Module`, `CleanupJob.Run` исполняется,
  негативные самотесты контрактных наборов testkit, фикстуры testkit под `-count>1`
  (RTST-01, RTST-02, RTST-04, RTST-05)
  - Проверка: `go test -race -count=3 ./tzkit/` → PASS; мутанты hk2-06, sess-11, tk3-01…03 (`M/reaudit/tg/scripts/rmutate.py`) → убиты · Закрывает: AC5
  - Сделано: `TestModule_IdempotencyAfterAuth` — другой субъект с тем же ключом; очистку сессий в тесте зовёт `CleanupJob.Run`; `TestRunContract_Broken…Fails` у четырёх наборов — сломанная реализация проваливает свой кейс в дочернем процессе; логгер фикстур молчит после конца теста. Мутанты руками: hk2-06 (по смыслу), sess-11, tk3-01…03 — убиты (`M/fixround1/logs/mutants-*.log`); `go test -race -count=3 ./tzkit/` → ok
- [x] **S16** — natskit и фейки testkit: один durable с двумя фильтрами — отказ, а не молчаливая
  потеря сообщений; расхождения фейков с боевыми выровнены или названы в доках (RTST-03)
  - Проверка: `go test -race ./natskit/ ./testkit/...` → PASS; зонд `M/reaudit/tg/probes/fakes2_probe_test.go` → DIVERGE только названные в доках · Закрывает: AC5
  - Сделано: второй `SubscribeStream` с тем же durable и другим фильтром при живой подписке — `natskit.ErrConfig` у шины и у `natstest` (`TestStreamBus_SecondFilterOnOneDurableIsRejected`, до правки — `nil` и потеря сообщений первого фильтра); фейки: panic обработчика — ошибка обработчика, stream-публикация читает ctx, `Counting.RecordSuccess` откатывается с транзакцией `pgtest`; остальные расхождения названы в `testkit/README.md` и `cachekit/README.md`

### Доки
- [x] **S12** — графы `fx.New` доков собираются и закреплены тестом; логгер в графе — `logkit.Module`;
  префикс ошибок auditkit, скоуп authnkit в SECURITY.md, рецепт входа под блокировкой строки
  пользователя, сверка имён Breaking в `/release` (RDOC-01…05, RCOR-01)
  - Проверка: `go test -short -run 'TestDocGraphs|TestDocs' .` → PASS · Закрывает: AC7
  - Сделано: четыре графа (httpkit и config — быстрые старты, пробы pgkit, инстансы outbox) дополнены недостающими провайдерами и стали телами функций `doc_graphs_test.go` — блок доки байт в байт, граф — `fx.ValidateApp` (`TestDocGraphs`); три README кладут логгер `logkit.Module`; RDOC-03…05 и рецепт входа — правки текста

### Доводка по валидации
- [x] **S17** — telemetry, httpkit: no-op-провайдер трасс — первым и единственным; trace-context
  клиента без доверия не идёт дальше OTel при любом провайдере (B1: регрессия KSEC-12, ROBS-02)
  - Проверка: `go test -short -race ./telemetry/ ./httpkit/` → PASS · Закрывает: AC10, AC6
  - Сделано: `Init` при `Disabled` ставит процессу только no-op трасс (`TestInitDisabled_TracerCreatedBeforeInitIsNoop`, свой процесс: до правки ранний tracer слал вниз `…-00` и свой корень); `httpkit.OTel` без доверия снимает с ctx remote-контекст (`TestServer_ClientTraceContextMatrix`, 8 клеток: до правки клетка «без доверия, no-op, входящий» — trace_id клиента в логе, его `traceparent` и `tracestate` внизу)
- [x] **S18** — natskit: счётчик доставок сверяется до вызова обработчика — сверх `MaxDeliver` с
  DLQ сообщение уходит в DLQ и `Term` без обработчика; фейк `natstest` — так же (B3)
  - Проверка: `go test -race ./natskit/ ./testkit/natstest/` → PASS · Закрывает: AC10, AC2
  - Сделано: `jsBus.run` — доставку сверх `MaxDeliver` обработчик не видит, её исход — `errPastMaxDeliver`, дальше DLQ и `Term` (отказ DLQ — `Nak`); `TestStreamBus_UnansweredMessageDiesAtMaxDeliver` (обработчик не отвечает: до правки — без предела, DLQ пуст) и `TestStreamBus_DLQFailureOnLastDeliveryIsRetried` (вызовов обработчика ровно `MaxDeliver`: до правки — 7 при пределе 2); `go test -race ./natskit/ ./testkit/natstest/` → PASS
- [x] **S19** — гейт: шаги-скрипты и хуки не читают окружение оболочки (`BASH_ENV`,
  экспортированные функции); формы — в тестах гейта; шапка guard и README — по факту (B2)
  - Проверка: `go test ./scripts/...` → PASS · Закрывает: AC10, AC8
  - Сделано: хуки и `scripts/*.sh` стартуют `#!/bin/bash -p`, Makefile снимает с окружения шагов `BASH_ENV`, `SHELLOPTS`, `BASHOPTS` и функции `BASH_FUNC_…`, pre-push снимает их с окружения гейта; `TestGateScriptStepsIgnoreShellEnvironment` (lint и plan-check на красном игрушечном дереве, и с `SHELL=/bin/bash`), формы в `TestPrePushGatesPushedCommit` и `TestCommitMsgHook`, `TestGateScriptsStartPrivileged` — до правки `BASH_ENV`, функция `set` и `SHELLOPTS=noexec` давали зелёный шаг и пропускали push (`M/fixround1/logs2/red-b2-gate.log`); вложенный make теста форм — без рабочего `go`: мутант «снят отказ первой строкой рецепта» краснеет за 3 с
- [x] **S20** — неблокирующие валидатора: бюджет идемпотентности и ротация токена (N2), тесты
  непокрытых веток (N4), тексты (N6, N7), два класса обхода guard из шаблона
  - Проверка: `make test-full` → PASS; `go test ./scripts/...` → PASS · Закрывает: AC3, AC5, AC8
  - Сделано: личность идемпотентности — только субъект (`TestModule_IdempotencyStoreWiring`: до правки запрос схемы без субъекта получал replay по scope токена); девять выживших мутантов — семь убиты новыми тестами (`…FirstBacklogSnapshotWaitsForMigrations`, `TestBacklogSnapshots_DeadlineAndInterruptedLoad`, `TestWorker_FailureTextIsCleanForAnyRepository`, `TestModule_StreamInFlightLeavesStopBudget`, спан на каждого обработчика, сломанный `Release` в самотесте набора, бюджет во втором окне), два ушли вместе с кодом отпечатка credentials; рецепт входа authnkit и `ctx` у `BacklogReporter` — в доках; guard отклоняет код для оболочки-интерпретатора (и с опциями перед `-c`, и после `|`) и алиасы с `include` конфига git (и значением в кавычках) — 76 новых случаев в `guard-cases.txt`, до правки все формы проходили

### Релиз
- [x] **S13** — пробы и группы экспериментов аудиторов на HEAD чисты; бэклог дополнен (FX-12,
  RAPI-01, отложенные minor)
  - Проверка: `ra-run.sh HEAD`, пробы `zzra` в worktree → FAIL только у отложенных в бэклог · Закрывает: AC2, AC3, AC4, AC6 · Зависит от: S1, S2, S3, S4, S5, S6, S7, S8, S9, S10, S11, S12, S15, S16
  - Сделано: эксперименты fx и observability (RX01, RX02 ×4, RX15, RT01, RT02, RT02b, RT05, RO2 — PASS, RO3 — секретов в спане нет; RO4 — ROBS-05 в бэклоге), базовые группы `correctness-security`, `httpkit-a`, `fx-observability`, `httpkit-b exp` — чисты (KX64 — FX-12, красный по построению; H37 и KX69 поправлены под новые правила в копиях `M/fixround1/`), контракты сигналов пересобраны генераторами — 0 расхождений; пробы `zzra`: C03, C05, C07, C12, S01, S06, S10, S10b, S10c — PASS, FAIL — только отложенные (C01, C06, S04, S05, S05b, S08, S13, S18, S19); 34 формы `gate_forge.sh` — честные; мутанты hk2-06, sess-11, tk3-01…03 — убиты; RCOR-05 доведён (проба C07 показала, что запас 2× теряет сообщение)
- [ ] **S14** — релиз `v1.5.0`: `make tools`, `make test-full`, `make gate-clean`, `make
  release-diff PREV=v1.4.0`, пробы на HEAD, шаблон через `replace`, аннотация и Handoff в `M/`,
  тег локально
  - Проверка: `make gate-clean` → `✓ check clean (./...)` · Закрывает: AC9 · Зависит от: S13, S17, S18, S19, S20

## Решения
- Решение владельца (по ходу круга): цель — девятка по каждому углу, запасной исход — 8.
  Приоритет: medium и пограничные; затем всё, из-за чего предписанные эксперименты не чисты
  (графы доков, расхождения фейков, фикстуры под `-count>1`, непойманные инъекции, негативные
  самотесты наборов); затем дешёвые minor — пока правка в несколько строк и без риска;
  остальное — строками в бэклог с ID находки и причиной.
- Личность запроса для идемпотентности — только субъект (`ContextWithSubject` в extractor'е
  схемы); запасного scope по credentials нет. Scope по токену менялся с его ротацией: бюджет
  ключей держал учётную запись лишь у extractor'а с субъектом, и десять токенов заполняли
  `MemoryStore` (N2 валидатора). Один способ вместо двух и минус разбор места схемы в OpenAPI.
  Цена — Breaking: запрос, чей extractor субъекта не кладёт, не дедуплицируется (строка
  аннотации со стрелкой); `oidckit` субъекта кладёт, `hubkit/sso` клал и раньше.
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
  несёт входящий контекст как есть и не порождает корень с `sampled=0`. Он ставится первым и
  единственным — делегат OTel привязывает ранние tracer'ы к первому провайдеру процесса; поле
  `Providers.TracerProvider` остаётся SDK-провайдером без экспорта (тип и «не nil» — контракт
  `Init`). Цена: `trace_id` в логах выключенного сервиса есть только у работы с доверенным
  входящим контекстом — строка «Поведения» и Handoff шаблону.
- Недоверие к trace-context клиента держит `httpkit.OTel`, а не провайдер трасс: remote-контекст
  после otelhttp снимается с ctx. `WithNewRoot` исполняет только SDK-tracer — no-op и
  провайдер OTel по умолчанию несли бы контекст клиента в лог и вниз. Отдельной ветки «SDK
  выключен» в ките нет: признак — remote-контекст в ctx, а не тип провайдера.
- Маска текста для спана — метод `MaskText` конкретных логгеров logkit, который кит находит
  утверждением интерфейса в `internal/spanerr`: правила масок графа (свой набор, выключатель)
  есть только у логгера, а экспорт дал бы второй способ спросить их при том же утверждении
  внутри (N8 валидатора); обёртка потребителя над логгером получает дефолтные маски. Под
  масками — только текст потребителя (обработчик шины, job); ошибки драйверов и самого кита
  (NATS, Redis, транспорт httpclient) идут как есть.
- С DLQ серверного предела доставок нет: запас «вдвое выше клиентского» проба аудитора RA-C07
  исчерпывает за секунду — любой конечный запас теряет сообщение при простое DLQ длиннее него.
  Предел держит клиент до вызова обработчика (`run`): доставка сверх `MaxDeliver` — DLQ и
  `Term` мимо него. Сообщение, роняющее процесс, останавливается на `MaxDeliver` падениях и
  попадает в DLQ; обработчик не зовётся сверх предела и при лежащем DLQ. Сверка — без условия
  на DLQ: без него серверный предел тот же, ветка лишь уравнивает исход при разном конфиге
  реплик. Цена: у копии, записанной сверх предела, `Natskit-Dlq-Error` — причина кита, ошибка
  обработчика — в записи `dlq publish failed` (поле `cause`). Фейк `natstest` обработчику
  больше `WithMaxDeliver` доставок не давал и до правки — менять его не пришлось.
- Окружение оболочки: по одному механизму на вход. Хуки git запускает их строка интерпретатора
  `bash -p`; make снимает переменные оболочки с окружения шагов (`unexport`) — это закрывает и
  рецепты там, где sh — это bash, и bash, запущенный шагами; pre-push снимает их с окружения
  гейта, который запускает, — вердикт не зависит от Makefile пушимого коммита. `ENV` не
  снимается: неинтерактивные sh и bash его не читают, а имя занято приложениями. Запуск guard
  в `.claude/settings.json` не менялся: его окружение задаёт сессия, а не команда. Подставной
  бинарь в `PATH` — названный остаток (README § Гейт).
- Guard: код для оболочки-интерпретатора и алиасы с `include` конфига git guard golibinfra не
  видел вовсе (стояли в «не видит» README) — классы, найденные в guard шаблона, были их частным
  случаем. Правила перенесены из guard шаблона вместе с его случаями: у двух репо одна модель.
  Цена — отказ на `git config alias.…` и на команде с интерпретатором, где флаг или имя хука
  стоит в тексте (сообщение коммита — heredoc'ом или файлом).
- Плавающий красный `TestBFF_DiscoveryBackoff` (oidckit) на релизном `make test-full` — гонка в
  коде кита, а не в тесте: вызов, опоздавший к уже упавшей попытке discovery, начинал новую.
  Исправлен код (отрицательный кэш сверяется внутри попытки), хотя кит вне объёма аудита:
  красный гейт чинят, а перегон оставил бы флак следующему релизу.
- Вложенный make теста форм гейта идёт без рабочего `go` (отвечает только `go env`): форма,
  прошедшая самозащиту, падает на шагах сразу, а не запускает настоящий гейт с этим же тестом
  внутри — у валидатора такой мутант поднял нагрузку машины до 75.
- Один durable с двумя фильтрами — отказ в пределах одной шины (`ErrConfig` на втором
  `SubscribeStream`, пока жива первая подписка), а не только дока: потеря сообщений молчаливая.
  Между шинами (процессы, два инстанса шины одного процесса) смену фильтра кит не отвергает —
  это и есть штатная выкатка нового фильтра; правило для реплик — в доке. Фейк `natstest`
  держит то же правило.
- Расхождения фейков: выровнены те, где фейк мягче шины на штатном пути теста (panic
  обработчика, ctx stream-публикации, откат `RecordSuccess`); ctx издателя у обработчика
  `natstest`, история стрима и диапазоны типов Postgres названы в доках — выравнивание меняет
  модель фейка (синхронная доставка) или тянет в фейки правила базы.
- Поток, которому не ставится дедлайн запроса, у операции huma определяет её описание (ответ
  `text/event-stream`), у маршрута мимо huma — по-прежнему заголовок запроса: описания у него
  нет, а пометка маршрута — новый экспорт (бэклог `httpkit-raw-route-stream-mark`).
- Бюджет ключей на субъекта — константа кита (10 000 ответов за окно TTL, счёт в памяти
  процесса), сверх него — отказ store этому клиенту: настройка появится с потребителем,
  которому её мало (бэклог `httpkit-idempotency-budget-knob`).
- Версия — `v1.5.0` (решение дирижёра по отчёту валидатора, N1): отказ старта на втором
  фильтре одного durable и идемпотентность только с личностью из `HumaAuth` требуют правки
  кода потребителя — это `Breaking`, а не «Поведение». `release-diff` по API, env и метрикам
  пуст: новых экспортов правки не вводят.

## Handoff
—

## Где мы сейчас
S1–S13, S15–S20 закрыты, AC1–AC8 подтверждены. Блокирующие валидатора круга (B1–B3) и его
неблокирующие закрыты. Остался S14 — релиз `v1.5.0`: `make tools`, `make test-full`, пробы
валидатора и аудиторов на HEAD в своих worktree (AC10), `make release-diff PREV=v1.4.0`, шаблон
через `replace`, аннотация `M/annotation-v1.5.0.txt`, Handoff и патч шаблона, закрытие плана,
`make gate-clean`, тег локально. Точка продолжения — `M/fixround1/progress-2.md`.
