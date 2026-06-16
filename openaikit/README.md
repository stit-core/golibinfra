# openaikit

Клиент OpenAI поверх [`openai-go/v3`](https://github.com/openai/openai-go): обе
текстовые поверхности — **Responses API** (`Respond`/`StreamResponse`,
рекомендуемый путь) и **Chat Completions** (`Chat`/`StreamChat`), плюс
`Embed`, `GenerateImage`, аудио `Transcribe` (STT) / `Speak` (TTS),
`Moderate`. Нормализованный учёт токенов (`Usage` по контракту
accounting-ports — две usage-формы Chat и Responses сводятся в одну) и
подсчитанная стоимость (`Cost` в микро-долларах) по прайс-таблице. Экзотику
(batch, files, vector stores, realtime control-plane, undocumented params) — через
`Raw()`. Base URL / org / project / auth-scheme инжектируемы (Azure-готовность). Кит
даёт сервисам единый, декаплированный от SDK вход в OpenAI API с
observability-by-default; хранение, леджер и агрегации usage/cost — на стороне
потребителя (БД у кита нет).

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/openaikit"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Быстрый старт

```go
package main

import (
	"context"
	"fmt"

	"git.stit.tech/stit-core/golibinfra/openaikit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func main() {
	cfg := openaikit.Config{APIKey: "sk-...", DefaultModel: "gpt-5.5"}

	client, err := openaikit.New(cfg, logkit.Nop())
	if err != nil {
		panic(err)
	}

	resp, err := client.Respond(context.Background(), openaikit.ResponsesRequest{
		Instructions: "Ты лаконичный ассистент.",
		Input:        "Столица Франции?",
	})
	if err != nil {
		panic(err)
	}
	if resp.IsRefusal() {
		return // отказ модели — не ошибка, это успешный 200
	}
	fmt.Println(resp.Text, resp.Usage.OutputTokens, resp.Cost)
}
```

Минимальный ENV: `OPENAI_API_KEY`.

## Responses vs Chat

| Поверхность | Метод | Когда |
|---|---|---|
| Responses API | `Respond` / `StreamResponse` | рекомендуемый путь: reasoning-effort, structured output, server-side chaining (`PreviousResponseID`) |
| Chat Completions | `Chat` / `StreamChat` | совместимость; кит сам выставляет `stream_options.include_usage` для usage в стриме |

Стрим-терминаторы различны (Responses — событие `response.completed`, без `[DONE]`;
Chat — `[DONE]`); оба нормализуются в единый `StreamEvent`. Мультимодальные парты,
built-in/function tools — через `Raw()`.

## Azure / совместимые эндпоинты

`Config.BaseURL` + `WithBaseURL` задают хост; `OPENAI_ORG_ID`/`OPENAI_PROJECT_ID` →
SDK-опции; произвольные `option.RequestOption` (Azure `api-key`/`api-version`) —
через `WithRequestOptions`. SDK без дефолт-таймаута — кит выставляет
`OPENAI_TIMEOUT` (default 120s) на попытку.

## Тестирование

`testkit/openaitest` — тест-двойники без сети: `Stub` (реализация `API`),
`ScriptStream`, `ErrStream`. Подменяй `*Func`-поля `Stub` для нужного поведения.
