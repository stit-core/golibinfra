package config

import "go.uber.org/fx"

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
