# Тестирование anthropickit

Сабпакет `testkit/anthropictest` — тест-двойники `anthropickit.API` для кода
потребителя. Всё без сети: ни `New`, ни реального SDK-клиента не требуется.

```go
import "git.stit.tech/stit-core/golibinfra/testkit/anthropictest"
```

## `Stub` — фейк `anthropickit.API`

`Stub` реализует весь интерфейс `anthropickit.API`. Подменяй нужные `*Func`-поля;
незаданные методы отдают безопасные дефолты.

| Поле | Подменяет | Дефолт (если поле `nil`) |
|---|---|---|
| `CompleteFunc` | `Complete` | `*Response` с одним `TextBlock{Text:"stub"}`, `StopReason=StopEndTurn`, `CostKnown=true`. |
| `StreamFunc` | `Stream` | `ScriptStream` с событиями `EventTextDelta("stub")` + `EventDone` и таким же финалом. |
| `CountTokensFunc` | `CountTokens` | `0, nil`. |
| `DefaultModelName` | `DefaultModel()` | `anthropictest.DefaultModel` (`claude-opus-5`). |
| `RawClient` | возвращается из `Raw()` | нулевой `anthropic.Client`. |

`Stub` удовлетворяет `anthropickit.API` статически (`var _ anthropickit.API = (*Stub)(nil)`),
поэтому годится как drop-in для зависимости, типизированной интерфейсом.

```go
stub := &anthropictest.Stub{
	CompleteFunc: func(ctx context.Context, req anthropickit.Request) (*anthropickit.Response, error) {
		return &anthropickit.Response{
			Role:       anthropickit.RoleAssistant,
			StopReason: anthropickit.StopEndTurn,
			Content:    []anthropickit.Block{anthropickit.TextBlock{Text: "42"}},
			Usage:      anthropickit.Usage{InputTokens: 10, OutputTokens: 1},
			Cost:       75,
			CostKnown:  true,
		}, nil
	},
}

svc := NewMyService(stub) // зависимость типизирована anthropickit.API
```

Отдать ошибку из `Complete` (например, проверить ветвление по `errors.Is`):

```go
stub := &anthropictest.Stub{
	CompleteFunc: func(context.Context, anthropickit.Request) (*anthropickit.Response, error) {
		return nil, anthropickit.ErrRateLimited
	},
}
```

## `ScriptStream` — скриптуемый `Stream`

`ScriptStream(final, events...)` строит `anthropickit.Stream`, который
проигрывает заранее заданные события, а в `Final()` отдаёт переданный `*Response`.
Удобно для тестов потребителя стрима без реального SSE.

```go
final := &anthropickit.Response{
	Role:       anthropickit.RoleAssistant,
	StopReason: anthropickit.StopEndTurn,
	Content:    []anthropickit.Block{anthropickit.TextBlock{Text: "hello"}},
}
st := anthropictest.ScriptStream(
	final,
	anthropickit.StreamEvent{Type: anthropickit.EventTextDelta, Text: "hel"},
	anthropickit.StreamEvent{Type: anthropickit.EventTextDelta, Text: "lo"},
	anthropickit.StreamEvent{Type: anthropickit.EventDone},
)
for st.Next() {
	// st.Event()
}
resp, _ := st.Final()
```

Дрейн-контракт совпадает с боевым `sseStream`, включая ошибочные ветки:

- `Event()` до первого `Next()` — нулевое событие (не паника).
- `Final()` до конца скрипта — `anthropickit.ErrStreamNotDrained`.
- `Next()` после `Close()` — `false`.
- `Usage()` — usage последнего проигранного события, несущего `Usage` (так же,
  как боевой стрим отдаёт накопленное).
- Горутины — как у боевого стрима: `Next`/`Event`/`Err`/`Final` из одной,
  `Close` и `Usage` из любой, поэтому тест cancel-пути проходит под `-race`.

Подменив `Stub.StreamFunc` на возврат `ScriptStream`, скриптуешь поток для всего
сервиса.

## `ScriptStreamErr` — обрыв посреди стрима

`ScriptStreamErr(err, events...)` проигрывает события, а затем ломается: главный
сценарий гейтвея — N дельт и обрыв. До конца скрипта `Err()` == `nil`, после —
`err`; `Final()` отдаёт `err`, `Usage()` — учёт, накопленный до обрыва.

```go
usage := anthropickit.Usage{InputTokens: 25, OutputTokens: 7}
st := anthropictest.ScriptStreamErr(context.Canceled,
	anthropickit.StreamEvent{Type: anthropickit.EventMessageStart, Usage: &usage},
	anthropickit.StreamEvent{Type: anthropickit.EventTextDelta, Text: "полов"},
)
// дрейн → Err() == context.Canceled, Usage() == usage: за это API уже выставит счёт
```

## `ErrStream` — стрим сразу в ошибке

`ErrStream(err)` — `anthropickit.Stream`, у которого `Next()==false`, а ошибка
доступна через `Err()` и `Final()`. Зеркалит боевой `errStream` (сбой маппинга
запроса до HTTP-вызова) и стрим, чей HTTP-запрос отклонил API. Это частный
случай `ScriptStreamErr` без событий.

```go
st := anthropictest.ErrStream(anthropickit.ErrInvalidRequest)
// st.Next() == false; st.Err() / st.Final() → ErrInvalidRequest
```

## Тесты самого кита

Боевой `Client` тестируется против `httptest.Server`, изображающего Anthropic
API (`WithBaseURL(srv.URL)`): JSON-ответы Messages/count_tokens/models и SSE-поток
с `Content-Type: text/event-stream`; метрики проверяются через
`testkit/metrictest`, спаны — `tracetest.SpanRecorder` в глобальном
`TracerProvider`, установленном до `New`. Ретраи SDK в тестах управляются заголовком
`Retry-After-Ms` в ответе сервера.

Маппинг запроса проверяется по телу HTTP-запроса (`wire_test.go`): табличный тест
сверяет JSON, который реально уходит в API, для каждого типа блока, tools,
tool_choice, thinking/display, effort, structured output, cache TTL, auto-cache и
metadata. Прайс-таблица кода сверяется с таблицей в
[configuration.md](configuration.md) тестом `TestDefaultPricesMatchDocs`.
