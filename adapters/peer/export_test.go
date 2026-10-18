package peer

import "time"

// SetHelloTimeout shortens how long a socket may take to send its hello.
func SetHelloTimeout(d time.Duration) { helloTimeout = d }

// SetRetryEvery shortens the wait between two binds of a listener that failed to bind.
func SetRetryEvery(d time.Duration) { retryEvery = d }
