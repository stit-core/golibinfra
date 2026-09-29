# Backlog

Кандидаты в работу, ещё не взятые в план: `- **<id>** — что и зачем. Триггер: когда браться.` Форма и жизненный цикл — skill `/plan`.

- **httpclient-sse-metric-race** — `TestMetrics_RequestDurationLabels` изредка падает под нагрузкой
  `make check` (нет точки `status="200/sse"`): `SSEStream.read` пишет метрику в `record()` после
  `close(events)`, а тест читает её сразу после `range`. Триггер: партия 2 (аудит httpclient).
- **logkit-redact-gaps** — маскирование пропускает двусмысленные короткие ключи (`pass`, `sid`,
  `code`, `signature`, `refresh`), список под `tokens` (слово не секрет — это счётчики LLM), поля
  структур в `Any`, `map` под указателем, base64url-токены, пароль в DSN, коды вне словаря
  (`passcode`, `verification_code`) и счётчики рядом с кодом (`otp_attempts` прячется целиком)
  (эксперимент валидатора
  `M/val-kits-A/experiments/logkit_zz_val_exp_test.go`). Триггер: партия 2 (аудит logkit).
