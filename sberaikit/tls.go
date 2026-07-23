package sberaikit

import (
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"fmt"
	"os"
)

// russianTrustedCAPEM — встроенный bundle Минцифры-CA (Russian Trusted Root CA +
// Sub CA, НУЦ Минцифры). GigaChat/OAuth-хосты подписаны цепочкой, которой нет ни
// в одном западном trust-store; без неё каждый вызов падает x509 unknown
// authority. Bundle версионируется в репо как immutable-артефакт (review +
// rotation при переиздании Минцифры). Переопределяется Config.CABundlePath.
//
//go:embed ca/russian_trusted_ca_bundle.pem
var russianTrustedCAPEM []byte

// newCertPool собирает scoped RootCAs ТОЛЬКО для egress на Sber-хосты: системные
// корни + Минцифры-CA. Пул отдаётся в выделенный http.Transport (newHTTPClient),
// НИКОГДА не в глобальный trust-store и НИКОГДА не InsecureSkipVerify —
// гос-контролируемый CA не должен ручаться за произвольные домены всему процессу.
//
// Источник CA: Config.CABundlePath (если задан) либо встроенный bundle. Системные
// корни сохраняем (AppendCerts), чтобы прокси/корп-MITM на пути тоже валидировался.
func newCertPool(caBundlePath string) (*x509.CertPool, error) {
	caPEM := russianTrustedCAPEM
	if caBundlePath != "" {
		b, err := os.ReadFile(caBundlePath) //nolint:gosec // путь из доверенного Config (env), не пользовательский ввод
		if err != nil {
			return nil, fmt.Errorf("%w: read CA bundle %s: %w", ErrConfig, caBundlePath, err)
		}
		caPEM = b
	}
	if len(caPEM) == 0 {
		return nil, fmt.Errorf("%w: empty Russian Trusted CA bundle", ErrConfig)
	}

	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%w: failed to append Russian Trusted CA bundle", ErrConfig)
	}
	return pool, nil
}

// newTLSConfig — TLS-конфиг для Sber-транспорта со scoped RootCAs (Минцифры-CA +
// системные). MinVersion TLS 1.2. InsecureSkipVerify здесь невозможен by design.
func newTLSConfig(caBundlePath string) (*tls.Config, error) {
	pool, err := newCertPool(caBundlePath)
	if err != nil {
		return nil, err
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}
