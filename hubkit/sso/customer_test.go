package sso_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
)

const testCustomerIssuer = "https://api.hub.stit.tech"

func customerStdClaims() jwt.Claims {
	c := stdClaims()
	c.Issuer = testCustomerIssuer
	c.Subject = "01990000-0000-7000-8000-0000000000c1"
	return c
}

func customerClaims() map[string]any {
	return map[string]any{
		"kind":         "customer",
		"company_id":   "01980000-0000-7000-8000-00000000c0c0",
		"account_role": "admin",
		"name":         "Иван Заказчиков",
		"email":        "ivan@acme.ru",
		"sid":          "csess-1",
		"products":     []string{"deal", "telemost"},
	}
}

// customerCfg — конфиг с включённым customer-контуром (employee-часть — как в
// проде: свой JWKS и свой iss).
func customerCfg(employeeJWKS, customerJWKS string) sso.Config {
	return sso.Config{
		JWKSURL:         employeeJWKS,
		Cookie:          "stit_session",
		Issuer:          testIssuer,
		CustomerJWKSURL: customerJWKS,
		CustomerCookie:  "stit_customer",
		CustomerIssuer:  testCustomerIssuer,
	}
}

func customerVerifier(t *testing.T, jwksURL string) *sso.CustomerVerifier {
	t.Helper()
	cv, err := sso.NewCustomerVerifier(sso.Config{CustomerJWKSURL: jwksURL, CustomerIssuer: testCustomerIssuer}, nil)
	if err != nil {
		t.Fatalf("NewCustomerVerifier: %v", err)
	}
	if cv == nil {
		t.Fatal("NewCustomerVerifier вернул nil при заданном JWKS-URL")
	}
	return cv
}

func TestCustomerVerify_Valid(t *testing.T) {
	ci := newSigningIdentity(t)
	cv := customerVerifier(t, ci.jwksServer(t).URL)

	id, err := cv.Verify(context.Background(), ci.sign(t, customerStdClaims(), customerClaims()))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want := sso.Identity{
		Subject:     "01990000-0000-7000-8000-0000000000c1",
		Name:        "Иван Заказчиков",
		Email:       "ivan@acme.ru",
		SID:         "csess-1",
		Kind:        sso.KindCustomer,
		CompanyID:   "01980000-0000-7000-8000-00000000c0c0",
		AccountRole: "admin",
		Products:    []string{"deal", "telemost"},
	}
	if !reflect.DeepEqual(id, want) {
		t.Fatalf("identity=%+v, ожидали %+v", id, want)
	}
	if !id.IsCustomer() || id.IsEmployee() {
		t.Fatalf("контур определён неверно: %+v", id)
	}
}

// Токен сотрудника, попавший в customer-слот, не принимается никогда: у него
// чужой iss и нет kind=customer.
func TestCustomerVerify_RejectsEmployeeToken(t *testing.T) {
	ci := newSigningIdentity(t)
	cv := customerVerifier(t, ci.jwksServer(t).URL)

	// Тот же ключ (худший случай: подпись сойдётся), но employee-claims.
	if _, err := cv.Verify(context.Background(), ci.sign(t, stdClaims(), hubClaims())); err == nil {
		t.Fatal("employee-токен принят customer-верификатором")
	}
	// Даже с правильным iss customer-контура — kind обязателен.
	if _, err := cv.Verify(context.Background(), ci.sign(t, customerStdClaims(), hubClaims())); !errors.Is(err, sso.ErrWrongKind) {
		t.Fatalf("ожидали ErrWrongKind, получили %v", err)
	}
}

// Обратное направление: customer-токен в employee-слоте — тоже отказ, даже если
// iss совпал (HUB_SSO_ISSUER мог быть не задан).
func TestVerify_RejectsCustomerToken(t *testing.T) {
	si := newSigningIdentity(t)
	v := verifier(t, si.jwksServer(t).URL)

	std := stdClaims()
	if _, err := v.Verify(context.Background(), si.sign(t, std, customerClaims())); !errors.Is(err, sso.ErrWrongKind) {
		t.Fatalf("ожидали ErrWrongKind, получили %v", err)
	}
}

// Пустой claim kind = employee: токены, выпущенные до второго контура, живут.
func TestVerify_EmptyKindIsEmployee(t *testing.T) {
	si := newSigningIdentity(t)
	v := verifier(t, si.jwksServer(t).URL)

	id, err := v.Verify(context.Background(), si.sign(t, stdClaims(), hubClaims()))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Kind != "" || !id.IsEmployee() || id.IsCustomer() {
		t.Fatalf("employee-токен без claim kind прочитан как %+v", id)
	}
}

func TestNewCustomerVerifier_Disabled(t *testing.T) {
	cv, err := sso.NewCustomerVerifier(sso.Config{JWKSURL: "https://id.stit.tech/jwks"}, nil)
	if err != nil {
		t.Fatalf("выключенный контур не должен быть ошибкой: %v", err)
	}
	if cv != nil {
		t.Fatal("без HUB_SSO_CUSTOMER_JWKS_URL верификатор быть не должен")
	}
}

func TestNewCustomerVerifier_IssuerRequired(t *testing.T) {
	if _, err := sso.NewCustomerVerifier(sso.Config{CustomerJWKSURL: "https://api.hub.stit.tech/jwks"}, nil); !errors.Is(err, sso.ErrCustomerIssuer) {
		t.Fatalf("ожидали ErrCustomerIssuer, получили %v", err)
	}
}

func dualExtractor(t *testing.T, emp, cust *signingIdentity) *sso.Extractor {
	t.Helper()
	cfg := customerCfg(emp.jwksServer(t).URL, cust.jwksServer(t).URL)
	return sso.NewDualExtractor(verifier(t, cfg.JWKSURL), customerVerifier(t, cfg.CustomerJWKSURL), cfg)
}

// Обе cookie заполнены → выигрывает employee (внутренний контур главнее).
func TestExtractor_BothCookies_EmployeeWins(t *testing.T) {
	emp, cust := newSigningIdentity(t), newSigningIdentity(t)
	e := dualExtractor(t, emp, cust)

	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.AddCookie(&http.Cookie{Name: "stit_session", Value: emp.sign(t, stdClaims(), hubClaims())})
	r.AddCookie(&http.Cookie{Name: "stit_customer", Value: cust.sign(t, customerStdClaims(), customerClaims())})

	ctx, err := e.Extract(r)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	id := sso.MustIdentityFrom(ctx)
	if !id.IsEmployee() || id.Username != "n.sycheva" {
		t.Fatalf("ожидали employee-сессию, получили %+v", id)
	}
}

// Протухшая рабочая сессия не должна ломать вход клиенту.
func TestExtractor_ExpiredEmployee_CustomerWins(t *testing.T) {
	emp, cust := newSigningIdentity(t), newSigningIdentity(t)
	e := dualExtractor(t, emp, cust)

	expired := stdClaims()
	expired.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	expired.IssuedAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))

	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.AddCookie(&http.Cookie{Name: "stit_session", Value: emp.sign(t, expired, hubClaims())})
	r.AddCookie(&http.Cookie{Name: "stit_customer", Value: cust.sign(t, customerStdClaims(), customerClaims())})

	ctx, err := e.Extract(r)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	id := sso.MustIdentityFrom(ctx)
	if !id.IsCustomer() || id.CompanyID != "01980000-0000-7000-8000-00000000c0c0" {
		t.Fatalf("ожидали customer-сессию, получили %+v", id)
	}
}

func TestExtractor_CustomerOnly(t *testing.T) {
	emp, cust := newSigningIdentity(t), newSigningIdentity(t)
	e := dualExtractor(t, emp, cust)

	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.AddCookie(&http.Cookie{Name: "stit_customer", Value: cust.sign(t, customerStdClaims(), customerClaims())})

	ctx, err := e.Extract(r)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if id := sso.MustIdentityFrom(ctx); !id.IsCustomer() {
		t.Fatalf("ожидали customer-сессию, получили %+v", id)
	}
}

// Employee-токен, положенный в customer-слот, не пускает: слот проверяется своим
// верификатором, а не «любым подходящим».
func TestExtractor_EmployeeTokenInCustomerSlot(t *testing.T) {
	emp, cust := newSigningIdentity(t), newSigningIdentity(t)
	e := dualExtractor(t, emp, cust)

	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.AddCookie(&http.Cookie{Name: "stit_customer", Value: emp.sign(t, stdClaims(), hubClaims())})

	if _, err := e.Extract(r); err == nil {
		t.Fatal("employee-токен в customer-слоте прошёл")
	}
}

// Выключенный customer-контур — поведение ровно как до второго контура.
func TestExtractor_CustomerDisabled(t *testing.T) {
	emp, cust := newSigningIdentity(t), newSigningIdentity(t)
	cfg := sso.Config{JWKSURL: emp.jwksServer(t).URL, Cookie: "stit_session", Issuer: testIssuer}
	cv, err := sso.NewCustomerVerifier(cfg, nil)
	if err != nil {
		t.Fatalf("NewCustomerVerifier: %v", err)
	}
	e := sso.NewDualExtractor(verifier(t, cfg.JWKSURL), cv, cfg)

	// Только customer-cookie — её никто не читает: как будто сессии нет вовсе.
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.AddCookie(&http.Cookie{Name: "stit_customer", Value: cust.sign(t, customerStdClaims(), customerClaims())})
	if _, err := e.Extract(r); !errors.Is(err, sso.ErrNoSessionCookie) {
		t.Fatalf("ожидали ErrNoSessionCookie, получили %v", err)
	}

	// Employee-cookie работает как раньше.
	r = httptest.NewRequest(http.MethodGet, "/x", nil)
	r.AddCookie(&http.Cookie{Name: "stit_session", Value: emp.sign(t, stdClaims(), hubClaims())})
	ctx, err := e.Extract(r)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if id := sso.MustIdentityFrom(ctx); !id.IsEmployee() || id.EmployeeID == "" {
		t.Fatalf("employee-сессия сломана: %+v", id)
	}
}

func TestIdentity_CanUseProduct(t *testing.T) {
	emp := sso.Identity{EmployeeID: "e1", Role: "admin"}
	for _, p := range []string{"deal", "telemost", "task", "calendar"} {
		if !emp.CanUseProduct(p) {
			t.Fatalf("сотруднику закрыт продукт %s", p)
		}
	}
	// Явный kind=employee — то же самое.
	if !(sso.Identity{Kind: sso.KindEmployee}).CanUseProduct("deal") {
		t.Fatal("employee с явным kind не пущен в продукт")
	}

	cust := sso.Identity{Kind: sso.KindCustomer, CompanyID: "c1", Products: []string{"deal", "telemost"}}
	if !cust.CanUseProduct("deal") || !cust.CanUseProduct("telemost") {
		t.Fatal("заказчик не пущен в выданный продукт")
	}
	if cust.CanUseProduct("task") || cust.CanUseProduct("") {
		t.Fatal("заказчик пущен в невыданный продукт")
	}
	// Пустой набор — закрыто по умолчанию.
	if (sso.Identity{Kind: sso.KindCustomer}).CanUseProduct("deal") {
		t.Fatal("заказчик без products пущен в продукт")
	}
}

func TestRequireKindGates(t *testing.T) {
	if _, err := sso.RequireEmployee(t.Context()); !errors.Is(err, sso.ErrNoIdentity) {
		t.Fatalf("пустой ctx: ожидали ErrNoIdentity, получили %v", err)
	}
	if _, err := sso.RequireCustomer(t.Context()); !errors.Is(err, sso.ErrNoIdentity) {
		t.Fatalf("пустой ctx: ожидали ErrNoIdentity, получили %v", err)
	}

	empCtx := sso.WithIdentity(t.Context(), sso.Identity{EmployeeID: "e1"})
	if id, err := sso.RequireEmployee(empCtx); err != nil || id.EmployeeID != "e1" {
		t.Fatalf("RequireEmployee: id=%+v err=%v", id, err)
	}
	if _, err := sso.RequireCustomer(empCtx); !errors.Is(err, sso.ErrWrongKind) {
		t.Fatalf("RequireCustomer на сотруднике: ожидали ErrWrongKind, получили %v", err)
	}

	custCtx := sso.WithIdentity(t.Context(), sso.Identity{Kind: sso.KindCustomer, CompanyID: "c1"})
	if id, err := sso.RequireCustomer(custCtx); err != nil || id.CompanyID != "c1" {
		t.Fatalf("RequireCustomer: id=%+v err=%v", id, err)
	}
	if _, err := sso.RequireEmployee(custCtx); !errors.Is(err, sso.ErrWrongKind) {
		t.Fatalf("RequireEmployee на заказчике: ожидали ErrWrongKind, получили %v", err)
	}
}

// Module — путь, которым контур подключают продукты: оба верификатора и dual-
// extractor должны собираться из одного Config, а выключенный контур (nil в
// графе) не должен ронять сборку.
func TestModule_Graph(t *testing.T) {
	emp, cust := newSigningIdentity(t), newSigningIdentity(t)
	cfg := customerCfg(emp.jwksServer(t).URL, cust.jwksServer(t).URL)

	var e *sso.Extractor
	app := fx.New(fx.NopLogger, fx.Supply(cfg), sso.Module, fx.Populate(&e))
	if err := app.Err(); err != nil {
		t.Fatalf("граф с включённым контуром: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.AddCookie(&http.Cookie{Name: "stit_customer", Value: cust.sign(t, customerStdClaims(), customerClaims())})
	ctx, err := e.Extract(r)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if id := sso.MustIdentityFrom(ctx); !id.IsCustomer() {
		t.Fatalf("ожидали customer-сессию, получили %+v", id)
	}

	off := sso.Config{JWKSURL: cfg.JWKSURL, Issuer: testIssuer}
	var eOff *sso.Extractor
	appOff := fx.New(fx.NopLogger, fx.Supply(off), sso.Module, fx.Populate(&eOff))
	if err := appOff.Err(); err != nil {
		t.Fatalf("граф с выключенным контуром: %v", err)
	}
	if _, err := eOff.Extract(r); !errors.Is(err, sso.ErrNoSessionCookie) {
		t.Fatalf("выключенный контур: ожидали ErrNoSessionCookie, получили %v", err)
	}
}

func TestConfig_Customer(t *testing.T) {
	off := sso.Config{JWKSURL: "https://id.stit.tech/jwks"}
	if off.CustomerEnabled() {
		t.Fatal("контур включён без HUB_SSO_CUSTOMER_JWKS_URL")
	}
	cfg := sso.Config{
		CustomerJWKSURL:  "https://api.hub.stit.tech/.well-known/jwks.json",
		CustomerLoginURL: "https://hub.stit.tech/customer/login",
	}
	if !cfg.CustomerEnabled() {
		t.Fatal("контур выключен при заданном JWKS-URL")
	}
	got := cfg.CustomerLoginURLFor("https://deal.stit.tech/my-company")
	want := "https://hub.stit.tech/customer/login?rd=https%3A%2F%2Fdeal.stit.tech%2Fmy-company"
	if got != want {
		t.Fatalf("CustomerLoginURLFor=%s, ожидали %s", got, want)
	}
	if got := cfg.CustomerLoginURLFor(""); got != "https://hub.stit.tech/customer/login" {
		t.Fatalf("пустой returnTo: %s", got)
	}
}
