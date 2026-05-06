package config

import (
	"testing"
)

func TestApplyVaultDataSetsField(t *testing.T) {
	type dst struct {
		JWTSecret string `vault:"jwt_secret"`
	}
	var d dst
	if err := applyVaultData(map[string]any{"jwt_secret": "x"}, &d); err != nil {
		t.Fatalf("applyVaultData: %v", err)
	}
	if d.JWTSecret != "x" {
		t.Fatalf("expected JWTSecret=x, got %q", d.JWTSecret)
	}
}

func TestApplyVaultDataNotPointer(t *testing.T) {
	type dst struct {
		JWTSecret string `vault:"jwt_secret"`
	}
	var d dst
	if err := applyVaultData(map[string]any{"jwt_secret": "x"}, d); err == nil {
		t.Fatal("expected error for non-pointer dst")
	}
}

func TestApplyVaultDataNotStruct(t *testing.T) {
	s := "hello"
	if err := applyVaultData(map[string]any{"jwt_secret": "x"}, &s); err == nil {
		t.Fatal("expected error for non-struct pointer")
	}
}

func TestApplyVaultDataValueNotString(t *testing.T) {
	type dst struct {
		JWTSecret string `vault:"jwt_secret"`
	}
	var d dst
	if err := applyVaultData(map[string]any{"jwt_secret": 42}, &d); err == nil {
		t.Fatal("expected error when value is not a string")
	}
}

func TestSplitMount(t *testing.T) {
	if got := splitMount("secret/data/foo"); got != "secret" {
		t.Fatalf("splitMount: expected %q, got %q", "secret", got)
	}
}

func TestSplitKey(t *testing.T) {
	if got := splitKey("secret/data/foo"); got != "data/foo" {
		t.Fatalf("splitKey: expected %q, got %q", "data/foo", got)
	}
}
