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
- **logkit-redact-disabled-literal** — конструктор логгера с `WithRedact(RedactConfig{Disabled: true, …})` не зовёт `Validate`:
  невалидный паттерн или замена у выключенного redact проходит (путь ENV — `NewRedactConfig` — отвергает), против «формат
  проверяется и у выключенного»; README logkit это называет. Правка — отказ конструктора: смена поведения, строка аннотации.
  Триггер: повторный аудит угла 1 или правка `logkit/build.go`.
- **readme-snippet-copies** — `TestReadme_*` logkit, `TestReadmeFlagshipExample` и `TestQuickStartGraphValidates` pgkit держат
  копию сниппета, а не блок README, быстрый старт archkit тест не держит: правку блока в README гейт не видит (у authnkit и
  httpkit блок побайтно равен коду теста). Графам fx нужна сверка опций и старт на пустой базе, сниппеты
  logkit «Без fx» — три фрагмента без проверки ошибок: кодом теста они не собираются, сверка — после их правки. Триггер: правка
  этих сниппетов или повторный аудит угла доков.
- **hubkit-sync-callback-lifecycle** — `hubkit/sync.NewEngine` регистрирует observable-callback движка в конструкторе, снимает его
  `Engine.Close` (`Module` — в `OnStop`): движок графа, не дошедшего до старта, оставляет callback в `MeterProvider` (у pgkit и
  outboxkit callback живёт от `OnStart` до `OnStop`). Кит вне объёма аудита; правка — контракт `NewEngine`/`Close`. Триггер:
  правка `hubkit/sync` или его тесты под `-count>1`.
- **guard-template-parity** — guard сессии (`.claude/hooks/guard-bash.sh`) не видит формы, которые закрыл guard шаблона: код для
  интерпретатора (`bash -c`, `eval`, heredoc), имя `git` в кавычках, алиасы и `include` конфига git, запись в `.git/config`,
  `git send-pack`; README § Гейт называет их. Триггер: повторный аудит угла 8 или общий guard двух репо.
- **pgkit-query-name-hyphen** — имя запроса для спана и `db.operation.name` (`queryName`, `pgkit/pool.go`) — ведущее слово
  комментария `-- name:` из букв, цифр и `_`: имя с дефисом (`-- name: get-user :one`) обрезается до `get`. Через sqlc
  недостижимо — имя запроса там имя Go-метода; путь — рукописный комментарий. Триггер: запросы с `-- name:` не от sqlc у
  потребителя или правка `queryName`.
- **envconf-anonymous-app-name** — ошибка ридера ENV называет поле как `<тип>.<поле>` (`internal/envconf`): у анонимной
  App-структуры имя типа пустое — `field .DB: envconfig tag on a nested struct`. Поле названо, структура — нет. Триггер:
  правка текстов ошибок `internal/envconf` или потребитель с анонимной App.
- **outbox-backlog-group-by-subject** — снимок очереди outbox (`outboxkit/pgstore.Store.Backlog`) — `GROUP BY subject`: при
  идентификаторе сущности в subject запрос отдаёт строку на сущность за каждый сбор метрик, хотя gauge'и несут серию на
  инстанс (с `WithMetricSubject` — на шаблон). Триггер: потребитель с id в subject и заметной ценой сбора или правка
  `Backlog`.
- **archkit-rule-gaps** — формы нарушений, которые правила `archkit` не видят (правила синтаксические — README кита § Инварианты):
  порт между доменами через `return (uc)`, `return Port(uc)`, переменную, именованный результат, alias типа и dot-импорт чужого
  домена; `time.Now` за обёрткой в `pkg/`; `io/ioutil`, `path/filepath` и `embed` в корне домена; в SQL — `public . users`,
  `FROM (a CROSS JOIN b)`, `CREATE UNLOGGED TABLE`, схема не `public`, представление над чужой таблицей; запись
  `WithUncheckedPackages` о пакете под другим доверенным. Триггер: повторный аудит archkit или такая форма у потребителя.
- **natskit-empty-durable** — `SubscribeStream` с пустым `durable` шина принимает и создаёт эфемерный консьюмер, хотя контракт
  метода — durable: позиция между переподключениями и раздел нагрузки между инстансами (`natskit/docs/architecture.md`);
  `natstest` такой вызов отвергает (README testkit: «строже шины» — как и фильтр вне subject'ов стрима, с которым подписка шины
  молчит). Правка — отказ кита: смена поведения, строка аннотации. Триггер: правка `SubscribeStream` или потребитель с пустым
  durable.
- **grpckit-health-test-time-bound** — `TestHealthProbeTimeoutIsIndependentOfInterval` (`grpckit/health_test.go`) отличает дедлайн
  пробы (50 мс) от `GRPC_HEALTH_INTERVAL` (400 мс) по часам процесса: запас 350 мс — допуск по времени, под тяжёлой нагрузкой тест
  может покраснеть без дефекта кита. Триггер: падение этого теста в гейте или pre-push.
