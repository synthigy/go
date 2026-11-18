package synthigy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// blockingSSE streams the given frames then keeps the connection open. Each
// time the handler is hit it sends one extra "tick" frame so reconnects don't
// matter for these tests.
func blockingSSE(frames []string) http.HandlerFunc {
	return sseHandler(frames)
}

func TestWatchDispatchesRecordEvent(t *testing.T) {
	frames := []string{
		"event: data\ndata: {\"type\":\"record/update\",\"record-xid\":\"r1\",\"before\":{\"n\":1},\"after\":{\"n\":2}}\n\n",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data/events", blockingSSE(frames))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	w, err := c.Watch(ctx, WatchInterest{Records: []string{"r1"}})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer w.Close()

	select {
	case ev := <-w.Events():
		if ev.Type != "record/update" || ev.Record != "r1" {
			t.Fatalf("unexpected event: %+v", ev)
		}
		if len(ev.Changed) != 1 || ev.Changed[0] != "n" {
			t.Errorf("expected changed [n], got %v", ev.Changed)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
}

// TestWatchEventsFansOutToMultipleConsumers proves Events() is fan-out, not
// a shared queue: two independent channels off the SAME handle both see
// every event — neither steals it from the other.
func TestWatchEventsFansOutToMultipleConsumers(t *testing.T) {
	frames := []string{
		"event: data\ndata: {\"type\":\"record/update\",\"record-xid\":\"r1\",\"before\":{\"n\":1},\"after\":{\"n\":2}}\n\n",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data/events", blockingSSE(frames))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	w, err := c.Watch(ctx, WatchInterest{Records: []string{"r1"}})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer w.Close()

	// THREE independent consumers on the SAME handle.
	ch1, ch2, ch3 := w.Events(), w.Events(), w.Events()

	for i, ch := range []<-chan WatchEvent{ch1, ch2, ch3} {
		select {
		case ev := <-ch:
			if ev.Type != "record/update" || ev.Record != "r1" {
				t.Fatalf("consumer %d: unexpected event: %+v", i, ev)
			}
		case <-ctx.Done():
			t.Fatalf("consumer %d: timed out waiting for event — fan-out broken, "+
				"one consumer stole it from the others", i)
		}
	}
}

func TestWatchSqlTemplateRefreshesOnTouch(t *testing.T) {
	var sqlCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		// Each sql-template call returns an incrementing count so the second
		// call differs from the first and triggers result/changed.
		n := atomic.AddInt32(&sqlCalls, 1)
		writeResults(w, []map[string]any{
			{"ok": true, "data": []map[string]any{{"n": int(n)}}},
		})
	})
	mux.HandleFunc("/data/events", blockingSSE([]string{
		"event: data\ndata: {\"type\":\"entity/touched\",\"entity\":\"user\",\"ts\":\"2026-06-16T00:00:00Z\"}\n\n",
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	tile, err := c.WatchSqlTemplate(ctx, "SELECT count(*) AS n FROM {user}", nil,
		Entities("user"))
	if err != nil {
		t.Fatalf("WatchSqlTemplate: %v", err)
	}
	defer tile.Close()

	if tile.First()["n"].(float64) != 2 {
		t.Fatalf("first after open = %v, want 2 (snapshot, then one refetch once subscribed)", tile.First())
	}

	for {
		select {
		case ev := <-tile.Events():
			if ev.Type != "result/changed" {
				t.Fatalf("unexpected event: %+v", ev)
			}
			if tile.First()["n"].(float64) >= 3 {
				return
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for the touch to refresh the result")
		}
	}
}

func TestWatchKeepsEventsBeforeTheFirstEventsCall(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	w, err := c.Watch(context.Background(), WatchInterest{Records: []string{"A"}})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.push(WatchEvent{Type: "record/update", Record: "A"})
	select {
	case ev := <-w.Events():
		if ev.Type != "record/update" || ev.Record != "A" {
			t.Fatalf("got %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an event pushed before Events() was lost")
	}
}

var rejection = map[string]any{"message": "no", "code": "FORBIDDEN", "hint": "h", "rule": "stamp", "doc": "errors/stamp"}

func nextRejected(t *testing.T, events <-chan WatchEvent) WatchEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type == "subscription/rejected" {
				return ev
			}
		case <-deadline:
			t.Fatal("no subscription/rejected event")
		}
	}
}

func assertRejectionError(t *testing.T, ev WatchEvent) {
	t.Helper()
	var se *Error
	if !errors.As(ev.Err, &se) {
		t.Fatalf("Err = %#v, want *Error", ev.Err)
	}
	if ev.Reason != ev.Err.Error() {
		t.Errorf("Reason = %q, want the error's message %q", ev.Reason, ev.Err.Error())
	}
	got := []string{se.Code, se.Hint, se.Rule, se.Doc}
	want := []string{"FORBIDDEN", "h", "stamp", "errors/stamp"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("code, hint, rule, doc = %q, want %q", got, want)
		}
	}
}

func TestWatchRejectedSubscribeCarriesTheError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": rejection})
	})
	mux.HandleFunc("/data/events", blockingSSE(nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, err := c.Watch(ctx, WatchInterest{Records: []string{"r1"}})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer w.Close()
	assertRejectionError(t, nextRejected(t, w.Events()))
}

func TestWatchQueryRejectedRefreshCarriesTheError(t *testing.T) {
	var dataCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&dataCalls, 1) == 1 {
			writeResults(w, []map[string]any{{"ok": true, "data": []map[string]any{{"xid": "m-1", "title": "A"}}}})
			return
		}
		writeResults(w, []map[string]any{{"ok": false, "error": rejection}})
	})
	mux.HandleFunc("/data/events", blockingSSE(nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	qw, err := c.WatchQuery(ctx, "movie", nil, Selection{"title": nil})
	if err != nil {
		t.Fatalf("WatchQuery: %v", err)
	}
	defer qw.Close()
	assertRejectionError(t, nextRejected(t, qw.Events()))
}

// The engine drops an identity's subscriptions when its last stream closes,
// so the multiplexer must post the same union again after a reconnect.
func TestAWatchOnEveryOpKeepsTheFilterOff(t *testing.T) {
	bodies := make(chan string, 16)
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies <- string(raw)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "event: open\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waitFor := func(want string) string {
		for {
			select {
			case b := <-bodies:
				if strings.Contains(b, want) {
					return b
				}
			case <-ctx.Done():
				t.Fatalf("no subscription POST containing %s", want)
			}
		}
	}
	w1, err := c.Watch(ctx, WatchInterest{Records: []string{"r1"}, Ops: []string{"insert"}})
	if err != nil {
		t.Fatal(err)
	}
	defer w1.Close()
	if b := waitFor(`"r1"`); !strings.Contains(b, `"operations":["insert"]`) {
		t.Fatalf("a lone filtered watch keeps its filter: %s", b)
	}
	w2, err := c.Watch(ctx, WatchInterest{Records: []string{"r2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	if b := waitFor(`"r2"`); strings.Contains(b, "operations") {
		t.Fatalf("r2 wants every op, so no filter may go out: %s", b)
	}
}

func TestWatchResubscribesAfterReconnect(t *testing.T) {
	var streams, posts atomic.Int32
	bodies := make(chan string, 8)
	opened := make(chan string, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		stream, _ := body["stream"].(string)
		sig, _ := json.Marshal(body["subscriptions"])
		posts.Add(1)
		bodies <- stream + " " + string(sig)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data/events", func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		opened <- r.URL.Query().Get("stream")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl.Flush()
		if streams.Add(1) == 1 {
			for posts.Load() == 0 {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
			return
		}
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, err := c.Watch(ctx, WatchInterest{Records: []string{"r1"}})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer w.Close()

	var got []string
	for len(got) < 2 {
		select {
		case b := <-bodies:
			got = append(got, b)
		case <-ctx.Done():
			t.Fatalf("no resubscribe after reconnect: streams=%d posts=%v", streams.Load(), got)
		}
	}
	first, second := <-opened, <-opened
	if len(first) != 32 || first == second {
		t.Fatalf("each connection needs a fresh 32-char stream id: %q then %q", first, second)
	}
	set0, set1 := strings.TrimPrefix(got[0], first+" "), strings.TrimPrefix(got[1], second+" ")
	if set0 == got[0] || set1 == got[1] {
		t.Fatalf("each POST must name its own stream (%s, %s): %v", first, second, got)
	}
	if set0 != set1 {
		t.Fatalf("resubscribe posted a different set: %s vs %s", set0, set1)
	}
}

func refusingEvents(hits *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "no events", "code": "ENTITY_FORBIDDEN"}})
	}
}

func TestListenStopsOnAny403(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(refusingEvents(&hits))
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := c.Listen(ctx)
	for range s.Events() {
	}
	var se *Error
	if err := s.Err(); !errors.As(err, &se) || se.Code != "ENTITY_FORBIDDEN" || se.Status != 403 {
		t.Fatalf("Err = %v", err)
	}
	if ctx.Err() != nil || hits.Load() != 1 {
		t.Fatalf("kept reconnecting: hits=%d", hits.Load())
	}
}

func TestAWatchAfterARefusalOpensANewStream(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data/events", refusingEvents(&hits))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i := 1; i <= 2; i++ {
		w, err := c.Watch(ctx, WatchInterest{Records: []string{"r1"}})
		if err != nil {
			t.Fatalf("Watch %d: %v", i, err)
		}
		select {
		case ev := <-w.Events():
			if ev.Type != "subscription/rejected" {
				t.Fatalf("watch %d event = %+v", i, ev)
			}
		case <-ctx.Done():
			t.Fatalf("watch %d never heard the refusal: the stream was not reopened", i)
		}
		if got := hits.Load(); got != int32(i) {
			t.Fatalf("watch %d: %d stream attempts", i, got)
		}
	}
}

func TestWatchGetsTheStreamRefusal(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data/events", refusingEvents(&hits))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	w, err := c.Watch(ctx, WatchInterest{Records: []string{"r1"}})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer w.Close()
	select {
	case ev := <-w.Events():
		var se *Error
		if ev.Type != "subscription/rejected" || !errors.As(ev.Err, &se) || se.Code != "ENTITY_FORBIDDEN" {
			t.Fatalf("event = %+v", ev)
		}
	case <-ctx.Done():
		t.Fatal("no subscription/rejected after the stream was refused")
	}
}

func TestAStaleStreamRefusalLeavesTheNewStreamRunning(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(refusingEvents(&hits))
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()
	m := newWatchMultiplexer(c, false, false)
	w := &WatchHandle{mux: m, sinks: map[*coalesceBuffer]struct{}{}, subscribed: make(chan struct{})}
	w.first = newCoalesceBuffer("coalesce", 100)
	w.sinks[w.first] = struct{}{}
	m.mu.Lock()
	m.running = true
	m.watches[w] = struct{}{}
	current := m.ctx
	m.mu.Unlock()
	m.runSSE(context.Background())
	m.mu.Lock()
	running, same := m.running, m.ctx == current
	m.mu.Unlock()
	if hits.Load() != 1 || !running || !same || current.Err() != nil {
		t.Fatalf("the stale stream's refusal reset the current one: running=%v same=%v err=%v", running, same, current.Err())
	}
	select {
	case <-w.subscribed:
		t.Fatal("the stale stream marked the current generation's watch subscribed")
	default:
	}
	select {
	case ev := <-w.first.out:
		t.Fatalf("the stale stream's refusal reached the current generation's watch: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
	w.first.close()
}
