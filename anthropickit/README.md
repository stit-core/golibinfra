# anthropickit

Клиент Claude поверх [`anthropic-sdk-go`](https://github.com/anthropics/anthropic-sdk-go):
синхронный (`Complete`) и стриминговый (`Stream`) Messages, нормализованный учёт
токенов (`Usage`) и подсчитанная стоимость (`Cost` в микро-долларах) по
прайс-таблице, предполётный `CountTokens`. Managed Agents и прочую экзотику
SDK — через `Raw()`. Кит даёт сервисам единый, декаплированный от SDK вход в
Claude API с observability-by-default; хранение, леджер и агрегации usage/cost —
на стороне потребителя (БД у кита нет).

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/anthropickit"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Быстрый старт

```go
package main

import (
	"context"
	"fmt"

	"git.stit.tech/stit-core/golibinfra/anthropickit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func main() {
	cfg := anthropickit.Config{APIKey: "sk-ant-...", DefaultModel: "claude-opus-4-8"}

	client, err := anthropickit.New(cfg, logkit.Nop())
	if err != nil {
		panic(err)
	}

	resp, err := client.Complete(context.Background(), anthropickit.Request{
		System: anthropickit.SystemText("Ты лаконичный ассистент."),
		Messages: []anthropickit.Message{
			{Role: anthropickit.RoleUser, Content: []anthropickit.Block{
				anthropickit.TextBlock{Text: "Столица Франции?"},
			}},
		},
	})
	if err != nil {
		panic(err)
	}
	if resp.IsRefusal() {
		return // отказ классификатора — не ошибка, это успешный 200
	}
	fmt.Println(resp.Text(), resp.Usage.OutputTokens, resp.Cost)
}
```

Минимальный ENV: `ANTHROPIC_API_KEY`. Подключение через fx — см.
[docs/usage.md](docs/usage.md).

## Документация

- [docs/architecture.md](docs/architecture.md) — устройство, компоненты, поток данных, проектные решения.
- [docs/configuration.md](docs/configuration.md) — полный справочник `Config`/ENV + опции конструктора.
- [docs/observability.md](docs/observability.md) — метрики и спаны (единый источник правды).
- [docs/usage.md](docs/usage.md) — рецепты: fx Module, прямой `New`, стриминг, tools, structured output, Managed Agents через `Raw()`.
- [docs/testing.md](docs/testing.md) — `testkit/anthropictest`: `Stub`, `ScriptStream`, `ErrStream`.
