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
- [ ] **AC1** — угол 1 (API): nil-логгер в функциях httpkit — `Nop`, тексты `Validate` называют
  переменную, отказ каталога миграций называет каталог. Проверка: `go test -short ./httpkit/ ./cachekit/ ./authnkit/ ./pgkit/` → PASS
- [ ] **AC2** — угол 2 (корректность): NUL в ошибке транспорта не стопорит тик outbox, отказ DLQ на
  последней доставке не теряет сообщение. Проверка: пробы RA-C05, RA-C07 в worktree HEAD → PASS
- [ ] **AC3** — угол 3 (безопасность): идемпотентность дедуплицирует только запрос с личностью,
  один scope не занимает store, заголовок клиента не снимает дедлайн операции. Проверка: пробы RA-S10, RA-S10b, RA-S10c, RA-S06, RA-S01 в worktree HEAD → PASS
- [ ] **AC4** — угол 4 (fx): обработчик шины и job в полёте оставляют бюджет остановки хукам
  после себя, `Stop` — `nil`. Проверка: `M/reaudit/fxobs/ra-run.sh HEAD fx fx-docker` → RX01, RX02 PASS
- [ ] **AC5** — угол 5 (тесты): scope идемпотентности по субъекту и `CleanupJob.Run` закреплены
  тестами. Проверка: мутанты hk2-06 и sess-11 руками → тесты красные
- [ ] **AC6** — угол 6 (observability): занятый пул не стопорит экспорт метрик, выключенный SDK не
  меняет решение о сэмплировании, текст ошибки в спане — под масками, рукопожатие Redis — без
  `result=error`. Проверка: `M/reaudit/fxobs/ra-run.sh HEAD tel tel-docker obs-unit obs` → RT02, RT02b, RT05, RO2 PASS, RO3 — маски
- [ ] **AC7** — угол 7 (доки): каждый полный граф `fx.New` в доках объёма собирается как написан и
  закреплён тестом. Проверка: `go test -short -run 'TestDocGraphs' .` → PASS
- [ ] **AC8** — угол 8 (гейт): `make -i check MAKECMDGOALS=x` не запускается, envdoc видит
  анонимную структуру с тегом, `//nolint` невключённого линтера — отказ. Проверка: `go test ./scripts/...` → PASS
- [ ] **AC9** — релиз готов: гейт на чистом дереве зелёный, аннотация и Handoff шаблону лежат в
  `M/`, тег стоит локально. Проверка: `make gate-clean` → `✓ check clean (./...)`; `git tag -l 'v1.*' | tail -1`

## Шаги
### Medium
- [ ] **S1** — httpkit: идемпотентность только с личностью (субъект или проверенные схемой
  credentials), бюджет ключей на scope, тест «два субъекта — один ключ» (RSEC-01, KSEC-14, RTST-01)
  - Проверка: `go test -short -race ./httpkit/ ./testkit/...` → PASS · Закрывает: AC3, AC5
- [ ] **S2** — httpkit: поток определяет операция, а не заголовок запроса (RSEC-05, KSEC-15)
  - Проверка: `go test -short -race ./httpkit/` → PASS · Закрывает: AC3
- [ ] **S3** — natskit, timerkit: ожидающие хуки оставляют треть бюджета остановки, ctx
  обработчиков подписки отменяется на исчерпании (RFX-01, RFX-02)
  - Проверка: `go test -race ./natskit/ ./timerkit/` → PASS · Закрывает: AC4
- [ ] **S4** — outboxkit: снимок backlog'а — вне пути сбора метрик (ROBS-01)
  - Проверка: `go test -race ./outboxkit/...` → PASS · Закрывает: AC6
- [ ] **S5** — telemetry: выключенный SDK не трогает trace-context (ROBS-02)
  - Проверка: `go test -short -race ./telemetry/` → PASS · Закрывает: AC6

### На границе medium
- [ ] **S6** — logkit, natskit, timerkit: текст ошибки потребителя в спане — под масками логгера (ROBS-03)
  - Проверка: `go test -short -race ./logkit/ ./natskit/ ./timerkit/ ./internal/otelkit/` → PASS · Закрывает: AC6
- [ ] **S7** — httpkit, cachekit, authnkit, pgkit: nil-логгер — `Nop`; тексты ошибок `Validate` и
  каталога миграций (RAPI-02, RAPI-03, RAPI-04)
  - Проверка: `go test -short -race ./httpkit/ ./cachekit/ ./authnkit/ ./pgkit/` → PASS · Закрывает: AC1

### Minor
- [ ] **S8** — outboxkit, natskit: NUL в тексте ошибки транспорта, отказ DLQ на последней
  доставке (RCOR-03, RCOR-05)
  - Проверка: `go test -race ./outboxkit/... ./natskit/` → PASS · Закрывает: AC2
- [ ] **S9** — logkit, cachekit: маски ключей на оборванном JSON, рукопожатие go-redis без
  `result=error` (RSEC-02, ROBS-04)
  - Проверка: `go test -race ./logkit/ ./cachekit/` → PASS · Закрывает: AC3, AC6
- [ ] **S10** — остальные дешёвые minor четырёх отчётов: правка в несколько строк — здесь,
  остальное — строкой в бэклог
  - Проверка: `make test-full` → PASS · Закрывает: AC2, AC3, AC6
- [ ] **S11** — тесты и гейт: `CleanupJob.Run` исполняется тестом, самозащита `make check` без
  `MAKECMDGOALS`, envdoc и анонимная структура, `//nolint` невключённых линтеров (RTST-02, RGATE-01…03)
  - Проверка: `go test ./scripts/... ./authnkit/session/pgstore/` → PASS · Закрывает: AC5, AC8

### Доки
- [ ] **S12** — графы `fx.New` доков собираются и закреплены тестом; логгер в графе — `logkit.Module`;
  префикс ошибок auditkit, скоуп authnkit в SECURITY.md, рецепт входа под блокировкой строки
  пользователя, сверка имён Breaking в `/release` (RDOC-01…05, RCOR-01)
  - Проверка: `go test -short -run 'TestDocGraphs|TestDocs' .` → PASS · Закрывает: AC7

### Релиз
- [ ] **S13** — пробы и группы экспериментов аудиторов на HEAD чисты; бэклог дополнен (FX-12,
  RAPI-01, отложенные minor)
  - Проверка: `ra-run.sh HEAD`, пробы `zzra` в worktree → FAIL только у отложенных в бэклог · Закрывает: AC2, AC3, AC4, AC6 · Зависит от: S1, S2, S3, S4, S5, S6, S7, S8, S9, S10, S11, S12
- [ ] **S14** — релиз: `make tools`, `make test-full`, `make gate-clean`, `make release-diff
  PREV=v1.4.0`, аннотация и Handoff шаблону в `M/`, тег локально
  - Проверка: `make gate-clean` → `✓ check clean (./...)` · Закрывает: AC9 · Зависит от: S13

## Решения
- Версия — патч `v1.4.1`: новых env, экспортируемых символов и имён метрик правки не вводят —
  предел ключей на scope и срок снимка backlog'а — константы кита, маска текста для спана —
  метод неэкспортируемого типа логгера.

## Handoff
—

## Где мы сейчас
План заведён; код не тронут. Следующий шаг — S1.
