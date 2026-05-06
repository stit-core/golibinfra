package config

import (
	"context"
	"fmt"
	"reflect"

	vault "github.com/hashicorp/vault/api"
)

type VaultClientConfig struct {
	Addr      string `envconfig:"VAULT_ADDR"`
	Token     string `envconfig:"VAULT_TOKEN"`
	Namespace string `envconfig:"VAULT_NAMESPACE"`
}

func NewVaultClient(cfg VaultClientConfig) (*vault.Client, error) {
	if cfg.Addr == "" {
		return nil, nil
	}
	c, err := vault.NewClient(&vault.Config{Address: cfg.Addr})
	if err != nil {
		return nil, fmt.Errorf("config: vault client: %w", err)
	}
	if cfg.Token != "" {
		c.SetToken(cfg.Token)
	}
	if cfg.Namespace != "" {
		c.SetNamespace(cfg.Namespace)
	}
	return c, nil
}

type vaultSource struct {
	client *vault.Client
	path   string
}

func Vault(client *vault.Client, path string) Source {
	return &vaultSource{client: client, path: path}
}

func (s *vaultSource) Apply(ctx context.Context, dst any) error {
	if s.client == nil {
		return nil
	}
	secret, err := s.client.KVv2(splitMount(s.path)).Get(ctx, splitKey(s.path))
	if err != nil {
		return fmt.Errorf("config: vault get %s: %w", s.path, err)
	}
	if secret == nil || secret.Data == nil {
		return fmt.Errorf("config: vault: empty secret at %s", s.path)
	}
	return applyVaultData(secret.Data, dst)
}

func applyVaultData(data map[string]any, dst any) error {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("config: vault: dst must be *struct")
	}
	v = v.Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		key := t.Field(i).Tag.Get("vault")
		if key == "" {
			continue
		}
		raw, ok := data[key]
		if !ok {
			continue
		}
		s, ok := raw.(string)
		if !ok {
			return fmt.Errorf("config: vault: key %s is not a string", key)
		}
		field := v.Field(i)
		if !field.CanSet() || field.Kind() != reflect.String {
			return fmt.Errorf("config: vault: field for %s must be settable string", key)
		}
		field.SetString(s)
	}
	return nil
}

func splitMount(path string) string {
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return path
}

func splitKey(path string) string {
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return ""
}
