# googleaikit

Клиент Google Gemini поверх [`google.golang.org/genai`](https://github.com/googleapis/go-genai):
**dual-backend** (Gemini Developer API / AI-Studio по API-ключу ИЛИ Vertex AI по
project+location/ADC), все модальности — `GenerateContent` (chat/мультимодал/thinking,
sync + `Stream`), `EmbedContent`, `GenerateImages` (Nano Banana + Imagen), видео Veo
(`SubmitVideo`/`PollVideo`/`WaitVideo`, async LRO), `GenerateSpeech` (TTS). Нормализованный
учёт токенов (`Usage`) и подсчитанная стоимость (`Cost` в микро-долларах) по прайс-таблице,
предполётный `CountTokens`. Экзотику (Files/Caches/Live/Batches/Tunings) и
native-passthrough — через `Raw()`. Кит даёт сервисам единый, декаплированный от SDK вход
в Gemini API с observability-by-default; хранение, леджер и агрегации usage/cost — на стороне
потребителя (БД у кита нет).

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/googleaikit"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Быстрый старт

```go
package main

import (
	"context"
	"fmt"

	"git.stit.tech/stit-core/golibinfra/googleaikit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func main() {
	cfg := googleaikit.Config{APIKey: "AIza...", Backend: "aistudio", DefaultModel: "gemini-2.5-flash"}

	client, err := googleaikit.New(cfg, logkit.Nop())
	if err != nil {
		panic(err)
	}

	resp, err := client.GenerateContent(context.Background(), googleaikit.Request{
		System: "Ты лаконичный ассистент.",
		Messages: []googleaikit.Message{
			{Role: googleaikit.RoleUser, Content: []googleaikit.Block{
				googleaikit.TextBlock{Text: "Столица Франции?"},
			}},
		},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(resp.Text(), resp.Usage.OutputTokens, resp.Cost)
}
```

Минимальный ENV (AI-Studio): `GOOGLE_API_KEY`. Vertex: `GOOGLE_BACKEND=vertex` +
`GOOGLE_PROJECT` + `GOOGLE_LOCATION` (креды из ADC).

## Backend

| Backend | ENV | Auth | Адресация |
|---|---|---|---|
| AI-Studio (default) | `GOOGLE_BACKEND=aistudio` | `GOOGLE_API_KEY` | глобально, без project в URL |
| Vertex AI | `GOOGLE_BACKEND=vertex` | ADC / OAuth2 (SA) | `GOOGLE_PROJECT` + `GOOGLE_LOCATION` |

Один `Client` умеет оба; различия (auth, project+location, Files vs GCS) нормализует кит.

## Veo (async)

Генерация видео — long-running operation: `SubmitVideo` возвращает незавершённую операцию,
`PollVideo` опрашивает её разово, `WaitVideo` блокирующе поллит до `Done` (паузы — через
`clockkit.Clock`). Скачивание готового видео по URI — через `Raw()`.

## Тестирование

`testkit/googletest` — тест-двойники без сети: `Stub` (реализация `API`), `ScriptStream`,
`ErrStream`. Подменяй `*Func`-поля `Stub` для нужного поведения.
