# Backlog

Кандидаты в работу, ещё не взятые в план: `- **<id>** — что и зачем. Триггер: когда браться.` Форма и жизненный цикл — skill `/plan`.

- **httpclient-sse-metric-race** — `TestMetrics_RequestDurationLabels` изредка падает под нагрузкой
  `make check` (нет точки `status="200/sse"`): `SSEStream.read` пишет метрику в `record()` после
  `close(events)`, а тест читает её сразу после `range`. Триггер: партия 2 (аудит httpclient).
