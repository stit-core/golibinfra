package testkit

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// fatalTB перехватывает Fatalf: newOptions обязан ругаться на чужую опцию,
// а не игнорировать её молча.
type fatalTB struct {
	testing.TB
	msgs []string
}

func (tb *fatalTB) Helper() {}

func (tb *fatalTB) Fatalf(format string, args ...any) {
	tb.msgs = append(tb.msgs, fmt.Sprintf(format, args...))
}

func TestNewOptions_DefaultsAndImageOverride(t *testing.T) {
	tb := &fatalTB{}
	if o := newOptions(tb, support{helper: "StartRedis"}, RedisImage, nil); o.image != RedisImage {
		t.Fatalf("image = %q, want default %q", o.image, RedisImage)
	}
	o := newOptions(tb, support{helper: "StartRedis"}, RedisImage, []Option{WithImage("redis:8-alpine")})
	if o.image != "redis:8-alpine" {
		t.Fatalf("image = %q, want override", o.image)
	}
	if len(tb.msgs) != 0 {
		t.Fatalf("unexpected fatals: %v", tb.msgs)
	}
}

func TestNewOptions_MigrationSourcesKeepOrder(t *testing.T) {
	tb := &fatalTB{}
	empty := fstest.MapFS{}
	o := newOptions(tb, support{helper: "StartPostgres", migrations: true}, PostgresImage, []Option{
		WithMigrations(pgkit.Source{Name: "a", FS: empty, Dir: "."}),
		WithMigrationsDir("testdata/migrations"),
		WithMigrations(pgkit.Source{Name: "b", FS: empty, Dir: "."}),
	})
	names := make([]string, 0, len(o.sources))
	for _, s := range o.sources {
		names = append(names, s.Name)
	}
	want := "a," + pgkit.AppSourceName + ",b"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("source order = %q, want %q", got, want)
	}
	if len(tb.msgs) != 0 {
		t.Fatalf("unexpected fatals: %v", tb.msgs)
	}
}

func TestNewOptions_RejectsForeignOptions(t *testing.T) {
	tests := []struct {
		name string
		sup  support
		opts []Option
		want string
	}{
		{"jetstream on redis", support{helper: "StartRedis"}, []Option{WithJetStream()}, "WithJetStream"},
		{"migrations on nats", support{helper: "StartNATS", jetStream: true}, []Option{WithMigrationsDir(".")}, "WithMigrations"},
		{"jetstream on postgres", support{helper: "StartPostgres", migrations: true}, []Option{WithJetStream()}, "WithJetStream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tb := &fatalTB{}
			newOptions(tb, tt.sup, "img", tt.opts)
			if len(tb.msgs) != 1 || !strings.Contains(tb.msgs[0], tt.want) {
				t.Fatalf("fatals = %v, want exactly one mentioning %q", tb.msgs, tt.want)
			}
			if !strings.Contains(tb.msgs[0], tt.sup.helper) {
				t.Fatalf("fatal %q must name the helper %q", tb.msgs[0], tt.sup.helper)
			}
		})
	}
}
