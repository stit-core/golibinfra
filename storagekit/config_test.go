package storagekit_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/storagekit"
)

func validConfig() storagekit.Config {
	return storagekit.Config{
		Endpoint:      "s3.example.com:9000",
		AccessKey:     "key",
		SecretKey:     "secret",
		Credentials:   storagekit.CredentialsStatic,
		Bucket:        "media",
		BucketLookup:  storagekit.BucketLookupAuto,
		PartSize:      16 << 20,
		MaxRetries:    3,
		ProbeTimeout:  5 * time.Second,
		URLStyle:      storagekit.URLStylePath,
		PublicBaseURL: "https://cdn.example.com",
	}
}

// Креды из endpoint'а и публичного URL не попадают в текст ошибки конфига — ни у URL, ни у адреса
// без схемы, ни у неразбираемого значения, ни у пароля с `#`, `?` и `цифры/` (B1 валидатора C).
func TestConfig_ErrorsHideURLSecrets(t *testing.T) {
	forms := map[string]string{
		"plain":         "AKIA:PWxsecret",
		"at":            "AKIA:PWx@secret",
		"percent":       "AKIA:PWx%zzsecret",
		"hash":          "AKIA:PWx#secret",
		"question mark": "AKIA:PWx?secret",
		"digits, slash": "AKIA:12/PWxsecret",
	}
	for form, userinfo := range forms {
		for name, mutate := range map[string]func(*storagekit.Config){
			"endpoint url":            func(c *storagekit.Config) { c.Endpoint = "https://" + userinfo + "@minio:9000" },
			"endpoint without scheme": func(c *storagekit.Config) { c.Endpoint = userinfo + "@minio:9000" },
			"public url":              func(c *storagekit.Config) { c.PublicBaseURL = "https://" + userinfo + "@cdn.example.com" },
		} {
			cfg := validConfig()
			mutate(&cfg)
			err := cfg.Validate()
			if !errors.Is(err, storagekit.ErrConfig) || strings.Contains(err.Error(), "PWx") || strings.Contains(err.Error(), "secret") ||
				!strings.Contains(err.Error(), "xxxxx@") {
				t.Errorf("%s, %s: Validate() = %v, want ErrConfig with the redacted value", name, form, err)
			}
		}
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*storagekit.Config)
		wantErr bool
	}{
		{name: "valid host:port"},
		{name: "valid host", mutate: func(c *storagekit.Config) { c.Endpoint = "s3.example.com" }},
		{name: "valid https url", mutate: func(c *storagekit.Config) { c.Endpoint = "https://s3.example.com" }},
		{name: "valid http url", mutate: func(c *storagekit.Config) { c.Endpoint = "http://localhost:9000" }},
		{name: "valid anonymous creds", mutate: func(c *storagekit.Config) { c.AccessKey, c.SecretKey = "", "" }},
		{name: "valid chain creds", mutate: func(c *storagekit.Config) {
			c.Credentials, c.AccessKey, c.SecretKey = storagekit.CredentialsChain, "", ""
		}},
		{name: "valid empty public base url", mutate: func(c *storagekit.Config) { c.PublicBaseURL = "" }},
		{name: "valid virtual url style", mutate: func(c *storagekit.Config) { c.URLStyle = storagekit.URLStyleVirtual }},

		{name: "empty endpoint", wantErr: true, mutate: func(c *storagekit.Config) { c.Endpoint = "" }},
		{name: "endpoint with path", wantErr: true, mutate: func(c *storagekit.Config) { c.Endpoint = "https://s3.example.com/bucket" }},
		{name: "endpoint with query", wantErr: true, mutate: func(c *storagekit.Config) { c.Endpoint = "https://s3.example.com?a=b" }},
		{name: "endpoint with creds", wantErr: true, mutate: func(c *storagekit.Config) { c.Endpoint = "https://user:pass@s3.example.com" }},
		{name: "endpoint with unknown scheme", wantErr: true, mutate: func(c *storagekit.Config) { c.Endpoint = "s3://s3.example.com" }},
		{name: "endpoint slash without scheme", wantErr: true, mutate: func(c *storagekit.Config) { c.Endpoint = "s3.example.com/bucket" }},
		{name: "endpoint empty port", wantErr: true, mutate: func(c *storagekit.Config) { c.Endpoint = "s3.example.com:" }},
		{name: "endpoint with two ports", wantErr: true, mutate: func(c *storagekit.Config) { c.Endpoint = "s3.example.com:90:00" }},
		{name: "endpoint host is only a port", wantErr: true, mutate: func(c *storagekit.Config) { c.Endpoint = "http://:9000" }},
		{name: "unparsable endpoint url", wantErr: true, mutate: func(c *storagekit.Config) { c.Endpoint = "https://s3.example%zz.com" }},
		{name: "empty bucket", wantErr: true, mutate: func(c *storagekit.Config) { c.Bucket = "" }},
		{name: "bucket with path", wantErr: true, mutate: func(c *storagekit.Config) { c.Bucket = "media/uploads" }},
		{name: "half creds", wantErr: true, mutate: func(c *storagekit.Config) { c.SecretKey = "" }},
		{name: "unknown creds source", wantErr: true, mutate: func(c *storagekit.Config) { c.Credentials = "iam" }},
		{name: "unknown bucket lookup", wantErr: true, mutate: func(c *storagekit.Config) { c.BucketLookup = "virtual" }},
		{name: "unknown url style", wantErr: true, mutate: func(c *storagekit.Config) { c.URLStyle = "cdn" }},
		{name: "part size below minimum", wantErr: true, mutate: func(c *storagekit.Config) { c.PartSize = 1 << 20 }},
		{name: "part size above maximum", wantErr: true, mutate: func(c *storagekit.Config) { c.PartSize = 6 << 30 }},
		{name: "negative retries", wantErr: true, mutate: func(c *storagekit.Config) { c.MaxRetries = -1 }},
		{name: "negative probe timeout", wantErr: true, mutate: func(c *storagekit.Config) { c.ProbeTimeout = -time.Second }},
		{name: "relative public base url", wantErr: true, mutate: func(c *storagekit.Config) { c.PublicBaseURL = "cdn.example.com" }},
		{name: "unparsable public base url", wantErr: true, mutate: func(c *storagekit.Config) { c.PublicBaseURL = "https://cdn.example.com/%zz" }},
		{name: "public base url with creds", wantErr: true, mutate: func(c *storagekit.Config) { c.PublicBaseURL = "https://u:p@cdn.example.com" }},
		{name: "zero value", wantErr: true, mutate: func(c *storagekit.Config) { *c = storagekit.Config{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			err := cfg.Validate()
			if tt.wantErr {
				if !errors.Is(err, storagekit.ErrConfig) {
					t.Fatalf("Validate() = %v, want ErrConfig", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestConfig_EndpointAddr(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   string
		wantAddr   string
		wantSecure bool
	}{
		{name: "host:port is TLS", endpoint: "s3.example.com:9000", wantAddr: "s3.example.com:9000", wantSecure: true},
		{name: "host is TLS", endpoint: "s3.example.com", wantAddr: "s3.example.com", wantSecure: true},
		{name: "https scheme", endpoint: "https://s3.example.com", wantAddr: "s3.example.com", wantSecure: true},
		{name: "plaintext only with http scheme", endpoint: "http://localhost:9000", wantAddr: "localhost:9000"},
		{name: "trailing slash allowed", endpoint: "https://s3.example.com/", wantAddr: "s3.example.com", wantSecure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Endpoint = tt.endpoint

			addr, secure, err := cfg.EndpointAddr()
			if err != nil {
				t.Fatalf("EndpointAddr: %v", err)
			}
			if addr != tt.wantAddr || secure != tt.wantSecure {
				t.Fatalf("EndpointAddr = %q,%v want %q,%v", addr, secure, tt.wantAddr, tt.wantSecure)
			}
		})
	}
}

func TestNewConfig_DefaultsAndValidation(t *testing.T) {
	for _, env := range []string{
		"STORAGE_S3_ENDPOINT", "STORAGE_S3_ACCESS_KEY", "STORAGE_S3_SECRET_KEY", "STORAGE_S3_BUCKET",
		"STORAGE_S3_CREDENTIALS", "STORAGE_S3_REGION", "STORAGE_S3_BUCKET_LOOKUP",
		"STORAGE_S3_CREATE_BUCKET", "STORAGE_S3_PART_SIZE", "STORAGE_S3_MAX_RETRIES", "STORAGE_S3_PROBE_TIMEOUT",
		"STORAGE_S3_PROBE_ON_START", "STORAGE_S3_URL_STYLE", "STORAGE_PUBLIC_BASE_URL",
	} {
		// envconfig берёт default только у неустановленной переменной, поэтому
		// пустое значение не годится: t.Setenv лишь регистрирует восстановление.
		t.Setenv(env, "")
		if err := os.Unsetenv(env); err != nil {
			t.Fatalf("unset %s: %v", env, err)
		}
	}
	t.Setenv("STORAGE_S3_ENDPOINT", "localhost:9000")
	t.Setenv("STORAGE_S3_BUCKET", "media")

	cfg, err := storagekit.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Credentials != storagekit.CredentialsStatic || cfg.BucketLookup != storagekit.BucketLookupAuto {
		t.Errorf("credentials/lookup defaults = %q/%q", cfg.Credentials, cfg.BucketLookup)
	}
	if cfg.PartSize != 16<<20 || cfg.MaxRetries != 3 {
		t.Errorf("PartSize/MaxRetries defaults = %d/%d", cfg.PartSize, cfg.MaxRetries)
	}
	if cfg.ProbeTimeout != 5*time.Second || !cfg.ProbeOnStart || cfg.CreateBucket {
		t.Errorf("probe defaults = %s/%v, CreateBucket = %v", cfg.ProbeTimeout, cfg.ProbeOnStart, cfg.CreateBucket)
	}
	if cfg.URLStyle != storagekit.URLStylePath {
		t.Errorf("URLStyle default = %q", cfg.URLStyle)
	}

	t.Setenv("STORAGE_S3_BUCKET", "")
	if _, err := storagekit.NewConfig(); !errors.Is(err, storagekit.ErrConfig) {
		t.Fatalf("NewConfig without bucket = %v, want ErrConfig", err)
	}
	t.Setenv("STORAGE_S3_BUCKET", "media")
	t.Setenv("STORAGE_S3_MAX_RETRIES", "not-a-number")
	if _, err := storagekit.NewConfig(); !errors.Is(err, storagekit.ErrConfig) {
		t.Fatalf("NewConfig with bad retries = %v, want ErrConfig", err)
	}
}
