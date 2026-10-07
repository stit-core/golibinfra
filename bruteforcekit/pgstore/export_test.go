package pgstore

import (
	"context"
	"time"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
)

// CleanupBatchSize — размер батча Cleanup для тестов пакета.
const CleanupBatchSize = cleanupBatchSize

// SetLockSeen ставит точку теста между upsert'ом, увидевшим блокировку пары, и её чтением.
func SetLockSeen(s *Store, fn func()) { s.hooks = &testHooks{lockSeen: fn} }

// SetSubjectReset ставит точку теста в RecordSuccess между строкой субъекта и строкой пары.
func SetSubjectReset(s *Store, fn func()) { s.hooks = &testHooks{subjectReset: fn} }

// RetryAfter — RetryAfter ответа «заблокировано» при сроке until и часах базы now.
func RetryAfter(s *Store, until, now time.Time) time.Duration {
	return s.locked(context.Background(), bruteforcekit.ScopePair, 1, until, now).RetryAfter
}
