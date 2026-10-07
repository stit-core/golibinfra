package minio_test

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	miniogo "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/storagekit"
	skminio "git.stit.tech/stit-core/golibinfra/storagekit/minio"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
)

// proxyTo — прокси перед контейнером MinIO: подпись SigV4 накрывает Host, а
// ReverseProxy отдаёт входящий Host дальше, поэтому запрос остаётся валидным.
// reject решает, какие запросы отбить, не доходя до MinIO.
func proxyTo(t *testing.T, endpoint string, tlsProxy bool, reject func(*http.Request) bool) *httptest.Server {
	t.Helper()
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	target, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse minio endpoint %q: %v", endpoint, err)
	}

	rp := httputil.NewSingleHostReverseProxy(target)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reject != nil && reject(r) {
			http.Error(w, "proxy rejected the request", http.StatusInternalServerError)
			return
		}
		rp.ServeHTTP(w, r)
	})

	srv := httptest.NewUnstartedServer(h)
	if tlsProxy {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Host
}

// Объект, отвергнутый по расхождению size, кит пытается удалить; провалившееся
// удаление обязано быть видно в логе — иначе мусор в бакете никто не заметит.
func TestStorage_CleanupFailureIsLogged(t *testing.T) {
	fix := minioFixture(t)
	proxy := proxyTo(t, fix.Endpoint, false, func(r *http.Request) bool {
		return r.Method == http.MethodDelete
	})

	cfg := containerConfig(t, func(c *storagekit.Config) { c.Endpoint = proxy.URL })
	client, err := skminio.NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.MakeBucket(t.Context(), cfg.Bucket, miniogo.MakeBucketOptions{}); err != nil {
		t.Fatalf("make bucket: %v", err)
	}

	rec := logtest.Capture()
	urls := storagekit.NewURLBuilder(cfg.PublicBaseURL, cfg.Bucket, cfg.URLStyle)
	st := mustStorage(t, client, cfg, urls, rec)

	_, err = st.Put(t.Context(), stream(bytes.NewReader([]byte("0123456789"))), "application/pdf", 4)
	if !errors.Is(err, storagekit.ErrSizeMismatch) {
		t.Fatalf("Put with a longer body = %v, want ErrSizeMismatch", err)
	}

	e, ok := rec.Find(slog.LevelError, "storagekit: rejected upload left behind")
	if !ok || e.Str("bucket") != cfg.Bucket || e.Str("key") == "" || e.Str("error") == "" {
		t.Fatalf("failed cleanup must be logged with bucket, key and error, got %+v ok=%v", e, ok)
	}
}

// Отказ S3 на записи — ошибка Put, а не частично загруженный объект: обе ветки
// (известная длина и поток) заворачиваются в ErrUploadFailed.
func TestStorage_PutFailureIsWrapped(t *testing.T) {
	fix := minioFixture(t)
	direct := containerConfig(t)
	client, err := skminio.NewClient(direct)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.MakeBucket(t.Context(), direct.Bucket, miniogo.MakeBucketOptions{}); err != nil {
		t.Fatalf("make bucket: %v", err)
	}

	proxy := proxyTo(t, fix.Endpoint, false, func(r *http.Request) bool {
		return r.Method == http.MethodPut || r.Method == http.MethodPost
	})
	cfg := direct
	cfg.Endpoint = proxy.URL
	blocked, err := skminio.NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient through proxy: %v", err)
	}
	urls := storagekit.NewURLBuilder(cfg.PublicBaseURL, cfg.Bucket, cfg.URLStyle)
	st := mustStorage(t, blocked, cfg, urls, nil)

	sizes := map[string]int64{"unknown size": -1, "known size": 4}
	for name, size := range sizes {
		t.Run(name, func(t *testing.T) {
			_, err := st.Put(t.Context(), stream(bytes.NewReader([]byte("body"))), "application/pdf", size)
			if !errors.Is(err, storagekit.ErrUploadFailed) {
				t.Fatalf("Put = %v, want ErrUploadFailed", err)
			}
			if errors.Is(err, storagekit.ErrSizeMismatch) {
				t.Fatalf("Put = %v, want a transport failure, not a size mismatch", err)
			}
		})
	}
}

// Бакет, который не удалось создать, валит старт: приложение не должно работать
// с хранилищем, которого нет.
func TestModule_CreateBucketFailureFailsStart(t *testing.T) {
	fix := minioFixture(t)
	proxy := proxyTo(t, fix.Endpoint, false, func(r *http.Request) bool {
		return r.Method == http.MethodPut
	})
	cfg := containerConfig(t, func(c *storagekit.Config) {
		c.Endpoint = proxy.URL
		c.CreateBucket = true
	})

	app := fx.New(appOptions(cfg))
	err := app.Start(t.Context())
	if err == nil {
		_ = app.Stop(t.Context())
		t.Fatal("start with a bucket that cannot be created = nil, want an error")
	}
	if !strings.Contains(err.Error(), "create bucket") {
		t.Fatalf("start error = %v, want it to name the failed bucket creation", err)
	}
}

// Данные кита ходят по TLS так же, как по plaintext: endpoint без схемы — TLS, и это
// проверяется на живом сервере, а не только сборкой клиента.
func TestStorage_ServesOverTLS(t *testing.T) {
	fix := minioFixture(t)
	proxy := proxyTo(t, fix.Endpoint, true, nil)

	pool := x509.NewCertPool()
	pool.AddCert(proxy.Certificate())
	cfg := containerConfig(t, func(c *storagekit.Config) {
		c.Endpoint = hostOf(t, proxy.URL)
	})
	if _, secure, err := cfg.EndpointAddr(); err != nil || !secure {
		t.Fatalf("EndpointAddr() secure = %v, err = %v, want secure", secure, err)
	}

	// Клиент собирается напрямую: доверие к сертификату теста задаётся
	// транспортом, а NewClient берёт системный пул.
	client, err := miniogo.New(cfg.Endpoint, &miniogo.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       true,
		Region:       cfg.Region,
		BucketLookup: miniogo.BucketLookupPath,
		Transport:    &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	})
	if err != nil {
		t.Fatalf("minio client over TLS: %v", err)
	}
	if err := client.MakeBucket(t.Context(), cfg.Bucket, miniogo.MakeBucketOptions{}); err != nil {
		t.Fatalf("make bucket over TLS: %v", err)
	}

	urls := storagekit.NewURLBuilder(cfg.PublicBaseURL, cfg.Bucket, cfg.URLStyle)
	st := mustStorage(t, client, cfg, urls, logkit.Nop())
	body := []byte("tls round trip")

	ref, err := st.Put(t.Context(), bytes.NewReader(body), "text/plain", int64(len(body)))
	if err != nil {
		t.Fatalf("Put over TLS: %v", err)
	}
	rc, info, err := st.Get(t.Context(), ref.Key)
	if err != nil {
		t.Fatalf("Get over TLS: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("close object: %v", err)
	}
	if !bytes.Equal(got, body) || info.Size != int64(len(body)) {
		t.Fatalf("object over TLS = %q (size %d), want %q", got, info.Size, body)
	}

	link, err := st.PresignGet(t.Context(), ref.Key, time.Minute)
	if err != nil {
		t.Fatalf("PresignGet over TLS: %v", err)
	}
	if !strings.HasPrefix(link, "https://") {
		t.Fatalf("presigned link = %q, want an https URL", link)
	}

	if err := st.Delete(t.Context(), ref.Key); err != nil {
		t.Fatalf("Delete over TLS: %v", err)
	}
	if _, err := st.Stat(t.Context(), ref.Key); !errors.Is(err, storagekit.ErrNotFound) {
		t.Fatalf("Stat after Delete = %v, want ErrNotFound", err)
	}
}
