package capability

import "context"

type connectionGoneKey struct{}

// WithConnectionGone returns a context that carries gone, a channel the native adapter closes
// when the connection a request came on has ended, before it tells the core (Disconnected).
// An operation's handler runs on a context that keeps the caller's values and not its
// cancellation (ADR-0020 §Adapter projections), so this is how a handler that makes
// something the connection owns (a foreground share, ADR-0108 §3) learns that the connection
// ended while it was still working: it checks after it registers what it made, and the
// Disconnected hook, which runs after the close, finds it if the check was too early.
func WithConnectionGone(ctx context.Context, gone <-chan struct{}) context.Context {
	return context.WithValue(ctx, connectionGoneKey{}, gone)
}

// ConnectionGone reports whether the connection the request of ctx came on has ended. It is
// false for a request that did not come on a connection that reports its end.
func ConnectionGone(ctx context.Context) bool {
	gone, _ := ctx.Value(connectionGoneKey{}).(<-chan struct{})
	if gone == nil {
		return false
	}
	select {
	case <-gone:
		return true
	default:
		return false
	}
}
