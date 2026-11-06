//go:build unix

package testkit

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// lease — индекс сервера пула kind (n серверов) в исключительной аренде теста: замок flock в каталоге dir держит
// открытый файл, t.Cleanup его закрывает; упавший процесс теста отпускает замок сам. Все заняты — ждёт свободный до
// конца теста.
func lease(t testing.TB, dir, kind string, n int) int {
	t.Helper()
	for {
		i, f, err := tryLease(dir, kind, n)
		if err != nil {
			t.Fatalf("%v", err)
			return 0
		}
		if f != nil {
			t.Cleanup(func() { _ = f.Close() })
			return i
		}
		select {
		case <-t.Context().Done():
			t.Fatalf("testkit: shared %s: no free server of %d: %v", kind, n, t.Context().Err())
			return 0
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// tryLease — первый свободный сервер пула и файл его замка или nil, если заняты все.
func tryLease(dir, kind string, n int) (int, *os.File, error) {
	for i := range n {
		path := filepath.Join(dir, fmt.Sprintf("%s-%d.lock", kind, i))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return 0, nil, fmt.Errorf("testkit: shared %s: lock: %w", kind, err)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return i, f, nil
		}
		_ = f.Close()
	}
	return 0, nil, nil
}
