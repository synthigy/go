package synthigy

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
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
// creation). A non-empty stream names this connection's own subscription set.
// onFrame returns false to stop early. Returns when the stream ends or ctx is
// cancelled.
func (c *Client) sseSession(ctx context.Context, lastEventID, stream string, onFrame func(sseFrame) bool) error {
	headers := map[string]string{"Accept": "text/event-stream"}
	if lastEventID != "" {
		headers["Last-Event-ID"] = lastEventID
	}
	u := c.endpoint + "/data/events"
	if stream != "" {
		u += "?stream=" + url.QueryEscape(stream)
	}
	resp, err := c.fetchAuth(ctx, http.MethodGet, u, nil, headers, false)
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

// isFatalStreamErr reports auth errors that should stop a reconnect loop; any 403 is one.
func isFatalStreamErr(err error) bool {
	var se *Error
	if errors.As(err, &se) {
		return se.Code == "UNAUTHORIZED" || se.Code == "FORBIDDEN" || se.Status == 403
	}
	return false
}

// newStreamID mints a fresh id for one event stream: the engine keeps that stream's subscription set apart.
func newStreamID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Listen opens an SSE connection to /data/events and streams change
// notifications, reconnecting with exponential backoff. The first event of
// each session is {Type: "sse/open", Stream: id}, the fresh id that
// connection was opened with; when an opened session ends, before the
// reconnect wait, it emits {Type: "sse/close", Stream: id}. It yields only what the client's subscription
// set selects; declare that interest with Watch, which also re-sends the set
// after a reconnect. Most callers want Watch(...).Events() instead.
func (c *Client) Listen(ctx context.Context, opts ...Opt) *Stream[Event] {
	return newStream(ctx, func(ctx context.Context, emit func(Event) bool) error {
		delay := sseInitialBackoff
		lastEventID := ""
		for ctx.Err() == nil {
			stream := newStreamID()
			opened := false
			err := c.sseSession(ctx, lastEventID, stream, func(f sseFrame) bool {
				if f.id != "" {
					lastEventID = f.id
				}
				delay = sseInitialBackoff
				if f.event == "open" {
					opened = true
					return emit(Event{Type: "sse/open", Stream: stream})
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
			if opened && !emit(Event{Type: "sse/close", Stream: stream}) {
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
