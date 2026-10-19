package peer

import "time"

// SetHelloTimeout shortens how long a socket may take to send its hello.
func SetHelloTimeout(d time.Duration) { helloTimeout = d }

// SetStreamFrameTimeout shortens how long a screen socket may take to send its stream id.
func SetStreamFrameTimeout(d time.Duration) { streamFrameTimeout = d }

// SetRetryEvery shortens the wait between two binds of a listener that failed to bind.
func SetRetryEvery(d time.Duration) { retryEvery = d }

// SetUploadBound shortens how long an upload's body may take to arrive.
func SetUploadBound(d time.Duration) { uploadBound = d }
