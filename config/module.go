package config

import "go.uber.org/fx"

// Module предоставляет VaultClientConfig из ENV и опциональный *vault.Client.
// Сам тип Config продукта собирается в его модуле, не здесь — config-пакет даёт только примитивы.
var Module = fx.Module("config",
	fx.Provide(
		newVaultClientConfig,
		NewVaultClient,
	),
)

func newVaultClientConfig() (VaultClientConfig, error) {
	var c VaultClientConfig
	if err := Env().Apply(nil, &c); err != nil {
		return c, err
	}
	return c, nil
}
