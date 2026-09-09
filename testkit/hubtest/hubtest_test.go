package hubtest_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/hubkit/client"
	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/hubtest"

	hubsync "git.stit.tech/stit-core/golibinfra/hubkit/sync"
)

func TestIssuer_EmployeeToken(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	id, err := iss.Verifier(t).Verify(t.Context(), iss.EmployeeToken(t))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !id.IsEmployee() || id.EmployeeID != hubtest.DefaultEmployeeID {
		t.Fatalf("identity = %+v", id)
	}
}

func TestIssuer_CustomerToken(t *testing.T) {
	cust := hubtest.NewCustomerIssuer(t)
	id, err := cust.CustomerVerifier(t).Verify(t.Context(), cust.CustomerToken(t))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !id.IsCustomer() || id.CompanyID != hubtest.DefaultCompanyID {
		t.Fatalf("identity = %+v", id)
	}
	if cust.IssuerURL() != hubtest.DefaultCustomerIssuerURL || cust.Audience() != hubtest.DefaultCustomerAudience {
		t.Fatalf("customer issuer defaults: %s / %s", cust.IssuerURL(), cust.Audience())
	}
}

func TestIssuer_TokenOptions(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	v := iss.Verifier(t)

	id, err := v.Verify(t.Context(), iss.EmployeeToken(t, hubtest.WithSubject("s2"), hubtest.WithRole("admin")))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Subject != "s2" || id.Role != "admin" {
		t.Fatalf("identity = %+v", id)
	}

	expired := iss.EmployeeToken(t, hubtest.WithExpiry(time.Now().Add(-time.Hour)))
	if _, err := v.Verify(t.Context(), expired); !errors.Is(err, sso.ErrInvalidClaims) {
		t.Fatalf("expired token: %v", err)
	}
	noExp := iss.EmployeeToken(t, hubtest.WithoutClaim("exp"))
	if _, err := v.Verify(t.Context(), noExp); !errors.Is(err, sso.ErrNoExpiry) {
		t.Fatalf("token without exp: %v", err)
	}
	unknownKID := iss.EmployeeToken(t, hubtest.WithKID("other"))
	if _, err := v.Verify(t.Context(), unknownKID); !errors.Is(err, sso.ErrUnknownKey) {
		t.Fatalf("unknown kid: %v", err)
	}
	if _, err := v.Verify(t.Context(), iss.SignHS256(t, map[string]any{"sub": "x"})); !errors.Is(err, sso.ErrInvalidToken) {
		t.Fatalf("hs256: %v", err)
	}
}

func TestIssuer_Rotate(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	first := iss.KID()
	iss.RotateKeepingPrevious(t)
	if iss.KID() == first {
		t.Fatal("kid did not change")
	}
	if len(iss.JWKS().Keys) != 2 {
		t.Fatalf("jwks keys = %d, want 2", len(iss.JWKS().Keys))
	}
	iss.Rotate(t)
	if len(iss.JWKS().Keys) != 1 {
		t.Fatalf("jwks keys after plain rotate = %d, want 1", len(iss.JWKS().Keys))
	}
}

func TestIssuer_JWKSRequests(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	if got := iss.JWKSRequests(); got != 0 {
		t.Fatalf("JWKSRequests before the first verification = %d, want 0", got)
	}

	v := iss.Verifier(t)
	tok := iss.EmployeeToken(t)
	if _, err := v.Verify(t.Context(), tok); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := iss.JWKSRequests(); got != 1 {
		t.Fatalf("JWKSRequests after the first verification = %d, want 1", got)
	}

	if _, err := v.Verify(t.Context(), tok); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := iss.JWKSRequests(); got != 1 {
		t.Fatalf("JWKSRequests = %d: the same kid comes from the verifier cache", got)
	}

	if _, err := iss.Verifier(t).Verify(t.Context(), tok); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := iss.JWKSRequests(); got != 2 {
		t.Fatalf("JWKSRequests = %d: a fresh verifier has its own cache", got)
	}
}

func TestExtractorAndIdentityHelpers(t *testing.T) {
	emp := hubtest.NewIssuer(t)
	cust := hubtest.NewCustomerIssuer(t)
	e := hubtest.Extractor(t, emp, cust)

	ctx, err := e.Extract(hubtest.Request(sso.DefaultCustomerCookie, cust.CustomerToken(t)))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if id, _ := sso.IdentityFrom(ctx); !id.IsCustomer() {
		t.Fatalf("identity = %+v", id)
	}
	if _, err := sso.RequireEmployee(hubtest.WithEmployee(t.Context())); err != nil {
		t.Fatalf("WithEmployee: %v", err)
	}
	if _, err := sso.RequireCustomer(hubtest.WithCustomer(t.Context())); err != nil {
		t.Fatalf("WithCustomer: %v", err)
	}
}

func TestSSOConfig_Valid(t *testing.T) {
	emp := hubtest.NewIssuer(t)
	if err := hubtest.SSOConfig(emp, nil).Validate(); err != nil {
		t.Fatalf("employee-only config: %v", err)
	}
	if err := hubtest.SSOConfig(emp, hubtest.NewCustomerIssuer(t)).Validate(); err != nil {
		t.Fatalf("dual config: %v", err)
	}
}

func TestSyncFakes(t *testing.T) {
	feed, proj, cur := hubtest.NewFeed(), hubtest.NewProjection(), hubtest.NewCursors()
	feed.Add(t, client.ResourceCompanies, 1, map[string]any{"id": "c1", "updated_seq": 1})
	feed.Add(t, client.ResourceCompanies, 2, map[string]any{"id": "c2", "updated_seq": 2})

	e, err := hubsync.NewEngine(client.ResourceCompanies, feed, proj, cur,
		hubsync.Config{Interval: time.Minute, Limit: 1}, logkit.Nop())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if err := e.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := cur.Cursor(client.ResourceCompanies); got != 2 {
		t.Fatalf("cursor = %d, want 2", got)
	}
	if got := len(proj.Items(client.ResourceCompanies)); got != 2 {
		t.Fatalf("items = %d, want 2", got)
	}
	if cur.Saves() != 2 || feed.Calls() < 2 {
		t.Fatalf("saves = %d, fetches = %d", cur.Saves(), feed.Calls())
	}

	var first map[string]any
	if err := json.Unmarshal(proj.Items(client.ResourceCompanies)[0], &first); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if first["id"] != "c1" {
		t.Fatalf("first item = %v", first)
	}

	boom := errors.New("boom")
	feed.FailWith(client.ResourceCompanies, boom)
	if err := e.Run(t.Context()); !errors.Is(err, boom) {
		t.Fatalf("FailWith: %v", err)
	}
}
