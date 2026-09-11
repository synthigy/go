package synthigy

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// WatchInterest declares what a Watch wants. At least one of Records,
// Entities, Relations, or RelationXids must be non-empty.
//
//   - Records:      record xids — data-track record/relation events.
//   - Entities:     entity names — entity/touched cache-invalidation pokes.
//   - Relations:    relation names ("Entity.label") — relation/touched pokes.
//   - RelationXids: relation xids, used locally to gate relation/link events.
//   - Ops:          optional op filter ("insert","update","delete","link","unlink").
type WatchInterest struct {
	Records      []string
	Entities     []string
	Relations    []string
	RelationXids []string
	Ops          []string
}

// WatchEvent is a shaped, multiplexed live event. Sentinels
// (subscription/rejected, connection/resumed, schema/changed) carry only
// Type and (for rejections) Reason. Raw holds the full source envelope for
// data events.
type WatchEvent struct {
	Type     string
	Record   string
	Ts       string
	Txid     string
	Actor    string
	Request  string
	Scope    string
	Tenant   any
	Before   map[string]any
	After    map[string]any
	Changed  []string
	Data     []string
	Entity   string
	Relation string
	Reason   string
	Raw      map[string]any
}

// getMultiplexer returns the client's shared watch multiplexer, creating it
// lazily. One SSE + one consolidated subscription POST per client.
func (c *Client) getMultiplexer() *watchMultiplexer {
	c.muxMu.Lock()
	defer c.muxMu.Unlock()
	if c.mux == nil {
		c.mux = newWatchMultiplexer(c, true, c.keepAlive)
	}
	return c.mux
}

type watchMultiplexer struct {
	client     *Client
	wantSchema bool
	resolver   *schemaResolver

	mu            sync.Mutex
	watches       map[*WatchHandle]struct{}
	schemaWatches map[*SchemaWatch]struct{}
	keepAlive     bool
	sseEverOpened bool
	lastUnion     string
	running       bool

	ctx      context.Context
	cancel   context.CancelFunc
	flushReq chan struct{}
}

func newWatchMultiplexer(c *Client, wantSchema, keepAlive bool) *watchMultiplexer {
	ctx, cancel := context.WithCancel(context.Background())
	m := &watchMultiplexer{
		client:        c,
		wantSchema:    wantSchema,
		watches:       map[*WatchHandle]struct{}{},
		schemaWatches: map[*SchemaWatch]struct{}{},
		keepAlive:     keepAlive,
		ctx:           ctx,
		cancel:        cancel,
		flushReq:      make(chan struct{}, 1),
	}
	if wantSchema {
		m.resolver = newSchemaResolver(c)
		go m.resolver.ensureLoaded(ctx) // warm eagerly; failures non-fatal
	}
	if keepAlive {
		m.ensureSSE()
	}
	return m
}

func (m *watchMultiplexer) register(w *WatchHandle) {
	m.mu.Lock()
	m.watches[w] = struct{}{}
	opened := m.sseEverOpened
	m.mu.Unlock()
	m.ensureSSE()
	if opened {
		m.scheduleFlush()
	}
}

func (m *watchMultiplexer) unregister(w *WatchHandle) {
	m.mu.Lock()
	if _, ok := m.watches[w]; !ok {
		m.mu.Unlock()
		return
	}
	delete(m.watches, w)
	empty := len(m.watches) == 0
	m.mu.Unlock()
	m.scheduleFlush()
	if empty && !m.keepAlive {
		m.closeSSE()
	}
}

func (m *watchMultiplexer) registerSchemaWatch(sw *SchemaWatch) {
	m.mu.Lock()
	m.schemaWatches[sw] = struct{}{}
	m.mu.Unlock()
	m.ensureSSE()
}

func (m *watchMultiplexer) unregisterSchemaWatch(sw *SchemaWatch) {
	m.mu.Lock()
	delete(m.schemaWatches, sw)
	m.mu.Unlock()
}

func (m *watchMultiplexer) close() {
	m.mu.Lock()
	m.keepAlive = false
	watches := make([]*WatchHandle, 0, len(m.watches))
	for w := range m.watches {
		watches = append(watches, w)
	}
	m.mu.Unlock()
	for _, w := range watches {
		w.Close()
	}
	m.closeSSE()
	m.cancel()
}

func (m *watchMultiplexer) ensureSSE() {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()
	go m.runFlush()
	go m.runSSE()
}

func (m *watchMultiplexer) closeSSE() {
	m.mu.Lock()
	m.running = false
	m.lastUnion = ""
	m.sseEverOpened = false
	m.mu.Unlock()
	// The SSE goroutine watches m.ctx; cancelling here would kill the whole
	// mux, so instead we rely on the next ensureSSE to restart. To actually
	// stop the stream we recreate the cancellable context.
	m.cancel()
	m.mu.Lock()
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.mu.Unlock()
}

func (m *watchMultiplexer) scheduleFlush() {
	select {
	case m.flushReq <- struct{}{}:
	default:
	}
}

func (m *watchMultiplexer) runFlush() {
	m.mu.Lock()
	ctx := m.ctx
	m.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.flushReq:
			m.flush(ctx)
		}
	}
}

func (m *watchMultiplexer) flush(ctx context.Context) {
	m.mu.Lock()
	watches := make([]*WatchHandle, 0, len(m.watches))
	for w := range m.watches {
		watches = append(watches, w)
	}
	m.mu.Unlock()

	records := map[string]struct{}{}
	entities := map[string]struct{}{}
	relations := map[string]struct{}{}
	ops := map[string]struct{}{}
	anyOps := false
	for _, w := range watches {
		i := w.getInterest()
		for _, x := range i.Records {
			records[x] = struct{}{}
		}
		for _, e := range i.Entities {
			entities[e] = struct{}{}
		}
		for _, r := range i.Relations {
			relations[r] = struct{}{}
		}
		if len(i.Ops) > 0 {
			anyOps = true
			for _, o := range i.Ops {
				ops[o] = struct{}{}
			}
		}
	}

	items := []map[string]any{}
	if len(records) > 0 {
		item := map[string]any{"type": "data", "records": sortedKeys(records)}
		if anyOps {
			item["operations"] = sortedKeys(ops)
		}
		items = append(items, item)
	}
	if len(entities) > 0 {
		items = append(items, map[string]any{"type": "entity", "entities": sortedKeys(entities)})
	}
	if len(relations) > 0 {
		items = append(items, map[string]any{"type": "relation", "relations": sortedKeys(relations)})
	}
	if m.wantSchema {
		items = append(items, map[string]any{"type": "runtime-model"})
	}

	sig, _ := json.Marshal(items)
	m.mu.Lock()
	if string(sig) == m.lastUnion {
		m.mu.Unlock()
		return
	}
	m.lastUnion = string(sig)
	m.mu.Unlock()

	if err := m.client.setSubscriptionsItems(ctx, items); err != nil {
		m.fanOutSentinel(WatchEvent{Type: "subscription/rejected", Reason: errMsg(err)})
	}
}

func (m *watchMultiplexer) runSSE() {
	m.mu.Lock()
	ctx := m.ctx
	m.mu.Unlock()

	stream := m.client.Listen(ctx)
	firstSession := true
	for ev := range stream.Events() {
		switch {
		case ev.Type == "sse/open":
			m.mu.Lock()
			m.sseEverOpened = true
			m.mu.Unlock()
			if !firstSession {
				m.fanOutSentinel(WatchEvent{Type: "connection/resumed"})
			}
			firstSession = false
			m.scheduleFlush()
		case ev.Type == "runtime-model" || ev.sseEvent == "runtime-model":
			if m.resolver != nil {
				go func() {
					if err := m.resolver.refresh(ctx); err == nil {
						m.fanOutSchema(WatchEvent{Type: "schema/changed"})
					}
				}()
			}
		default:
			m.dispatch(ev)
		}
	}
}

func (m *watchMultiplexer) fanOutSentinel(s WatchEvent) {
	m.mu.Lock()
	watches := make([]*WatchHandle, 0, len(m.watches))
	for w := range m.watches {
		watches = append(watches, w)
	}
	m.mu.Unlock()
	for _, w := range watches {
		w.pushSentinel(s)
	}
}

func (m *watchMultiplexer) fanOutSchema(s WatchEvent) {
	m.mu.Lock()
	sws := make([]*SchemaWatch, 0, len(m.schemaWatches))
	for sw := range m.schemaWatches {
		sws = append(sws, sw)
	}
	m.mu.Unlock()
	for _, sw := range sws {
		sw.push(s)
	}
}

func (m *watchMultiplexer) dispatch(env Event) {
	if !strings.Contains(env.Type, "/") {
		return
	}
	shaped := shapeEvent(env)
	m.mu.Lock()
	watches := make([]*WatchHandle, 0, len(m.watches))
	for w := range m.watches {
		watches = append(watches, w)
	}
	m.mu.Unlock()
	for _, w := range watches {
		if w.matches(env) {
			w.pushEvent(shaped)
		}
	}
}

// shapeEvent translates a raw channel envelope into a WatchEvent. before/after
// keys are already attribute-name-keyed by the server (2026-06-01 trim).
func shapeEvent(env Event) WatchEvent {
	track := strings.SplitN(env.Type, "/", 2)[0]
	op := ""
	if parts := strings.SplitN(env.Type, "/", 2); len(parts) == 2 {
		op = parts[1]
	}
	if env.Type == "entity/touched" {
		return WatchEvent{Type: env.Type, Entity: rawStr(env.Raw, "entity"), Ts: env.Ts, Raw: env.Raw}
	}
	if env.Type == "relation/touched" {
		return WatchEvent{Type: env.Type, Relation: rawStr(env.Raw, "relation"), Ts: env.Ts, Raw: env.Raw}
	}
	base := WatchEvent{
		Type: env.Type, Ts: env.Ts, Txid: env.Txid, Actor: env.Actor,
		Request: env.Request, Tenant: env.Tenant, Scope: env.Scope, Raw: env.Raw,
	}
	switch track {
	case "record":
		base.Record = env.RecordXID
		base.Before = env.Before
		base.After = env.After
		base.Changed = computeChanged(env.Before, env.After, op)
	case "relation":
		base.Data = env.Data
	}
	return base
}

func computeChanged(before, after map[string]any, op string) []string {
	switch op {
	case "insert":
		return mapKeys(after)
	case "delete":
		return mapKeys(before)
	}
	if before == nil || after == nil {
		return nil
	}
	out := []string{}
	for k, av := range after {
		if !shallowEq(before[k], av) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func shallowEq(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func mapKeys(m map[string]any) []string {
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func rawStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Watch is a live subscription handle. Range over Events(); Close() drops it
// from the multiplex union and closes every channel Events() has handed out.
//
// FAN-OUT, not a shared queue: every call to Events() gets its OWN
// coalesceBuffer (and so its own goroutine + channel) and sees the FULL
// event stream independently — mirrors sdk/js watch.js's Watch (a fresh
// CoalesceBuffer per `events` access, fanned out to a Set) and sdk/py's
// AsyncWatchHandle.events(). Two goroutines ranging over two Events()
// channels from the SAME handle both see every event; neither "steals" it
// from the other the way two readers of one plain channel would.
type WatchHandle struct {
	mux    *watchMultiplexer
	mu     sync.Mutex
	intr   WatchInterest
	closed bool
	sinks  map[*coalesceBuffer]struct{}
}

// Watch opens a live subscription. The interest fuses onto the client's
// shared SSE + subscription POST. Cancel via ctx or Watch.Close.
func (c *Client) Watch(ctx context.Context, interest WatchInterest, opts ...Opt) (*WatchHandle, error) {
	ni, err := normalizeInterest(interest)
	if err != nil {
		return nil, err
	}
	mux := c.getMultiplexer()
	w := &WatchHandle{mux: mux, intr: ni, sinks: map[*coalesceBuffer]struct{}{}}
	mux.register(w)
	if ctx != nil {
		go func() {
			<-ctx.Done()
			w.Close()
		}()
	}
	return w, nil
}

// Events returns a FRESH event channel — call as many times as you like;
// every call sees the full stream independently (see the FAN-OUT note on
// WatchHandle). Each channel's backing goroutine runs until Close() ends
// the whole handle — there is no per-channel unsubscribe, matching the
// same "consumers live for the handle's lifetime" contract the other SDKs
// accept when a caller doesn't explicitly tear down its own iterator.
func (w *WatchHandle) Events() <-chan WatchEvent {
	buf := newCoalesceBuffer("coalesce", 100)
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		buf.close()
		return buf.out
	}
	w.sinks[buf] = struct{}{}
	w.mu.Unlock()
	return buf.out
}

func (w *WatchHandle) getInterest() WatchInterest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.intr
}

// Add widens the watch's record interest.
func (w *WatchHandle) Add(xids ...string) {
	w.mu.Lock()
	if w.closed || len(xids) == 0 {
		w.mu.Unlock()
		return
	}
	set := map[string]struct{}{}
	for _, x := range w.intr.Records {
		set[x] = struct{}{}
	}
	for _, x := range xids {
		set[x] = struct{}{}
	}
	w.intr.Records = sortedKeys(set)
	w.mu.Unlock()
	w.mux.scheduleFlush()
}

// Remove narrows the watch's record interest.
func (w *WatchHandle) Remove(xids ...string) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	set := map[string]struct{}{}
	for _, x := range w.intr.Records {
		set[x] = struct{}{}
	}
	for _, x := range xids {
		delete(set, x)
	}
	w.intr.Records = sortedKeys(set)
	w.mu.Unlock()
	w.mux.scheduleFlush()
}

// SetInterest replaces the watch's interest.
func (w *WatchHandle) SetInterest(interest WatchInterest) error {
	ni, err := normalizeInterest(interest)
	if err != nil {
		return err
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.intr = ni
	w.mu.Unlock()
	w.mux.scheduleFlush()
	return nil
}

// Close stops the watch and closes every channel Events() has handed out.
func (w *WatchHandle) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	sinks := make([]*coalesceBuffer, 0, len(w.sinks))
	for buf := range w.sinks {
		sinks = append(sinks, buf)
	}
	w.mu.Unlock()
	for _, buf := range sinks {
		buf.close()
	}
	w.mux.unregister(w)
}

func (w *WatchHandle) pushEvent(ev WatchEvent)    { w.push(ev) }
func (w *WatchHandle) pushSentinel(ev WatchEvent) { w.push(ev) }

func (w *WatchHandle) push(ev WatchEvent) {
	w.mu.Lock()
	sinks := make([]*coalesceBuffer, 0, len(w.sinks))
	for buf := range w.sinks {
		sinks = append(sinks, buf)
	}
	w.mu.Unlock()
	for _, buf := range sinks {
		buf.push(ev)
	}
}

func (w *WatchHandle) matches(env Event) bool {
	i := w.getInterest()
	switch env.Type {
	case "record/insert", "record/update", "record/delete":
		if contains(i.Records, env.RecordXID) {
			return opMatch(i.Ops, env.Type)
		}
		return false
	case "relation/link", "relation/unlink":
		if len(env.Data) >= 2 && contains(i.Records, env.Data[0]) {
			return opMatch(i.Ops, env.Type)
		}
		return false
	case "entity/touched":
		return contains(i.Entities, rawStr(env.Raw, "entity"))
	case "relation/touched":
		return contains(i.Relations, rawStr(env.Raw, "relation"))
	}
	return false
}

func opMatch(ops []string, typ string) bool {
	if len(ops) == 0 {
		return true
	}
	parts := strings.SplitN(typ, "/", 2)
	if len(parts) != 2 {
		return false
	}
	return contains(ops, parts[1])
}

func contains(s []string, v string) bool {
	if v == "" {
		return false
	}
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func normalizeInterest(i WatchInterest) (WatchInterest, error) {
	out := WatchInterest{
		Records:      dedupe(i.Records),
		Entities:     dedupe(i.Entities),
		Relations:    dedupe(i.Relations),
		RelationXids: dedupe(i.RelationXids),
		Ops:          dedupe(i.Ops),
	}
	if len(out.Records) == 0 && len(out.Entities) == 0 &&
		len(out.Relations) == 0 && len(out.RelationXids) == 0 {
		return WatchInterest{}, newError(
			"watch requires at least one of: Records, Entities, Relations, RelationXids",
			"EMPTY_INTEREST")
	}
	return out, nil
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	set := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, ok := set[v]; ok {
			continue
		}
		set[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// SchemaWatch is a stream of schema-deploy events ({Type: "schema/changed"}).
type SchemaWatch struct {
	mux    *watchMultiplexer
	buf    *coalesceBuffer
	closed bool
	mu     sync.Mutex
}

// WatchSchema opens a stream of schema-deploy events.
func (c *Client) WatchSchema(ctx context.Context) *SchemaWatch {
	mux := c.getMultiplexer()
	sw := &SchemaWatch{mux: mux, buf: newCoalesceBuffer("lossless", 32)}
	mux.registerSchemaWatch(sw)
	if ctx != nil {
		go func() {
			<-ctx.Done()
			sw.Close()
		}()
	}
	return sw
}

// Events returns the schema-deploy event channel.
func (sw *SchemaWatch) Events() <-chan WatchEvent { return sw.buf.out }

func (sw *SchemaWatch) push(ev WatchEvent) { sw.buf.push(ev) }

// Close stops the schema watch.
func (sw *SchemaWatch) Close() {
	sw.mu.Lock()
	if sw.closed {
		sw.mu.Unlock()
		return
	}
	sw.closed = true
	sw.mu.Unlock()
	sw.buf.close()
	sw.mux.unregisterSchemaWatch(sw)
}
