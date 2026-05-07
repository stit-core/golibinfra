package authntest

import "strings"

const HashPrefix = "hash:"

type DeterministicHasher struct{}

func (DeterministicHasher) Hash(plain string) (string, error) {
	return HashPrefix + plain, nil
}

func (DeterministicHasher) Verify(plain, encoded string) (bool, error) {
	return encoded == HashPrefix+plain, nil
}

func StripHashPrefix(encoded string) string {
	return strings.TrimPrefix(encoded, HashPrefix)
}
