package synthigy

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

const refreshCoalesce = 50 * time.Millisecond

// SqlTemplateWatch is a live raw-SQL result. It snapshots a SQL template,
// then watches the declared entities'/relations' touch events and re-runs the
// template on change (coalesced), emitting result/changed events.
type SqlTemplateWatch struct {
	client   *Client
	template string
	params   []any
	opts     []Opt

	mu    sync.Mutex
	value []Record

	watch   *WatchHandle
	buf     *coalesceBuffer
	ctx     context.Context
	cancel  context.CancelFunc
	trigger chan WatchEvent
}

// WatchSqlTemplate opens a live SQL-template result. Entities(...) (and/or
// the relations via the underlying interest) are REQUIRED so the watch knows
// what to subscribe to. Pass WatchRecords(...) for per-row update refresh.
// The initial snapshot is taken synchronously; ongoing refreshes run until
// Close (or ctx cancellation).
func (c *Client) WatchSqlTemplate(ctx context.Context, template string, params []any, opts ...Opt) (*SqlTemplateWatch, error) {
	o := applyOpts(opts)
	if len(o.entities) == 0 {
		return nil, newError(
			"WatchSqlTemplate requires Entities(...) — list the entity names the SQL reads from",
			"INVALID_INTEREST")
	}

	value, err := c.SQLTemplate(ctx, template, params, opts...)
	if err != nil {
		return nil, err
	}

	wctx, cancel := context.WithCancel(context.Background())
	interest := WatchInterest{Entities: o.entities, Records: o.records}
	w, err := c.Watch(wctx, interest)
	if err != nil {
		cancel()
		return nil, err
	}

	stw := &SqlTemplateWatch{
		client:   c,
		template: template,
		params:   params,
		opts:     opts,
		value:    value,
		watch:    w,
		buf:      newCoalesceBuffer("lossless", 1<<20),
		ctx:      wctx,
		cancel:   cancel,
		trigger:  make(chan WatchEvent, 1),
	}
	go stw.drain()
	go stw.refreshLoop()
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				stw.Close()
			case <-wctx.Done():
			}
		}()
	}
	return stw, nil
}

// Value returns the current SQL result rows.
func (s *SqlTemplateWatch) Value() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}

// First returns the first row of the current result, or nil.
func (s *SqlTemplateWatch) First() Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.value) == 0 {
		return nil
	}
	return s.value[0]
}

// Events returns the change-event channel (result/changed + sentinels).
func (s *SqlTemplateWatch) Events() <-chan WatchEvent { return s.buf.out }

// Close stops the watch.
func (s *SqlTemplateWatch) Close() {
	s.cancel()
	s.watch.Close()
	s.buf.close()
}

func (s *SqlTemplateWatch) drain() {
	for ev := range s.watch.Events() {
		switch {
		case ev.Type == "entity/touched" || ev.Type == "relation/touched" ||
			hasPrefix(ev.Type, "record/") || hasPrefix(ev.Type, "relation/"):
			select {
			case s.trigger <- ev:
			default:
				// a refresh is already pending; keep the existing trigger
			}
		default:
			s.buf.push(ev) // sentinels pass through
		}
	}
}

func (s *SqlTemplateWatch) refreshLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case ev := <-s.trigger:
			ev = s.debounce(ev)
			if s.ctx.Err() != nil {
				return
			}
			s.refresh(ev)
		}
	}
}

func (s *SqlTemplateWatch) debounce(ev WatchEvent) WatchEvent {
	timer := time.NewTimer(refreshCoalesce)
	defer timer.Stop()
	for {
		select {
		case ev2 := <-s.trigger:
			ev = ev2
		case <-timer.C:
			return ev
		case <-s.ctx.Done():
			return ev
		}
	}
}

func (s *SqlTemplateWatch) refresh(trigger WatchEvent) {
	after, err := s.client.SQLTemplate(s.ctx, s.template, s.params, s.opts...)
	if err != nil {
		s.buf.push(WatchEvent{Type: "subscription/rejected", Reason: errMsg(err)})
		return
	}
	s.mu.Lock()
	before := s.value
	s.value = after
	s.mu.Unlock()
	if !jsonEqual(before, after) {
		s.buf.push(WatchEvent{
			Type: "result/changed", Ts: trigger.Ts, Txid: trigger.Txid,
			Actor: trigger.Actor, Request: trigger.Request,
			Raw: map[string]any{"before": before, "after": after},
		})
	}
}

// QueryWatch is a live result-set for a search or XSQL query. It snapshots
// the query, watches the result xids (top-level + nested) and the entity's
// relation xids, and on change re-runs the query and diffs, emitting
// query/added, query/changed, and query/removed events.
type QueryWatch struct {
	c      *Client
	kind   string // "search" | "xsql"
	entity string
	args   Args
	sel    Selection
	xsql   string
	params map[string]any
	opts   []Opt

	mu      sync.Mutex
	records map[string]Record
	initial []Record

	relationXids []string
	watch        *WatchHandle
	buf          *coalesceBuffer
	ctx          context.Context
	cancel       context.CancelFunc
	trigger      chan WatchEvent
}

// WatchQuery opens a live search result-set. The initial snapshot is taken
// synchronously; the result set updates until Close (or ctx cancellation).
func (c *Client) WatchQuery(ctx context.Context, entity string, args Args, sel Selection, opts ...Opt) (*QueryWatch, error) {
	return c.newQueryWatch(ctx, "search", entity, args, sel, "", nil, opts)
}

// WatchQueryXSQL opens a live XSQL result-set. The Entity(name) option is
// required (kebab-case root) so relation interest can be derived.
func (c *Client) WatchQueryXSQL(ctx context.Context, xsql string, params map[string]any, opts ...Opt) (*QueryWatch, error) {
	o := applyOpts(opts)
	if o.entity == "" {
		return nil, newError("WatchQueryXSQL requires Entity(name) (kebab-case root)", "INVALID_INTEREST")
	}
	return c.newQueryWatch(ctx, "xsql", o.entity, nil, nil, xsql, params, opts)
}

func (c *Client) newQueryWatch(ctx context.Context, kind, entity string, args Args, sel Selection, xsql string, params map[string]any, opts []Opt) (*QueryWatch, error) {
	mux := c.getMultiplexer()
	var relXids []string
	if mux.resolver != nil {
		_ = mux.resolver.ensureLoaded(ctx)
		if exid := mux.resolver.entityXidByName(entity); exid != "" {
			relXids = mux.resolver.relationsForEntity(exid)
		}
	}

	qw := &QueryWatch{
		c: c, kind: kind, entity: entity, args: args, sel: sel,
		xsql: xsql, params: params, opts: opts,
		records:      map[string]Record{},
		relationXids: relXids,
		buf:          newCoalesceBuffer("lossless", 1<<20),
		trigger:      make(chan WatchEvent, 1),
	}

	rows, err := qw.runQuery(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if id := rowKey(r); id != "" {
			qw.records[id] = r
		}
	}
	qw.initial = rows

	wctx, cancel := context.WithCancel(context.Background())
	qw.ctx = wctx
	qw.cancel = cancel
	interest := WatchInterest{Records: collectAllXids(rows), RelationXids: relXids}
	// A query with an empty snapshot still needs some interest to watch; if
	// there are no rows and no relation xids, fall back to entity touch.
	if len(interest.Records) == 0 && len(interest.RelationXids) == 0 {
		interest.Entities = []string{kebab(entity)}
	}
	w, err := c.Watch(wctx, interest)
	if err != nil {
		cancel()
		return nil, err
	}
	qw.watch = w
	go qw.drain()
	go qw.refreshLoop()
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				qw.Close()
			case <-wctx.Done():
			}
		}()
	}
	return qw, nil
}

// Initial returns the first snapshot of rows.
func (q *QueryWatch) Initial() []Record { return q.initial }

// List snapshots the current live records as a slice.
func (q *QueryWatch) List() []Record {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Record, 0, len(q.records))
	for _, r := range q.records {
		out = append(out, r)
	}
	return out
}

// Events returns the change-event channel.
func (q *QueryWatch) Events() <-chan WatchEvent { return q.buf.out }

// Close stops the watch.
func (q *QueryWatch) Close() {
	q.cancel()
	if q.watch != nil {
		q.watch.Close()
	}
	q.buf.close()
}

func (q *QueryWatch) runQuery(ctx context.Context) ([]Record, error) {
	if q.kind == "xsql" {
		return q.c.QueryRecords(ctx, q.xsql, q.params, q.opts...)
	}
	return q.c.Search(ctx, q.entity, q.args, q.sel, q.opts...)
}

func (q *QueryWatch) drain() {
	for ev := range q.watch.Events() {
		if hasPrefix(ev.Type, "entity/") || hasPrefix(ev.Type, "relation/") ||
			hasPrefix(ev.Type, "record/") {
			select {
			case q.trigger <- ev:
			default:
			}
		} else {
			q.buf.push(ev)
		}
	}
}

func (q *QueryWatch) refreshLoop() {
	for {
		select {
		case <-q.ctx.Done():
			return
		case ev := <-q.trigger:
			ev = q.debounce(ev)
			if q.ctx.Err() != nil {
				return
			}
			q.refresh(ev)
		}
	}
}

func (q *QueryWatch) debounce(ev WatchEvent) WatchEvent {
	timer := time.NewTimer(refreshCoalesce)
	defer timer.Stop()
	for {
		select {
		case ev2 := <-q.trigger:
			ev = ev2
		case <-timer.C:
			return ev
		case <-q.ctx.Done():
			return ev
		}
	}
}

func (q *QueryWatch) refresh(trigger WatchEvent) {
	rows, err := q.runQuery(q.ctx)
	if err != nil {
		q.buf.push(WatchEvent{Type: "subscription/rejected", Reason: errMsg(err)})
		return
	}
	q.mu.Lock()
	prev := q.records
	next := map[string]Record{}
	for _, r := range rows {
		if id := rowKey(r); id != "" {
			next[id] = r
		}
	}
	q.records = next
	q.mu.Unlock()

	prov := func(e *WatchEvent) {
		e.Ts = trigger.Ts
		e.Txid = trigger.Txid
		e.Actor = trigger.Actor
		e.Request = trigger.Request
	}
	for id, r := range next {
		before, ok := prev[id]
		if !ok {
			ev := WatchEvent{Type: "query/added", Record: id, After: r}
			prov(&ev)
			q.buf.push(ev)
		} else if !jsonEqual(before, r) {
			changed, beforeMap, afterMap := diffRow(before, r)
			ev := WatchEvent{Type: "query/changed", Record: id,
				Before: beforeMap, After: afterMap, Changed: changed}
			prov(&ev)
			q.buf.push(ev)
		}
	}
	for id := range prev {
		if _, ok := next[id]; !ok {
			ev := WatchEvent{Type: "query/removed", Record: id}
			prov(&ev)
			q.buf.push(ev)
		}
	}

	// Re-sync the underlying watch's record interest.
	if q.watch != nil {
		_ = q.watch.SetInterest(WatchInterest{
			Records:      collectAllXids(rows),
			RelationXids: q.relationXids,
		})
	}
}

// ---- helpers -------------------------------------------------------------

func rowKey(r Record) string {
	if r == nil {
		return ""
	}
	if s := asStr(r["xid"]); s != "" {
		return s
	}
	return asStr(r["euuid"])
}

// collectAllXids walks the result tree and collects every record xid found —
// top-level and nested — so updates on nested rows reach the parent watch.
func collectAllXids(rows []Record) []string {
	set := map[string]struct{}{}
	var visit func(v any)
	visit = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if s, ok := t["xid"].(string); ok {
				set[s] = struct{}{}
			}
			for k, vv := range t {
				if k == "xid" {
					continue
				}
				visit(vv)
			}
		case []any:
			for _, e := range t {
				visit(e)
			}
		case []Record:
			for _, e := range t {
				visit(map[string]any(e))
			}
		}
	}
	for _, r := range rows {
		visit(map[string]any(r))
	}
	return sortedKeys(set)
}

func diffRow(before, after Record) (changed []string, beforeMap, afterMap map[string]any) {
	beforeMap = map[string]any{}
	afterMap = map[string]any{}
	keys := map[string]struct{}{}
	for k := range before {
		keys[k] = struct{}{}
	}
	for k := range after {
		keys[k] = struct{}{}
	}
	for k := range keys {
		if !shallowEq(before[k], after[k]) {
			changed = append(changed, k)
			beforeMap[k] = before[k]
			afterMap[k] = after[k]
		}
	}
	return changed, beforeMap, afterMap
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}
