package pgstore

// CleanupBatchSize — размер батча Cleanup для тестов пакета.
const CleanupBatchSize = cleanupBatchSize

// SetLockSeen ставит точку теста между upsert'ом, увидевшим блокировку, и её чтением.
func SetLockSeen(s *Store, fn func()) { s.lockSeen = fn }
