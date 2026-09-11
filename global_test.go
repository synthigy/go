package synthigy

import (
	"context"
	"errors"
	"testing"
)

// staticCfg builds an offline client (static token, no network) for exercising
// the single-client lifecycle without a server.
func staticCfg(tok string) Config {
	return Config{Endpoint: "http://127.0.0.1:0", Token: tok, StaticToken: true}
}

func TestConnectDisconnectLifecycle(t *testing.T) {
	Disconnect()
	if Default() != nil {
		t.Fatal("Default should be nil before Connect")
	}
	if err := Connect(staticCfg("tok-1")); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if Default() == nil {
		t.Fatal("Default should be set after Connect")
	}
	// Static token resolves offline via the default client.
	got, err := Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "tok-1" {
		t.Fatalf("Token = %q, want tok-1", got)
	}
	Disconnect()
	if Default() != nil {
		t.Fatal("Default should be nil after Disconnect")
	}
}

func TestNotConnectedError(t *testing.T) {
	Disconnect()
	_, err := Search(context.Background(), "user", nil, Selection{"name": nil})
	var se *Error
	if !errors.As(err, &se) || se.Code != "NOT_CONNECTED" {
		t.Fatalf("want NOT_CONNECTED error, got %v", err)
	}
}

func TestNotConnectedPanics(t *testing.T) {
	Disconnect()
	defer func() {
		if recover() == nil {
			t.Fatal("Listen before Connect should panic")
		}
	}()
	_ = Listen(context.Background())
}

// TestDestructiveReconnect covers the server-restart idiom: a second Connect
// installs a distinct client (the previous one is destroyed), and a failed
// Connect leaves the current default untouched.
func TestDestructiveReconnect(t *testing.T) {
	if err := Connect(staticCfg("A")); err != nil {
		t.Fatalf("Connect A: %v", err)
	}
	first := Default()

	if err := Connect(staticCfg("B")); err != nil {
		t.Fatalf("Connect B: %v", err)
	}
	second := Default()
	if first == second {
		t.Fatal("reconnect should install a new client instance")
	}
	if tok, _ := Token(context.Background()); tok != "B" {
		t.Fatalf("Token after reconnect = %q, want B", tok)
	}

	// A construction error must NOT replace the current default.
	if err := Connect(Config{Endpoint: ""}); err == nil {
		t.Fatal("Connect with empty endpoint should error")
	}
	if Default() != second {
		t.Fatal("failed Connect must leave the current default in place")
	}
	Disconnect()
}
