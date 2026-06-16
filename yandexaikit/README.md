# yandexaikit

Клиент Yandex Cloud Foundation Models (AI Studio) поверх **raw REST** (golibinfra
[`httpclient`](../httpclient), без SDK): синхронный (`Completion`) и стриминговый
(`Stream`, **cumulative**) completion, отложенный completion (`CompletionAsync` →
`AwaitCompletion`, LRO), text embeddings (`TextEmbedding`), YandexART
(`ImageGenerationAsync` → `AwaitImage`, LRO) и нативный passthrough (`Raw`).
Нормализованный учёт токенов (`Usage`) и подсчитанная стоимость (`Cost` в
**микро-рублях**) по прайс-таблице. Хранение, леджер и агрегации usage/cost — на
стороне потребителя (БД у кита нет).

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/yandexaikit"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Auth (две схемы)

Выбор по `Config` — `Api-Key` приоритетнее:

- **Api-Key** — статичный ключ сервис-аккаунта (`Authorization: Api-Key <key>`).
  `YANDEX_FOLDER_ID` **запрещён** (каталог уже в ключе).
- **IAM-token** — `Authorization: Bearer <iam>`, где IAM добывается обменом
  **PS256-JWT** авторизованного ключа сервис-аккаунта на `iam.api.cloud.yandex.net`,
  кэшируется и проактивно обновляется по `clockkit` (TTL ≤12h, refresh за 5 мин до
  истечения). `YANDEX_FOLDER_ID` (`x-folder-id`) **обязателен**.

## Быстрый старт

```go
package main

import (
	"context"
	"fmt"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/yandexaikit"
)

func main() {
	hc := httpclient.New(httpclient.Config{})
	cfg := yandexaikit.Config{APIKey: "AQVN...", DefaultModel: "yandexgpt"}

	client, err := yandexaikit.New(cfg, hc, clockkit.Real{}, nil)
	if err != nil {
		panic(err)
	}

	resp, err := client.Completion(context.Background(), yandexaikit.Request{
		Messages: []yandexaikit.Message{
			{Role: yandexaikit.RoleUser, Text: "Столица Франции?"},
		},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(resp.Text(), resp.Usage.OutputTokens, resp.Cost) // Cost в микро-рублях
}
```

Минимальный ENV: `YANDEX_API_KEY` (Api-Key) **или** `YANDEX_FOLDER_ID` +
`YANDEX_SERVICE_ACCOUNT_{ID,KEY_ID,PRIVATE_KEY}` (IAM). Подключение через fx —
`yandexaikit.Module` + `fx.Provide(yandexaikit.NewConfig)` (забирает из графа
`*httpclient.Client`, `clockkit.Clock`, `logkit.Logger`).

## Async (LRO)

`CompletionAsync` / `ImageGenerationAsync` возвращают operation-id. `AwaitCompletion`
/ `AwaitImage` поллят `operation.api.cloud.yandex.net` (другой хост!) до `done` с
экспоненциальным бэкоффом по `clockkit` (`WithPollConfig` настраивает интервалы и
бюджет). Для пер-поллинга на стороне gateway — `PollOperation` (один проход).

## Учёт (Usage / Cost)

Нативные `inputTextTokens`/`completionTokens`/`reasoningTokens` — **int64 как
JSON-строки** — парсятся в `Usage{InputTokens, OutputTokens, ReasoningTokens}`
(инвариант `ReasoningTokens ⊆ OutputTokens`). Embeddings → `InputTokens=numTokens`;
YandexART → `Images=1` (без токенов). Валюта `Cost` — **RUB** (в микро-рублях).
Async-вызовы тарифицируются по отдельной (≈½) таблице. Своя таблица/наценка —
`WithPriceTable` / `WithAsyncPriceTable`; `Cost`/`CostAsync` — без `Client`.

## Тестирование

`testkit/yandextest`: `Stub` (реализация `API` с `*Func`-полями), `ScriptStream`,
`ErrStream` — всё без сети. Юнит-тесты кита гоняют клиент против `httptest.Server`.
