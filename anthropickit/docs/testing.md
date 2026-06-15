# Тестирование anthropickit

Сабпакет `testkit/anthropictest` — тест-двойники `anthropickit.API` для кода
потребителя. Всё без сети: ни `New`, ни реального SDK-клиента не требуется.

```go
import "git.stit.tech/stit-core/golibinfra/anthropickit/testkit/anthropictest"
```

## `Stub` — фейк `anthropickit.API`

`Stub` реализует весь интерфейс `anthropickit.API`. Подменяй нужные `*Func`-поля;
незаданные методы отдают безопасные дефолты.

| Поле | Подменяет | Дефолт (если поле `nil`) |
|---|---|---|
| `CompleteFunc` | `Complete` | `*Response` с одним `TextBlock{Text:"stub"}`, `StopReason=StopEndTurn`. |
| `StreamFunc` | `Stream` | `ScriptStream` с событиями `EventTextDelta("stub")` + `EventDone` и таким же финалом. |
| `CountTokensFunc` | `CountTokens` | `0, nil`. |
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

Дрейн-контракт совпадает с боевым `sseStream`: `Next()` до `false`, затем
`Final()`. Подменив `Stub.StreamFunc` на возврат `ScriptStream`, скриптуешь поток
для всего сервиса.

## `ErrStream` — стрим сразу в ошибке

`ErrStream(err)` — `anthropickit.Stream`, у которого `Next()==false`, а ошибка
доступна через `Err()` и `Final()`. Зеркалит боевой `errStream` (сбой маппинга
запроса до открытия SSE).

```go
st := anthropictest.ErrStream(anthropickit.ErrInvalidRequest)
// st.Next() == false; st.Err() / st.Final() → ErrInvalidRequest
```
