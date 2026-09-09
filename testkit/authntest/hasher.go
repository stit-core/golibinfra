// Package authntest — детерминированный authnkit.Hasher для тестов,
// где Argon2 стоит дороже самой проверки.
package authntest

import (
	"context"
	"strings"
	"testing"
)

// HashPrefix — префикс «хеша» DeterministicHasher.
const HashPrefix = "hash:"

// DeterministicHasher — authnkit.Hasher без Argon2: хеш = HashPrefix + plain.
// Вне `go test` любой его вызов паникует: пароли в открытом виде не должны
// попасть в базу из-за ошибки проводки fx.
type DeterministicHasher struct{}

// Hash возвращает HashPrefix + plain.
func (DeterministicHasher) Hash(_ context.Context, plain string) (string, error) {
	mustRunInTest(testing.Testing())
	return HashPrefix + plain, nil
}

// Verify сравнивает encoded с HashPrefix + plain.
func (DeterministicHasher) Verify(_ context.Context, plain, encoded string) (bool, error) {
	mustRunInTest(testing.Testing())
	return encoded == HashPrefix+plain, nil
}

// NeedsRehash — всегда false: параметров у детерминированного хеша нет.
func (DeterministicHasher) NeedsRehash(string) bool {
	mustRunInTest(testing.Testing())
	return false
}

// VerifyDummy — no-op: выравнивать нечего, хеш не считается.
func (DeterministicHasher) VerifyDummy(context.Context) {
	mustRunInTest(testing.Testing())
}

// StripHashPrefix снимает HashPrefix — для проверок на исходном значении.
func StripHashPrefix(encoded string) string {
	return strings.TrimPrefix(encoded, HashPrefix)
}

func mustRunInTest(inTest bool) {
	if !inTest {
		panic("authntest: DeterministicHasher used outside go test")
	}
}
