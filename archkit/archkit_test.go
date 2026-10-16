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
			name:  "entry that is the beginning of a violation",
			edits: edits{crossref: goSrc("order", domains+"/customer/delivery/http")},
			opts:  []Option{WithKnownViolations(map[string]string{crossrefWhat: "ADP-02"})},
			want: []string{
				crossrefWhat + "/delivery/http: " + whyCrossDomain,
				`known violation "` + crossrefWhat + `" (ADP-02) matches no violation: remove it`,
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

// Запись WithUncheckedPackages — исключение из правила корня: запись про запас (пакет чист и без
// неё; корень домена его не импортирует) и запись мимо pkg/ и internal/platform/ — отказ.
func TestUncheckedPackages(t *testing.T) {
	t.Parallel()
	const (
		clientPkg = "pkg/client"
		client    = clientPkg + "/client.go"
		wrap      = "pkg/wrap/wrap.go"
		root      = "internal/domain/order/remote.go"
	)
	stale := func(pkg string) []string {
		return []string{`unchecked package "` + pkg + `" sees nothing the domain root does not: remove it from WithUncheckedPackages`}
	}
	unseen := func(pkg string) []string {
		return []string{`unchecked package "` + pkg + `" is imported by no domain root, directly or through pkg/ and internal/platform/: remove it from WithUncheckedPackages`}
	}
	runCases(t, []ruleCase{
		{
			name:  "package with I/O under the domain root",
			edits: edits{client: goSrc("client", "net/http"), root: goSrc("order", mod+"/"+clientPkg)},
			opts:  []Option{WithUncheckedPackages(clientPkg)},
		},
		{
			name: "package with I/O under a wrapper in pkg",
			edits: edits{
				client: goSrc("client", "net/http"),
				wrap:   goSrc("wrap", mod+"/"+clientPkg),
				root:   goSrc("order", mod+"/pkg/wrap"),
			},
			opts: []Option{WithUncheckedPackages(clientPkg)},
		},
		{
			name: "package without I/O",
			opts: []Option{WithUncheckedPackages("pkg/policy")},
			want: stale("pkg/policy"),
		},
		{
			name: "package that is not there",
			opts: []Option{WithUncheckedPackages("internal/platform/ghost")},
			want: stale("internal/platform/ghost"),
		},
		{
			name:  "package over another unchecked one",
			edits: edits{"pkg/caller/caller.go": goSrc("caller", "context", mod+"/internal/platform/identity")},
			opts:  []Option{WithUncheckedPackages("pkg/caller")},
			want:  stale("pkg/caller"),
		},
		// Из двух записей по одной цепочке лишняя — верхняя: без неё дерево по-прежнему зелёное.
		{
			name: "two entries along one chain",
			edits: edits{
				client: goSrc("client", "net/http"),
				wrap:   goSrc("wrap", mod+"/"+clientPkg),
				root:   goSrc("order", mod+"/pkg/wrap"),
			},
			opts: []Option{WithUncheckedPackages("pkg/wrap", clientPkg)},
			want: stale("pkg/wrap"),
		},
		{
			name: "package with I/O that no domain root imports",
			opts: []Option{WithUncheckedPackages("internal/platform/events")},
			want: unseen("internal/platform/events"),
		},
		{
			name: "package with I/O that only a test of the domain root imports",
			edits: edits{
				client:                                 goSrc("client", "net/http"),
				"internal/domain/order/remote_test.go": goSrc("order", mod+"/"+clientPkg),
			},
			opts: []Option{WithUncheckedPackages(clientPkg)},
			want: unseen(clientPkg),
		},
		{
			name: "package with I/O that only the layers of a domain import",
			edits: edits{
				client: goSrc("client", "net/http"),
				"internal/domain/order/infra/postgres/remote.go": goSrc("postgres", mod+"/"+clientPkg),
				"internal/domain/order/module/remote.go":         goSrc("module", mod+"/"+clientPkg),
			},
			opts: []Option{WithUncheckedPackages(clientPkg)},
			want: unseen(clientPkg),
		},
		{
			name: "package whose subpackage the domain root imports",
			edits: edits{
				client:                  goSrc("client", "net/http"),
				"pkg/client/sub/sub.go": goSrc("sub"),
				root:                    goSrc("order", mod+"/"+clientPkg+"/sub"),
			},
			opts: []Option{WithUncheckedPackages(clientPkg)},
			want: unseen(clientPkg),
		},
		// Цепочку до I/O правило корня ведёт только по pkg/ и internal/platform/: пакет за каталогом
		// WithInternalDirs оно не видит, и запись о нём ничего не меняет.
		{
			name: "package with I/O behind a package that the domain root may not import",
			edits: edits{
				client:                                goSrc("client", "net/http"),
				"internal/testkit/orderfix/remote.go": goSrc("orderfix", mod+"/"+clientPkg),
				root:                                  goSrc("order", mod+"/internal/testkit/orderfix"),
			},
			opts: []Option{WithUncheckedPackages(clientPkg)},
			want: append([]string{root + " imports internal/testkit/orderfix: " + whyBridge}, unseen(clientPkg)...),
		},
		{
			name:  "violations and the entry of no use together",
			edits: edits{crossref: goSrc("order", domains+"/customer")},
			opts:  []Option{WithUncheckedPackages("pkg/paging")},
			want:  append([]string{crossrefWhat + ": " + whyCrossDomain}, stale("pkg/paging")...),
		},
	})

	for _, pkg := range []string{"identity", "internal/domain/order", "internal/testkit/orderfix", "cmd/api", "internal", "pkgs/policy", ""} {
		t.Run("form "+pkg, func(t *testing.T) {
			t.Parallel()
			got, err := inspect(fixture(t, nil), configure(append(consumer(), WithUncheckedPackages(pkg))...))
			if want := fmt.Sprintf("WithUncheckedPackages(%q): a package of pkg/ or internal/platform/ is named from the module root, such as internal/platform/identity", pkg); err == nil || err.Error() != want {
				t.Fatalf("inspect = %q, %v; want the error %q", got, err, want)
			}
		})
	}
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

func TestPkgPackagesOption(t *testing.T) {
	t.Parallel()
	for _, pkg := range []string{"pkg", "pkg/", "policy", "pkg/policy/", "pkg//policy", "pkg/./policy", "pkg/../policy", "./pkg/policy", "internal/platform/identity", ""} {
		t.Run(pkg, func(t *testing.T) {
			t.Parallel()
			got, err := inspect(fixture(t, nil), configure(append(consumer(), WithPkgPackages(pkg))...))
			if want := fmt.Sprintf("WithPkgPackages(%q): a package of pkg/ is named from the module root, such as pkg/policy", pkg); err == nil || err.Error() != want {
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

// Правила читают пакеты шаблона ./... и пакеты модуля, которые этот код импортирует: сборка берёт
// пакет по импорту, как бы ни назывался его каталог, а Go-файл-ссылку читает как файл.
func TestFileSet(t *testing.T) {
	t.Parallel()
	const (
		importer = "internal/domain/order/infra/postgres/bridge.go"
		clock    = "package tool\n\nimport \"time\"\n\nvar started = time.Now()\n"
	)
	bridge := goSrc("bridge", domains+"/customer")

	for name, tc := range map[string]struct{ dir, why string }{
		"testdata of a domain":        {"internal/domain/order/testdata/bridge", whyCrossDomain},
		"underscore name in a domain": {"internal/domain/order/_bridge", whyCrossDomain},
		"dot name in a domain":        {"internal/domain/order/.bridge", whyCrossDomain},
		"testdata of pkg":             {"pkg/testdata/bridge", whyLowLayer},
	} {
		t.Run("imported "+name, func(t *testing.T) {
			t.Parallel()
			got := failures(t, edits{tc.dir + "/bridge.go": bridge, importer: goSrc("postgres", mod+"/"+tc.dir)})
			requireFailures(t, got, []string{tc.dir + "/bridge.go imports internal/domain/customer: " + tc.why})
		})
	}

	runCases(t, []ruleCase{
		{
			name: "imported nested module",
			edits: edits{
				"pkg/bridge/go.mod":    "module " + mod + "/pkg/bridge\n",
				"pkg/bridge/bridge.go": bridge,
				importer:               goSrc("postgres", mod+"/pkg/bridge"),
			},
			want: []string{"pkg/bridge/bridge.go imports internal/domain/customer: " + whyLowLayer},
		},
		{
			name: "client in testdata under the domain root",
			edits: edits{
				"pkg/testdata/client/client.go":  goSrc("client", "net/http"),
				"internal/domain/order/probe.go": goSrc("order", mod+"/pkg/testdata/client"),
			},
			want: []string{"internal/domain/order/probe.go imports pkg/testdata/client: " + whyRoot + " (pkg/testdata/client → net/http)"},
		},
		{
			name: "package imported by an imported package",
			edits: edits{
				"pkg/testdata/a/a.go": goSrc("a", mod+"/pkg/_b"),
				"pkg/_b/b.go":         goSrc("b", domains+"/customer"),
				importer:              goSrc("postgres", mod+"/pkg/testdata/a"),
			},
			want: []string{"pkg/_b/b.go imports internal/domain/customer: " + whyLowLayer},
		},
		{
			name: "package imported by a test",
			edits: edits{
				"internal/domain/order/order_test.go":         goSrc("order_test", domains+"/order/testdata/fake"),
				"internal/domain/order/testdata/fake/fake.go": "package fake\n\nimport \"time\"\n\nvar started = time.Now()\n",
			},
			want: []string{"internal/domain/order/testdata/fake/fake.go uses time.Now: " + whyClock},
		},
		{
			name: "not imported",
			edits: edits{
				"vendor/example.org/dep/dep.go":                    goSrc("dep", domains+"/customer/mocks"),
				"pkg/testdata/bridge/bridge.go":                    bridge,
				"internal/domain/order/testdata/bridge/bridge.go":  bridge,
				"internal/domain/order/testdata/clock.go":          clock,
				"internal/domain/order/testdata/broken.go":         "package {",
				"internal/domain/order/_tool/clock.go":             clock,
				"internal/domain/order/.tool/clock.go":             clock,
				"internal/domain/order/tool/go.mod":                "module example.org/tool\n",
				"internal/domain/order/tool/clock.go":              clock,
				"internal/domain/order/tool/broken/x.go":           "package {",
				"internal/domain/order/infra/notes/note.md":        "time.Now()",
				"internal/domain/order/infra/notes/clock.go.txt":   "package {",
				"internal/domain/order/infra/postgres/missing.go":  goSrc("postgres", mod+"/pkg/ghost"),
				"cmd/api/outside.go":                               goSrc("main", mod+"/../escape", mod+"/"),
				"internal/domain/order/infra/postgres/latest.go":   goSrc("postgres", domains+"/order"),
				"internal/domain/order/infra/postgres/doc_test.go": goSrc("postgres", "os"),
			},
		},
		{
			name: "file of the tree next to a module of its own",
			edits: edits{
				"internal/domain/order/tool/clock.go": clock,
			},
			want: []string{"internal/domain/order/tool/clock.go uses time.Now: " + whyClock},
		},
		{
			name: "file named like a test",
			edits: edits{
				"internal/domain/order/clock_tests.go": "package order\n\nimport \"time\"\n\nvar started = time.Now()\n",
			},
			want: []string{"internal/domain/order/clock_tests.go uses time.Now: " + whyClock},
		},
		// Правило каталогов смотрит, где лежит код, а не что из него собирается.
		{
			name: "Go files of internal in any directory",
			edits: edits{
				"internal/_draft/draft.go":       goSrc("draft"),
				"internal/.cache/cache.go":       goSrc("cache"),
				"internal/testdata/data/data.go": "package {",
				"internal/tools/go.mod":          "module example.org/tools\n",
				"internal/tools/tools.go":        goSrc("tools"),
				"internal/notes/readme.go.txt":   "not a Go file",
			},
			want: []string{
				"internal/.cache: " + whyLayout,
				"internal/_draft: " + whyLayout,
				"internal/testdata/data: " + whyLayout,
				"internal/tools: " + whyLayout,
			},
		},
	})

	// Ссылки — в памяти: содержимое записи fstest.MapFS с ModeSymlink — цель ссылки.
	link := func(target string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte(target), Mode: fs.ModeSymlink}
	}
	inspectLinks := func(t *testing.T, files map[string]*fstest.MapFile) []string {
		t.Helper()
		fsys := fixture(t, nil)
		for name, f := range files {
			fsys[name] = f
		}
		got, err := inspect(fsys, configure(consumer()...))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	t.Run("linked file", func(t *testing.T) {
		t.Parallel()
		got := inspectLinks(t, map[string]*fstest.MapFile{
			"tools/crossref.go.txt":             {Data: []byte(goSrc("order", domains+"/customer"))},
			"tools/clock.go.txt":                {Data: []byte("package order\n\nimport \"time\"\n\nvar started = time.Now()\n")},
			"internal/domain/order/crossref.go": link("../../../tools/crossref.go.txt"),
			"internal/domain/order/clock.go":    link("../../../tools/clock.go.txt"),
		})
		requireFailures(t, got, []string{
			crossrefWhat + ": " + whyCrossDomain,
			"internal/domain/order/clock.go uses time.Now: " + whyClock,
		})
	})
	t.Run("linked directory imported by its path", func(t *testing.T) {
		t.Parallel()
		got := inspectLinks(t, map[string]*fstest.MapFile{
			"tools/bridge/bridge.go":       {Data: []byte(bridge)},
			"internal/domain/order/bridge": link("../../../tools/bridge"),
			importer:                       {Data: []byte(goSrc("postgres", domains+"/order/bridge"))},
		})
		requireFailures(t, got, []string{"internal/domain/order/bridge/bridge.go imports internal/domain/customer: " + whyCrossDomain})
	})
	t.Run("link that leads nowhere or to a directory", func(t *testing.T) {
		t.Parallel()
		got := inspectLinks(t, map[string]*fstest.MapFile{
			"tools/dir.go/clock.go":          {Data: []byte(clock)},
			"internal/domain/order/gone.go":  link("../nowhere.go"),
			"internal/domain/order/dir.go":   link("../../../tools/dir.go"),
			"internal/domain/order/notes.md": link("../../../tools/dir.go/clock.go"),
		})
		requireFailures(t, got, nil)
	})
}
