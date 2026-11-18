package synthigy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type setRecorder struct {
	mu     sync.Mutex
	bodies []map[string]any
	reply  func(w http.ResponseWriter, body map[string]any)
}

func (s *setRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.bodies = append(s.bodies, body)
	reply := s.reply
	s.mu.Unlock()
	if reply != nil {
		reply(w, body)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *setRecorder) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func muxWithWatch(t *testing.T, h http.Handler) (*watchMultiplexer, *WatchHandle) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	m := newWatchMultiplexer(c, false, false)
	w := &WatchHandle{mux: m, intr: WatchInterest{Records: []string{"r1"}}, sinks: map[*coalesceBuffer]struct{}{}, subscribed: make(chan struct{})}
	w.first = newCoalesceBuffer("coalesce", 100)
	w.sinks[w.first] = struct{}{}
	m.watches[w] = struct{}{}
	t.Cleanup(w.first.close)
	return m, w
}

func isSubscribed(w *WatchHandle) bool {
	select {
	case <-w.subscribed:
		return true
	default:
		return false
	}
}

func noEvent(t *testing.T, w *WatchHandle) {
	t.Helper()
	select {
	case ev := <-w.first.out:
		t.Fatalf("unexpected event %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestListenOpenCarriesAFreshStreamID(t *testing.T) {
	var asked atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Store(r.URL.Query().Get("stream"))
		sseHandler(nil)(w, r)
	}))
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := c.Listen(ctx)
	defer s.Close()
	ev := <-s.Events()
	if ev.Type != "sse/open" || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(ev.Stream) {
		t.Fatalf("open = %+v", ev)
	}
	if got, _ := asked.Load().(string); got != ev.Stream {
		t.Fatalf("connected with stream %q, open says %q", got, ev.Stream)
	}
}

func TestFlushNamesTheStreamAndWaitsForOne(t *testing.T) {
	rec := &setRecorder{}
	m, w := muxWithWatch(t, rec)
	m.flush(context.Background())
	if rec.count() != 0 || isSubscribed(w) {
		t.Fatalf("posted without a stream: posts=%d subscribed=%v", rec.count(), isSubscribed(w))
	}
	m.streamID = "s1-stream"
	m.flush(context.Background())
	if rec.count() != 1 || rec.bodies[0]["stream"] != "s1-stream" || !isSubscribed(w) {
		t.Fatalf("posts=%v subscribed=%v", rec.bodies, isSubscribed(w))
	}
}

func TestStreamNotFoundIsNotARejection(t *testing.T) {
	for _, newer := range []bool{false, true} {
		rec := &setRecorder{}
		m, w := muxWithWatch(t, rec)
		rec.reply = func(rw http.ResponseWriter, body map[string]any) {
			if newer {
				m.mu.Lock()
				m.streamID = "s2-stream"
				m.mu.Unlock()
			}
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(400)
			_ = json.NewEncoder(rw).Encode(map[string]any{"error": map[string]any{"message": "gone", "code": "STREAM_NOT_FOUND"}})
		}
		m.streamID = "s1-stream"
		m.flush(context.Background())
		want := ""
		if newer {
			want = "s2-stream"
		}
		if m.currentStream() != want || m.lastUnion != "" || isSubscribed(w) {
			t.Fatalf("newer=%v: stream=%q lastUnion=%q subscribed=%v", newer, m.currentStream(), m.lastUnion, isSubscribed(w))
		}
		noEvent(t, w)
	}
}

func TestTheUnionIsRecordedOnlyAfterAPost(t *testing.T) {
	rec := &setRecorder{}
	m, w := muxWithWatch(t, rec)
	rec.reply = func(rw http.ResponseWriter, body map[string]any) { rw.WriteHeader(500) }
	m.streamID = "s1-stream"
	m.flush(context.Background())
	if ev := <-w.first.out; ev.Type != "subscription/rejected" {
		t.Fatalf("event = %+v", ev)
	}
	rec.mu.Lock()
	rec.reply = nil
	rec.mu.Unlock()
	m.flush(context.Background())
	m.flush(context.Background())
	if rec.count() != 2 {
		t.Fatalf("a refused union must be posted again, once: %d posts", rec.count())
	}
}

func TestAPostThatOutlivesItsSessionSettlesNothing(t *testing.T) {
	rec := &setRecorder{}
	m, w := muxWithWatch(t, rec)
	rec.reply = func(rw http.ResponseWriter, body map[string]any) {
		m.streamClosed(m.ctx, "s1-stream")
		_ = json.NewEncoder(rw).Encode(map[string]any{"ok": true})
	}
	m.streamID = "s1-stream"
	m.flush(m.ctx)
	if isSubscribed(w) || m.lastUnion != "" {
		t.Fatalf("after the session ended: subscribed=%v lastUnion=%q", isSubscribed(w), m.lastUnion)
	}
	rec.mu.Lock()
	rec.reply = nil
	rec.mu.Unlock()
	m.streamID = "s2-stream"
	m.flush(m.ctx)
	if rec.count() != 2 || !isSubscribed(w) {
		t.Fatalf("next open: posts=%d subscribed=%v", rec.count(), isSubscribed(w))
	}
}

func TestClosingTheStreamClearsItsID(t *testing.T) {
	m, _ := muxWithWatch(t, &setRecorder{})
	m.streamID, m.sseEverOpened, m.running = "s1-stream", true, true
	m.closeSSE()
	if m.currentStream() != "" || m.sseEverOpened {
		t.Fatalf("stream=%q opened=%v", m.currentStream(), m.sseEverOpened)
	}
}

func TestAStaleSessionsOpenLeavesTheStreamAlone(t *testing.T) {
	var hit atomic.Bool
	mux := http.NewServeMux()
	mux.Handle("/data/subscription/set", &setRecorder{})
	mux.HandleFunc("/data/events", func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
		sseHandler(nil)(w, r)
	})
	m, _ := muxWithWatch(t, mux)
	stale, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.runSSE(stale); close(done) }()
	for !hit.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if s := m.currentStream(); s != "" {
		t.Fatalf("a stale session set the stream to %q", s)
	}
}

func TestAFlushForAClosedGenerationDoesNotPost(t *testing.T) {
	rec := &setRecorder{}
	m, _ := muxWithWatch(t, rec)
	m.streamID = "s1-stream"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.flush(ctx)
	if rec.count() != 0 {
		t.Fatal("posted on a cancelled generation")
	}
	select {
	case <-m.flushReq:
	default:
		t.Fatal("the flush was dropped instead of handed to the current generation")
	}
}

func addWatch(m *watchMultiplexer, records ...string) *WatchHandle {
	w := &WatchHandle{mux: m, intr: WatchInterest{Records: records}, sinks: map[*coalesceBuffer]struct{}{}, subscribed: make(chan struct{})}
	w.first = newCoalesceBuffer("coalesce", 100)
	w.sinks[w.first] = struct{}{}
	m.mu.Lock()
	m.watches[w] = struct{}{}
	m.mu.Unlock()
	return w
}

func TestAFlushWithNoStreamMarksNothing(t *testing.T) {
	rec := &setRecorder{}
	m, _ := muxWithWatch(t, rec)
	m.streamID = "s1-stream"
	m.flush(context.Background())
	m.mu.Lock()
	m.streamID = ""
	m.mu.Unlock()
	w2 := addWatch(m, "r1")
	defer w2.first.close()
	m.flush(context.Background())
	if rec.count() != 1 || isSubscribed(w2) {
		t.Fatalf("posts=%d subscribed=%v", rec.count(), isSubscribed(w2))
	}
}

func TestStreamNotFoundForgetsTheLastUnion(t *testing.T) {
	rec := &setRecorder{}
	m, _ := muxWithWatch(t, rec)
	m.streamID = "s1-stream"
	m.flush(context.Background())
	rec.mu.Lock()
	rec.reply = func(rw http.ResponseWriter, body map[string]any) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(400)
		_ = json.NewEncoder(rw).Encode(map[string]any{"error": map[string]any{"message": "gone", "code": "STREAM_NOT_FOUND"}})
	}
	rec.mu.Unlock()
	w2 := addWatch(m, "r2")
	defer w2.first.close()
	m.flush(context.Background())
	if m.currentStream() != "" || m.lastUnion != "" {
		t.Fatalf("stream=%q lastUnion=%q", m.currentStream(), m.lastUnion)
	}
}

func TestAPostCutByTheGenerationIsNotARejection(t *testing.T) {
	rec := &setRecorder{}
	m, w := muxWithWatch(t, rec)
	ctx, cancel := context.WithCancel(context.Background())
	rec.reply = func(rw http.ResponseWriter, body map[string]any) {
		cancel()
		time.Sleep(100 * time.Millisecond)
		rw.WriteHeader(500)
	}
	m.streamID = "s1-stream"
	m.flush(ctx)
	if isSubscribed(w) || m.lastUnion != "" {
		t.Fatalf("subscribed=%v lastUnion=%q", isSubscribed(w), m.lastUnion)
	}
	noEvent(t, w)
}

func TestSubscriptionsAsksForTheWatchStream(t *testing.T) {
	var asked atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Store(r.URL.RawQuery)
		_ = json.NewEncoder(w).Encode(map[string]any{"subscriptions": []any{}})
	}))
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()
	if _, err := c.Subscriptions(context.Background()); err != nil || asked.Load() != "" {
		t.Fatalf("without a watch: err=%v query=%v", err, asked.Load())
	}
	c.mux = newWatchMultiplexer(c, false, false)
	c.mux.streamID = "s1-stream"
	if _, err := c.Subscriptions(context.Background()); err != nil || asked.Load() != "stream=s1-stream" {
		t.Fatalf("with a stream: err=%v query=%v", err, asked.Load())
	}
}

func liveQueryServer(calls *atomic.Int32) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.Handle("/data/subscription/set", &setRecorder{})
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeResults(w, []map[string]any{{"ok": true, "data": []map[string]any{{"xid": "m-1", "n": 1}}}})
	})
	mux.HandleFunc("/data/events", sseHandler(nil))
	return httptest.NewServer(mux)
}

func waitCalls(t *testing.T, calls *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("query ran %d times, want %d", calls.Load(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLiveQueriesRerunOnResumed(t *testing.T) {
	t.Run("WatchQuery", func(t *testing.T) {
		var calls atomic.Int32
		srv := liveQueryServer(&calls)
		defer srv.Close()
		c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
		defer c.Close()
		q, err := c.WatchQuery(context.Background(), "movie", nil, Selection{"n": nil})
		if err != nil {
			t.Fatal(err)
		}
		defer q.Close()
		time.Sleep(3 * refreshCoalesce)
		before := calls.Load()
		q.watch.pushSentinel(WatchEvent{Type: "connection/resumed"})
		waitCalls(t, &calls, before+1)
	})
	t.Run("WatchSqlTemplate", func(t *testing.T) {
		var calls atomic.Int32
		srv := liveQueryServer(&calls)
		defer srv.Close()
		c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
		defer c.Close()
		s, err := c.WatchSqlTemplate(context.Background(), "SELECT 1 AS n FROM {user}", nil, Entities("user"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		time.Sleep(3 * refreshCoalesce)
		before := calls.Load()
		s.watch.pushSentinel(WatchEvent{Type: "connection/resumed"})
		waitCalls(t, &calls, before+1)
	})
}

func TestListenClosesEachSessionWithItsID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := c.Listen(ctx)
	defer s.Close()
	open, closed := <-s.Events(), <-s.Events()
	if open.Type != "sse/open" || closed.Type != "sse/close" || closed.Stream != open.Stream {
		t.Fatalf("open=%+v close=%+v", open, closed)
	}
}

func TestAStaleCloseLeavesTheStreamAlone(t *testing.T) {
	m, _ := muxWithWatch(t, &setRecorder{})
	m.streamID, m.lastUnion = "s2-stream", "[]"
	m.streamClosed(m.ctx, "s1-stream")
	stale, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.streamClosed(stale, "s2-stream")
	if m.currentStream() != "s2-stream" || m.lastUnion != "[]" {
		t.Fatalf("stream=%q lastUnion=%q", m.currentStream(), m.lastUnion)
	}
	m.streamClosed(m.ctx, "s2-stream")
	if m.currentStream() != "" || m.lastUnion != "" {
		t.Fatalf("stream=%q lastUnion=%q", m.currentStream(), m.lastUnion)
	}
}

func TestAWatchDuringReconnectWaitsForTheNextOpen(t *testing.T) {
	rec := &setRecorder{}
	end1 := make(chan struct{})
	var sessions atomic.Int32
	mux := http.NewServeMux()
	mux.Handle("/data/subscription/set", rec)
	mux.HandleFunc("/data/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		if sessions.Add(1) == 1 {
			select {
			case <-end1:
			case <-r.Context().Done():
			}
			return
		}
		<-r.Context().Done()
	})
	m, w := muxWithWatch(t, mux)
	t.Cleanup(m.close)
	m.ensureSSE()
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor("first post", func() bool { return rec.count() == 1 && isSubscribed(w) })
	close(end1)
	waitFor("stream id not cleared on close", func() bool { return m.currentStream() == "" })
	w2 := addWatch(m, "r1")
	defer w2.first.close()
	m.mu.Lock()
	ctx := m.ctx
	m.mu.Unlock()
	m.flush(ctx)
	if isSubscribed(w2) || rec.count() != 1 {
		t.Fatalf("during backoff: subscribed=%v posts=%d", isSubscribed(w2), rec.count())
	}
	waitFor("not subscribed after the next open", func() bool { return rec.count() == 2 && isSubscribed(w2) })
}
