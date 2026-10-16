package archkit_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"git.stit.tech/stit-core/golibinfra/archkit"
)

const kit = "git.stit.tech/stit-core/golibinfra"

// recorder записывает отказы Check вместо теста под ним.
type recorder struct {
	testing.TB
	errors []string
	fatal  string
}

func (r *recorder) Helper()                   {}
func (r *recorder) Errorf(f string, a ...any) { r.errors = append(r.errors, fmt.Sprintf(f, a...)) }
func (r *recorder) Fatalf(f string, a ...any) { r.fatal = fmt.Sprintf(f, a...) }

// check зовёт Check из каталога dir с настройками потребителя фикстуры и opts.
func check(t *testing.T, dir string, opts ...archkit.Option) *recorder {
	t.Helper()
	t.Chdir(dir)
	rec := &recorder{TB: t}
	archkit.Check(rec, append([]archkit.Option{
		archkit.WithInternalDirs("internal/testkit"),
		archkit.WithPkgPackages("pkg/paging", "pkg/policy"),
		archkit.WithPurePackages("internal/platform/identity", "pkg/policy"),
		archkit.WithUncheckedPackages("internal/platform/identity"),
		archkit.WithRootLibs(kit+"/clockkit", kit+"/idkit", kit+"/logkit", kit+"/outboxkit", kit+"/pgkit", "go.uber.org/fx"),
	}, opts...)...)
	return rec
}

// service — копия чистой фикстуры на диске с файлами files поверх.
func service(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join("testdata", "clean"))); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Корень модуля — ближайший go.mod вверх от каталога теста: Check зовут из пакета в глубине дерева.
func TestCheck_CleanService(t *testing.T) {
	rec := check(t, filepath.Join("testdata", "clean", "internal", "testkit", "orderfix"))
	if rec.fatal != "" || len(rec.errors) != 0 {
		t.Fatalf("the clean service: fatal %q, errors %q", rec.fatal, rec.errors)
	}
}

func TestCheck_Violations(t *testing.T) {
	const (
		neighbour = "internal/domain/order/crossref.go imports internal/domain/customer"
		clock     = "internal/domain/order/crossref.go uses time.Now"
	)
	root := service(t, map[string]string{
		"internal/domain/order/crossref.go": "package order\n\nimport (\n\t\"time\"\n\n\t\"example.org/shop/internal/domain/customer\"\n)\n\n" +
			"var notFoundAt = map[error]time.Time{customer.ErrNotFound: time.Now()}\n",
	})
	want := []string{
		"archkit: " + neighbour + ": another domain is reached through a port and an adapter in module/",
		"archkit: " + clock + ": the domain reads time through an injected clockkit.Clock",
	}
	t.Run("each is a test error", func(t *testing.T) {
		rec := check(t, filepath.Join(root, "internal", "domain", "order"), nil)
		if rec.fatal != "" || !slices.Equal(rec.errors, want) {
			t.Fatalf("fatal %q, errors:\n%q\nwant:\n%q", rec.fatal, rec.errors, want)
		}
	})
	t.Run("known ones are silent", func(t *testing.T) {
		rec := check(t, root, archkit.WithKnownViolations(map[string]string{neighbour: "ADP-02", clock: "ENF-03"}))
		if rec.fatal != "" || len(rec.errors) != 0 {
			t.Fatalf("fatal %q, errors %q", rec.fatal, rec.errors)
		}
	})
	t.Run("a stale entry is an error", func(t *testing.T) {
		rec := check(t, root, archkit.WithKnownViolations(map[string]string{neighbour: "ADP-02", clock: "ENF-03", "gone": "ADP-09"}))
		stale := []string{`archkit: known violation "gone" (ADP-09) matches no violation: remove it`}
		if rec.fatal != "" || !slices.Equal(rec.errors, stale) {
			t.Fatalf("fatal %q, errors %q, want %q", rec.fatal, rec.errors, stale)
		}
	})
}

// Сборка читает Go-файл-ссылку как файл и берёт пакет по импорту из любого каталога — правила тоже.
func TestCheck_ReadsWhatTheBuildReads(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "crossref.go.txt")
	if err := os.WriteFile(outside, []byte("package order\n\nimport \"example.org/shop/internal/domain/customer\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := service(t, map[string]string{
		"internal/domain/order/testdata/bridge/bridge.go": "package bridge\n\nimport \"example.org/shop/internal/domain/customer\"\n",
		"internal/domain/order/infra/postgres/bridge.go":  "package postgres\n\nimport \"example.org/shop/internal/domain/order/testdata/bridge\"\n",
		"cmd/api/broken.go": "package main\n\nimport \"example.org/shop/../escape\"\n",
	})
	if err := os.Symlink(outside, filepath.Join(root, "internal", "domain", "order", "crossref.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "nowhere.go"), filepath.Join(root, "internal", "domain", "order", "gone.go")); err != nil {
		t.Fatal(err)
	}
	const why = ": another domain is reached through a port and an adapter in module/"
	want := []string{
		"archkit: internal/domain/order/crossref.go imports internal/domain/customer" + why,
		"archkit: internal/domain/order/testdata/bridge/bridge.go imports internal/domain/customer" + why,
	}
	rec := check(t, root)
	if rec.fatal != "" || !slices.Equal(rec.errors, want) {
		t.Fatalf("fatal %q, errors:\n%q\nwant:\n%q", rec.fatal, rec.errors, want)
	}
}

func TestCheck_TreeTheRulesDoNotSee(t *testing.T) {
	t.Run("no domains", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.org/empty\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := check(t, root)
		if want := "archkit: no Go packages under internal/domain: the rules hold the layout internal/domain/<d>"; rec.fatal != want {
			t.Fatalf("fatal %q, errors %q; want fatal %q", rec.fatal, rec.errors, want)
		}
	})
	t.Run("no go.mod", func(t *testing.T) {
		dir := t.TempDir()
		for up := dir; up != filepath.Dir(up); up = filepath.Dir(up) {
			if _, err := os.Stat(filepath.Join(up, "go.mod")); err == nil {
				t.Skipf("the temporary directory lies inside the module of %s", up)
			}
		}
		rec := check(t, dir)
		if want := "archkit: no go.mod in the working directory or above it"; rec.fatal != want {
			t.Fatalf("fatal %q, errors %q; want fatal %q", rec.fatal, rec.errors, want)
		}
	})
}
