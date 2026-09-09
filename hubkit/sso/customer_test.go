package sso_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/hubtest"
)

func customerVerifier(t *testing.T, employee, customer *hubtest.Issuer) *sso.CustomerVerifier {
	t.Helper()
	v, err := sso.NewCustomerVerifier(hubtest.SSOConfig(employee, customer), nil)
	if err != nil {
		t.Fatalf("NewCustomerVerifier: %v", err)
	}
	return v
}

func TestCustomerVerify_Valid(t *testing.T) {
	emp := hubtest.NewIssuer(t)
	cust := hubtest.NewCustomerIssuer(t)
	id, err := customerVerifier(t, emp, cust).Verify(t.Context(), cust.CustomerToken(t))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !id.IsCustomer() || id.CompanyID != hubtest.DefaultCompanyID {
		t.Fatalf("identity: %+v", id)
	}
}

func TestCustomerVerify_NoCompanyID(t *testing.T) {
	emp := hubtest.NewIssuer(t)
	cust := hubtest.NewCustomerIssuer(t)
	token := cust.CustomerToken(t, hubtest.WithoutClaim(sso.ClaimCompanyID))
	_, err := customerVerifier(t, emp, cust).Verify(t.Context(), token)
	if !errors.Is(err, sso.ErrNoCompany) || !errors.Is(err, sso.ErrInvalidClaims) {
		t.Fatalf("err = %v, want ErrNoCompany (ErrInvalidClaims)", err)
	}
}

func TestVerify_ContourSwap(t *testing.T) {
	emp := hubtest.NewIssuer(t)
	cust := hubtest.NewCustomerIssuer(t)

	// Employee-токен в customer-слот и наоборот: подпись/issuer могли бы сойтись,
	// решает claim kind.
	_, err := customerVerifier(t, emp, cust).Verify(t.Context(), cust.EmployeeToken(t))
	if !errors.Is(err, sso.ErrWrongKind) {
		t.Fatalf("employee token in customer slot: %v", err)
	}
	_, err = emp.Verifier(t).Verify(t.Context(), emp.CustomerToken(t))
	if !errors.Is(err, sso.ErrWrongKind) {
		t.Fatalf("customer token in employee slot: %v", err)
	}
}

func TestVerify_EmptyKindIsEmployee(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	id, err := iss.Verifier(t).Verify(t.Context(), iss.EmployeeToken(t, hubtest.WithClaim(sso.ClaimKind, "")))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Kind != sso.KindEmployee {
		t.Fatalf("kind = %q", id.Kind)
	}
}

func TestNewCustomerVerifier_Disabled(t *testing.T) {
	emp := hubtest.NewIssuer(t)
	v, err := sso.NewCustomerVerifier(hubtest.SSOConfig(emp, nil), nil)
	if err != nil {
		t.Fatalf("NewCustomerVerifier: %v", err)
	}
	if v != nil {
		t.Fatal("customer verifier built with the contour disabled")
	}
}

func TestIdentity_Gates(t *testing.T) {
	tests := map[string]struct {
		id       sso.Identity
		employee bool
		customer bool
		deal     bool
	}{
		"zero value": {id: sso.Identity{}},
		"employee":   {id: hubtest.EmployeeIdentity(), employee: true, deal: true},
		"employee without kind claim": {
			id:       hubtest.EmployeeIdentity(func(i *sso.Identity) { i.Kind = "" }),
			employee: true, deal: true,
		},
		"employee id without subject": {
			id: sso.Identity{EmployeeID: "e1"},
		},
		"customer with product": {id: hubtest.CustomerIdentity(), customer: true, deal: true},
		"customer without product": {
			id:       hubtest.CustomerIdentity(func(i *sso.Identity) { i.Products = nil }),
			customer: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tc.id.IsEmployee(); got != tc.employee {
				t.Errorf("IsEmployee() = %v, want %v", got, tc.employee)
			}
			if got := tc.id.IsCustomer(); got != tc.customer {
				t.Errorf("IsCustomer() = %v, want %v", got, tc.customer)
			}
			if got := tc.id.CanUseProduct("deal"); got != tc.deal {
				t.Errorf("CanUseProduct(deal) = %v, want %v", got, tc.deal)
			}
		})
	}
}

func TestRequireKindGates(t *testing.T) {
	ctx := t.Context()
	if _, err := sso.RequireEmployee(ctx); !errors.Is(err, sso.ErrNoIdentity) {
		t.Fatalf("no identity: %v", err)
	}
	// Нулевая Identity в ctx не должна проходить за сотрудника.
	if _, err := sso.RequireEmployee(sso.WithIdentity(ctx, sso.Identity{})); !errors.Is(err, sso.ErrNoIdentity) {
		t.Fatalf("zero identity: %v", err)
	}
	if _, err := sso.RequireEmployee(hubtest.WithEmployee(ctx)); err != nil {
		t.Fatalf("employee: %v", err)
	}
	if _, err := sso.RequireEmployee(hubtest.WithCustomer(ctx)); !errors.Is(err, sso.ErrWrongKind) {
		t.Fatalf("customer in employee gate: %v", err)
	}
	if _, err := sso.RequireCustomer(hubtest.WithCustomer(ctx)); err != nil {
		t.Fatalf("customer: %v", err)
	}
	if _, err := sso.RequireCustomer(hubtest.WithEmployee(ctx)); !errors.Is(err, sso.ErrWrongKind) {
		t.Fatalf("employee in customer gate: %v", err)
	}
}

func TestIdentity_SubjectKey(t *testing.T) {
	if got := hubtest.EmployeeIdentity().SubjectKey(); got != "employee:"+hubtest.DefaultSubject {
		t.Fatalf("SubjectKey() = %q", got)
	}
	if got := (sso.Identity{}).SubjectKey(); got != "" {
		t.Fatalf("zero SubjectKey() = %q, want empty", got)
	}
}

func TestMustIdentityFrom(t *testing.T) {
	if got := sso.MustIdentityFrom(hubtest.WithEmployee(t.Context())); got.Subject != hubtest.DefaultSubject {
		t.Fatalf("identity = %+v", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustIdentityFrom без identity обязан паниковать")
		}
	}()
	sso.MustIdentityFrom(t.Context())
}

func TestRegisterIdentityLogAttr(t *testing.T) {
	sso.RegisterIdentityLogAttr()
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{Level: "info", Format: "json"}, logkit.WithWriter(&buf))
	if err != nil {
		t.Fatalf("logkit.New: %v", err)
	}

	log.Info(hubtest.WithEmployee(t.Context()), "with employee")
	log.Info(hubtest.WithCustomer(t.Context()), "with customer")
	log.Info(t.Context(), "no identity")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("log lines = %d", len(lines))
	}
	want := []string{hubtest.DefaultEmployeeID, hubtest.DefaultCustomerSubject, ""}
	for i, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("unmarshal line %d: %v", i, err)
		}
		got, _ := rec["user_id"].(string)
		if got != want[i] {
			t.Fatalf("line %d user_id = %q, want %q", i, got, want[i])
		}
	}
}
