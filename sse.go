package synthigy

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	sseInitialBackoff = time.Second
	sseMaxBackoff     = 30 * time.Second
)

// sseFrame is one parsed SSE frame. A synthetic frame with event=="open" is
// emitted once after the response headers arrive.
type sseFrame struct {
	event string
	id    string
	data  string
}

// sseSession opens one SSE session against /data/events and invokes onFrame
// for each frame. It first emits a synthetic {event:"open"} frame after the
// headers arrive (so callers can POST /subscription/set without racing stream
// creation). onFrame returns false to stop early. Returns when the stream
// ends or ctx is cancelled.
func (c *Client) sseSession(ctx context.Context, lastEventID string, onFrame func(sseFrame) bool) error {
	headers := map[string]string{"Accept": "text/event-stream"}
	if lastEventID != "" {
		headers["Last-Event-ID"] = lastEventID
	}
	resp, err := c.fetchAuth(ctx, http.MethodGet, c.endpoint+"/data/events", nil, headers, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw := readBody(resp)
		return httpError(raw, resp.StatusCode, "SSE connect failed")
	}

	// Headers received = the server has created the event stream session.
	if !onFrame(sseFrame{event: "open"}) {
		return nil
	}

	reader := bufio.NewReader(resp.Body)
	eventType := "message"
	var dataLines []string
	eventID := ""

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, "event:"):
				eventType = strings.TrimSpace(line[6:])
			case strings.HasPrefix(line, "data:"):
				dataLines = append(dataLines, strings.TrimSpace(line[5:]))
			case strings.HasPrefix(line, "id:"):
				eventID = strings.TrimSpace(line[3:])
			case line == "":
				if len(dataLines) > 0 {
					if !onFrame(sseFrame{
						event: eventType,
						id:    eventID,
						data:  strings.Join(dataLines, "\n"),
					}) {
						return nil
					}
				}
				eventType = "message"
				dataLines = nil
				eventID = ""
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			// io.EOF or transport error ends the session; caller reconnects.
			return nil
		}
	}
}

// isFatalStreamErr reports auth errors that should stop a reconnect loop.
func isFatalStreamErr(err error) bool {
	var se *Error
	if errors.As(err, &se) {
		return se.Code == "UNAUTHORIZED" || se.Code == "FORBIDDEN"
	}
	return false
}

// Listen opens an SSE connection to /data/events and streams change
// notifications, reconnecting with exponential backoff. The first event of
// each session is {Type: "sse/open"}. Events are notification-only — fetch
// updated records via Search/Get after receiving one. Subscriptions persist
// across reconnects; call Subscribe once at startup.
func (c *Client) Listen(ctx context.Context, opts ...Opt) *Stream[Event] {
	return newStream(ctx, func(ctx context.Context, emit func(Event) bool) error {
		delay := sseInitialBackoff
		lastEventID := ""
		for ctx.Err() == nil {
			err := c.sseSession(ctx, lastEventID, func(f sseFrame) bool {
				if f.id != "" {
					lastEventID = f.id
				}
				delay = sseInitialBackoff
				if f.event == "open" {
					return emit(Event{Type: "sse/open"})
				}
				if f.data == "" {
					return true
				}
				ev, perr := parseEvent([]byte(f.data))
				if perr != nil {
					return true // skip malformed frame
				}
				ev.sseEvent = f.event
				ev.sseID = f.id
				return emit(ev)
			})
			if ctx.Err() != nil {
				return nil
			}
			if isFatalStreamErr(err) {
				return err
			}
			if !sleep(ctx, delay) {
				return nil
			}
			delay = min(delay*2, sseMaxBackoff)
		}
		return nil
	})
}

// Observe subscribes to a record-set and streams matching change events,
// reconnecting automatically. Because the server tears down subscription
// state on SSE close, Observe re-subscribes before each session. Pass
// Backfill(true) to replay /history events missed during a disconnect; the
// subscription is removed when the stream closes.
func (c *Client) Observe(ctx context.Context, d Descriptor, opts ...Opt) (*Stream[Event], error) {
	nd, err := normalizeDescriptor(d)
	if err != nil {
		return nil, err
	}
	o := applyOpts(opts)
	key := o.key
	if key == "" {
		key = descriptorKey(nd)
	}

	return newStream(ctx, func(ctx context.Context, emit func(Event) bool) error {
		defer func() {
			if c.removeDataSub(key) {
				// Best-effort cleanup with a detached, bounded context.
				cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = c.flushSubscriptions(cctx)
				cancel()
			}
		}()

		delay := sseInitialBackoff
		lastSeenTs := ""
		for ctx.Err() == nil {
			// Re-register before each session — the server drops sub state on close.
			c.addDataSub(key, nd)
			if ferr := c.flushSubscriptions(ctx); ferr != nil {
				if isFatalStreamErr(ferr) {
					return ferr
				}
			} else {
				sErr := c.sseSession(ctx, "", func(f sseFrame) bool {
					if f.event == "open" || f.data == "" {
						return true
					}
					ev, perr := parseEvent([]byte(f.data))
					if perr != nil {
						return true
					}
					// Only entity/relation channel events have a slash-typed type.
					if !strings.Contains(ev.Type, "/") {
						return true
					}
					if !ev.matchesDescriptor(nd) {
						return true
					}
					if ev.Ts != "" {
						lastSeenTs = ev.Ts
					}
					delay = sseInitialBackoff
					return emit(ev)
				})
				if ctx.Err() != nil {
					return nil
				}
				if isFatalStreamErr(sErr) {
					return sErr
				}
			}

			if ctx.Err() != nil {
				return nil
			}
			if o.backfill && lastSeenTs != "" {
				lastSeenTs = c.backfillObserve(ctx, nd, lastSeenTs, o.backfillLimit, emit)
			}
			if !sleep(ctx, delay) {
				return nil
			}
			delay = min(delay*2, sseMaxBackoff)
		}
		return nil
	}), nil
}

// sleep waits for d or ctx cancellation. Returns false if ctx was cancelled.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
