package synthigy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOnboardSuccess(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"onboard_url": "http://x/oauth/claim?token=t", "expires_at": 123,
			"user": map[string]any{"xid": "user-xid-1"},
		})
	}))
	out, err := c.Onboard(context.Background(), "user-xid-1",
		OnboardReset(true), OnboardMethods("password"), OnboardTTL(600),
		OnboardReturnURL("https://app.example.com/callback"))
	if err != nil {
		t.Fatalf("Onboard: %v", err)
	}
	if gotPath != "/oauth/onboard" {
		t.Errorf("path = %s", gotPath)
	}
	if out.ExpiresAt != 123 {
		t.Errorf("expires_at = %v", out.ExpiresAt)
	}
	if out.User.XID != "user-xid-1" {
		t.Errorf("user.xid = %v", out.User.XID)
	}
	if gotBody["xid"] != "user-xid-1" || gotBody["reset"] != true ||
		gotBody["ttl_seconds"] != float64(600) ||
		gotBody["return_url"] != "https://app.example.com/callback" {
		t.Errorf("body wrong: %v", gotBody)
	}
	methods, _ := gotBody["methods"].([]any)
	if len(methods) != 1 || methods[0] != "password" {
		t.Errorf("methods wrong: %v", gotBody["methods"])
	}
}

func TestOnboardOmitsUnsetOptionals(t *testing.T) {
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"onboard_url": "x", "expires_at": 1})
	}))
	if _, err := c.Onboard(context.Background(), "user-xid-1"); err != nil {
		t.Fatalf("Onboard: %v", err)
	}
	if len(gotBody) != 1 || gotBody["xid"] != "user-xid-1" {
		t.Errorf("expected only xid in body, got %v", gotBody)
	}
}

func TestOnboardForbiddenMapsToError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "provision_forbidden"})
	}))
	_, err := c.Onboard(context.Background(), "user-xid-1")
	var se *Error
	if err == nil {
		t.Fatal("expected error")
	}
	se, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T", err)
	}
	if se.Code != "PROVISION_FORBIDDEN" || se.Category != "auth" || se.Status != 403 {
		t.Errorf("bad error: %+v", se)
	}
}

func TestOnboardUnknownXIDMapsToError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "user_not_found"})
	}))
	_, err := c.Onboard(context.Background(), "does-not-exist")
	se, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T (%v)", err, err)
	}
	if se.Code != "USER_NOT_FOUND" || se.Status != 404 {
		t.Errorf("bad error: %+v", se)
	}
}

func TestOnboardNonJSONErrorBodyDoesNotCrash(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not found"))
	}))
	_, err := c.Onboard(context.Background(), "user-xid-1")
	se, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T (%v)", err, err)
	}
	if se.Code != "HTTP_ERROR" || se.Status != 404 {
		t.Errorf("bad error: %+v", se)
	}
}

func TestOnboardCompleteSuccess(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user": map[string]any{"xid": "user-xid-1"}, "active": true,
		})
	}))
	out, err := c.OnboardComplete(context.Background(), "the-ticket")
	if err != nil {
		t.Fatalf("OnboardComplete: %v", err)
	}
	if gotPath != "/oauth/onboard/complete" {
		t.Errorf("path = %s", gotPath)
	}
	if !out.Active || out.User.XID != "user-xid-1" {
		t.Errorf("bad result: %+v", out)
	}
	if len(gotBody) != 1 || gotBody["ticket"] != "the-ticket" {
		t.Errorf("expected only ticket in body (never a credential), got %v", gotBody)
	}
}

func TestOnboardCompleteWrongClientMapsToError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "claim_invalid"})
	}))
	_, err := c.OnboardComplete(context.Background(), "not-my-ticket")
	se, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T (%v)", err, err)
	}
	if se.Code != "CLAIM_INVALID" || se.Category != "auth" || se.Status != 400 {
		t.Errorf("bad error: %+v", se)
	}
}

func TestOnboardPackageLevelMirrors(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"xid": "u"}, "active": true})
	}))
	defer srv.Close()
	Disconnect()
	defer Disconnect()
	if err := Connect(Config{Endpoint: srv.URL, Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if out, err := Onboard(context.Background(), "u"); err != nil || out.User.XID != "u" {
		t.Fatalf("Onboard = %+v, %v", out, err)
	}
	if out, err := OnboardComplete(context.Background(), "ticket"); err != nil || !out.Active {
		t.Fatalf("OnboardComplete = %+v, %v", out, err)
	}
	if strings.Join(paths, ",") != "/oauth/onboard,/oauth/onboard/complete" {
		t.Fatalf("paths = %v", paths)
	}
}
