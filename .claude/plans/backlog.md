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
- **readme-snippet-copies** — `TestReadme_*` logkit и `TestReadmeFlagshipExample` держат копию сниппета, а не блок README: правку
  блока в README гейт не видит (у authnkit и httpkit блок побайтно равен коду теста). Графам fx нужна сверка опций, сниппеты
  logkit «Без fx» — три фрагмента без проверки ошибок: кодом теста они не собираются, сверка — после их правки. Триггер: правка
  этих сниппетов или повторный аудит угла доков.
- **hubkit-sync-callback-lifecycle** — `hubkit/sync.NewEngine` регистрирует observable-callback движка в конструкторе, снимает его
  `Engine.Close` (`Module` — в `OnStop`): движок графа, не дошедшего до старта, оставляет callback в `MeterProvider` (у pgkit и
  outboxkit callback живёт от `OnStart` до `OnStop`). Кит вне объёма аудита; правка — контракт `NewEngine`/`Close`. Триггер:
  правка `hubkit/sync` или его тесты под `-count>1`.
- **guard-template-parity** — guard сессии (`.claude/hooks/guard-bash.sh`) не видит формы, которые закрыл guard шаблона: код для
  интерпретатора (`bash -c`, `eval`, heredoc), имя `git` в кавычках, алиасы и `include` конфига git, запись в `.git/config`,
  `git send-pack`; README § Гейт называет их. Триггер: повторный аудит угла 8 или общий guard двух репо.
- **authnkit-maxlength-runes** — доки authnkit (`README.md`, `docs/usage.md`, doc-comment `MaxPasswordLen`) обещают DTO с
  `maxLength:"1024"` отказ 4xx до usecase; huma считает `maxLength` в символах, предел кита — байты: пароль не из ASCII короче
  1024 символов доходит до `Hash` (`ErrPasswordTooLong`). Правка — точность доков или хелпер проверки длины в байтах для DTO.
  Триггер: правка доков authnkit, повторный аудит угла доков или 500 у потребителя на длинном пароле.
