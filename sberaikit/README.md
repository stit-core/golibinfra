# sberaikit

Клиент Sber GigaChat на raw `net/http` (без SDK): синхронный (`Chat`) и
стриминговый (`ChatStream`, SSE + `[DONE]`) chat, `Embeddings`, image-as-file
(`GenerateImage`: built-in `text2image` → `<img src="file-id"/>` → `FileContent`),
нативный passthrough (`Raw`). Нормализованный учёт токенов (`Usage`) и посчитанная
стоимость (`Cost` в микро-рублях) по прайс-таблице. Кит даёт сервисам единый,
декаплированный вход в GigaChat с observability-by-default; хранение, леджер и
агрегации usage/cost — на стороне потребителя (БД у кита нет).

Особенности GigaChat, инкапсулированные китом:

- **OAuth2 client-credentials** — `Authorization: Basic <SBER_AUTH_KEY>` + свежий
  `RqUID` (UUIDv4) на каждый запрос → `access_token` (~30 мин). Проактивный
  рефреш по `clockkit` со скосом 60s, single-flight, один 401→refresh-retry.
- **Минцифры-CA (Russian Trusted Root CA)** — TLS со **scoped `RootCAs`** (root +
  sub из встроенного bundle либо `SBER_CA_BUNDLE_PATH`), системные корни
  сохранены. Пул живёт только в выделенном `http.Client` кита — **никогда** в
  глобальном trust-store, **никогда** `InsecureSkipVerify`.
- **Стрим** — SSE, терминатор `data: [DONE]`; `usage` снимается из финального
  чанка перед `[DONE]`.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/sberaikit"
```

## Быстрый старт

```go
package main

import (
	"context"
	"fmt"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/sberaikit"
)

func main() {
	cfg := sberaikit.Config{
		AuthKey:      "<base64(client_id:client_secret)>",
		Scope:        "GIGACHAT_API_PERS",
		DefaultModel: "GigaChat-2",
	}

	client, err := sberaikit.New(cfg, logkit.Nop(), clockkit.Real{})
	if err != nil {
		panic(err)
	}

	resp, err := client.Chat(context.Background(), sberaikit.ChatRequest{
		Messages: []sberaikit.Message{
			{Role: sberaikit.RoleUser, Content: "Столица России?"},
		},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(resp.Text(), resp.Usage.OutputTokens, resp.Cost) // Cost — микро-RUB
}
```

Минимальный ENV: `SBER_AUTH_KEY`. Полный контракт — в `Config` (`config.go`).

## Конфигурация (ENV / Vault)

| ENV | Назначение | Default |
|---|---|---|
| `SBER_AUTH_KEY` | Authorization key = `base64(client_id:client_secret)` (vault: `sber_auth_key`) | — (required) |
| `SBER_SCOPE` | тариф: `GIGACHAT_API_PERS` / `_B2B` / `_CORP` | `GIGACHAT_API_PERS` |
| `SBER_BASE_URL` | API plane | `https://gigachat.devices.sberbank.ru/api/v1` |
| `SBER_OAUTH_URL` | OAuth plane (порт 9443) | `https://ngw.devices.sberbank.ru:9443/api/v2/oauth` |
| `SBER_DEFAULT_MODEL` | модель по умолчанию | `GigaChat-2` |
| `SBER_CA_BUNDLE_PATH` | путь к PEM Минцифры-CA (root+sub); пусто → встроенный bundle | `""` |
| `SBER_TIMEOUT` | таймаут HTTP | `60s` |
| `SBER_MAX_RETRIES` | ретраи на 429/5xx | `3` |

Опции конструктора (`options.go`): `WithHTTPClient` (тест-сервер/прокси —
**внимание**: подменяет scoped Минцифры-CA транспорт), `WithPriceTable` (наценка/
новые модели), `WithBaseURL` (stub-сервер в тестах).

## Учёт (Usage → Consumption)

Нормализация по `ai-gateway-template/docs/domains/_shared/accounting-ports.md`:
`prompt_tokens → InputTokens`, `completion_tokens → OutputTokens`,
`precached_prompt_tokens → CachedTokens` (подмножество входа), `total_tokens →
TotalTokens`. Неизвестные usage-ключи удерживаются в `Usage.Extra` (forward-
совместимость биллинга). Стоимость — `Cost(model, usage)` (RUB-микро) или своя
таблица через `WithPriceTable`.

## Метрики

`sber_api_calls_total{op,model,status}`, `sber_api_latency_seconds{op,model}`,
`sber_api_tokens_total{model,kind}`, `sber_api_cost_micros_total{model}`. Meter —
`git.stit.tech/stit-core/golibinfra/sberaikit`.

## Тесты

`testkit/sbertest`: `Stub` (реализация `sberaikit.API`), `ScriptStream`,
`ErrStream` — тест-двойники без сети.
