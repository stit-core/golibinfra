package config

import (
	vault "github.com/hashicorp/vault/api"
	"go.uber.org/fx"
)

var Module = fx.Module("config",
	fx.Provide(
		newVaultClientConfig,
		newVaultPathConfig,
		NewVaultClient,
		newSources,
	),
)

func newVaultClientConfig() (VaultClientConfig, error) {
	var c VaultClientConfig
	if err := Env().Apply(nil, &c); err != nil {
		return c, err
	}
	return c, nil
}

type VaultPathConfig struct {
	Path string `envconfig:"VAULT_PATH"`
}

func newVaultPathConfig() (VaultPathConfig, error) {
	var c VaultPathConfig
	if err := Env().Apply(nil, &c); err != nil {
		return c, err
	}
	return c, nil
}

func newSources(client *vault.Client, vp VaultPathConfig) []Source {
	srcs := []Source{Env()}
	if client != nil && vp.Path != "" {
		srcs = append(srcs, Vault(client, vp.Path))
	}
	return srcs
}
