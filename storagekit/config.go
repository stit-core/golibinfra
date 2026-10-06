package storagekit

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// Источник учётных данных S3.
const (
	// CredentialsStatic — ключи из STORAGE_S3_ACCESS_KEY/STORAGE_S3_SECRET_KEY.
	CredentialsStatic = "static"
	// CredentialsChain — цепочка провайдеров minio-go: AWS/MinIO env, затем IAM (IRSA, EC2/ECS).
	CredentialsChain = "chain"
)

// Способ адресации бакета в запросах к S3.
const (
	// BucketLookupAuto — minio-go выбирает path/dns по эндпоинту.
	BucketLookupAuto = "auto"
	// BucketLookupPath — `<endpoint>/<bucket>/<key>` (MinIO, S3-совместимые без wildcard-DNS).
	BucketLookupPath = "path"
	// BucketLookupDNS — `<bucket>.<endpoint>/<key>` (virtual-hosted style AWS S3).
	BucketLookupDNS = "dns"
)

// Форма публичного URL объекта.
const (
	// URLStylePath — `<PublicBaseURL>/<bucket>/<key>`.
	URLStylePath = "path"
	// URLStyleVirtual — `<PublicBaseURL>/<key>`: бакет уже часть хоста base URL или CDN.
	URLStyleVirtual = "virtual"
)

// Границы PartSize, заданные протоколом S3, и значение по умолчанию.
const (
	// MinPartSize — минимальный размер части multipart-загрузки.
	MinPartSize uint64 = 5 << 20
	// MaxPartSize — максимальный размер части multipart-загрузки.
	MaxPartSize uint64 = 5 << 30
	// DefaultPartSize — часть по умолчанию (16 MiB): столько же памяти стоит
	// один Put с неизвестной длиной тела.
	DefaultPartSize uint64 = 16 << 20
)

// Config — конфигурация боевого S3-адаптера (`storagekit/minio`). Part of public
// API: имена полей и envconfig-теги меняются только мажором. MemoryStorage
// конфига не требует.
type Config struct {
	Endpoint     string `envconfig:"STORAGE_S3_ENDPOINT"`
	AccessKey    string `envconfig:"STORAGE_S3_ACCESS_KEY"`
	SecretKey    string `envconfig:"STORAGE_S3_SECRET_KEY"`
	Credentials  string `envconfig:"STORAGE_S3_CREDENTIALS" default:"static"`
	Bucket       string `envconfig:"STORAGE_S3_BUCKET"`
	Region       string `envconfig:"STORAGE_S3_REGION"`
	UseSSL       bool   `envconfig:"STORAGE_S3_USE_SSL" default:"false"`
	BucketLookup string `envconfig:"STORAGE_S3_BUCKET_LOOKUP" default:"auto"`
	CreateBucket bool   `envconfig:"STORAGE_S3_CREATE_BUCKET" default:"false"`

	// PartSize — размер части multipart-загрузки в байтах: он же верхняя оценка
	// памяти на один Put с неизвестной длиной.
	PartSize   uint64 `envconfig:"STORAGE_S3_PART_SIZE" default:"16777216"`
	MaxRetries int    `envconfig:"STORAGE_S3_MAX_RETRIES" default:"3"`

	ProbeTimeout time.Duration `envconfig:"STORAGE_S3_PROBE_TIMEOUT" default:"5s"`
	ProbeOnStart bool          `envconfig:"STORAGE_S3_PROBE_ON_START" default:"true"`

	URLStyle      string `envconfig:"STORAGE_S3_URL_STYLE" default:"path"`
	PublicBaseURL string `envconfig:"STORAGE_PUBLIC_BASE_URL"`
}

// NewConfig читает Config из ENV и валидирует его: неверная конфигурация валит
// старт приложения, а не первый запрос.
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate проверяет конфигурацию; все ошибки обёрнуты в ErrConfig.
func (c Config) Validate() error {
	if _, _, err := c.EndpointAddr(); err != nil {
		return err
	}
	if strings.TrimSpace(c.Bucket) == "" {
		return fmt.Errorf("%w: STORAGE_S3_BUCKET is required", ErrConfig)
	}
	if strings.ContainsAny(c.Bucket, "/\\ ") {
		return fmt.Errorf("%w: STORAGE_S3_BUCKET %q must be a bucket name, not a path", ErrConfig, c.Bucket)
	}
	switch c.Credentials {
	case CredentialsStatic:
		if (c.AccessKey == "") != (c.SecretKey == "") {
			return fmt.Errorf("%w: STORAGE_S3_ACCESS_KEY and STORAGE_S3_SECRET_KEY must be set together", ErrConfig)
		}
	case CredentialsChain:
	default:
		return fmt.Errorf("%w: STORAGE_S3_CREDENTIALS %q must be %q or %q", ErrConfig, c.Credentials, CredentialsStatic, CredentialsChain)
	}
	switch c.BucketLookup {
	case BucketLookupAuto, BucketLookupPath, BucketLookupDNS:
	default:
		return fmt.Errorf("%w: STORAGE_S3_BUCKET_LOOKUP %q must be one of auto|path|dns", ErrConfig, c.BucketLookup)
	}
	switch c.URLStyle {
	case URLStylePath, URLStyleVirtual:
	default:
		return fmt.Errorf("%w: STORAGE_S3_URL_STYLE %q must be one of path|virtual", ErrConfig, c.URLStyle)
	}
	if c.PartSize < MinPartSize || c.PartSize > MaxPartSize {
		return fmt.Errorf("%w: STORAGE_S3_PART_SIZE %d outside [%d, %d] bytes", ErrConfig, c.PartSize, MinPartSize, MaxPartSize)
	}
	if c.MaxRetries < 0 {
		return fmt.Errorf("%w: STORAGE_S3_MAX_RETRIES %d must be >= 0", ErrConfig, c.MaxRetries)
	}
	if c.ProbeTimeout < 0 {
		return fmt.Errorf("%w: STORAGE_S3_PROBE_TIMEOUT %s must be >= 0", ErrConfig, c.ProbeTimeout)
	}
	if c.PublicBaseURL != "" {
		u, err := url.Parse(c.PublicBaseURL)
		if err != nil {
			return fmt.Errorf("%w: STORAGE_PUBLIC_BASE_URL %q: %w", ErrConfig, c.PublicBaseURL, err)
		}
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%w: STORAGE_PUBLIC_BASE_URL %q must be an absolute http(s) URL", ErrConfig, c.PublicBaseURL)
		}
	}
	return nil
}

// EndpointAddr разбирает STORAGE_S3_ENDPOINT в `host[:port]` и флаг TLS.
// Схема в значении (`https://s3.example.com`) задаёт TLS сама; без схемы TLS
// берётся из STORAGE_S3_USE_SSL.
func (c Config) EndpointAddr() (addr string, secure bool, err error) {
	raw := strings.TrimSpace(c.Endpoint)
	if raw == "" {
		return "", false, fmt.Errorf("%w: STORAGE_S3_ENDPOINT is required", ErrConfig)
	}
	if !strings.Contains(raw, "://") {
		if err := validateHostPort(raw); err != nil {
			return "", false, err
		}
		return raw, c.UseSSL, nil
	}

	u, parseErr := url.Parse(raw)
	if parseErr != nil {
		return "", false, fmt.Errorf("%w: STORAGE_S3_ENDPOINT %q: %w", ErrConfig, raw, parseErr)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false, fmt.Errorf("%w: STORAGE_S3_ENDPOINT %q: scheme must be http or https", ErrConfig, raw)
	}
	if u.Host == "" || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.User != nil {
		return "", false, fmt.Errorf("%w: STORAGE_S3_ENDPOINT %q: expected scheme://host[:port] without path, query or credentials", ErrConfig, raw)
	}
	if err := validateHostPort(u.Host); err != nil {
		return "", false, err
	}
	secure = u.Scheme == "https"
	if c.UseSSL && !secure {
		return "", false, fmt.Errorf("%w: STORAGE_S3_ENDPOINT %q is http, but STORAGE_S3_USE_SSL is true", ErrConfig, raw)
	}
	return u.Host, secure, nil
}

func validateHostPort(hostPort string) error {
	host := hostPort
	if strings.Contains(hostPort, ":") {
		h, port, err := net.SplitHostPort(hostPort)
		if err != nil {
			return fmt.Errorf("%w: STORAGE_S3_ENDPOINT %q: expected host[:port]: %w", ErrConfig, hostPort, err)
		}
		if port == "" {
			return fmt.Errorf("%w: STORAGE_S3_ENDPOINT %q: empty port", ErrConfig, hostPort)
		}
		host = h
	}
	if host == "" || strings.ContainsAny(hostPort, "/?#@ ") {
		return fmt.Errorf("%w: STORAGE_S3_ENDPOINT %q: expected host[:port] without path, query or credentials", ErrConfig, hostPort)
	}
	return nil
}
