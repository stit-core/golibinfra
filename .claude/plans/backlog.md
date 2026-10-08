# Backlog

Кандидаты в работу, ещё не взятые в план: `- **<id>** — что и зачем. Триггер: когда браться.` Форма и жизненный цикл — skill `/plan`.

- **httpkit-route-after-check** — маршрут, добавленный после проверки старта (`OnStart` потребителя
  позже хука сервера, рантайм), проверку публичности не проходит (A4 валидатора B
  `harden-gate-defaults`): `Router()` отдаёт `*chi.Mux`, перехват регистрации ломает API, правило
  «маршруты — в `Invoke`» держат доки httpkit. Триггер: потребитель регистрирует маршрут позже
  `Invoke` или повторный аудит находит такой граф.
- **timerkit-raw-group** — job с сырым тегом группы `timerkit.jobs` обходит маркер модуля (проверку
  `timerkit.Module` в графе); контракт «только `timerkit.ProvideJob`» держит README. Не правило
  раскладки, поэтому не `archkit` (решение владельца: узко). Триггер: сырой тег у потребителя или
  второй кит с той же дырой маркера.
- **bool-default-true-literal** — поля bool с `default:"true"` (`outboxkit/pgstore.RequireTx`, `cachekit.PingOnStart`,
  `storagekit.ProbeOnStart`, `pgkit.OTelTrace`, `natskit.StreamManaged`) в литерале с нулями выключаются — расходится с «ноль
  литерала — значение доки» (по нему снят `STORAGE_S3_USE_SSL`); у `pgstore.Config{}` снят tx-guard. Триггер: повторный аудит
  угла 1 или потребитель на литерале конфига.
- **testkit-reaper-test-logger** — фикстуры testkit отдают жнецу testcontainers `tclog.TestLogger(t)` (`testkit/postgres.go:49`,
  nats, redis, minio): под `-count>1` контейнер прошлого прогона снимается логгером завершённого теста, `tzkit` `TestPgxInterop`
  паникует «Log in goroutine after … has completed». Триггер: прогоны с `-count>1` (stress, flaky) или падение в pre-push.
