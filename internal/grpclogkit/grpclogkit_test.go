package grpclogkit_test

import (
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc/grpclog"

	"git.stit.tech/stit-core/golibinfra/internal/grpclogkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
)

func TestBridgeMapsLevels(t *testing.T) {
	rec := logtest.Capture()
	lg := grpclogkit.New(rec)

	lg.Info("transport ready")
	lg.Infoln("resolver updated")
	lg.Infof("dial %s", "billing:9090")
	lg.Warning("adjusting keepalive")
	lg.Warningln("subchannel is idle")
	lg.Warningf("retry %d", 2)
	lg.Error("connection closed")
	lg.Errorln("name resolution failed")
	lg.Errorf("received GoAway: %s", "too_many_pings")

	cases := []struct {
		level slog.Level
		want  string
	}{
		{slog.LevelDebug, "transport ready"},
		{slog.LevelDebug, "resolver updated"},
		{slog.LevelDebug, "dial billing:9090"},
		{slog.LevelWarn, "adjusting keepalive"},
		{slog.LevelWarn, "subchannel is idle"},
		{slog.LevelWarn, "retry 2"},
		{slog.LevelError, "connection closed"},
		{slog.LevelError, "name resolution failed"},
		{slog.LevelError, "received GoAway: too_many_pings"},
	}
	for _, tc := range cases {
		e, ok := rec.FindFunc(func(e logtest.Entry) bool {
			return e.Level == tc.level && strings.TrimSpace(e.Str("msg")) == tc.want
		})
		if !ok {
			t.Errorf("%v %q not captured, entries: %+v", tc.level, tc.want, rec.Entries())
			continue
		}
		if e.Msg != "grpc" {
			t.Errorf("entry msg = %q, want %q", e.Msg, "grpc")
		}
	}
}

func TestVFollowsLoggerLevel(t *testing.T) {
	if !grpclogkit.New(logtest.Capture()).V(2) {
		t.Fatal("V must be true while the logger accepts Debug")
	}
	if grpclogkit.New(logtest.CaptureAt(slog.LevelInfo)).V(2) {
		t.Fatal("V must be false while Debug is disabled")
	}
}

// Nop-логгер ставят конструкторы, которым логгер не передали: он не должен
// занимать мост навсегда, иначе настоящий логгер графа не увидит ни одной
// записи транспорта. Из настоящих логгеров выигрывает последний.
func TestInstallSkipsNopAndLastRealLoggerWins(t *testing.T) {
	first, second := logtest.Capture(), logtest.Capture()

	grpclogkit.Install(logkit.Nop())
	grpclogkit.Install(first)
	grpclog.Error("boom")
	if !first.Has(slog.LevelError, "grpc") {
		t.Fatalf("installed logger must receive grpclog output, entries: %+v", first.Entries())
	}

	first.Reset()
	grpclogkit.Install(logkit.Nop())
	grpclogkit.Install(nil)
	grpclog.Warning("still first")
	if !first.Has(slog.LevelWarn, "grpc") {
		t.Fatalf("Install(nil) and Install(Nop) must not take the bridge, entries: %+v", first.Entries())
	}

	first.Reset()
	grpclogkit.Install(second)
	grpclog.Error("second wins")
	if !second.Has(slog.LevelError, "grpc") {
		t.Fatalf("the last real logger must receive grpclog output, entries: %+v", second.Entries())
	}
	if len(first.Entries()) != 0 {
		t.Fatalf("replaced logger must stop receiving output, entries: %+v", first.Entries())
	}
}
