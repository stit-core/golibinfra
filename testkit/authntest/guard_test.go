package authntest

import "testing"

// Предохранитель: вне go test хеш «hash:<пароль>» не должен считаться валидным.
func TestMustRunInTest(t *testing.T) {
	t.Parallel()
	if !testing.Testing() {
		t.Fatal("testing.Testing() must be true under go test")
	}
	mustRunInTest(true)

	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic outside go test")
		}
	}()
	mustRunInTest(false)
}
