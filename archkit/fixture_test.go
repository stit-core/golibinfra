package archkit

import (
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// Фикстура — сервис testdata/clean на этом module path и китах golibinfra.
const (
	mod     = "example.org/shop"
	kit     = "git.stit.tech/stit-core/golibinfra"
	domains = mod + "/internal/domain"
)

// clean — чистая фикстура в памяти: правки случая ложатся поверх её копии.
var clean = sync.OnceValues(func() (fstest.MapFS, error) {
	disk := os.DirFS("testdata/clean")
	fsys := fstest.MapFS{}
	err := fs.WalkDir(disk, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(disk, p)
		fsys[p] = &fstest.MapFile{Data: data}
		return err
	})
	return fsys, err
})

// rootLibs — внешние пакеты, которые видит корень домена фикстуры.
var rootLibs = []string{kit + "/clockkit", kit + "/idkit", kit + "/logkit", kit + "/outboxkit", kit + "/pgkit", "go.uber.org/fx"}

// Настройки потребителя фикстуры.
var (
	fixtureDirs      = WithInternalDirs("internal/testkit")
	fixturePure      = WithPurePackages("internal/platform/identity", "pkg/policy")
	fixtureUnchecked = WithUncheckedPackages("internal/platform/identity")
	fixtureLibs      = WithRootLibs(rootLibs...)
)

func consumer() []Option { return []Option{fixtureDirs, fixturePure, fixtureUnchecked, fixtureLibs} }

// edits — правки фикстуры: путь → содержимое, "" — файла нет.
type edits map[string]string

func fixture(t *testing.T, e edits) fstest.MapFS {
	t.Helper()
	base, err := clean()
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	fsys := maps.Clone(base)
	for name, content := range e {
		if content == "" {
			delete(fsys, name)
			continue
		}
		fsys[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return fsys
}

func configure(opts ...Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// failures — строки отказа по фикстуре с правками и настройками потребителя, дополненными opts.
func failures(t *testing.T, e edits, opts ...Option) []string {
	t.Helper()
	return failuresWith(t, e, append(consumer(), opts...)...)
}

// failuresWith — строки отказа по фикстуре с правками и ровно этими настройками.
func failuresWith(t *testing.T, e edits, opts ...Option) []string {
	t.Helper()
	got, err := inspect(fixture(t, e), configure(opts...))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	return got
}

type ruleCase struct {
	name  string
	edits edits
	opts  []Option
	want  []string
}

func runCases(t *testing.T, cases []ruleCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireFailures(t, failures(t, tc.edits, tc.opts...), tc.want)
		})
	}
}

func requireFailures(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("failures:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// goSrc — Go-файл пакета pkg с импортами: правилам импортов хватает их.
func goSrc(pkg string, imports ...string) string {
	var b strings.Builder
	b.WriteString("package " + pkg + "\n")
	for _, imp := range imports {
		b.WriteString("\nimport \"" + imp + "\"\n")
	}
	return b.String()
}
