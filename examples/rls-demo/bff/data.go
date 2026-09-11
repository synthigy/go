package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	gen "github.com/synthigy/rls-demo-bff/gen"
	synthigy "github.com/synthigy/go"
)

// panel is the fully-resolved, RLS-scoped view for one principal. Row types are
// generated from synthigy/project.xsql (see gen/).
type panel struct {
	User      User
	Projects  []gen.ProjectList
	Tasks     []gen.ProjectTaskList
	TaskCount int // count(*) over project_task via sql-template (acting_as) — RLS-scoped analytics
	ProjCount int
	Err       string
}

// fetchPanel runs every per-principal query for one user via the generated
// typed API. Every read carries acting_as, so the server applies that
// principal's RLS — the identical query returns different rows per user.
func (s *server) fetchPanel(ctx context.Context, u User) panel {
	p := panel{User: u}
	aa := synthigy.ActingAs(u.XID)

	projects, err := gen.API.Project.List(ctx, gen.ProjectListParams{}, aa) // _limit defaults to 7
	if err != nil {
		p.Err = err.Error()
		return p
	}
	p.Projects = projects

	// assignee/project are LEFT-joined in the .xsql (`->` is left by default), so
	// a task the principal can see isn't dropped when its relation is hidden.
	tasks, err := gen.API.ProjectTask.List(ctx, aa)
	if err != nil {
		p.Err = err.Error()
		return p
	}
	p.Tasks = tasks

	// RLS-aware analytics: the SAME COUNT(*) returns a different number per
	// principal because the sql-template runs under the acting_as RLS scope.
	// Surface query errors into the panel — swallowing them hides real bugs
	// (e.g. a stale codegen contract misrouting the sql-template op).
	if rows, err := gen.API.Project.TaskCount(ctx, aa); err != nil {
		p.Err = "taskCount: " + err.Error()
	} else if len(rows) > 0 {
		p.TaskCount = int(rows[0].N)
	}
	if rows, err := gen.API.Project.ProjectCount(ctx, aa); err != nil {
		p.Err = "projectCount: " + err.Error()
	} else if len(rows) > 0 {
		p.ProjCount = int(rows[0].N)
	}
	return p
}

// fetchAll resolves every panel concurrently.
func (s *server) fetchAll(ctx context.Context) []panel {
	out := make([]panel, len(roster))
	var wg sync.WaitGroup
	for i, u := range roster {
		wg.Add(1)
		go func(i int, u User) {
			defer wg.Done()
			out[i] = s.fetchPanel(ctx, u)
		}(i, u)
	}
	wg.Wait()
	return out
}

// ---- write beats ---------------------------------------------------------

var statusCycle = []string{"ToDo", "In_Progress", "Done"}

func nextStatus(cur string) string {
	for i, s := range statusCycle {
		if s == cur {
			return statusCycle[(i+1)%len(statusCycle)]
		}
	}
	return statusCycle[0]
}

// handleCycleTask advances a task's status (ToDo→In_Progress→Done→…) as the BFF
// service user, then nudges every stream. The change shows up live in exactly
// the panels whose principal can see that task.
func (s *server) handleCycleTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	title := r.URL.Query().Get("title")
	if title == "" {
		title = "Alpha Task 2 - Implement Feature X"
	}
	// Find the task as the service user (no acting_as → full visibility).
	tasks, err := synthigy.Search(ctx, "project_task",
		synthigy.Args{"title": synthigy.Eq(title)},
		synthigy.Selection{"title": nil, "status": nil})
	if err != nil || len(tasks) == 0 {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	xid, _ := tasks[0]["xid"].(string)
	cur, _ := tasks[0]["status"].(string)
	if _, err := synthigy.Sync(ctx, "project_task", map[string]any{
		"xid":    xid,
		"status": nextStatus(cur),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.hub.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

// genCounter rotates the assignee across clicks so successive "Generate task"
// presses land on different principals — you can watch each one's RLS-scoped
// count tick up (or not) in the panels.
var (
	genCounter int
	genMu      sync.Mutex
)

// handleGenerateTask creates a project_task, assigns it to a principal, and
// joins it to one of that principal's projects — as the BFF service user (no
// acting_as → full write visibility, so the create always succeeds). Then it
// nudges every stream: the new row and the bumped count(*) appear live in
// exactly the panels whose principal can see it under RLS. Use it to watch what
// happens to each panel's counts when a task lands in a given project/assignee.
//
//	POST /generate-task                      # rotates assignee, picks their project
//	POST /generate-task?assignee=bob         # assign to Bob, his first project
//	POST /generate-task?assignee=bob&project=<xid>   # explicit project (try one Bob can't see)
func (s *server) handleGenerateTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Assignee: explicit ?assignee=<key>, else rotate through the roster.
	who := User{}
	if key := r.URL.Query().Get("assignee"); key != "" {
		for _, u := range roster {
			if u.Key == key {
				who = u
			}
		}
	}
	if who.XID == "" {
		genMu.Lock()
		who = roster[genCounter%len(roster)]
		genCounter++
		genMu.Unlock()
	}

	// Project: explicit ?project=<xid>, else the assignee's first visible
	// project (so the task lands in their scope); fall back to any project.
	projXid := r.URL.Query().Get("project")
	if projXid == "" {
		if ps, _ := synthigy.Search(ctx, "project", synthigy.Args{"_limit": 1},
			synthigy.Selection{"name": nil}, synthigy.ActingAs(who.XID)); len(ps) > 0 {
			projXid, _ = ps[0]["xid"].(string)
		}
	}
	if projXid == "" {
		if ps, _ := synthigy.Search(ctx, "project", synthigy.Args{"_limit": 1},
			synthigy.Selection{"name": nil}); len(ps) > 0 {
			projXid, _ = ps[0]["xid"].(string)
		}
	}

	task := map[string]any{
		"title":    fmt.Sprintf("Generated task for %s @ %s", who.Name, time.Now().Format("15:04:05")),
		"status":   "ToDo",
		"priority": "Medium",
		"assignee": map[string]any{"xid": who.XID},
	}
	if projXid != "" {
		task["project"] = map[string]any{"xid": projXid}
	}
	if _, err := synthigy.Sync(ctx, "project_task", task); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.hub.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

// handleToggleRLS flips the deployed Project Management version between 0.1.2
// (RLS on) and 0.1.1 (RLS off) via the dev nREPL. Config-only diff → data-safe,
// no DDL. Panels diverge/converge live.
func (s *server) handleToggleRLS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	wantOn := !s.rlsOn // flip
	s.mu.Unlock()

	// Deploy through the real /data `deploy` op (no nREPL). Both datasets are
	// (re)deployed in-place: ON deploys the guarded models; OFF deploys models
	// with rls.enabled=false on every entity (both PM and RP — merge-entity-rls
	// is last-deploy-wins on :enabled, guards still union). The transit bodies
	// are pre-baked (Go has no transit encoder; see datasets/deploy/*.transit
	// and the gen snippet in README). Verify + retry guards against the
	// deploy/rebuild fold-order race when two datasets co-define an entity.
	if err := s.deployRLS(r.Context(), wantOn); err != nil {
		http.Error(w, "deploy failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.rlsOn = wantOn
	s.mu.Unlock()
	s.hub.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

// deployRLS (re)deploys both demo datasets through the /data `deploy` op to
// turn RLS on/off, then verifies and retries. The request bodies are
// pre-baked transit (datasets/deploy/rls-{on,off}.transit) because the model
// must travel as a transit string and Go has no transit encoder.
func (s *server) deployRLS(ctx context.Context, on bool) error {
	file := "rls-off.transit"
	if on {
		file = "rls-on.transit"
	}
	body := readFirst("../datasets/deploy/"+file, "sdk/go/examples/rls-demo/datasets/deploy/"+file)
	if body == "" {
		return fmt.Errorf("deploy body not found: datasets/deploy/%s", file)
	}
	var dianaXID string
	for _, u := range roster {
		if u.Key == "diana" {
			dianaXID = u.XID
		}
	}

	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if err := s.postTransit(ctx, []byte(body)); err != nil {
			lastErr = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		// Verify against Diana: RLS on → she sees 0 tasks; off → she sees all.
		rows, err := synthigy.Search(ctx, "project_task", synthigy.Args{},
			synthigy.Selection{"title": nil}, synthigy.ActingAs(dianaXID))
		if err == nil && (len(rows) > 0) == !on {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	if lastErr != nil {
		return lastErr
	}
	return nil // best-effort; the stream re-render shows the real state
}

// postTransit POSTs a pre-baked transit body to /data with the BFF's bearer.
func (s *server) postTransit(ctx context.Context, body []byte) error {
	tok, err := synthigy.Token(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint+"/data", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/transit+json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("deploy HTTP %d: %s", resp.StatusCode, string(b))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// handleReseed resets the demo data to its pristine seeded state by re-syncing
// the committed JSON fixtures over /data. Idempotent (sync upserts by xid).
func (s *server) handleReseed(w http.ResponseWriter, r *http.Request) {
	if err := s.seedFromFixtures(r.Context()); err != nil {
		http.Error(w, "reseed failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.hub.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

func readFirst(paths ...string) string {
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			return string(b)
		}
	}
	return ""
}

// sortedTasks returns tasks ordered by title for stable rendering.
func sortedTasks(tasks []gen.ProjectTaskList) []gen.ProjectTaskList {
	out := append([]gen.ProjectTaskList(nil), tasks...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].Title < out[j].Title
	})
	return out
}
