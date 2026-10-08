package main

import (
	"io/fs"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// repo — копия .go, .md и go.mod репозитория в памяти: инъекции правят её, а не дерево.
var repo = sync.OnceValues(func() (fstest.MapFS, error) {
	root := os.DirFS("../..")
	files := fstest.MapFS{}
	err := fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".md") || p == "go.mod" {
			data, err := fs.ReadFile(root, p)
			if err != nil {
				return err
			}
			files[p] = &fstest.MapFile{Data: data}
		}
		return nil
	})
	return files, err
})

// edit заменяет ровно одно совпадение re в файле копии.
func edit(t *testing.T, files fstest.MapFS, path, re, repl string) {
	t.Helper()
	f := files[path]
	if f == nil {
		t.Fatalf("no %s in repo copy", path)
	}
	rx := regexp.MustCompile(re)
	if n := len(rx.FindAllIndex(f.Data, -1)); n != 1 {
		t.Fatalf("%s: %d matches of %q, want 1", path, n, re)
	}
	files[path] = &fstest.MapFile{Data: rx.ReplaceAll(f.Data, []byte(repl))}
}

func add(files fstest.MapFS, path, src string) {
	files[path] = &fstest.MapFile{Data: []byte(src)}
}

// Инъекции E1–E8 базового аудита (KDOC-04), G32 и G01 (поле без тега) — каждая
// краснит проверку с понятной причиной; E0 и E7 — контроль.
func TestCheck(t *testing.T) {
	tests := []struct {
		name   string
		inject func(*testing.T, fstest.MapFS)
		want   string // фрагмент проблемы; пусто — проблем нет
	}{
		{name: "E0 repo as is", inject: func(*testing.T, fstest.MapFS) {}},
		{
			name: "E1 code default changed, docs not",
			inject: func(t *testing.T, f fstest.MapFS) {
				edit(t, f, "outboxkit/config.go", `(envconfig:"OUTBOX_BATCH_SIZE"\s+default:)"100"`, `${1}"50"`)
			},
			want: "OUTBOX_BATCH_SIZE: дефолт в доках «`100`», в коде «50»",
		},
		{
			name: "E2 tag renamed in code",
			inject: func(t *testing.T, f fstest.MapFS) {
				edit(t, f, "outboxkit/config.go", `"OUTBOX_BATCH_SIZE"`, `"OUTBOX_BATCHSIZE"`)
			},
			want: "OUTBOX_BATCHSIZE — нет строки таблицы",
		},
		{
			name: "E3 tag under internal without README",
			inject: func(_ *testing.T, f fstest.MapFS) {
				add(f, "internal/otelkit/zz_inj.go", "package otelkit\n\ntype injCfg struct {\n\tX string `envconfig:\"OTELKIT_INJECTED\"`\n}\n")
			},
			want: "internal/otelkit: нет README.md ни в каталоге, ни выше — переменные без доков: OTELKIT_INJECTED",
		},
		{
			name: "E4 root table default differs from code",
			inject: func(t *testing.T, f fstest.MapFS) {
				edit(t, f, "README.md", "(\\| `NATS_URL` \\| `natskit` \\| )`nats://localhost:4222`", "${1}`nats://evil:4222`")
			},
			want: "NATS_URL: дефолт в корневой таблице «`nats://evil:4222`»",
		},
		{
			name: "E5 kit README default differs from code",
			inject: func(t *testing.T, f fstest.MapFS) {
				edit(t, f, "logkit/README.md", "(\\| `LOG_LEVEL` \\| `Level` \\| `string` \\| )`info`", "${1}`debug`")
			},
			want: "LOG_LEVEL: дефолт в доках «`debug`», в коде «info»",
		},
		{
			name: "E6 os.Getenv past envconfig",
			inject: func(_ *testing.T, f fstest.MapFS) {
				add(f, "cachekit/zz_inj.go", "package cachekit\n\nimport \"os\"\n\nvar injKnob = os.Getenv(\"CACHEKIT_SECRET_KNOB\")\n")
			},
			want: "CACHEKIT_SECRET_KNOB — нет строки таблицы в доках cachekit",
		},
		{
			name: "E7 non-endpoint row dropped from root table",
			inject: func(t *testing.T, f fstest.MapFS) {
				edit(t, f, "README.md", "\\| `OTEL_SDK_DISABLED` \\|[^\n]*\n", "")
			},
		},
		{
			name: "E8 tag left only in prose",
			inject: func(t *testing.T, f fstest.MapFS) {
				edit(t, f, "outboxkit/docs/configuration.md", "\\| `OUTBOX_INTERVAL` \\|[^\n]*\n", "")
			},
			want: "OUTBOX_INTERVAL — нет строки таблицы в доках outboxkit",
		},
		{
			name: "G32 new kit without README",
			inject: func(_ *testing.T, f fstest.MapFS) {
				add(f, "zzkit/zzkit.go", "package zzkit\n\n// Config — кит без README.\ntype Config struct {\n\tToken string `envconfig:\"ZZKIT_TOKEN\"`\n}\n")
			},
			want: "zzkit: нет README.md ни в каталоге, ни выше — переменные без доков: ZZKIT_TOKEN",
		},
		{
			name: "G01 config field without tag",
			inject: func(t *testing.T, f fstest.MapFS) {
				edit(t, f, "flagkit/flag.go", "(Definitions string `envconfig:\"FEATURE_FLAGS\"`)", "${1}\n\tExtra string")
			},
			want: "поле Config.Extra без envconfig-тега",
		},
		{
			name: "field marked ignored",
			inject: func(t *testing.T, f fstest.MapFS) {
				edit(t, f, "flagkit/flag.go", "(Definitions string `envconfig:\"FEATURE_FLAGS\"`)", "${1}\n\tExtra string `ignored:\"true\"`")
			},
		},
		{
			name: "untagged field inside nested config",
			inject: func(t *testing.T, f fstest.MapFS) {
				edit(t, f, "flagkit/flag.go", "(Definitions string `envconfig:\"FEATURE_FLAGS\"`)", "${1}\n\tExtra extraConfig")
				add(f, "flagkit/zz_extra.go", "package flagkit\n\ntype extraConfig struct {\n\tKnob string\n}\n")
			},
			want: "поле Config.Extra.Knob без envconfig-тега",
		},
	}
	base, err := repo()
	if err != nil {
		t.Fatalf("read repo: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := maps.Clone(base)
			tt.inject(t, files)
			problems, _, err := check(files)
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			got := strings.Join(problems, "\n")
			switch {
			case tt.want == "" && len(problems) > 0:
				t.Fatalf("want no problems, got:\n%s", got)
			case tt.want != "" && !strings.Contains(got, tt.want):
				t.Fatalf("want a problem with %q, got:\n%s", tt.want, got)
			}
		})
	}
}

// Вход make release-diff: смена дефолта и снятый тег видны как пропавшая и новая строка, а дерево
// с проблемами (поле без тега) читается — так читается и прошлая версия.
func TestCodeVars(t *testing.T) {
	base, err := repo()
	if err != nil {
		t.Fatalf("read repo: %v", err)
	}
	files := maps.Clone(base)
	before, err := codeVars(files)
	if err != nil {
		t.Fatalf("codeVars: %v", err)
	}
	if !slices.IsSorted(before) || len(slices.Compact(slices.Clone(before))) != len(before) {
		t.Fatalf("lines are not sorted and unique:\n%s", strings.Join(before, "\n"))
	}
	for _, want := range []string{"OUTBOX_BATCH_SIZE=100", "FEATURE_FLAGS="} {
		if !slices.Contains(before, want) {
			t.Fatalf("no line %q in:\n%s", want, strings.Join(before, "\n"))
		}
	}

	edit(t, files, "outboxkit/config.go", `(envconfig:"OUTBOX_BATCH_SIZE"\s+default:)"100"`, `${1}"50"`)
	edit(t, files, "flagkit/flag.go", "(Definitions string) `envconfig:\"FEATURE_FLAGS\"`", "${1}")
	after, err := codeVars(files)
	if err != nil {
		t.Fatalf("codeVars of a tree with problems: %v", err)
	}
	gone := slices.DeleteFunc(slices.Clone(before), func(v string) bool { return slices.Contains(after, v) })
	added := slices.DeleteFunc(slices.Clone(after), func(v string) bool { return slices.Contains(before, v) })
	if !slices.Equal(gone, []string{"FEATURE_FLAGS=", "OUTBOX_BATCH_SIZE=100"}) || !slices.Equal(added, []string{"OUTBOX_BATCH_SIZE=50"}) {
		t.Fatalf("gone %q, added %q", gone, added)
	}
}
