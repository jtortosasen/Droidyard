package peer

import "time"

// SetHelloTimeout shortens how long a socket may take to send its hello.
func SetHelloTimeout(d time.Duration) { helloTimeout = d }

// SetStreamFrameTimeout shortens how long a screen socket may take to send its stream id.
func SetStreamFrameTimeout(d time.Duration) { streamFrameTimeout = d }

// SetUploadBound shortens how long an upload's body may take to arrive.
func SetUploadBound(d time.Duration) { uploadBound = d }

// SetSessionCloseDelay holds a session socket's close back by d when a listener shuts down,
// so a test puts it after the live views' sockets.
func SetSessionCloseDelay(d time.Duration) { sessionCloseDelay = d }

// SetJoinTimeout shortens how long a join connection may take, from its handshake to its close.
func SetJoinTimeout(d time.Duration) { joinTimeout = d }
