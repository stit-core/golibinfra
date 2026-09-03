# Roadmap

Единственный артефакт планирования — этот файл. Подпапок, `<slug>.md`, архивов и
changelog'ов нет: они расходятся с правдой и мешают.

**Planning-first.** Изменение идёт через план — `### <slug>`-секцию под `## Active` ниже.
Прямая правка без плана допустима только для truly-atomic (опечатка, один очевидный файл) и
багфиксов (RED-first). Отклонение от плана — осознанное, проговаривается с юзером.

**Жизненный цикл.** `planning → in-progress → blocked? → done`. По `done` секция удаляется
целиком — история остаётся в `git log`. Незакрытый follow-up переезжает в `## Backlog` или
новой `### `-секцией, а не хвостом в удаляемой.

**Где это запускается.** Репо — библиотека, живого стека нет: гейт локальный — `make check`
(fmt, vet, lint, test -short -race, build, tidy, govulncheck, envdoc), перед тегом —
`make gate-clean` (то же в чистом worktree). Выпуск версии — тег `vX.Y.Z` (skill `/release`),
консьюмеры подтягивают `go get` сами.

## Active

### ideal-state — довести библиотеку до 8+ по всем линзам аудита

**Создан:** 2026-09-03 · **Статус:** in-progress

**Контекст.** Аудит на 843842e: 4,7/10, гейт красный (несобираемый sberaikit, флаки
rate-limit, нет CI), восемь критических дефектов в самых используемых китах (httpkit,
httpclient, authnkit, outboxkit, health-проводка), 17 достижимых CVE, дрейф доков. Решения
оператора: удалить openaikit/googleaikit/yandexaikit/sberaikit/webpushkit; остальные киты
остаются; API можно ломать одним breaking-minor; коммиты по ходу без push.

**Не входит в скоуп.** CI-раннер (инфраструктура вне репо; заменён `make gate-clean`),
вынос hubkit/amocrmkit в отдельные модули, правки в репо-потребителях, bump версии и тег.

**Атомы.**
- [x] **A0:** удаление пяти китов, детерминированный тест rate-limit, Makefile-гейт,
  `gate-clean`, `envdoc-check`, bump x/crypto, grpc, x/text, go 1.26.6.
  - Verify: `go build ./...` + `go test -short -race ./...` + `govulncheck ./...` чистые.
- [ ] **A1 (health):** leaf-пакет `healthkit` с именованным `Probe`; `httpkit.Probe` — alias;
  `pgkit`/`natskit`/`cachekit` возвращают `healthkit.Probe`; `/ready` не отдаёт текст ошибок.
  - Verify: тест проводки через `httpkit.Module` видит пробы китов · Зависит от: A0
- [ ] **A2 (httpkit):** `MapSentinel` без `errors.As` для `error`; `Idempotency` со scoped-ключом,
  NX-reserve, только 2xx/4xx; OTel первым в цепочке + span-name по route; синхронный Listen;
  limiter с eviction и `TrustProxy`; wrappers с `Unwrap`/`Flush`; `Vary: Origin`; лимит
  `X-Request-ID`; `ReadHeaderTimeout`; envconfig-теги у всех полей конфигов.
  - Verify: регрессионные тесты на каждый пункт · Зависит от: A1
- [ ] **A3 (httpclient):** retry через `GetBody` и только идемпотентных методов (opt-in для
  остальных), валидация `RetryWait`, cap backoff, `Retry-After`, per-host breaker, hook перед
  попыткой; request-id через `internal/requestid` без импорта httpkit.
  - Verify: тесты retry с телом, POST без retry, `RetryWait=0` · Зависит от: A0
- [ ] **A4 (authnkit):** валидация PHC-параметров в `Verify`, длина секрета, `exp` обязателен,
  go-jose вместо golang-jwt, экземплярный RBAC-реестр, `NeedsRehash`.
  - Verify: fuzz-корпус с t=0/p=0/пустым хэшем зелёный · Зависит от: A0
- [ ] **A5 (pgkit):** defer-rollback в `WithTx`; версии миграций на источник под advisory-lock;
  драйвер pgx/v5 для migrate; `MigrateModule` с multi-source; метрики через глобальный
  `otel.Meter`, реплика инструментирована; предикаты 55P03/57014.
  - Verify: тест «новая миграция в ките не сдвигает app-версии» · Зависит от: A0
- [ ] **A6 (outboxkit):** `EnsurePartitions` на старте; claim строк в транзакции; таймаут тика;
  `PublishAsyncTimeout`; атрибут `instance` в метриках; retention не дропает неопубликованное.
  - Verify: тест двух воркеров без дублей; тест партиций после старта · Зависит от: A5
- [ ] **A7 (natskit):** drain с ожиданием; DLQ без `Nats-Msg-Id`; deadline + `InProgress` в
  JS-хендлере; управляемые поля стрима; `Subscription` с отпиской; error-handlers.
  - Verify: тест DLQ внутри стрима; тест drain · Зависит от: A0
- [ ] **A8 (cachekit, storagekit, timerkit):** `Close`/Ping/`GetDel`/`Reserve`/теги + метрики
  в cachekit; стриминговый `Put` + метрики в storagekit; валидация интервала, WaitGroup,
  таймаут и метрики в timerkit.
  - Verify: unit-тесты, minio-тест под `testing.Short()` · Зависит от: A0
- [ ] **A9 (logkit, telemetry):** redact через `ReplaceAttr` (включая `With`); `Span` с
  fallback; стандартная семантика `OTEL_EXPORTER_OTLP_ENDPOINT`; OTel log-bridge или удаление
  LoggerProvider; fail-fast на невалидный уровень/паттерн.
  - Verify: структурные тесты redact · Зависит от: A0
- [ ] **A10 (сессии):** `internal/jwks` с троттлингом и singleflight для oidckit и hubkit/sso;
  reuse-detection и хэшированные ключи refresh; `GETDEL`; обязательный aud; единый дефолт
  `COOKIE_SECURE`; клиенты с таймаутом; метрики verify.
  - Verify: тесты повторного refresh и JWKS-шторма · Зависит от: A8
- [ ] **A11 (anthropickit, amocrmkit, grpc):** прайс с текущими моделями и `CostKnown`; спаны;
  метрики при `Close` стрима; regex subdomain/TLD; eviction лимитера; мёртвый API убран;
  уровень логов и текст ошибок в grpckit.
  - Verify: httptest-тесты Complete/Stream · Зависит от: A3
- [ ] **A12 (общее):** `internal/otelkit` вместо копий метрик, forward-only в комментариях,
  `bizmetric` суффиксы, `bruteforcekit.Window`, `mapkit` консистентность, testkit без
  `:latest` и с JetStream.
  - Verify: `make check` зелёный · Зависит от: A1–A11
- [ ] **A13 (доки):** README/CLAUDE.md/CONTRIBUTING/skills синхронны с кодом, env-таблицы
  проходят `envdoc-check`, обещания README сверены с кодом, release notes в аннотации тега.
  - Verify: `make gate-clean` зелёный · Зависит от: A12

## Backlog

- Вынос `hubkit` (SDK хаба) и `amocrmkit` в отдельные модули; триггер — второй потребитель
  вне stit-core или конфликт версий зависимостей у потребителя.
- CI на Gitea Actions, когда появится раннер: `make check` + `make test-full`.
- `hubkit/projection`: синхронизировать схему с клиентом (departments, tags, entity-tags) или
  перенести в шаблон продукта.
