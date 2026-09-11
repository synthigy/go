package synthigy

// Contract test for the supervised-stdio token source — builds and spawns
// the SDK's OWN process (testdata/supervisedharness, using only the public
// New/Client.Token/Client.Search surface) under a stub parent speaking
// auth.token, per docs/plans/PLAN-EXEC-IDENTITY.md steps 3-4. The harness
// prints its result to STDERR as one JSON line, keeping stdout exclusively
// the auth.token protocol channel this test drives.

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var harnessBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "synthigy-supervised-harness")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	harnessBin = filepath.Join(dir, "harness")
	build := exec.Command("go", "build", "-o", harnessBin, "./testdata/supervisedharness")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("building supervisedharness: " + err.Error())
	}

	os.Exit(m.Run())
}

// harnessResult mirrors testdata/supervisedharness's result struct.
type harnessResult struct {
	OK     bool     `json:"ok"`
	Token  string   `json:"token"`
	Tokens []string `json:"tokens"`
	Error  string   `json:"error"`
	Code   string   `json:"code"`
}

type harness struct {
	t    *testing.T
	cmd  *exec.Cmd
	out  *bufio.Reader
	errR *bufio.Reader
	in   io.WriteCloser
}

// spawnHarness starts the harness in `mode`, wired for SYNTHIGY_SUPERVISED=1
// with no other auth source, and CHILD env vars carried through for
// modes that need them (e.g. "search" reads its endpoint from argv, not
// env, but keeping the env clean of stray auth sources matters for all
// modes).
func spawnHarness(t *testing.T, mode string, extraArgs ...string) *harness {
	return spawnHarnessEnv(t, nil, mode, extraArgs...)
}

func spawnHarnessEnv(t *testing.T, extraEnv []string, mode string, extraArgs ...string) *harness {
	t.Helper()
	args := append([]string{mode}, extraArgs...)
	cmd := exec.Command(harnessBin, args...)
	cmd.Env = append([]string{"SYNTHIGY_SUPERVISED=1"}, extraEnv...)
	for _, kv := range os.Environ() {
		// Carry through everything except the auth-relevant vars and any
		// pre-existing SYNTHIGY_SUPERVISED so the harness only ever sees
		// our own SYNTHIGY_SUPERVISED=1 set above.
		if hasPrefix(kv, "SYNTHIGY_TOKEN=") || hasPrefix(kv, "SYNTHIGY_CLIENT_ID=") ||
			hasPrefix(kv, "SYNTHIGY_CLIENT_SECRET=") || hasPrefix(kv, "SYNTHIGY_SUPERVISED=") {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting harness: %v", err)
	}

	h := &harness{
		t: t, cmd: cmd,
		out:  bufio.NewReader(stdout),
		errR: bufio.NewReader(stderr),
		in:   stdin,
	}
	t.Cleanup(func() {
		_ = h.in.Close()
		_ = cmd.Wait()
	})
	return h
}

// readFrame reads and JSON-decodes one line off the harness's stdout — an
// auth.token JSON-RPC request.
func (h *harness) readFrame() map[string]any {
	h.t.Helper()
	line, err := h.out.ReadString('\n')
	if line == "" {
		h.t.Fatalf("harness never wrote a frame to stdout: %v", err)
	}
	var frame map[string]any
	if err := json.Unmarshal([]byte(line), &frame); err != nil {
		h.t.Fatalf("frame not valid JSON: %v (%q)", err, line)
	}
	return frame
}

// writeLine writes one raw line (with trailing "\n") to the harness's stdin.
func (h *harness) writeLine(s string) {
	h.t.Helper()
	if _, err := h.in.Write([]byte(s + "\n")); err != nil {
		h.t.Fatalf("writing to harness stdin: %v", err)
	}
}

func (h *harness) writeResponse(id any, result map[string]any) {
	h.writeLine(mustJSON(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}))
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// readResult reads and decodes the harness's one EDN^H^H^H JSON result line
// off stderr.
func (h *harness) readResult() harnessResult {
	h.t.Helper()
	line, err := h.errR.ReadString('\n')
	if line == "" {
		h.t.Fatalf("harness never wrote a result to stderr: %v", err)
	}
	var r harnessResult
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		h.t.Fatalf("result not valid JSON: %v (%q)", err, line)
	}
	return r
}

func TestSupervisedAsksAndReceivesToken(t *testing.T) {
	h := spawnHarness(t, "ask")
	frame := h.readFrame()
	if frame["jsonrpc"] != "2.0" || frame["method"] != "auth.token" || frame["id"] == nil {
		t.Fatalf("unexpected frame: %v", frame)
	}
	h.writeResponse(frame["id"], map[string]any{"token": "supervised-token-abc", "expires_in": 300})

	r := h.readResult()
	if !r.OK || r.Token != "supervised-token-abc" {
		t.Fatalf("unexpected result: %+v", r)
	}
}

func TestSupervisedStrayAndMismatchedLinesAreIgnored(t *testing.T) {
	// Non-frame noise and a response for a DIFFERENT request id on the
	// same stdin must not corrupt dispatch of the real response.
	h := spawnHarness(t, "ask")
	frame := h.readFrame()
	id := frame["id"].(float64)

	h.writeLine("not json at all")
	h.writeResponse(id+999, map[string]any{"token": "wrong-request"})
	h.writeResponse(id, map[string]any{"token": "right-token", "expires_in": 300})

	r := h.readResult()
	if !r.OK || r.Token != "right-token" {
		t.Fatalf("unexpected result: %+v", r)
	}
}

func TestSupervisedTimeoutFallsThroughToTeachingThrow(t *testing.T) {
	// A hung or missing parent must not hang the bot — see step 3.
	h := spawnHarness(t, "ask")
	h.readFrame()
	// Never respond — the harness's ~5s ask timeout fires.
	r := h.readResult()
	if r.OK || r.Code != "NO_TOKEN" {
		t.Fatalf("expected NO_TOKEN failure, got %+v", r)
	}
}

func TestSupervisedPipeBeatsEnvToken(t *testing.T) {
	// SYNTHIGY_TOKEN in env AND SYNTHIGY_SUPERVISED=1: the pipe wins —
	// exec injects the cached env token and supervises, and only the pipe
	// can refresh mid-run (the env value is a frozen snapshot).
	h := spawnHarnessEnv(t, []string{"SYNTHIGY_TOKEN=stale-env-snapshot"}, "ask")
	frame := h.readFrame()
	if frame["method"] != "auth.token" {
		t.Fatalf("supervised harness must ask the pipe even with SYNTHIGY_TOKEN set, got %v", frame)
	}
	h.writeResponse(frame["id"], map[string]any{"token": "fresh-pipe-token", "expires_in": 300})

	r := h.readResult()
	if !r.OK || r.Token != "fresh-pipe-token" {
		t.Fatalf("unexpected result: %+v", r)
	}
}

func TestSupervisedMalformedLineDoesNotKillTheReader(t *testing.T) {
	// A garbage line between two independent asks must not take the
	// background dispatcher down with it — the second ask still has to
	// work, or every future request in this process hangs forever.
	h := spawnHarness(t, "ask-twice")
	frame1 := h.readFrame()
	id1 := frame1["id"].(float64)

	h.writeLine("\xff not even valid json {")
	h.writeLine("{ this looks like a frame but isn't valid json")
	h.writeResponse(id1, map[string]any{"token": "first-token", "expires_in": 300})

	frame2 := h.readFrame()
	id2 := frame2["id"].(float64)
	if id2 == id1 {
		t.Fatalf("second frame reused the first request id")
	}
	h.writeResponse(id2, map[string]any{"token": "second-token", "expires_in": 300})

	r := h.readResult()
	if !r.OK || len(r.Tokens) != 2 || r.Tokens[0] != "first-token" || r.Tokens[1] != "second-token" {
		t.Fatalf("unexpected result: %+v", r)
	}
}

// stub401Once: first POST -> 401, every POST after -> 200 with an empty
// search result. Proves supervisedTokenSource.clear() is wired into the
// SDK's REAL existing 401 clear+retry-once path (fetchAuth), not just
// present.
func stub401Once(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var authHeaders []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if len(authHeaders) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "Unauthorized", "code": "UNAUTHORIZED"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"ok": true, "data": []map[string]any{}}},
		})
	}))
	return srv, &authHeaders
}

func TestSupervisedFull401ClearAndReaskLifecycle(t *testing.T) {
	// The full lifecycle a real bot exercises: ask, use, get 401 (token
	// revoked/invalid despite our clock saying it's fresh), clear, ask
	// again, retry succeeds with the NEW token.
	srv, authHeaders := stub401Once(t)
	defer srv.Close()

	h := spawnHarness(t, "search", srv.URL)

	frame1 := h.readFrame()
	id1 := frame1["id"].(float64)
	h.writeResponse(id1, map[string]any{"token": "stale-token", "expires_in": 300})

	frame2 := h.readFrame()
	id2 := frame2["id"].(float64)
	if id2 == id1 {
		t.Fatalf("the 401 must trigger a SECOND, independent ask")
	}
	h.writeResponse(id2, map[string]any{"token": "fresh-token", "expires_in": 300})

	r := h.readResult()
	if !r.OK {
		t.Fatalf("unexpected result: %+v", r)
	}
	want := []string{"Bearer stale-token", "Bearer fresh-token"}
	got := *authHeaders
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("auth headers = %v, want %v", got, want)
	}
}
