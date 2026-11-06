---
status: in-progress
parent: —
blocked-by: —
---

# port-fork-kit-fixes — общие правки китов из форка проекта и выпуск v1.7.0

## Цель
Форк golibinfra в проекте на шаблонах stit нашёл на живых сервисах правки, общие для всех потребителей: общий режим
testkit (серверы прогона вместо контейнера на вызов — основа быстрого `make mutate` шаблона бэкенда), образ MinIO
testkit (`minio/minio` на Docker Hub больше нет), уязвимость `klauspost/compress` и ожидание потока автора в natskit.
Перенести их без проектного и выпустить `v1.7.0`: с него `go-backend-template` берёт общий режим для раннера мутаций.

## Контекст
- База форка — `f4c4b8b` этого репо; правки — четыре коммита форка, перенесены патчами с заменой module path:
  natskit (ожидание потока), testkit (общий режим), testkit (образ MinIO), deps (compress).
- `minio/minio:RELEASE.2025-01-20T14-49-07Z` на Docker Hub нет (`docker manifest inspect` — отказ): `StartMinIO` на
  машине без образа в кеше не стартует. Замена — `pgsty/silo` (форк MinIO с теми же S3 API, `MINIO_*` и пробами).
- GO-2026-6865 в `github.com/klauspost/compress` v1.20.0 (путь — `httpclient`) красит шаг `vuln` гейта.
- Потребитель общего режима — раннер `make mutate` шаблона `go-backend-template`: поднимает серверы один раз на
  прогон и отдаёт их файл тестам в `TESTKIT_SHARED`.

## Не входит
- passkit и `testkit/passtest` форка — пропуск доступа проекта: роли и каналы его сотрудников; в stit эту роль
  играет hubkit.
- Раннер, который поднимает серверы общего режима, — план `go-backend-template`.

## Критерии приёмки
- [x] **AC1** — С `TESTKIT_SHARED` у теста своя база Postgres (клон шаблона миграций), NATS с JetStream, Redis и MinIO
  — в исключительной аренде и чистые; другой образ и NATS без JetStream — свой контейнер, без переменной —
  контейнер на вызов. Проверка: `go test -race -run 'TestShared|TestLease' ./testkit/` → PASS
  - Подтверждено: `TestShared_Postgres`, `…ClonesInParallel`, `…TemplateInParallel`, `…DropsOpenDatabase`,
    `…BrokenMigration`, `TestShared_NATS`, `…Redis`, `…MinIO`, `…File`, `TestLease` — PASS (-race, Docker)
- [x] **AC2** — `StartMinIO` поднимает образ, который есть в registry. Проверка: `docker manifest inspect
  pgsty/silo:RELEASE.2026-10-26T00-00-00Z` → манифест; `go test -run 'TestStartMinIO|TestShared_MinIO' ./testkit/` → PASS
  - Подтверждено: `docker manifest inspect` Silo — манифест (`minio/minio` — отказ); `TestStartMinIO`, `TestShared_MinIO` — PASS
- [x] **AC3** — `SubscribeStream` на поток, которого нет, отдаёт handle сразу и встаёт, когда автор заведёт поток;
  ожидание видно по WARN и `nats_subscriptions_waiting{stream}`, `SubscribeOnStart` не валит старт. Проверка:
  `go test -race -run TestStreamWait ./natskit/` → PASS
  - Подтверждено: `TestStreamWait_SubscribesWhenStreamAppears`, `…StopWhileWaiting`, `…RetriesOtherErrors`, `…Reminds`,
    `…SubscribeOnStartDoesNotFailStart` — PASS (-race, Docker)
- [x] **AC4** — Уязвимостей нет. Проверка: шаг `vuln` `make check` → No vulnerabilities found
  - Подтверждено: шаг `vuln` `make check` — No vulnerabilities found
- [ ] **AC5** — Выпуск `v1.7.0`: аннотация по `make release-diff PREV=v1.6.0` и `git log v1.6.0..`, тег в origin и
  github. Проверка: `git ls-remote origin refs/tags/v1.7.0` и `git ls-remote github refs/tags/v1.7.0` → тег

## Шаги
- [x] **S1** — `klauspost/compress` 1.20.2
  - Проверка: шаг `vuln` → чисто · Закрывает: AC4
  - Сделано: `klauspost/compress` v1.20.0 → v1.20.2 (indirect), новых модулей нет; шаг `vuln` — No vulnerabilities found
- [x] **S2** — natskit: подписка на поток, которого нет, ждёт его автора
  - Проверка: `go test -race ./natskit/` → PASS · Закрывает: AC3
  - Сделано: `natskit/streamwait.go`, `TestStreamWait_*`; доки natskit, строки README и testkit/README; `natskit` — PASS (-race)
- [x] **S3** — testkit: общий режим `TESTKIT_SHARED`
  - Проверка: `go test -race -run 'TestShared|TestLease' ./testkit/` → PASS · Закрывает: AC1
  - Сделано: `testkit/shared.go`, аренда `shared_unix.go`, `TestShared_*` и `TestLease`; README кита § Общий режим, строка
    корневого README; `make envdoc-check` — OK
- [x] **S4** — testkit: образ MinIO — `pgsty/silo`
  - Проверка: `go test -run 'TestStartMinIO|TestShared_MinIO' ./testkit/` → PASS · Закрывает: AC2 · Зависит от: S3
  - Сделано: `MinIOImage` — `pgsty/silo:RELEASE.2026-10-26T00-00-00Z`, таблица образов и пример JSON общего режима в README
- [ ] **S5** — Трения проекта для китов — строками бэклога; гейт и выпуск `v1.7.0` (`/release`)
  - Проверка: `make check` → `✓ check clean (./...)`; тег в origin и github · Закрывает: AC5 · Зависит от: S1–S4

## Handoff
- Потребителю: `go get git.stit.tech/stit-core/golibinfra@v1.7.0` + `go mod tidy`; testkit тянет в граф тестов
  `nats.go` и `minio-go` (их держит модуль). Тест, который ждал ошибку `SubscribeStream` на поток, которого нет, —
  на ожидание (WARN, `nats_subscriptions_waiting`).

## Где мы сейчас
S1–S4 закрыты, `make check` на итоговом дереве — `✓ check clean (./...)`; дальше — S5: бэклог, закрытие плана, тег `v1.7.0`.
