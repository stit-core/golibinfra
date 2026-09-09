package hubtest

import (
	"maps"
	"net/http"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
)

// TokenOption — правка claims выпускаемого токена.
type TokenOption func(*tokenSpec)

type tokenSpec struct {
	claims map[string]any
	drop   []string
	issued time.Time
	ttl    time.Duration
	expiry *time.Time
	kid    string
}

// WithSubject задаёт sub токена.
func WithSubject(sub string) TokenOption { return WithClaim("sub", sub) }

// WithSID задаёт claim sid (идентификатор сессии).
func WithSID(sid string) TokenOption { return WithClaim(sso.ClaimSID, sid) }

// WithEmployeeID задаёт claim employee_id.
func WithEmployeeID(id string) TokenOption { return WithClaim(sso.ClaimEmployeeID, id) }

// WithRole задаёт claim role.
func WithRole(role string) TokenOption { return WithClaim(sso.ClaimRole, role) }

// WithCompanyID задаёт claim company_id (контур заказчика).
func WithCompanyID(id string) TokenOption { return WithClaim(sso.ClaimCompanyID, id) }

// WithAccountRole задаёт claim account_role (контур заказчика).
func WithAccountRole(role string) TokenOption { return WithClaim(sso.ClaimAccountRole, role) }

// WithProducts задаёт claim products (контур заказчика).
func WithProducts(products ...string) TokenOption {
	return WithClaim(sso.ClaimProducts, products)
}

// WithClaim добавляет или заменяет один claim (в т.ч. стандартный: iss, aud).
func WithClaim(name string, value any) TokenOption {
	return func(s *tokenSpec) { s.claims[name] = value }
}

// WithClaims добавляет набор claim'ов поверх дефолтных.
func WithClaims(claims map[string]any) TokenOption {
	return func(s *tokenSpec) { maps.Copy(s.claims, claims) }
}

// WithoutClaim убирает claim из токена (негативные кейсы: exp, sub, company_id).
func WithoutClaim(names ...string) TokenOption {
	return func(s *tokenSpec) { s.drop = append(s.drop, names...) }
}

// WithTTL задаёт срок жизни токена от момента выпуска (default DefaultTokenTTL).
func WithTTL(ttl time.Duration) TokenOption { return func(s *tokenSpec) { s.ttl = ttl } }

// WithIssuedAt сдвигает iat/nbf токена (например в прошлое).
func WithIssuedAt(at time.Time) TokenOption { return func(s *tokenSpec) { s.issued = at } }

// WithExpiry задаёт exp явно (истёкший токен: WithExpiry(time.Now().Add(-time.Hour))).
func WithExpiry(at time.Time) TokenOption {
	return func(s *tokenSpec) { s.expiry = &at }
}

// WithKID подписывает активным ключом, но кладёт в заголовок другой kid
// (проверка реакции verifier'а на неизвестный ключ).
func WithKID(kid string) TokenOption { return func(s *tokenSpec) { s.kid = kid } }

// EmployeeToken — валидный hub-session JWT сотрудника этого issuer'а.
func (i *Issuer) EmployeeToken(t testing.TB, opts ...TokenOption) string {
	t.Helper()
	claims := map[string]any{
		"sub":               DefaultSubject,
		sso.ClaimEmployeeID: DefaultEmployeeID,
		sso.ClaimUsername:   DefaultUsername,
		sso.ClaimName:       DefaultName,
		sso.ClaimEmail:      DefaultEmail,
		sso.ClaimRole:       DefaultRole,
		sso.ClaimSID:        DefaultSID,
	}
	return i.Sign(t, claims, opts...)
}

// CustomerToken — валидный hub-session JWT заказчика этого issuer'а.
func (i *Issuer) CustomerToken(t testing.TB, opts ...TokenOption) string {
	t.Helper()
	claims := map[string]any{
		"sub":                DefaultCustomerSubject,
		sso.ClaimName:        DefaultName,
		sso.ClaimEmail:       DefaultEmail,
		sso.ClaimSID:         DefaultSID,
		sso.ClaimKind:        sso.KindCustomer,
		sso.ClaimCompanyID:   DefaultCompanyID,
		sso.ClaimAccountRole: DefaultAccountRole,
		sso.ClaimProducts:    []string{"deal", "telemost"},
	}
	return i.Sign(t, claims, opts...)
}

// Sign подписывает произвольный набор claims активным ключом, дополняя его
// iss/aud/iat/nbf/exp issuer'а. Опции применяются поверх.
func (i *Issuer) Sign(t testing.TB, claims map[string]any, opts ...TokenOption) string {
	t.Helper()
	spec := tokenSpec{claims: map[string]any{}, issued: time.Now(), ttl: DefaultTokenTTL}
	maps.Copy(spec.claims, claims)
	for _, apply := range opts {
		apply(&spec)
	}

	std := jwt.Claims{
		Issuer:    i.issuer,
		Audience:  jwt.Audience{i.audience},
		IssuedAt:  jwt.NewNumericDate(spec.issued),
		NotBefore: jwt.NewNumericDate(spec.issued),
		Expiry:    jwt.NewNumericDate(spec.issued.Add(spec.ttl)),
	}
	if spec.expiry != nil {
		std.Expiry = jwt.NewNumericDate(*spec.expiry)
	}
	full := standardMap(std)
	maps.Copy(full, spec.claims)
	for _, name := range spec.drop {
		delete(full, name)
	}

	signer := i.signerFor(t, spec.kid)
	raw, err := jwt.Signed(signer).Claims(full).Serialize()
	if err != nil {
		t.Fatalf("hubtest: sign: %v", err)
	}
	return raw
}

// SignHS256 подписывает claims симметрично, взяв публичный ключ issuer'а как
// секрет: регрессия на key-confusion (verifier обязан отвергнуть чужой alg).
func (i *Issuer) SignHS256(t testing.TB, claims map[string]any) string {
	t.Helper()
	pub, err := i.PublicKey().ECDH()
	if err != nil {
		t.Fatalf("hubtest: public key: %v", err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: pub.Bytes()},
		(&jose.SignerOptions{}).WithHeader("kid", i.KID()).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("hubtest: hs256 signer: %v", err)
	}
	std := jwt.Claims{
		Issuer:    i.issuer,
		Audience:  jwt.Audience{i.audience},
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		NotBefore: jwt.NewNumericDate(time.Now()),
		Expiry:    jwt.NewNumericDate(time.Now().Add(DefaultTokenTTL)),
	}
	full := standardMap(std)
	maps.Copy(full, claims)
	raw, err := jwt.Signed(signer).Claims(full).Serialize()
	if err != nil {
		t.Fatalf("hubtest: hs256 sign: %v", err)
	}
	return raw
}

func (i *Issuer) signerFor(t testing.TB, kid string) jose.Signer {
	t.Helper()
	i.mu.RLock()
	priv, active, signer := i.priv, i.kid, i.signer
	i.mu.RUnlock()
	if kid == "" || kid == active {
		return signer
	}
	custom, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: priv},
		(&jose.SignerOptions{}).WithHeader("kid", kid).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("hubtest: signer: %v", err)
	}
	return custom
}

func standardMap(std jwt.Claims) map[string]any {
	full := map[string]any{
		"iss": std.Issuer,
		"aud": []string(std.Audience),
	}
	if std.IssuedAt != nil {
		full["iat"] = std.IssuedAt.Time().Unix()
	}
	if std.NotBefore != nil {
		full["nbf"] = std.NotBefore.Time().Unix()
	}
	if std.Expiry != nil {
		full["exp"] = std.Expiry.Time().Unix()
	}
	return full
}

// Cookie — cookie слота с этим токеном (имя из sso.Config продукта). Атрибуты
// не выставляются: extractor читает только имя и значение.
func Cookie(name, token string) *http.Cookie {
	return &http.Cookie{Name: name, Value: token} //nolint:gosec // тестовая cookie, по сети не уходит
}

// Request — GET-запрос с cookie сессии: вход для sso.Extractor.Extract.
func Request(name, token string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "https://product.test/api/things", nil) //nolint:noctx // тестовый запрос без сети
	r.AddCookie(Cookie(name, token))
	return r
}
