# Backlog

Кандидаты в работу, ещё не взятые в план: `- **<id>** — что и зачем. Триггер: когда браться.` Форма и жизненный цикл — skill `/plan`.

- **httpclient-sse-metric-race** — `TestMetrics_RequestDurationLabels` изредка падает под нагрузкой
  `make check` (нет точки `status="200/sse"`): `SSEStream.read` пишет метрику в `record()` после
  `close(events)`, а тест читает её сразу после `range`. Триггер: партия 2 (аудит httpclient).
- **logkit-redact-gaps** — маскирование пропускает двусмысленные короткие ключи (`pass`, `sid`,
  `code`, `signature`, `refresh`), список под `tokens` (слово не секрет — это счётчики LLM), поля
  структур в `Any`, base64url-токены, пароль в DSN, коды вне словаря (`passcode`,
  `verification_code`) и счётчики рядом с кодом (`otp_attempts` прячется целиком) (эксперимент
  валидатора `M/val-kits-A/experiments/logkit_zz_val_exp_test.go`). Триггер: партия 2 (аудит logkit).
- **logkit-redact-containers** — ключи-секреты не ищутся в `map` под указателем (безопасно —
  указатель считать контейнером, только если его цель — контейнер) и в JSON из `json.RawMessage`
  и `[]byte` (тело запроса `{"password":…}`): их видят только маски. Триггер: партия 2 (аудит logkit).
- **logkit-redact-otpauth** — ключ `otpauth` (URI provisioning TOTP) по правилу ключей не секрет, а
  base32-секрет в `secret=` дефолтные маски не ловят — он уходит в лог открытым. Триггер: партия 2
  (аудит logkit).
- **bruteforcekit-lock-remaining** — `Retry-After` отказа потребитель считает своими часами от
  `LockState.Until` (часы БД): расхождение часов даёт неверную паузу; отдавать остаток блокировки
  от хранилища (N11 валидатора harden-kits B). Триггер: партия 2 (аудит bruteforcekit).
- **bruteforcekit-attempt-retry-test** — ветку `Attempt`, которая переучитывает попытку, когда
  блокировку сняли между чтением и upsert (до 3 раз), тест не воспроизводит. Триггер: партия 2
  (аудит bruteforcekit).
- **outboxtest-withtesttx** — `pgkit.WithTestTx` на ctx usecase'а снова пропускает `Publish` мимо
  транзакции: у фейков `pgtest` и ручной пометки один маркер; контракт держат
  `outboxkit/docs/testing.md` и аннотация v1.2.0, машина — нет. Триггер: партия 2 (аудит testkit).
- **timerkit-raw-group** — job с сырым тегом группы `timerkit.jobs` обходит маркер модуля (проверку
  `timerkit.Module` в графе); контракт «только `timerkit.Provide`» держит README. Триггер: партия 3
  (`archkit`).
- **httpkit-auth-bypass** — `api.Adapter().Handle(&op)` с `Security` и сырые маршруты chi
  (`Router().Get`, `Mount`) идут мимо `HumaAuth` и проверки старта: huma вешает middleware API
  только в `huma.Register`, в OpenAPI их нет (N9 валидатора этапа httpkit, эксперимент E20).
  Триггер: партия 2, пункт B1 (явные публичные операции, дубль METHOD+path).
