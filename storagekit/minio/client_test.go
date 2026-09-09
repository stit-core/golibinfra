package minio_test

import (
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/storagekit"
	skminio "git.stit.tech/stit-core/golibinfra/storagekit/minio"
)

func unitConfig() storagekit.Config {
	return storagekit.Config{
		Endpoint:     "s3.example.com:9000",
		AccessKey:    "key",
		SecretKey:    "secret",
		Credentials:  storagekit.CredentialsStatic,
		Bucket:       "media",
		Region:       "us-east-1",
		BucketLookup: storagekit.BucketLookupAuto,
		PartSize:     16 << 20,
		MaxRetries:   3,
		ProbeTimeout: 5 * time.Second,
		URLStyle:     storagekit.URLStylePath,
	}
}

func TestNewClient_RejectsInvalidConfig(t *testing.T) {
	tests := map[string]func(*storagekit.Config){
		"no endpoint":         func(c *storagekit.Config) { c.Endpoint = "" },
		"no bucket":           func(c *storagekit.Config) { c.Bucket = "" },
		"endpoint with path":  func(c *storagekit.Config) { c.Endpoint = "https://s3.example.com/media" },
		"unknown credentials": func(c *storagekit.Config) { c.Credentials = "iam" },
		"tiny part size":      func(c *storagekit.Config) { c.PartSize = 1 << 10 },
		"negative retries":    func(c *storagekit.Config) { c.MaxRetries = -1 },
		// Хост проходит проверку кита, но minio-go знает про AWS больше: бакет в
		// имени хоста — частая опечатка в STORAGE_S3_ENDPOINT.
		"bucket in amazon endpoint": func(c *storagekit.Config) { c.Endpoint = "media.s3.amazonaws.com" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := unitConfig()
			mutate(&cfg)

			client, err := skminio.NewClient(cfg)
			if !errors.Is(err, storagekit.ErrConfig) {
				t.Fatalf("NewClient = %v, want ErrConfig", err)
			}
			if client != nil {
				t.Fatal("NewClient returned a client along with an error")
			}
		})
	}
}

func TestNewClient_EndpointAndTLS(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   string
		useSSL     bool
		wantScheme string
		wantHost   string
	}{
		{name: "plain host:port", endpoint: "s3.example.com:9000", wantScheme: "http", wantHost: "s3.example.com:9000"},
		{name: "use ssl flag", endpoint: "s3.example.com:9000", useSSL: true, wantScheme: "https", wantHost: "s3.example.com:9000"},
		{name: "https url", endpoint: "https://s3.example.com", wantScheme: "https", wantHost: "s3.example.com"},
		{name: "http url", endpoint: "http://localhost:9000", wantScheme: "http", wantHost: "localhost:9000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := unitConfig()
			cfg.Endpoint, cfg.UseSSL = tt.endpoint, tt.useSSL

			client, err := skminio.NewClient(cfg)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			u := client.EndpointURL()
			if u.Scheme != tt.wantScheme || u.Host != tt.wantHost {
				t.Fatalf("endpoint = %s://%s, want %s://%s", u.Scheme, u.Host, tt.wantScheme, tt.wantHost)
			}
		})
	}
}

func TestNewClient_CredentialsAndLookup(t *testing.T) {
	for _, creds := range []string{storagekit.CredentialsStatic, storagekit.CredentialsChain} {
		for _, lookup := range []string{storagekit.BucketLookupAuto, storagekit.BucketLookupPath, storagekit.BucketLookupDNS} {
			cfg := unitConfig()
			cfg.Credentials, cfg.BucketLookup = creds, lookup
			if creds == storagekit.CredentialsChain {
				cfg.AccessKey, cfg.SecretKey = "", ""
			}

			client, err := skminio.NewClient(cfg)
			if err != nil || client == nil {
				t.Fatalf("NewClient(creds=%s, lookup=%s): %v", creds, lookup, err)
			}
		}
	}
}
