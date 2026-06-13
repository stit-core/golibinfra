package grpcclient

import (
	"context"
	"testing"
)

func TestDialBuildsConn(t *testing.T) {
	// grpc.NewClient ленивый — соединение не устанавливается, проверяем сборку.
	conn, err := Dial(Config{Target: "passthrough:///x", Token: "tok"}, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	if conn == nil {
		t.Fatal("nil conn")
	}
}

func TestBearerPerRPC(t *testing.T) {
	c := bearerPerRPC{token: "t", secure: false}
	md, err := c.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	if md["authorization"] != "Bearer t" {
		t.Fatalf("authorization = %q, want %q", md["authorization"], "Bearer t")
	}
	if c.RequireTransportSecurity() {
		t.Fatal("RequireTransportSecurity = true, want false for insecure")
	}
}
