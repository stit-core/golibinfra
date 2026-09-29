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
- **httpkit-auth-bypass** — `api.Adapter().Handle(&op)` с `Security` и сырые маршруты chi
  (`Router().Get`, `Mount`) идут мимо `HumaAuth` и проверки старта: huma вешает middleware API
  только в `huma.Register`, в OpenAPI их нет (N9 валидатора этапа httpkit, эксперимент E20).
  Триггер: партия 2, пункт B1 (явные публичные операции, дубль METHOD+path).
