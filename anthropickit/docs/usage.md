# Рецепты anthropickit

## API-поверхность

```go
type API interface {
	Complete(ctx context.Context, req Request) (*Response, error)
	Stream(ctx context.Context, req Request) Stream
	CountTokens(ctx context.Context, req Request) (int64, error)
	Raw() anthropic.Client
}
```

`*Client` реализует `API`. Для DI/моков типизируйте зависимость как
`anthropickit.API`.

## Подключение через fx Module

`anthropickit.Module` provide'ит `API` (через `newClient`). `Config` он **не**
provide'ит — её собирает приложение. Дешёвый путь — добавить `NewConfig`:

```go
app := fx.New(
	fx.Provide(anthropickit.NewConfig), // Config из ENV (envconfig + Validate)
	logkit.Module,                       // даёт logkit.Logger
	anthropickit.Module,                 // даёт anthropickit.API
	fx.Invoke(func(api anthropickit.API) {
		// ...
	}),
)
```

Нужны опции (`WithPriceTable`, `WithHTTPClient`, `WithBaseURL`) — заведите свой
конструктор поверх `New` и provide'ите его вместо `anthropickit.Module`:

```go
fx.Provide(func(cfg anthropickit.Config, log logkit.Logger) (anthropickit.API, error) {
	return anthropickit.New(cfg, log, anthropickit.WithPriceTable(myPrices))
})
```

## Прямой `New`

```go
client, err := anthropickit.New(
	anthropickit.Config{APIKey: "sk-ant-...", DefaultModel: "claude-opus-5", MaxRetries: 2},
	logkit.Nop(),
	anthropickit.WithBaseURL("http://localhost:8080"), // напр. stub в тестах
)
```

`New` падает с `ErrConfig`, если `APIKey` пуст, `MaxRetries < 0` или
`Timeout < 0`. `nil`-логгер заменяется на `logkit.Nop()`.

## Синхронный вызов и обработка отказа

```go
resp, err := client.Complete(ctx, anthropickit.Request{
	Model:  "claude-sonnet-5", // "" → Config.DefaultModel
	System: anthropickit.SystemText("Отвечай кратко."),
	Messages: []anthropickit.Message{
		{Role: anthropickit.RoleUser, Content: []anthropickit.Block{
			anthropickit.TextBlock{Text: "Дай определение энтропии."},
		}},
	},
	MaxTokens: 2000, // 0 → дефолт 16_000 для sync
	Effort:    anthropickit.EffortHigh,
})
if err != nil {
	// транспортный/протокольный сбой — ветвление через errors.Is (см. ниже)
}
if resp.IsRefusal() {
	// успешный 200 со StopReason=refusal — не ошибка
}
text := resp.Text()                 // конкатенация всех TextBlock
total := resp.Usage.TotalInput()    // input + cache_write + cache_read
spent := resp.Cost                  // USDMicros по прайс-таблице
if !resp.CostKnown {
	// модели нет в таблице: Cost == 0, тарифицируйте сами / WithPriceTable
}
```

## Ошибки

Sentinel-ошибки для `errors.Is`: `ErrConfig`, `ErrInvalidRequest` (400/413/422 и
ошибки маппинга запроса), `ErrAuth` (401/403), `ErrNotFound` (404),
`ErrRateLimited` (429), `ErrOverloaded` (529), `ErrServer` (прочие 5xx).
Исходная `*anthropic.Error` сохраняется в цепочке (`errors.As`): `StatusCode`,
`RequestID`, `Type()`. Таймаут попытки (`ANTHROPIC_TIMEOUT`) и отмена `ctx`
приходят как `context.DeadlineExceeded`/`context.Canceled`.

## Стриминг

`Stream` выполняет HTTP-запрос сразу и возвращает итератор; события читаются в
`Next`. Дрейните до `Next()==false`, затем `Final()` для итогового ответа с
авторитетными `Usage`/`Cost`. `Close()` обязателен: он отдаёт соединение, а при
вызове до конца стрима дописывает метрики брошенного стрима (`status=abandoned`)
с накопленным usage/cost.

```go
st := client.Stream(ctx, anthropickit.Request{
	Messages: []anthropickit.Message{
		{Role: anthropickit.RoleUser, Content: []anthropickit.Block{
			anthropickit.TextBlock{Text: "Напиши хайку."},
		}},
	},
})
defer st.Close()

for st.Next() {
	ev := st.Event()
	switch ev.Type {
	case anthropickit.EventTextDelta:
		fmt.Print(ev.Text)
	case anthropickit.EventThinkingDelta:
		// ev.Thinking
	case anthropickit.EventToolUseStart:
		// ev.ToolName, ev.ToolUseID
	case anthropickit.EventToolUseDelta:
		// ev.PartialJSON
	}
}
if err := st.Err(); err != nil {
	// ошибка стрима (в т.ч. ошибка самого HTTP-запроса: Next() сразу false)
}
final, _ := st.Final() // *Response с финальным Usage+Cost
```

Типы событий: `EventMessageStart`, `EventTextDelta`, `EventThinkingDelta`,
`EventToolUseStart`, `EventToolUseDelta`, `EventBlockStop`, `EventMessageDelta`,
`EventDone`, `EventOther`. Usage в `EventMessageStart` — вход/кэш; в
`EventMessageDelta` — кумулятивно (но без cache-write split по TTL —
авторитетный финал берите из `Final`).

## Tools (function calling)

```go
req := anthropickit.Request{
	Messages: msgs,
	Tools: []anthropickit.ToolDef{{
		Name:        "get_weather",
		Description: "Текущая погода в городе.",
		Schema: map[string]any{ // properties-объект JSON Schema
			"city": map[string]any{"type": "string"},
		},
		Required: []string{"city"},
	}},
	ToolChoice: &anthropickit.ToolChoice{Type: anthropickit.ToolChoiceAuto},
}
resp, _ := client.Complete(ctx, req)
for _, blk := range resp.Content {
	if tu, ok := blk.(anthropickit.ToolUseBlock); ok {
		// tu.ID, tu.Name, tu.Input (json.RawMessage)
		// результат вернуть следующим сообщением:
		// Message{Role: RoleUser, Content: []Block{ToolResultBlock{ToolUseID: tu.ID, Content: "..."}}}
	}
}
```

`ToolChoice.Type` ∈ `ToolChoiceAuto` / `ToolChoiceAny` / `ToolChoiceTool` (тогда
заполнить `Name`) / `ToolChoiceNone`.

## Structured output

```go
resp, _ := client.Complete(ctx, anthropickit.Request{
	Messages: msgs,
	JSONSchema: map[string]any{ // → output_config.format (json_schema)
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
		},
	},
})
```

`nil` `JSONSchema` → свободный вывод (`OutputConfig` не выставляется).

## Thinking и effort

`Request.Thinking` ∈ `ThinkingAdaptive` (дефолт — модель сама решает глубину) /
`ThinkingDisabled`. `Request.Effort` ∈ `EffortLow`/`EffortMedium`/`EffortHigh`/
`EffortXHigh`/`EffortMax` (пусто → не выставляется).

## Кэширование промпта

Любой блок (`TextBlock`/`ImageBlock`/`DocumentBlock`) и системный `TextBlock`
несут поле `Cache *CacheControl`. Ненулевой `Cache` ставит ephemeral
cache-control на этот блок. `CacheTTL` (`CacheTTL5m`/`CacheTTL1h`) объявлен в
типах; маппинг ставит ephemeral-маркер (точку кэша) на соответствующий блок.

## Предполётный подсчёт токенов

```go
n, err := client.CountTokens(ctx, req) // входные токены для бюджетирования
```

Учитывает `Messages`, `System`, `Tools`, `Thinking`, `ToolChoice` из `Request`.
Пишет `anthropic_api_calls_total{op="count_tokens"}` и латентность; токены в
`anthropic_api_tokens_total` не попадают.

## Стоимость без клиента

```go
cost, known := anthropickit.Cost("claude-opus-5", usage)
// known=false для модели вне defaultPrices — fallback решает потребитель
```

`Client.Complete`/`Stream.Final` считают `Cost`/`CostKnown` автоматически по
таблице клиента (с учётом `WithPriceTable`). Свободная `Cost` всегда использует
`defaultPrices`.

## Managed Agents и экзотика — `Raw()`

```go
raw := client.Raw() // anthropic.Client
agent, err := raw.Beta.Agents.New(ctx, /* ... */)
// сессии: raw.Beta.Sessions.* ; batches/files и пр. — тоже через raw
```

Кит покрывает Messages-поверхность; всё остальное SDK (Managed Agents, batches,
files, server-side tools) — через `Raw()`. Вызовы через `Raw()` идут тем же
otelhttp-транспортом (спаны есть), но метрики кита не пишут.
