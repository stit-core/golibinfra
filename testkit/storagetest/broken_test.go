package storagetest_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/storagekit"
	"git.stit.tech/stit-core/golibinfra/testkit/storagetest"
)

const brokenEnv = "STORAGETEST_BROKEN"

// trustingStorage не сверяет заявленный размер с телом: усечённая загрузка принимается.
type trustingStorage struct{ storagekit.Storage }

func (s trustingStorage) Put(ctx context.Context, r io.Reader, contentType string, _ int64) (storagekit.Ref, error) {
	return s.Storage.Put(ctx, r, contentType, -1)
}

// Регрессия RTST-04: набор — мерка для боевых адаптеров, и заведомо сломанная реализация обязана
// его провалить — в том кейсе, который держит нарушенное правило. Кейс с ослабленным
// утверждением её пропустил бы; сам набор идёт в дочернем процессе этого же бинарника.
func TestRunContract_BrokenStorageFails(t *testing.T) {
	if os.Getenv(brokenEnv) != "" {
		storagetest.RunContract(t, func(*testing.T) storagetest.Subject {
			m := storagekit.NewMemoryStorage(storagekit.NewURLBuilder("https://cdn.example.com", "media", storagekit.URLStylePath))
			return storagetest.Subject{Storage: trustingStorage{m}, Presigner: m}
		})
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunContract_BrokenStorageFails$", "-test.count=1")
	cmd.Env = append(os.Environ(), brokenEnv+"=put-trusts-the-size")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "--- FAIL: TestRunContract_BrokenStorageFails/PutRejectsSizeMismatch") {
		t.Fatalf("the contract must fail a storage that does not check the declared size (err %v):\n%s", err, out)
	}
}
