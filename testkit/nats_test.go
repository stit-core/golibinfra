package testkit_test

import (
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"git.stit.tech/stit-core/golibinfra/testkit"
)

func connectNATS(t *testing.T, url string) *natsgo.Conn {
	t.Helper()
	nc, err := natsgo.Connect(url, natsgo.Timeout(10*time.Second))
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func TestStartNATS_CoreOnlyByDefault(t *testing.T) {
	fix := testkit.StartNATS(t)
	if !strings.HasPrefix(fix.URL, "nats://") || fix.Container == nil {
		t.Fatalf("fixture = %+v", fix)
	}

	nc := connectNATS(t, fix.URL)
	sub, err := nc.SubscribeSync("core.subject")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := nc.Publish("core.subject", []byte("ping")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("next msg: %v", err)
	}
	if string(msg.Data) != "ping" {
		t.Fatalf("payload = %q, want %q", msg.Data, "ping")
	}

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream client: %v", err)
	}
	if _, err := js.AccountInfo(t.Context()); err == nil {
		t.Fatal("JetStream must be off without WithJetStream")
	}
}

func TestStartNATS_WithJetStream(t *testing.T) {
	fix := testkit.StartNATS(t, testkit.WithJetStream())
	js, err := jetstream.New(connectNATS(t, fix.URL))
	if err != nil {
		t.Fatalf("jetstream client: %v", err)
	}
	if _, err := js.AccountInfo(t.Context()); err != nil {
		t.Fatalf("AccountInfo: %v", err)
	}

	stream, err := js.CreateStream(t.Context(), jetstream.StreamConfig{
		Name:     "TESTKIT",
		Subjects: []string{"testkit.>"},
		Storage:  jetstream.MemoryStorage,
	})
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if _, err := js.Publish(t.Context(), "testkit.event", []byte("v")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	info, err := stream.Info(t.Context())
	if err != nil {
		t.Fatalf("stream info: %v", err)
	}
	if info.State.Msgs != 1 {
		t.Fatalf("stream msgs = %d, want 1", info.State.Msgs)
	}
}
