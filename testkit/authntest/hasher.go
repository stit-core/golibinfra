// Package authntest — детерминированный authnkit.Hasher для тестов,
// где Argon2 стоит дороже самой проверки.
package authntest

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"git.stit.tech/stit-core/golibinfra/authnkit"
)

// HashPrefix — префикс «хеша» DeterministicHasher.
const HashPrefix = "hash:"

// DeterministicHasher — authnkit.Hasher без Argon2: хеш = HashPrefix + plain; считает проверки
// пароля (VerifyCalls, VerifyDummyCalls). Нулевое значение готово: &DeterministicHasher{}.
// Вне `go test` любой его вызов паникует: пароли в открытом виде не должны
// попасть в базу из-за ошибки проводки fx.
type DeterministicHasher struct {
	// HashErr — сбой хэширования: не nil — Hash отдаёт его вместо хеша.
	HashErr error

	verifies, dummies atomic.Int64
}

// Hash возвращает HashPrefix + plain; plain длиннее authnkit.MaxPasswordLen —
// authnkit.ErrPasswordTooLong, как у боевого хэшера, затем HashErr, если он задан.
func (h *DeterministicHasher) Hash(_ context.Context, plain string) (string, error) {
	mustRunInTest(testing.Testing())
	if len(plain) > authnkit.MaxPasswordLen {
		return "", authnkit.ErrPasswordTooLong
	}
	if h.HashErr != nil {
		return "", h.HashErr
	}
	return HashPrefix + plain, nil
}

// Verify сравнивает encoded с HashPrefix + plain. Как у боевого хэшера: plain длиннее
// authnkit.MaxPasswordLen — authnkit.ErrPasswordTooLong, encoded без HashPrefix —
// authnkit.ErrInvalidHash.
func (h *DeterministicHasher) Verify(_ context.Context, plain, encoded string) (bool, error) {
	mustRunInTest(testing.Testing())
	h.verifies.Add(1)
	if len(plain) > authnkit.MaxPasswordLen {
		return false, authnkit.ErrPasswordTooLong
	}
	if !strings.HasPrefix(encoded, HashPrefix) {
		return false, fmt.Errorf("%w: not a %q string", authnkit.ErrInvalidHash, HashPrefix)
	}
	return encoded == HashPrefix+plain, nil
}

// NeedsRehash — true только у строки без HashPrefix (боевой хэшер нечитаемую строку тоже
// перехэширует): параметров у детерминированного хеша нет.
func (h *DeterministicHasher) NeedsRehash(encoded string) bool {
	mustRunInTest(testing.Testing())
	return !strings.HasPrefix(encoded, HashPrefix)
}

// VerifyDummy ничего не считает, кроме самого вызова: выравнивать время нечему.
func (h *DeterministicHasher) VerifyDummy(context.Context) {
	mustRunInTest(testing.Testing())
	h.dummies.Add(1)
}

// VerifyCalls — сколько раз звали Verify.
func (h *DeterministicHasher) VerifyCalls() int { return int(h.verifies.Load()) }

// VerifyDummyCalls — сколько раз звали VerifyDummy: холостая проверка на пути «учётной записи нет».
func (h *DeterministicHasher) VerifyDummyCalls() int { return int(h.dummies.Load()) }

// StripHashPrefix снимает HashPrefix — для проверок на исходном значении.
func StripHashPrefix(encoded string) string {
	return strings.TrimPrefix(encoded, HashPrefix)
}

func mustRunInTest(inTest bool) {
	if !inTest {
		panic("authntest: DeterministicHasher used outside go test")
	}
}
