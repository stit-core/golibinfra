# config

Загрузка типизированных Config-структур из ENV и/или HashiCorp Vault.

## Идея

Config-структура продукта собирается из нескольких источников последовательно — каждый следующий перекрывает поля предыдущего. Локально работаем на ENV; в проде Vault поверх ENV докладывает секреты.

## Использование

```go
type AppConfig struct {
    pgkit.PoolConfig
    httpkit.Config
    telemetry.Config

    JWTSecret string `envconfig:"JWT_SECRET" vault:"jwt_secret"`
}

vaultClient, _ := config.NewVaultClient(config.VaultClientConfig{
    Addr:  os.Getenv("VAULT_ADDR"),
    Token: os.Getenv("VAULT_TOKEN"),
})

var cfg AppConfig
err := config.Load(ctx, &cfg,
    config.Env(),
    config.Vault(vaultClient, "secret/data/myapp"),
)
```

Если Vault недоступен (`VAULT_ADDR` пуст) — `NewVaultClient` возвращает `nil`, `config.Vault(nil, ...)` no-op, конфиг едет на одних ENV.

## Теги

- `envconfig:"NAME"` — из ENV, формат [kelseyhightower/envconfig](https://github.com/kelseyhightower/envconfig)
- `vault:"key"` — из KV v2 секрета по указанному пути
- Поле может иметь оба тега; vault перекроет env, если ключ присутствует
