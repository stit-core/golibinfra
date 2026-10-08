package archkit

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

const (
	crossref     = "internal/domain/order/crossref.go"
	crossrefWhat = crossref + " imports internal/domain/customer"
)

func TestKnownViolations(t *testing.T) {
	t.Parallel()
	violation := edits{crossref: goSrc("order", domains+"/customer")}
	runCases(t, []ruleCase{
		{
			name:  "violation outside the list",
			edits: violation,
			opts:  []Option{WithKnownViolations(nil), WithKnownViolations(map[string]string{})},
			want:  []string{crossrefWhat + ": " + whyCrossDomain},
		},
		{
			name:  "known violation",
			edits: violation,
			opts:  []Option{WithKnownViolations(map[string]string{crossrefWhat: "ADP-02"})},
		},
		{
			name:  "known among others",
			edits: edits{crossref: goSrc("order", domains+"/customer", "os")},
			opts:  []Option{WithKnownViolations(map[string]string{crossrefWhat: "ADP-02"})},
			want:  []string{crossref + " imports os: " + whyRoot},
		},
		{
			name:  "lists add up",
			edits: edits{crossref: goSrc("order", domains+"/customer", "os")},
			opts: []Option{
				WithKnownViolations(map[string]string{crossrefWhat: "ADP-02"}),
				WithKnownViolations(map[string]string{crossref + " imports os": "TST-05"}),
			},
		},
		{
			name: "entry without a violation",
			opts: []Option{WithKnownViolations(map[string]string{crossrefWhat: "ADP-02"})},
			want: []string{`known violation "` + crossrefWhat + `" (ADP-02) matches no violation: remove it`},
		},
		{
			name:  "entry for another file",
			edits: violation,
			opts:  []Option{WithKnownViolations(map[string]string{"internal/domain/order/other.go imports internal/domain/customer": "ADP-02"})},
			want: []string{
				crossrefWhat + ": " + whyCrossDomain,
				`known violation "internal/domain/order/other.go imports internal/domain/customer" (ADP-02) matches no violation: remove it`,
			},
		},
		{
			name:  "entry without a finding ID",
			edits: violation,
			opts:  []Option{WithKnownViolations(map[string]string{crossrefWhat: "TODO"})},
			want:  []string{`known violation "` + crossrefWhat + `" needs a finding ID such as ADP-02, got "TODO"`},
		},
	})

	for id, valid := range map[string]bool{
		"ADP-02": true, "KGATE-08": true, "PROJ-1234": true, "A1-7": true,
		"": false, "TODO": false, "adp-02": false, "ADP-": false, "-02": false, "ADP 02": false, "ADP-02 later": false, "see ADP-02": false, "ADP-0x": false,
	} {
		t.Run(fmt.Sprintf("id %q", id), func(t *testing.T) {
			t.Parallel()
			got := failures(t, violation, WithKnownViolations(map[string]string{crossrefWhat: id}))
			if valid != (len(got) == 0) {
				t.Fatalf("finding ID %q: failures %q, valid = %v", id, got, valid)
			}
		})
	}
}

// Дерево, которое правила не видят, — отказ, а не зелёный прогон.
func TestBrokenTree(t *testing.T) {
	t.Parallel()
	withoutDomains := edits{}
	withoutTables := edits{"migrations/0001_customers.up.sql": "", "migrations/0002_orders.up.sql": ""}
	base, err := clean()
	if err != nil {
		t.Fatal(err)
	}
	for name := range base {
		if strings.HasPrefix(name, domainDir+"/") || strings.HasPrefix(name, "cmd/") || strings.HasPrefix(name, "internal/testkit/") {
			withoutDomains[name] = ""
		}
	}
	for _, tc := range []struct {
		name  string
		edits edits
		want  string
	}{
		{"no go.mod", edits{"go.mod": ""}, "read go.mod: open go.mod: file does not exist"},
		{"go.mod without a module", edits{"go.mod": "go 1.26\n\n// module example.org/shop\n"}, "go.mod has no module line"},
		{"no domains", withoutDomains, "no Go packages under internal/domain: the rules hold the layout internal/domain/<d>"},
		{"queries without tables", withoutTables, "domains have queries, but migrations/*.up.sql create no tables: the rule of own tables would check nothing"},
		{"domain file that does not parse", edits{"internal/domain/order/broken.go": "package order\n\nfunc {\n"}, "internal/domain/order/broken.go:3:6: expected 'IDENT', found '{'"},
		{"imports that do not parse", edits{"pkg/broken/broken.go": "package broken\n\nimport (\n"}, "pkg/broken/broken.go:3:10: expected ')', found 'EOF'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := inspect(fixture(t, tc.edits), configure(consumer()...))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("inspect = %q, %v; want the error %q", got, err, tc.want)
			}
		})
	}

	t.Run("service without a database", func(t *testing.T) {
		t.Parallel()
		e := edits{}
		for name := range base {
			if strings.HasSuffix(name, ".sql") {
				e[name] = ""
			}
		}
		requireFailures(t, failures(t, e), nil)
	})
}

func TestInternalDirsOption(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"internal", "internal/", "testkit", "internal/testkit/orderfix", "pkg/testkit", ""} {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			got, err := inspect(fixture(t, nil), configure(append(consumer(), WithInternalDirs(dir))...))
			if want := fmt.Sprintf("WithInternalDirs(%q): a directory of internal/ is named from the module root, such as internal/testkit", dir); err == nil || err.Error() != want {
				t.Fatalf("inspect = %q, %v; want the error %q", got, err, want)
			}
		})
	}
}

// Module path — из go.mod: копия сервиса под другим путём проверяется теми же правилами.
func TestModulePath(t *testing.T) {
	t.Parallel()
	violation := crossrefWhat + ": " + whyCrossDomain
	for name, gomod := range map[string]string{
		"quoted":       "module \"" + mod + "\"\n\ngo 1.26\n",
		"with comment": "// Сервис магазина.\nmodule " + mod + " // корень\n\ngo 1.26\n",
		"tab and CRLF": "module\t" + mod + "\r\n\r\ngo 1.26\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requireFailures(t, failures(t, edits{"go.mod": gomod}), nil)
			requireFailures(t, failures(t, edits{"go.mod": gomod, crossref: goSrc("order", domains+"/customer")}), []string{violation})
		})
	}

	t.Run("fork", func(t *testing.T) {
		t.Parallel()
		const fork = "git.example.org/acme/backend"
		base, err := clean()
		if err != nil {
			t.Fatal(err)
		}
		renamed := fstest.MapFS{}
		for name, f := range base {
			renamed[name] = &fstest.MapFile{Data: []byte(strings.ReplaceAll(string(f.Data), mod, fork))}
		}
		inspectFork := func(src string) []string {
			fsys := fstest.MapFS{crossref: &fstest.MapFile{Data: []byte(src)}}
			for name, f := range renamed {
				fsys[name] = f
			}
			got, err := inspect(fsys, configure(consumer()...))
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		requireFailures(t, inspectFork(goSrc("order")), nil)
		requireFailures(t, inspectFork(goSrc("order", fork+"/internal/domain/customer")), []string{violation})
		// Прежний путь для копии — чужой модуль: не сосед по домену, а внешний пакет в корне.
		requireFailures(t, inspectFork(goSrc("order", domains+"/customer")),
			[]string{crossref + " imports " + domains + "/customer: " + whyRoot})
	})
}

// Правила видят те же каталоги, что go build: без testdata, vendor, имён на "." и "_" и вложенных
// модулей.
func TestSkippedDirectories(t *testing.T) {
	t.Parallel()
	const clock = "package tool\n\nimport \"time\"\n\nvar started = time.Now()\n"
	e := edits{
		"internal/_draft/draft.go":                  goSrc("draft"),
		"internal/.cache/cache.go":                  goSrc("cache"),
		"internal/testdata/data.go":                 goSrc("data"),
		"vendor/example.org/dep/dep.go":             goSrc("dep", domains+"/customer/mocks"),
		"internal/domain/order/testdata/clock.go":   clock,
		"internal/domain/order/_tool/clock.go":      clock,
		"internal/domain/order/.tool/clock.go":      clock,
		"internal/domain/order/tool/go.mod":         "module example.org/tool\n",
		"internal/domain/order/tool/clock.go":       clock,
		"internal/domain/order/tool/broken/x.go":    "package {",
		"internal/domain/order/testdata/broken.go":  "package {",
		"internal/domain/order/infra/notes/note.md": "time.Now()",
	}
	requireFailures(t, failures(t, e), nil)

	e["internal/domain/order/tool/go.mod"] = ""
	e["internal/domain/order/tool/broken/x.go"] = ""
	requireFailures(t, failures(t, e), []string{"internal/domain/order/tool/clock.go uses time.Now: " + whyClock})

	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		fsys := fixture(t, nil)
		fsys["internal/domain/order/link.go"] = &fstest.MapFile{Data: []byte("package {"), Mode: fs.ModeSymlink}
		got, err := inspect(fsys, configure(consumer()...))
		if err != nil {
			t.Fatal(err)
		}
		requireFailures(t, got, nil)
	})
}
