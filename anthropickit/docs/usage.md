# Рецепты anthropickit

## API-поверхность

```go
type API interface {
	Complete(ctx context.Context, req Request) (*Response, error)
	Stream(ctx context.Context, req Request) Stream
	CountTokens(ctx context.Context, req Request) (int64, error)
	DefaultModel() string
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
	fx.Provide(anthropickit.NewConfig, logkit.NewConfig), // конфиги из ENV (envconfig + Validate)
	logkit.Module,                                        // даёт logkit.Logger
	anthropickit.Module,                                  // даёт anthropickit.API
	healthkit.Provide(anthropickit.Probe),                // опционально: проба в /ready
	fx.Invoke(func(api anthropickit.API) {
		// ...
	}),
)
```

`logkit.Module` требует `logkit.Config` в графе, поэтому `logkit.NewConfig`
регистрируется рядом с `anthropickit.NewConfig`. Граф из этого примера закреплён
тестом (`TestModule_DocsFxExampleValidates`).

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
	anthropickit.Config{
		APIKey:       "sk-ant-...",
		BaseURL:      "https://api.anthropic.com",
		DefaultModel: "claude-opus-5",
		MaxRetries:   2,
		Timeout:      10 * time.Minute,
	},
	logkit.Nop(),
	anthropickit.WithBaseURL("http://localhost:8080"), // напр. stub в тестах
)
```

`Config` целиком валидируется в `New`: пустой `APIKey`/`DefaultModel`,
`BaseURL` не вида `http(s)://host`, `MaxRetries < 0`, `Timeout <= 0` → `ErrConfig`.
Литерал `Config{...}` без этих полей не заработает — задавайте их явно либо
берите `NewConfig()` с её дефолтами. `nil`-логгер заменяется на `logkit.Nop()`.

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
	MaxTokens: 2000, // 0 → дефолт 16_000 для sync; потолок sync — 21_333
	Effort:    anthropickit.EffortHigh,
	UserID:    "u-42", // metadata.user_id для атрибуции/abuse-детекта
})
if err != nil {
	// транспортный/протокольный сбой — ветвление через errors.Is (см. ниже)
}
if resp.IsRefusal() {
	// успешный 200 со StopReason=refusal — не ошибка;
	// resp.StopDetails (Category/Explanation) заполнен именно здесь
}
text := resp.Text()                 // конкатенация всех TextBlock
total := resp.Usage.TotalInput()    // input + cache_write + cache_read
spent := resp.Cost                  // USDMicros по прайс-таблице
if !resp.CostKnown {
	// модели нет в таблице: Cost == 0, тарифицируйте сами / WithPriceTable
}
```

`Complete` с `MaxTokens > 21_333` возвращает `ErrMaxTokensRequiresStreaming` до
HTTP-вызова: столько токенов API не отдаёт в non-streaming-режиме, нужен
`Stream` (см. [configuration.md § Дефолты и потолок `max_tokens`](configuration.md)).

## Причины остановки

`Response.StopReason` ∈ `StopEndTurn`, `StopMaxTokens`, `StopToolUse`,
`StopStopSequence`, `StopPauseTurn`, `StopRefusal`, `StopContextExceeded`.
Особые случаи:

- `StopRefusal` — отказ классификатора, `StopDetails` заполнен; не ошибка.
- `StopPauseTurn` — API поставил длинный ход на паузу. Кит цикл не ведёт:
  чтобы продолжить, отправьте следующий запрос, добавив `Response.Content`
  как ход ассистента без изменений (thinking- и redacted-thinking-блоки
  включительно).
- `StopMaxTokens` — ответ обрезан по `MaxTokens`, не ошибка.

## Ошибки

Sentinel-ошибки для `errors.Is`:

| Sentinel | Когда |
|---|---|
| `ErrConfig` | невалидный `Config` (или `WithBaseURL`) |
| `ErrInvalidRequest` | 400/413/422 и ошибки маппинга запроса |
| `ErrAuth` | 401/403 |
| `ErrNotFound` | 404 |
| `ErrRateLimited` | 429 |
| `ErrOverloaded` | 529 |
| `ErrServer` | 408, 409 и прочие 5xx |
| `ErrStreamNotDrained` | `Stream.Final()` вызван до `Next()==false` |
| `ErrMaxTokensRequiresStreaming` | `Complete` с `MaxTokens` выше sync-потолка |

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

Контракт итератора:

- `Next()` после `Close()` — `false`, `Err()` при этом не портится.
- `Final()` до дрейна — `ErrStreamNotDrained` (частичный ответ молча не отдаётся).
- Стрим оборвался (отмена `ctx`, разрыв SSE) — `Final()` возвращает **и** ошибку,
  **и** частичный `*Response` с накопленным `Usage`/`Cost`, если успел прийти
  `message_start`. Тарифицируйте по нему: API прерванную генерацию считает.
- `Usage()` доступен в любой момент, в том числе после `Close()` без дрейна, —
  это учёт того, что уже сгенерировано.
- Горутины: `Next`/`Event`/`Err`/`Final` — из одной; `Close` и `Usage` — из
  любой, в том числе параллельно с `Next`. Гейтвей закрывает стрим из горутины
  отмены: соединение рвётся сразу, а метрики брошенного стрима пишутся ровно
  один раз, под `status=abandoned` (обрыв чтения от собственного `Close` сбоем
  не считается).

Типы событий: `EventMessageStart`, `EventTextDelta`, `EventThinkingDelta`,
`EventToolUseStart`, `EventToolUseDelta`, `EventBlockStop`, `EventMessageDelta`,
`EventDone`, `EventOther`. Usage в `EventMessageStart` — вход/кэш; в
`EventMessageDelta` — кумулятивно (cache-write там лумпованный, без split по
TTL — авторитетный финал берите из `Final`).

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
		Strict:   true, // API гарантирует валидный по схеме Input
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

`ToolDef.Strict` уезжает как `strict: true` вместе с `additionalProperties:
false` в схеме — API отвергает strict-инструмент с открытой схемой.

`ToolChoice.Type` ∈ `ToolChoiceAuto` / `ToolChoiceAny` / `ToolChoiceTool` (тогда
заполнить `Name`) / `ToolChoiceNone`. Принудительный выбор (`Any`/`Tool`) часть
моделей не принимает и отвечает 400 (`ErrInvalidRequest`); переносимая замена —
`Auto` + `Strict: true` либо structured output.

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

## Thinking, display и effort

`Request.Thinking` ∈ `ThinkingAdaptive` (дефолт — модель сама решает глубину) /
`ThinkingDisabled`. `Request.Effort` ∈ `EffortLow`/`EffortMedium`/`EffortHigh`/
`EffortXHigh`/`EffortMax` (пусто → не выставляется).

`Request.ThinkingDisplay` управляет тем, возвращается ли содержимое размышлений:
`ThinkingDisplaySummarized` — читаемая выжимка, `ThinkingDisplayOmitted` — пусто.
Пустое значение поля не шлёт, и тогда действует дефолт API, который зависит от
модели: у актуальных моделей он `omitted`, то есть `EventThinkingDelta` и
`ThinkingBlock.Thinking` приходят пустыми. Стримите размышления в UI —
выставляйте `ThinkingDisplaySummarized` явно. На стоимость и наличие
`thinking`-токенов display не влияет.

Ответные `ThinkingBlock` и `RedactedThinkingBlock` возвращайте в следующем ходе
как есть (та же модель): `Signature`/`Data` — часть протокола реплея, и
ход с `tool_use` без своих thinking-блоков API отвергает.

## Кэширование промпта

Любой блок (`TextBlock`/`ImageBlock`/`DocumentBlock`) и системный `TextBlock`
несут поле `Cache *CacheControl`. Ненулевой `Cache` ставит точку кэша на этом
блоке; `CacheControl{TTL: CacheTTL1h}` уезжает на wire как
`{"type":"ephemeral","ttl":"1h"}`, `CacheTTL5m` — как `"5m"`, пустой `TTL` —
дефолт API (5 минут).

`Request.AutoCache` — тот же маркер, но верхнего уровня: точку кэша расставляет
API само, без ручных брейкпоинтов по блокам.

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
`defaultPrices`. Что расчёт не учитывает (batch, fast mode, округление) —
[configuration.md § Что `Cost` не учитывает](configuration.md).

## Health-проба

```go
healthkit.Provide(anthropickit.Probe) // в fx-граф, если нужно в /ready
```

Проба зовёт `Models.Get(Config.DefaultModel)` с таймаутом 3 с: ключ, endpoint и
модель проверяются без тарификации. Автоматически кит её не регистрирует.

## Managed Agents и экзотика — `Raw()`

```go
raw := client.Raw() // anthropic.Client
agent, err := raw.Beta.Agents.New(ctx, /* ... */)
// сессии: raw.Beta.Sessions.* ; batches/files и пр. — тоже через raw
```

Кит покрывает Messages-поверхность; всё остальное SDK (Managed Agents, batches,
files, server-side tools) — через `Raw()`. Вызовы через `Raw()` идут тем же
otelhttp-транспортом (спаны транспорта есть), но метрик и `gen_ai.*`-спанов кита
не пишут.
