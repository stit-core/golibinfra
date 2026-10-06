package envconf_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"reflect"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/amocrmkit"
	"git.stit.tech/stit-core/golibinfra/anthropickit"
	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/authnkit/session"
	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/flagkit"
	"git.stit.tech/stit-core/golibinfra/grpcclient"
	"git.stit.tech/stit-core/golibinfra/grpckit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	hubclient "git.stit.tech/stit-core/golibinfra/hubkit/client"
	"git.stit.tech/stit-core/golibinfra/hubkit/issuer"
	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
	hubsync "git.stit.tech/stit-core/golibinfra/hubkit/sync"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/oidckit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
	outboxpg "git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/storagekit"
	"git.stit.tech/stit-core/golibinfra/telemetry"
	"git.stit.tech/stit-core/golibinfra/tzkit"
)

// kitConfig — конструктор конфига кита: префикс его переменных и sentinel ошибки.
type kitConfig struct {
	name      string
	prefix    string
	errConfig error
	load      func() (any, error)
}

func ctor[T any](f func() (T, error)) func() (any, error) {
	return func() (any, error) { return f() }
}

func kitConfigs() []kitConfig {
	return []kitConfig{
		{"logkit.NewConfig", "", logkit.ErrConfig, ctor(logkit.NewConfig)},
		{"logkit.NewRedactConfig", "", logkit.ErrConfig, ctor(logkit.NewRedactConfig)},
		{"logkit.NewServiceConfig", "", logkit.ErrConfig, ctor(logkit.NewServiceConfig)},
		{"pgkit.NewPoolConfig", "", pgkit.ErrConfig, ctor(pgkit.NewPoolConfig)},
		{"pgkit.NewReplicaConfig", "", pgkit.ErrConfig, ctor(pgkit.NewReplicaConfig)},
		{"pgkit.NewMigrateRunnerConfig", "", pgkit.ErrConfig, ctor(pgkit.NewMigrateRunnerConfig)},
		{"authnkit.NewArgon2idParams", "", authnkit.ErrConfig, ctor(authnkit.NewArgon2idParams)},
		{"authnkit.NewJWTConfig", "", authnkit.ErrConfig, ctor(authnkit.NewJWTConfig)},
		{"authnkit.NewTOTPConfig", "", authnkit.ErrConfig, ctor(authnkit.NewTOTPConfig)},
		{"session.NewConfig", "", session.ErrConfig, ctor(session.NewConfig)},
		{"outboxkit.NewConfig", "", outboxkit.ErrConfig, ctor(outboxkit.NewConfig)},
		{"outboxkit/pgstore.NewConfig", "", outboxkit.ErrConfig, ctor(outboxpg.NewConfig)},
		{"bruteforcekit.NewConfig", "", bruteforcekit.ErrConfig, ctor(bruteforcekit.NewConfig)},
		{"natskit.NewConfig", "", natskit.ErrConfig, ctor(natskit.NewConfig)},
		{"natskit.NewInstanceConfig", "HUB", natskit.ErrConfig, func() (any, error) { return natskit.NewInstanceConfig("hub") }},
		{"cachekit.NewConfig", "", cachekit.ErrConfig, ctor(cachekit.NewConfig)},
		{"httpclient.NewConfig", "", httpclient.ErrConfig, ctor(httpclient.NewConfig)},
		{"httpclient.NewConfigPrefixed", "AMOCRM", httpclient.ErrConfig, func() (any, error) { return httpclient.NewConfigPrefixed("AMOCRM") }},
		{"storagekit.NewConfig", "", storagekit.ErrConfig, ctor(storagekit.NewConfig)},
		{"telemetry.NewConfig", "", telemetry.ErrConfig, ctor(telemetry.NewConfig)},
		{"tzkit.NewConfig", "", tzkit.ErrConfig, ctor(tzkit.NewConfig)},
		{"flagkit.NewConfig", "", flagkit.ErrConfig, ctor(flagkit.NewConfig)},
		{"httpkit.NewConfig", "", httpkit.ErrConfig, ctor(httpkit.NewConfig)},
		{"amocrmkit.NewConfig", "", amocrmkit.ErrConfig, ctor(amocrmkit.NewConfig)},
		{"anthropickit.NewConfig", "", anthropickit.ErrConfig, ctor(anthropickit.NewConfig)},
		{"grpcclient.NewConfig", "", grpcclient.ErrConfig, ctor(grpcclient.NewConfig)},
		{"grpckit.NewConfig", "", grpckit.ErrConfig, ctor(grpckit.NewConfig)},
		{"oidckit.NewConfig", "", oidckit.ErrConfig, ctor(oidckit.NewConfig)},
		{"oidckit.NewCookieConfig", "", oidckit.ErrConfig, ctor(oidckit.NewCookieConfig)},
		{"hubkit/client.NewConfig", "", hubclient.ErrConfig, ctor(hubclient.NewConfig)},
		{"hubkit/sso.NewConfig", "", sso.ErrConfig, ctor(sso.NewConfig)},
		{"hubkit/sync.NewConfig", "", hubsync.ErrConfig, ctor(hubsync.NewConfig)},
		{"hubkit/issuer.NewKeyConfig", "", issuer.ErrConfig, ctor(issuer.NewKeyConfig)},
		{"hubkit/issuer.NewCookieConfig", "", issuer.ErrConfig, ctor(issuer.NewCookieConfig)},
		{"hubkit/issuer.NewRefreshConfig", "", issuer.ErrConfig, ctor(issuer.NewRefreshConfig)},
	}
}

// setRequired задаёт обязательные переменные валидными значениями: без них конструкторы
// отвергают конфиг ещё до проверяемых полей.
func setRequired(t *testing.T) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"PG_DSN":                  "postgres://u:p@localhost:5432/db",
		"JWT_SECRET":              "0123456789abcdef0123456789abcdef",
		"TOTP_ISSUER":             "svc",
		"APP_TZ":                  "Europe/Moscow",
		"OTEL_SERVICE_NAME":       "svc",
		"STORAGE_S3_ENDPOINT":     "localhost:9000",
		"STORAGE_S3_BUCKET":       "b",
		"STORAGE_S3_ACCESS_KEY":   "a",
		"STORAGE_S3_SECRET_KEY":   "s",
		"ANTHROPIC_API_KEY":       "sk-test",
		"GRPC_CLIENT_TARGET":      "localhost:9090",
		"OIDC_ISSUER":             "https://idp.example",
		"OIDC_CLIENT_ID":          "client",
		"OIDC_CLIENT_SECRET":      "secret",
		"OIDC_REDIRECT_URL":       "https://app.example/callback",
		"HUB_BASE_URL":            "https://hub.example",
		"HUB_SERVICE_TOKEN":       "token",
		"HUB_SERVICE_NAME":        "svc",
		"HUB_SSO_JWKS_URL":        "https://id.example/jwks.json",
		"HUB_SSO_AUDIENCE":        "svc",
		"HUB_SESSION_PRIVATE_KEY": string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})),
		"HUB_SESSION_AUDIENCE":    "svc",
	} {
		t.Setenv(k, v)
	}
}

// leaf — поле конфига с тегом envconfig: имя переменной без префикса, default и вид значения.
type leaf struct {
	tag, def string
	kind     reflect.Kind
}

func leaves(t reflect.Type) []leaf {
	var out []leaf
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() || f.Tag.Get("ignored") == "true" {
			continue
		}
		if tag := f.Tag.Get("envconfig"); tag != "" {
			out = append(out, leaf{tag: tag, def: f.Tag.Get("default"), kind: f.Type.Kind()})
			continue
		}
		if f.Type.Kind() == reflect.Struct {
			out = append(out, leaves(f.Type)...)
		}
	}
	return out
}

func envName(prefix, tag string) string {
	if prefix == "" {
		return tag
	}
	return prefix + "_" + tag
}

// Пустая переменная в каждом конфиге кита — как незаданная: Helm `value: ""` и compose без
// подстановки дают дефолт, а не ошибку старта и не молча пустое значение.
func TestKitConfigs_EmptyVariableTakesDefault(t *testing.T) {
	for _, kc := range kitConfigs() {
		t.Run(kc.name, func(t *testing.T) {
			setRequired(t)
			want, err := kc.load()
			if err != nil {
				t.Fatalf("%s with defaults: %v", kc.name, err)
			}
			checked := 0
			for _, l := range leaves(reflect.TypeOf(want)) {
				if l.def == "" {
					continue
				}
				key := envName(kc.prefix, l.tag)
				t.Setenv(key, "")
				got, err := kc.load()
				if err != nil {
					t.Errorf("%s=: %v", key, err)
					continue
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s=: config differs from defaults:\n got %+v\nwant %+v", key, got, want)
				}
				checked++
			}
			t.Logf("%d variables with a default", checked)
		})
	}
}

// Неразбираемое значение — ErrConfig кита с именем переменной.
func TestKitConfigs_ParseErrorIsErrConfigWithVariable(t *testing.T) {
	for _, kc := range kitConfigs() {
		t.Run(kc.name, func(t *testing.T) {
			setRequired(t)
			cfg, err := kc.load()
			if err != nil {
				t.Fatalf("%s with defaults: %v", kc.name, err)
			}
			for _, l := range leaves(reflect.TypeOf(cfg)) {
				switch l.kind {
				case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
					reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
					reflect.Float32, reflect.Float64:
				default:
					continue
				}
				key := envName(kc.prefix, l.tag)
				t.Run(key, func(t *testing.T) {
					t.Setenv(key, "x!")
					_, err := kc.load()
					if !errors.Is(err, kc.errConfig) || !strings.Contains(err.Error(), key+": ") {
						t.Fatalf("%s=x!: %v, want %v naming %s", key, err, kc.errConfig, key)
					}
				})
			}
		})
	}
}
