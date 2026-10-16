package archkit

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

func TestCleanFixture(t *testing.T) {
	t.Parallel()
	requireFailures(t, failures(t, nil), nil)
}

func TestInternalLayout(t *testing.T) {
	t.Parallel()
	runCases(t, []ruleCase{
		{
			name:  "unknown directory",
			edits: edits{"internal/bridge/userbridge/bridge.go": goSrc("userbridge")},
			want:  []string{"internal/bridge/userbridge: " + whyLayout},
		},
		{
			name:  "file in internal itself",
			edits: edits{"internal/doc.go": goSrc("internal")},
			want:  []string{"internal: " + whyLayout},
		},
		{
			name:  "test file counts",
			edits: edits{"internal/tools/tools_test.go": goSrc("tools")},
			want:  []string{"internal/tools: " + whyLayout},
		},
		{
			name:  "directory named like domain",
			edits: edits{"internal/domainx/x.go": goSrc("domainx"), "internal/platforms/x.go": goSrc("platforms")},
			want:  []string{"internal/domainx: " + whyLayout, "internal/platforms: " + whyLayout},
		},
		{
			name:  "one line per package",
			edits: edits{"internal/jobs/a.go": goSrc("jobs"), "internal/jobs/b.go": goSrc("jobs")},
			want:  []string{"internal/jobs: " + whyLayout},
		},
		{
			name:  "directory of the consumer",
			edits: edits{"internal/jobs/jobs.go": goSrc("jobs"), "internal/jobs/cleanup/cleanup.go": goSrc("cleanup")},
			opts:  []Option{WithInternalDirs("internal/jobs")},
		},
	})

	t.Run("without directories of the consumer", func(t *testing.T) {
		t.Parallel()
		got := failuresWith(t, nil, fixturePkg, fixturePure, fixtureUnchecked, fixtureLibs)
		requireFailures(t, got, []string{"internal/testkit/orderfix: " + whyLayout})
	})
}

// Пакет pkg/ — запись WithPkgPackages: перечень пакетов, а не каталогов, и код, где бы он ни лежал.
func TestPkgLayout(t *testing.T) {
	t.Parallel()
	const util = "pkg/util/util.go"
	listed := func(e edits, opts ...Option) []string {
		t.Helper()
		return failuresWith(t, e, append([]Option{fixtureDirs, fixturePure, fixtureUnchecked, fixtureLibs}, opts...)...)
	}
	for _, tc := range []struct {
		name  string
		edits edits
		opts  []Option
		want  []string
	}{
		{name: "listed packages", opts: []Option{fixturePkg}},
		{
			name: "lists add up",
			opts: []Option{WithPkgPackages("pkg/paging"), WithPkgPackages(), WithPkgPackages("pkg/policy")},
		},
		{
			name: "without the list",
			want: []string{"pkg/paging: " + whyPkg, "pkg/policy: " + whyPkg},
		},
		{
			name:  "new package",
			edits: edits{util: goSrc("util"), "pkg/util/util_test.go": goSrc("util_test")},
			opts:  []Option{fixturePkg},
			want:  []string{"pkg/util: " + whyPkg},
		},
		{
			name:  "new package listed",
			edits: edits{util: goSrc("util")},
			opts:  []Option{fixturePkg, WithPkgPackages("pkg/util")},
		},
		{
			name:  "subpackage of a listed package",
			edits: edits{"pkg/policy/rules/rules.go": goSrc("rules")},
			opts:  []Option{fixturePkg},
			want:  []string{"pkg/policy/rules: " + whyPkg},
		},
		{
			name:  "package of tests only",
			edits: edits{"pkg/util/util_test.go": goSrc("util_test")},
			opts:  []Option{fixturePkg},
			want:  []string{"pkg/util: " + whyPkg},
		},
		{
			name:  "file in pkg itself",
			edits: edits{"pkg/doc.go": goSrc("pkg")},
			opts:  []Option{fixturePkg},
			want:  []string{"pkg: " + whyPkg},
		},
		{
			name: "code of pkg in any directory",
			edits: edits{
				"pkg/_draft/draft.go":     goSrc("draft"),
				"pkg/.cache/cache.go":     goSrc("cache"),
				"pkg/testdata/a/a.go":     "package {",
				"pkg/tools/go.mod":        "module example.org/tools\n",
				"pkg/tools/tools.go":      goSrc("tools"),
				"pkg/notes/readme.go.txt": "not a Go file",
			},
			opts: []Option{fixturePkg},
			want: []string{
				"pkg/.cache: " + whyPkg,
				"pkg/_draft: " + whyPkg,
				"pkg/testdata/a: " + whyPkg,
				"pkg/tools: " + whyPkg,
			},
		},
		{
			name: "entry without a package",
			opts: []Option{fixturePkg, WithPkgPackages("pkg/ghost", "pkg/policy/rules")},
			want: []string{
				`pkg package "pkg/ghost" has no Go files: remove it from WithPkgPackages`,
				`pkg package "pkg/policy/rules" has no Go files: remove it from WithPkgPackages`,
			},
		},
		{
			name:  "entry of a package whose code lies in its subpackages",
			edits: edits{"pkg/api/v1/api.go": goSrc("v1")},
			opts:  []Option{fixturePkg, WithPkgPackages("pkg/api")},
			want: []string{
				"pkg/api/v1: " + whyPkg,
				`pkg package "pkg/api" has no Go files: remove it from WithPkgPackages`,
			},
		},
		{
			name:  "known violation",
			edits: edits{util: goSrc("util")},
			opts:  []Option{fixturePkg, WithKnownViolations(map[string]string{"pkg/util": "RHAR-13"})},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireFailures(t, listed(tc.edits, tc.opts...), tc.want)
		})
	}

	// Каталог-ссылка: сборка берёт пакет по импорту, правило — тоже.
	t.Run("linked directory imported by its path", func(t *testing.T) {
		t.Parallel()
		fsys := fixture(t, edits{
			"tools/util/util.go":            goSrc("util"),
			"internal/domain/order/util.go": goSrc("order", mod+"/pkg/util"),
		})
		fsys["pkg/util"] = &fstest.MapFile{Data: []byte("../tools/util"), Mode: fs.ModeSymlink}
		got, err := inspect(fsys, configure(fixtureDirs, fixturePkg, fixturePure, fixtureUnchecked, fixtureLibs))
		if err != nil {
			t.Fatal(err)
		}
		requireFailures(t, got, []string{"pkg/util: " + whyPkg})
	})
}

func TestImportBoundaries(t *testing.T) {
	t.Parallel()
	runCases(t, []ruleCase{
		// Моки.
		{
			name:  "mocks in production code",
			edits: edits{"cmd/api/wire.go": goSrc("main", domains+"/customer/mocks")},
			want:  []string{"cmd/api/wire.go imports internal/domain/customer/mocks: " + whyMocks},
		},
		{
			name:  "mocks in a shared helper",
			edits: edits{"internal/testkit/orderfix/mock.go": goSrc("orderfix", domains+"/customer/mocks")},
			want:  []string{"internal/testkit/orderfix/mock.go imports internal/domain/customer/mocks: " + whyMocks},
		},
		{
			name:  "mocks of the domain in its own code",
			edits: edits{"internal/domain/customer/fake.go": goSrc("customer", domains+"/customer/mocks")},
			want:  []string{"internal/domain/customer/fake.go imports internal/domain/customer/mocks: " + whyMocks},
		},
		{
			name:  "package named like mocks",
			edits: edits{"pkg/policy/policymocks/fake.go": goSrc("policymocks"), "cmd/api/fake.go": goSrc("main", mod+"/pkg/policy/policymocks")},
		},
		{
			name:  "mocks of pkg",
			edits: edits{"pkg/paging/mocks/pager.go": goSrc("mocks"), "pkg/policy/fake.go": goSrc("policy", mod+"/pkg/paging/mocks")},
			want:  []string{"pkg/policy/fake.go imports pkg/paging/mocks: " + whyMocks},
		},

		// platform и pkg ниже доменов.
		{
			name:  "pkg orchestrates domains",
			edits: edits{"pkg/orchestrator/orchestrator.go": goSrc("orchestrator", domains+"/customer", domains+"/order")},
			want: []string{
				"pkg/orchestrator/orchestrator.go imports internal/domain/customer: " + whyLowLayer,
				"pkg/orchestrator/orchestrator.go imports internal/domain/order: " + whyLowLayer,
			},
		},
		{
			name:  "platform bridges a domain",
			edits: edits{"internal/platform/userbridge/bridge.go": goSrc("userbridge", domains+"/customer")},
			want:  []string{"internal/platform/userbridge/bridge.go imports internal/domain/customer: " + whyLowLayer},
		},
		{
			name:  "pkg imports a directory of the consumer",
			edits: edits{"pkg/policy/known.go": goSrc("policy", mod+"/internal/testkit/orderfix")},
			want:  []string{"pkg/policy/known.go imports internal/testkit/orderfix: " + whyLowLayer},
		},
		{
			name:  "platform imports cmd",
			edits: edits{"internal/platform/identity/kits.go": goSrc("identity", mod+"/cmd/migrate")},
			want:  []string{"internal/platform/identity/kits.go imports cmd/migrate: " + whyLowLayer},
		},
		{
			name: "platform and pkg import each other",
			edits: edits{
				"internal/platform/probe/probe.go": goSrc("probe", mod+"/internal/platform/identity", mod+"/pkg/policy"),
				"pkg/probe/probe.go":               goSrc("probe", mod+"/internal/platform/identity", mod+"/pkg/paging"),
			},
		},

		// Домен, кроме доменов, знает только platform и pkg.
		{
			name:  "domain imports cmd",
			edits: edits{"internal/domain/order/kits.go": goSrc("order", mod+"/cmd/migrate")},
			want:  []string{"internal/domain/order/kits.go imports cmd/migrate: " + whyBridge},
		},
		{
			name:  "domain imports a directory of the consumer",
			edits: edits{"internal/domain/order/fixture.go": goSrc("order", mod+"/internal/testkit/orderfix")},
			want:  []string{"internal/domain/order/fixture.go imports internal/testkit/orderfix: " + whyBridge},
		},
		{
			name:  "domain infra imports a package outside the layers",
			edits: edits{"tools/gen/gen.go": goSrc("gen"), "internal/domain/order/infra/postgres/gen.go": goSrc("postgres", mod+"/tools/gen")},
			want:  []string{"internal/domain/order/infra/postgres/gen.go imports tools/gen: " + whyBridge},
		},
		{
			name: "domain and pkg import the package of the module root",
			edits: edits{
				"bridge.go": goSrc("shop", domains+"/customer", domains+"/order/infra/postgres"),
				"internal/domain/order/infra/postgres/bridge.go": goSrc("postgres", mod),
				"pkg/policy/bridge.go":                           goSrc("policy", mod),
			},
			want: []string{
				"internal/domain/order/infra/postgres/bridge.go imports .: " + whyBridge,
				"pkg/policy/bridge.go imports .: " + whyLowLayer,
			},
		},
		{
			name: "cmd and directories of the consumer import anything",
			edits: edits{
				"cmd/worker/main.go":                goSrc("main", domains+"/order/infra/postgres", domains+"/order/module", mod+"/internal/testkit/orderfix"),
				"internal/testkit/orderfix/repo.go": goSrc("orderfix", domains+"/order/infra/postgres", domains+"/customer/module"),
			},
		},

		// Outbox домена.
		{
			name:  "neighbour mounts the outbox",
			edits: edits{"internal/domain/customer/module/outbox.go": goSrc("module", domains+"/order/infra/outbox")},
			want:  []string{"internal/domain/customer/module/outbox.go imports internal/domain/order/infra/outbox: " + whyOutbox},
		},
		{
			name:  "cmd/api mounts the outbox",
			edits: edits{"cmd/api/outbox.go": goSrc("main", domains+"/order/infra/outbox")},
			want:  []string{"cmd/api/outbox.go imports internal/domain/order/infra/outbox: " + whyOutbox},
		},
		{
			name:  "own delivery imports the outbox",
			edits: edits{"internal/domain/order/delivery/http/outbox.go": goSrc("http", domains+"/order/infra/outbox")},
			want:  []string{"internal/domain/order/delivery/http/outbox.go imports internal/domain/order/infra/outbox: " + whyOutbox},
		},
		{
			name:  "command named like cmd/migrate",
			edits: edits{"cmd/migrate-seed/main.go": goSrc("main", domains+"/order/infra/outbox")},
			want:  []string{"cmd/migrate-seed/main.go imports internal/domain/order/infra/outbox: " + whyOutbox},
		},
		{
			name:  "package of cmd/migrate",
			edits: edits{"cmd/migrate/sources/sources.go": goSrc("sources", domains+"/order/infra/outbox")},
		},

		// sqlc-типы.
		{
			name:  "neighbour adapter reads sqlc types",
			edits: edits{"internal/domain/order/module/raw.go": goSrc("module", domains+"/customer/infra/postgres/db")},
			want:  []string{"internal/domain/order/module/raw.go imports internal/domain/customer/infra/postgres/db: " + whySqlc},
		},
		{
			name:  "neighbour repository reads sqlc types",
			edits: edits{"internal/domain/order/infra/postgres/raw.go": goSrc("postgres", domains+"/customer/infra/postgres/db")},
			want:  []string{"internal/domain/order/infra/postgres/raw.go imports internal/domain/customer/infra/postgres/db: " + whySqlc},
		},
		{
			name:  "own delivery reads sqlc types",
			edits: edits{"internal/domain/customer/delivery/http/raw.go": goSrc("http", domains+"/customer/infra/postgres/db")},
			want:  []string{"internal/domain/customer/delivery/http/raw.go imports internal/domain/customer/infra/postgres/db: " + whySqlc},
		},
		{
			name:  "own root reads sqlc types",
			edits: edits{"internal/domain/customer/raw.go": goSrc("customer", domains+"/customer/infra/postgres/db")},
			want:  []string{"internal/domain/customer/raw.go imports internal/domain/customer/infra/postgres/db: " + whySqlc},
		},
		{
			name:  "cmd reads sqlc types",
			edits: edits{"cmd/api/raw.go": goSrc("main", domains+"/customer/infra/postgres/db")},
			want:  []string{"cmd/api/raw.go imports internal/domain/customer/infra/postgres/db: " + whySqlc},
		},
		{
			name:  "package under infra/postgres reads sqlc types",
			edits: edits{"internal/domain/customer/infra/postgres/sub/sub.go": goSrc("sub", domains+"/customer/infra/postgres/db")},
			want:  []string{"internal/domain/customer/infra/postgres/sub/sub.go imports internal/domain/customer/infra/postgres/db: " + whySqlc},
		},

		// Чужой домен — через порт и адаптер в module/.
		{
			name:  "new domain imports neighbours from its root",
			edits: edits{"internal/domain/billing/billing.go": goSrc("billing", domains+"/customer", domains+"/order")},
			want: []string{
				"internal/domain/billing/billing.go imports internal/domain/customer: " + whyCrossDomain,
				"internal/domain/billing/billing.go imports internal/domain/order: " + whyCrossDomain,
			},
		},
		{
			name:  "infra imports a neighbour",
			edits: edits{"internal/domain/order/infra/postgres/neighbour.go": goSrc("postgres", domains+"/customer")},
			want:  []string{"internal/domain/order/infra/postgres/neighbour.go imports internal/domain/customer: " + whyCrossDomain},
		},
		{
			name:  "delivery imports a neighbour",
			edits: edits{"internal/domain/order/delivery/http/neighbour.go": goSrc("http", domains+"/customer")},
			want:  []string{"internal/domain/order/delivery/http/neighbour.go imports internal/domain/customer: " + whyCrossDomain},
		},
		{
			name:  "package under module is not module",
			edits: edits{"internal/domain/order/module/sub/sub.go": goSrc("sub", domains+"/customer")},
			want:  []string{"internal/domain/order/module/sub/sub.go imports internal/domain/customer: " + whyCrossDomain},
		},
		{
			name: "module reaches inside a neighbour",
			edits: edits{"internal/domain/order/module/peek.go": goSrc("module",
				domains+"/customer/delivery/http", domains+"/customer/infra/postgres", domains+"/customer/module")},
			want: []string{
				"internal/domain/order/module/peek.go imports internal/domain/customer/delivery/http: " + whyModuleRoot,
				"internal/domain/order/module/peek.go imports internal/domain/customer/infra/postgres: " + whyModuleRoot,
				"internal/domain/order/module/peek.go imports internal/domain/customer/module: " + whyModuleRoot,
			},
		},

		// Слои своего домена.
		{
			name: "root imports its subpackages",
			edits: edits{"internal/domain/order/transport.go": goSrc("order",
				domains+"/order/delivery/http", domains+"/order/infra/postgres", domains+"/order/module")},
			want: []string{
				"internal/domain/order/transport.go imports internal/domain/order/delivery/http: " + whyRootPorts,
				"internal/domain/order/transport.go imports internal/domain/order/infra/postgres: " + whyRootPorts,
				"internal/domain/order/transport.go imports internal/domain/order/module: " + whyRootPorts,
			},
		},
		{
			name:  "handler reads the repository",
			edits: edits{"internal/domain/customer/delivery/http/repo.go": goSrc("http", domains+"/customer/infra/postgres")},
			want:  []string{"internal/domain/customer/delivery/http/repo.go imports internal/domain/customer/infra/postgres: " + whyDelivery},
		},
		{
			name:  "subscriber reads the repository",
			edits: edits{"internal/domain/customer/delivery/nats/repo.go": goSrc("nats", domains+"/customer/infra/postgres")},
			want:  []string{"internal/domain/customer/delivery/nats/repo.go imports internal/domain/customer/infra/postgres: " + whyDelivery},
		},

		// Вне правила.
		{
			name: "tests",
			edits: edits{
				"internal/domain/order/cross_test.go": goSrc("order_test",
					domains+"/customer", domains+"/customer/infra/postgres/db", domains+"/order/infra/outbox", mod+"/internal/testkit/orderfix"),
				"pkg/policy/policy_test.go": goSrc("policy_test", domains+"/order"),
			},
		},
		{
			name:  "module with the same prefix",
			edits: edits{"internal/domain/order/delivery/http/ext.go": goSrc("http", "example.org/shopping/internal/domain/customer")},
		},
	})
}

func TestDomainRoot(t *testing.T) {
	t.Parallel()
	const root = "internal/domain/order/probe.go"
	importing := func(imports ...string) edits { return edits{root: goSrc("order", imports...)} }

	for _, imp := range []string{
		"database/sql", "database/sql/driver", "net", "net/http", "net/http/httputil", "os", "os/exec", "os/signal", "syscall",
		"github.com/jackc/pgx/v5", "github.com/nats-io/nats.go", kit + "/httpkit", kit + "/cachekit", kit + "/outboxkit/pgstore",
	} {
		t.Run(imp, func(t *testing.T) {
			t.Parallel()
			requireFailures(t, failures(t, importing(imp)), []string{root + " imports " + imp + ": " + whyRoot})
		})
	}

	runCases(t, []ruleCase{
		{
			name: "stdlib without I/O and root libs",
			edits: importing("bufio", "context", "crypto/sha256", "encoding/hex", "encoding/json", "errors", "io", "math/big",
				"net/mail", "net/netip", "net/url", "regexp", "sort", "strconv", "time", "unicode/utf8", kit+"/idkit", "go.uber.org/fx"),
		},
		{
			name:  "client in pkg",
			edits: edits{"pkg/client/client.go": goSrc("client", "context", "net/http", "os"), root: goSrc("order", mod+"/pkg/client")},
			want:  []string{root + " imports pkg/client: " + whyRoot + " (pkg/client → net/http)"},
		},
		{
			name: "wrapper over a client in pkg",
			edits: edits{
				"pkg/client/client.go": goSrc("client", "context", "net/http"),
				"pkg/wrap/wrap.go":     goSrc("wrap", "errors", mod+"/pkg/client"),
				root:                   goSrc("order", mod+"/pkg/wrap"),
			},
			want: []string{root + " imports pkg/wrap: " + whyRoot + " (pkg/wrap → pkg/client → net/http)"},
		},
		{
			name:  "platform package with I/O",
			edits: edits{"internal/platform/probeio/io.go": goSrc("probeio", "os"), root: goSrc("order", mod+"/internal/platform/probeio")},
			want:  []string{root + " imports internal/platform/probeio: " + whyRoot + " (internal/platform/probeio → os)"},
		},
		{
			name: "wrapper in pkg over a platform package with I/O",
			edits: edits{
				"internal/platform/probeio/io.go": goSrc("probeio", "net/http"),
				"pkg/wrap/wrap.go":                goSrc("wrap", mod+"/internal/platform/probeio"),
				root:                              goSrc("order", mod+"/pkg/wrap"),
			},
			want: []string{root + " imports pkg/wrap: " + whyRoot + " (pkg/wrap → internal/platform/probeio → net/http)"},
		},
		{
			name: "bridge in platform into a transport of a domain",
			edits: edits{
				"internal/platform/userbridge/bridge.go": goSrc("userbridge", domains+"/customer/delivery/http"),
				root:                                     goSrc("order", mod+"/internal/platform/userbridge"),
			},
			want: []string{"internal/platform/userbridge/bridge.go imports internal/domain/customer/delivery/http: " + whyLowLayer},
		},
		{
			name:  "client in a package of the pure layers",
			edits: edits{"pkg/policy/client.go": goSrc("policy", "net/http")},
			want: []string{
				"internal/domain/customer/policy.go imports pkg/policy: " + whyRoot + " (pkg/policy → net/http)",
				"internal/domain/customer/usecase.go imports pkg/paging: " + whyRoot + " (pkg/paging → pkg/policy → net/http)",
			},
		},
		{
			name: "wrapper in a package of the pure layers",
			edits: edits{
				"pkg/client/client.go": goSrc("client", "net/http"),
				"pkg/policy/client.go": goSrc("policy", mod+"/pkg/client"),
			},
			want: []string{
				"internal/domain/customer/policy.go imports pkg/policy: " + whyRoot + " (pkg/policy → pkg/client → net/http)",
				"internal/domain/customer/usecase.go imports pkg/paging: " + whyRoot + " (pkg/paging → pkg/policy → pkg/client → net/http)",
			},
		},
		{
			name:  "client in an unchecked package",
			edits: edits{"internal/platform/identity/client.go": goSrc("identity", "net/http", "os")},
		},
		// Запись называет пакет, а не каталог: подпакет доверенного пакета корень проверяет.
		{
			name: "client in a subpackage of an unchecked package",
			edits: edits{
				"internal/platform/identity/remote/remote.go": goSrc("remote", "net/http"),
				root: goSrc("order", mod+"/internal/platform/identity/remote"),
			},
			want: []string{root + " imports internal/platform/identity/remote: " + whyRoot + " (internal/platform/identity/remote → net/http)"},
		},
		{
			name:  "platform package over a kit with I/O",
			edits: importing(mod + "/internal/platform/events"),
			want: []string{root + " imports internal/platform/events: " + whyRoot +
				" (internal/platform/events → " + kit + "/outboxkit/pgstore)"},
		},
		{
			name: "package without I/O all the way down",
			edits: edits{
				"pkg/money/money.go":              goSrc("money", "math/big", kit+"/idkit", mod+"/pkg/paging", mod+"/internal/platform/rates"),
				"internal/platform/rates/rate.go": goSrc("rates", "strconv", mod+"/internal/platform/identity"),
				root:                              goSrc("order", mod+"/pkg/money"),
			},
		},
		{
			name: "I/O in tests of a package",
			edits: edits{
				"pkg/money/money.go":      goSrc("money"),
				"pkg/money/money_test.go": goSrc("money", "net/http", "os"),
				root:                      goSrc("order", mod+"/pkg/money"),
			},
		},
		{
			name: "packages that import each other",
			edits: edits{
				"pkg/a/a.go": goSrc("a", mod+"/pkg/b"),
				"pkg/b/b.go": goSrc("b", mod+"/pkg/a"),
				root:         goSrc("order", mod+"/pkg/a"),
			},
		},
		{
			name: "outside the root",
			edits: edits{
				"internal/domain/order/probe_test.go":         goSrc("order", "os", "net/http"),
				"internal/domain/order/infra/postgres/env.go": goSrc("postgres", "os", "net/http", "github.com/jackc/pgx/v5"),
				"internal/domain/order/delivery/http/env.go":  goSrc("http", "os"),
				"internal/domain/order/module/env.go":         goSrc("module", "os"),
			},
		},
	})

	t.Run("without a root lib", func(t *testing.T) {
		t.Parallel()
		libs := WithRootLibs(kit+"/clockkit", kit+"/logkit", kit+"/outboxkit", kit+"/pgkit", "go.uber.org/fx")
		got := failuresWith(t, nil, fixtureDirs, fixturePkg, fixturePure, fixtureUnchecked, libs)
		requireFailures(t, got, []string{"internal/domain/order/usecase.go imports " + kit + "/idkit: " + whyRoot})
	})

	// Пакет WithUncheckedPackages корень видит, не заглядывая внутрь: без записи identity фикстуры
	// выдаёт свой импорт httpkit каждому файлу корня, который её берёт, — и чистому слою.
	t.Run("without unchecked packages", func(t *testing.T) {
		t.Parallel()
		chain := whyRoot + " (internal/platform/identity → " + kit + "/httpkit)"
		requireFailures(t, failuresWith(t, nil, fixtureDirs, fixturePkg, fixturePure, fixtureLibs), []string{
			"internal/domain/customer/entity.go imports internal/platform/identity: " + chain,
			"internal/domain/customer/policy.go imports internal/platform/identity: " + chain,
			"internal/domain/customer/usecase.go imports internal/platform/identity: " + chain,
			"internal/domain/order/usecase.go imports internal/platform/identity: " + chain,
		})
	})

	// Чистым слоям пакет открывает только WithPurePackages: запись WithUncheckedPackages — не она.
	t.Run("without pure packages", func(t *testing.T) {
		t.Parallel()
		requireFailures(t, failuresWith(t, nil, fixtureDirs, fixturePkg, fixtureUnchecked, fixtureLibs), []string{
			"internal/domain/customer/entity.go imports internal/platform/identity: " + whyPure,
			"internal/domain/customer/policy.go imports internal/platform/identity: " + whyPure,
			"internal/domain/customer/policy.go imports pkg/policy: " + whyPure,
		})
	})
}

func TestPureLayers(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"entity.go", "policy.go", "dto.go", "errors.go", "events.go"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			file := "internal/domain/order/" + name
			requireFailures(t, failures(t, edits{file: goSrc("order", "io")}), []string{file + " imports io: " + whyPure})
		})
	}

	const entity = "internal/domain/order/entity.go"
	runCases(t, []ruleCase{
		{
			name:  "streams, the OS and kits",
			edits: edits{entity: goSrc("order", "bufio", "io/fs", "os", kit+"/logkit", "go.uber.org/fx")},
			want: []string{
				entity + " imports bufio: " + whyPure,
				entity + " imports io/fs: " + whyPure,
				entity + " imports os: " + whyPure,
				entity + " imports " + kit + "/logkit: " + whyPure,
				entity + " imports go.uber.org/fx: " + whyPure,
			},
		},
		{
			name:  "package of the module outside WithPurePackages",
			edits: edits{entity: goSrc("order", mod+"/pkg/paging", mod+"/internal/platform/events")},
			want: []string{
				entity + " imports pkg/paging: " + whyPure,
				entity + " imports internal/platform/events: " + whyPure,
			},
		},
		{
			name: "side-effect-free stdlib and pure packages",
			edits: edits{entity: goSrc("order", "context", "encoding/json", "errors", "fmt", "net/mail", "net/netip", "net/url",
				"regexp", "strings", "time", mod+"/internal/platform/identity", mod+"/pkg/policy")},
		},
		{
			name: "other files of the root and the same names below it",
			edits: edits{
				"internal/domain/order/reader.go":            goSrc("order", "bufio", "io", mod+"/pkg/paging"),
				"internal/domain/order/entity_test.go":       goSrc("order", "io"),
				"internal/domain/order/delivery/http/dto.go": goSrc("http", "io", "net/http"),
			},
		},
	})
}
