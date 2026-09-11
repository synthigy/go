package synthigy

import (
	"context"
	"sync"
)

// Stream is a cancellable channel of T produced by a background goroutine.
// Range over Events() to consume; the channel closes when the stream ends
// (ctx cancelled, Close called, or a terminal error). After the channel
// closes, Err() reports the terminal error, if any.
//
//	s := client.Observe(ctx, synthigy.RecordsDesc("u-1"))
//	defer s.Close()
//	for ev := range s.Events() {
//	    // handle ev
//	}
//	if err := s.Err(); err != nil { log.Print(err) }
type Stream[T any] struct {
	ch     chan T
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	err    error
}

// Events returns the receive-only channel of stream values.
func (s *Stream[T]) Events() <-chan T { return s.ch }

// Close stops the stream and releases its resources. Safe to call multiple
// times. After Close, the Events channel drains and closes.
func (s *Stream[T]) Close() { s.cancel() }

// Err blocks until the stream has fully stopped, then returns its terminal
// error (nil for a clean stop / context cancellation). Call it after the
// Events channel has closed.
func (s *Stream[T]) Err() error {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// newStream wires up a producer goroutine. run should call emit for each
// value (emit returns false when the consumer/ctx is gone — stop promptly)
// and return a terminal error, if any.
func newStream[T any](parent context.Context, run func(ctx context.Context, emit func(T) bool) error) *Stream[T] {
	ctx, cancel := context.WithCancel(parent)
	s := &Stream[T]{
		ch:     make(chan T),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	emit := func(v T) bool {
		select {
		case s.ch <- v:
			return true
		case <-ctx.Done():
			return false
		}
	}
	go func() {
		defer close(s.done)
		defer close(s.ch)
		err := run(ctx, emit)
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
	}()
	return s
}
