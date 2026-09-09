package hubtest

import (
	"context"
	"testing"

	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
)

// SSOConfig — sso.Config продукта под эти issuer'ы: employee обязателен,
// customer — второй контур или nil (тогда контур выключен).
func SSOConfig(employee, customer *Issuer) sso.Config {
	cfg := sso.Config{
		JWKSURL:            employee.JWKSURL(),
		Cookie:             sso.DefaultCookie,
		Issuer:             employee.IssuerURL(),
		Audience:           employee.Audience(),
		LoginURL:           employee.IssuerURL() + "/auth/login",
		Leeway:             sso.DefaultLeeway,
		KeyGrace:           sso.DefaultKeyGrace,
		SecuritySchemeName: sso.DefaultSecuritySchemeName,

		CustomerCookie:             sso.DefaultCustomerCookie,
		CustomerLoginURL:           "https://hub.test/customer/login",
		CustomerSecuritySchemeName: sso.DefaultCustomerSecuritySchemeName,
	}
	if customer != nil {
		cfg.CustomerJWKSURL = customer.JWKSURL()
		cfg.CustomerIssuer = customer.IssuerURL()
		cfg.CustomerAudience = customer.Audience()
	}
	return cfg
}

// Verifier — верификатор сотруднического контура на этом issuer'е.
func (i *Issuer) Verifier(t testing.TB, opts ...sso.Option) *sso.Verifier {
	t.Helper()
	v, err := sso.NewVerifier(SSOConfig(i, nil), nil, opts...)
	if err != nil {
		t.Fatalf("hubtest: new verifier: %v", err)
	}
	return v
}

// CustomerVerifier — верификатор контура заказчика на этом issuer'е.
func (i *Issuer) CustomerVerifier(t testing.TB, opts ...sso.Option) *sso.CustomerVerifier {
	t.Helper()
	v, err := sso.NewCustomerVerifier(SSOConfig(i, i), nil, opts...)
	if err != nil {
		t.Fatalf("hubtest: new customer verifier: %v", err)
	}
	return v
}

// Extractor — extractor обоих контуров (customer = nil → только сотрудник).
func Extractor(t testing.TB, employee, customer *Issuer) *sso.Extractor {
	t.Helper()
	cfg := SSOConfig(employee, customer)
	v, err := sso.NewVerifier(cfg, nil)
	if err != nil {
		t.Fatalf("hubtest: new verifier: %v", err)
	}
	cv, err := sso.NewCustomerVerifier(cfg, nil)
	if err != nil {
		t.Fatalf("hubtest: new customer verifier: %v", err)
	}
	return sso.NewDualExtractor(v, cv, cfg)
}

// EmployeeIdentity — Identity сотрудника с дефолтами issuer'а; правится
// функцией-модификатором для теста доменной политики.
func EmployeeIdentity(mods ...func(*sso.Identity)) sso.Identity {
	id := sso.Identity{
		EmployeeID: DefaultEmployeeID,
		Subject:    DefaultSubject,
		Username:   DefaultUsername,
		Name:       DefaultName,
		Email:      DefaultEmail,
		Role:       DefaultRole,
		SID:        DefaultSID,
		Kind:       sso.KindEmployee,
	}
	for _, mod := range mods {
		mod(&id)
	}
	return id
}

// CustomerIdentity — Identity заказчика с дефолтами issuer'а.
func CustomerIdentity(mods ...func(*sso.Identity)) sso.Identity {
	id := sso.Identity{
		Subject:     DefaultCustomerSubject,
		Name:        DefaultName,
		Email:       DefaultEmail,
		SID:         DefaultSID,
		Kind:        sso.KindCustomer,
		CompanyID:   DefaultCompanyID,
		AccountRole: DefaultAccountRole,
		Products:    []string{"deal", "telemost"},
	}
	for _, mod := range mods {
		mod(&id)
	}
	return id
}

// WithEmployee кладёт в ctx Identity сотрудника: вход в хендлер за
// security-гейтом без поднятия HTTP-слоя.
func WithEmployee(ctx context.Context, mods ...func(*sso.Identity)) context.Context {
	return sso.WithIdentity(ctx, EmployeeIdentity(mods...))
}

// WithCustomer кладёт в ctx Identity заказчика.
func WithCustomer(ctx context.Context, mods ...func(*sso.Identity)) context.Context {
	return sso.WithIdentity(ctx, CustomerIdentity(mods...))
}
