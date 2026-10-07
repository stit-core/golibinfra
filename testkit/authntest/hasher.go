// Package authntest — детерминированный authnkit.Hasher для тестов,
// где Argon2 стоит дороже самой проверки.
package authntest

import (
	"context"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/authnkit"
)

// HashPrefix — префикс «хеша» DeterministicHasher.
const HashPrefix = "hash:"

// DeterministicHasher — authnkit.Hasher без Argon2: хеш = HashPrefix + plain.
// Вне `go test` любой его вызов паникует: пароли в открытом виде не должны
// попасть в базу из-за ошибки проводки fx.
type DeterministicHasher struct{}

// Hash возвращает HashPrefix + plain; plain длиннее authnkit.MaxPasswordLen —
// authnkit.ErrPasswordTooLong, как у боевого хэшера.
func (DeterministicHasher) Hash(_ context.Context, plain string) (string, error) {
	mustRunInTest(testing.Testing())
	if len(plain) > authnkit.MaxPasswordLen {
		return "", authnkit.ErrPasswordTooLong
	}
	return HashPrefix + plain, nil
}

// Verify сравнивает encoded с HashPrefix + plain; plain длиннее authnkit.MaxPasswordLen —
// authnkit.ErrPasswordTooLong, как у боевого хэшера.
func (DeterministicHasher) Verify(_ context.Context, plain, encoded string) (bool, error) {
	mustRunInTest(testing.Testing())
	if len(plain) > authnkit.MaxPasswordLen {
		return false, authnkit.ErrPasswordTooLong
	}
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
