package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Protocol is the socket protocol's version, sent in every hello.
const Protocol = 1

// helloTimeout bounds how long a connection may take to say hello.
const helloTimeout = 10 * time.Second

// Hello is a client's first line (ADR-0019 §7).
type Hello struct {
	Protocol int                   `json:"protocol"`
	Version  string                `json:"version"`
	Client   capability.ClientKind `json:"client"`
	Root     string                `json:"root,omitempty"`
	Cwd      string                `json:"cwd"`
	// WaitMs is an MCP session's wait budget, from `droidyard mcp --wait`
	// (ADR-0020 §Adapter projections). Zero means the default.
	WaitMs int64 `json:"waitMs,omitempty"`
	// Tools is an MCP session's tool profile, from `droidyard mcp --tools core`
	// (ADR-0036 Decision 4). Empty means every tool.
	Tools string `json:"tools,omitempty"`
	// Chat is the id of the assistant's chat an MCP session belongs to, when the client
	// names one: `droidyard mcp` fills it from CLAUDE_CODE_SESSION_ID (ADR-0088 §1). It
	// matches ChatPattern or is empty; the daemon clears one that does not.
	Chat string `json:"chat,omitempty"`
}

// ChatPattern is what a hello's chat id must match (ADR-0088 §1).
var ChatPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

type helloLine struct {
	Hello *Hello `json:"hello"`
}

// HelloReply is the daemon's answer: its version and the session id, or an error.
type HelloReply struct {
	Version string            `json:"version"`
	Session string            `json:"session,omitempty"`
	Error   *capability.Error `json:"error,omitempty"`
}

// Dial connects to the daemon's socket and says hello. It returns the connection and
// the reader that read the reply, which the caller keeps: bytes after the reply may
// already be in its buffer. A refused hello is returned as its *capability.Error.
func Dial(socket string, h Hello) (net.Conn, *bufio.Reader, HelloReply, error) {
	var reply HelloReply
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, nil, reply, err
	}
	conn.SetDeadline(time.Now().Add(helloTimeout))
	line, _ := json.Marshal(helloLine{Hello: &h})
	if _, err := conn.Write(append(line, '\n')); err != nil {
		conn.Close()
		return nil, nil, reply, err
	}
	r := bufio.NewReader(conn)
	raw, err := r.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return nil, nil, reply, fmt.Errorf("the daemon closed the connection without answering the hello: %w", err)
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		conn.Close()
		return nil, nil, reply, fmt.Errorf("the daemon's hello reply is not JSON: %w", err)
	}
	if reply.Error != nil {
		conn.Close()
		return nil, nil, reply, reply.Error
	}
	if reply.Session == "" {
		conn.Close()
		return nil, nil, reply, errors.New("the daemon's hello reply carries no session")
	}
	conn.SetDeadline(time.Time{})
	return conn, r, reply, nil
}
