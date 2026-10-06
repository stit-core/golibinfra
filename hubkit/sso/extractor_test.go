package sso_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/hubtest"
)

func TestExtractor_EmployeeCookie(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	e := hubtest.Extractor(t, iss, nil)

	ctx, err := e.Extract(hubtest.Request(sso.DefaultCookie, iss.EmployeeToken(t)))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	id, ok := sso.IdentityFrom(ctx)
	if !ok || id.EmployeeID != hubtest.DefaultEmployeeID {
		t.Fatalf("identity: %+v ok=%v", id, ok)
	}
	if got := httpkit.SubjectFrom(ctx); got != id.SubjectKey() {
		t.Fatalf("subject in ctx = %q, want %q", got, id.SubjectKey())
	}
}

func TestExtractor_NoCookie(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/things", nil)
	if _, err := hubtest.Extractor(t, iss, nil).Extract(req); !errors.Is(err, sso.ErrNoSessionCookie) {
		t.Fatalf("err = %v, want ErrNoSessionCookie", err)
	}
}

func TestExtractor_BadToken(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	req := hubtest.Request(sso.DefaultCookie, "garbage")
	if _, err := hubtest.Extractor(t, iss, nil).Extract(req); !errors.Is(err, sso.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

func TestExtractor_JWKSDownIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	iss := hubtest.NewIssuer(t)
	cfg := hubtest.SSOConfig(iss, nil)
	cfg.JWKSURL = srv.URL
	v, err := sso.NewVerifier(cfg, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	// httpkit по этой обёртке отвечает 503, а не 401: недоступный хаб не должен
	// разлогинивать пользователей.
	_, err = sso.NewExtractor(v, cfg).Extract(hubtest.Request(cfg.Cookie, iss.EmployeeToken(t)))
	if !errors.Is(err, httpkit.ErrAuthUnavailable) {
		t.Fatalf("err = %v, want httpkit.ErrAuthUnavailable", err)
	}
}

func TestExtractor_TwoContours(t *testing.T) {
	emp := hubtest.NewIssuer(t)
	cust := hubtest.NewCustomerIssuer(t)
	e := hubtest.Extractor(t, emp, cust)
	employeeToken := emp.EmployeeToken(t)
	customerToken := cust.CustomerToken(t)

	both := hubtest.Request(sso.DefaultCookie, employeeToken)
	both.AddCookie(hubtest.Cookie(sso.DefaultCustomerCookie, customerToken))
	ctx, err := e.Extract(both)
	if err != nil {
		t.Fatalf("both slots: %v", err)
	}
	if id, _ := sso.IdentityFrom(ctx); !id.IsEmployee() {
		t.Fatalf("employee slot must win: %+v", id)
	}

	expired := emp.EmployeeToken(t, hubtest.WithExpiry(time.Now().Add(-time.Hour)))
	stale := hubtest.Request(sso.DefaultCookie, expired)
	stale.AddCookie(hubtest.Cookie(sso.DefaultCustomerCookie, customerToken))
	ctx, err = e.Extract(stale)
	if err != nil {
		t.Fatalf("expired employee + live customer: %v", err)
	}
	if id, _ := sso.IdentityFrom(ctx); !id.IsCustomer() {
		t.Fatalf("customer slot must win: %+v", id)
	}

	// Контур заказчика выключен — его cookie не рассматривается вовсе.
	only := hubtest.Extractor(t, emp, nil)
	if _, err := only.Extract(hubtest.Request(sso.DefaultCustomerCookie, customerToken)); !errors.Is(err, sso.ErrNoSessionCookie) {
		t.Fatalf("disabled contour: %v", err)
	}
}

func TestConfig_LoginURLFor(t *testing.T) {
	cfg := sso.Config{
		LoginURL:         "https://id.test/auth/login",
		CustomerLoginURL: "https://hub.test/customer/login?theme=dark",
	}
	if got := cfg.LoginURLFor("https://deal.test/x?y=1"); got != "https://id.test/auth/login?rd=https%3A%2F%2Fdeal.test%2Fx%3Fy%3D1" {
		t.Fatalf("LoginURLFor = %q", got)
	}
	if got := cfg.LoginURLFor(""); got != cfg.LoginURL {
		t.Fatalf("empty returnTo = %q", got)
	}
	if got := cfg.CustomerLoginURLFor("/back"); got != "https://hub.test/customer/login?theme=dark&rd=%2Fback" {
		t.Fatalf("CustomerLoginURLFor = %q", got)
	}
}

func TestModule_Graph(t *testing.T) {
	emp := hubtest.NewIssuer(t)
	cust := hubtest.NewCustomerIssuer(t)
	cfg := hubtest.SSOConfig(emp, cust)

	var schemes []httpkit.SecuritySchemeEntry
	type schemesIn struct {
		fx.In
		Schemes []httpkit.SecuritySchemeEntry `group:"httpkit.security_scheme"`
	}
	app := fxtest.New(
		t,
		fx.Supply(cfg),
		fx.Provide(logkit.Nop),
		sso.Module,
		fx.Invoke(func(in schemesIn) { schemes = in.Schemes }),
	)
	app.RequireStart().RequireStop()

	byName := map[string]httpkit.SecuritySchemeEntry{}
	for _, s := range schemes {
		byName[s.Name] = s
	}
	employee, customer := byName[cfg.SchemeName()], byName[cfg.CustomerSchemeName()]
	if employee.Extract == nil || customer.Extract == nil {
		t.Fatalf("security schemes = %v, want both contours with an extractor", byName)
	}
	// Схема открывает операцию только своим cookie: сессия заказчика не открывает контур сотрудника.
	req := hubtest.Request(sso.DefaultCustomerCookie, cust.CustomerToken(t))
	if _, err := employee.Extract(req); !errors.Is(err, sso.ErrNoSessionCookie) {
		t.Fatalf("employee scheme with a customer cookie: %v, want ErrNoSessionCookie", err)
	}
	if _, err := customer.Extract(req); err != nil {
		t.Fatalf("customer scheme with its cookie: %v", err)
	}
}

func TestProvideOption_InjectsClock(t *testing.T) {
	emp := hubtest.NewIssuer(t)
	clock := clockkit.NewFixed(time.Now())
	var v *sso.Verifier
	app := fxtest.New(
		t,
		fx.Supply(hubtest.SSOConfig(emp, nil)),
		sso.Verifiers,
		sso.ProvideOption(func() sso.Option { return sso.WithClock(clock) }),
		fx.Populate(&v),
	)
	app.RequireStart().RequireStop()
	if _, err := v.Verify(t.Context(), emp.EmployeeToken(t)); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifiers_ComposableExtractor(t *testing.T) {
	emp := hubtest.NewIssuer(t)
	cfg := hubtest.SSOConfig(emp, nil)

	// Verifiers не связывает httpkit.AuthExtractor: продукт со своим составным
	// extractor'ом объявляет его сам.
	var own httpkit.AuthExtractor
	app := fxtest.New(
		t,
		fx.Supply(cfg),
		sso.Verifiers,
		fx.Provide(func(e *sso.Extractor) httpkit.AuthExtractor {
			return func(r *http.Request) (context.Context, error) {
				if r.Header.Get("X-Service-Token") == "s3cret" {
					return r.Context(), nil
				}
				return e.Extract(r)
			}
		}),
		fx.Populate(&own),
	)
	app.RequireStart().RequireStop()
	if own == nil {
		t.Fatal("composed extractor not provided")
	}
}
