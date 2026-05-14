// Package flagkit — простейшие feature flags из ENV:
// `FEATURE_FLAGS=foo=true,bar=baz` парсится в Store, который отдаёт
// Bool/String. Без remote-источников и динамического refresh — для
// тех случаев, когда достаточно kill-switch'а на рестарт.
package flagkit

import (
	"strings"
)

type Config struct {
	Definitions string `envconfig:"FEATURE_FLAGS"`
}

type Store interface {
	Bool(name string) bool
	String(name string) string
}

type EnvStore struct {
	flags map[string]string
}

func NewEnvStore(cfg Config) *EnvStore {
	flags := make(map[string]string)
	if cfg.Definitions == "" {
		return &EnvStore{flags: flags}
	}
	for _, pair := range strings.Split(cfg.Definitions, ",") {
		pair = strings.TrimSpace(pair)
		idx := strings.IndexByte(pair, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(pair[:idx])
		val := strings.TrimSpace(pair[idx+1:])
		if key != "" {
			flags[key] = val
		}
	}
	return &EnvStore{flags: flags}
}

func (s *EnvStore) Bool(name string) bool {
	v, ok := s.flags[name]
	if !ok {
		return false
	}
	return strings.EqualFold(v, "true") || v == "1"
}

func (s *EnvStore) String(name string) string {
	return s.flags[name]
}
