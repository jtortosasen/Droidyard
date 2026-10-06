package gradle

import (
	"encoding/json"
	"errors"
)

// The JSON-RPC 2.0 codec of gradle.md §Protocol: one message per line, requests with
// an integer id, replies correlated by id. An id-less message is a notification and is
// ignored here (step 5 serves no notification; builds in step 7 do).

// request is one JSON-RPC request the client sends.
type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// message is whatever the sidecar writes: a reply carries an id; a notification does
// not. Exactly one of result and error is set.
type message struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// rpcError is a JSON-RPC error object. Data carries the sidecar's own detail (a Gradle
// failure message); it is what the core turns into an error's cause.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// The JSON-RPC reserved codes gradle.md §Protocol names, for the failures the client
// tells apart.
const (
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInternalError  = -32603
	// codeApplication is the sidecar's own: a TAPI call it could not serve.
	codeApplication = -32000
)

func encodeRequest(id int, method string, params any) (string, error) {
	b, err := json.Marshal(request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// errNotJSON is a stdout line that is not a JSON-RPC message: the sidecar has lost the
// protocol channel and the client must treat it as a crash (gradle.md §Protocol, D6).
var errNotJSON = errors.New("stdout line is not JSON")

func decodeMessage(line string) (message, error) {
	var m message
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return message{}, errNotJSON
	}
	return m, nil
}

// dataText returns the error's data as text: a JSON string unquoted, anything else
// verbatim. It becomes an error's cause, never its message.
func (e *rpcError) dataText() string {
	if len(e.Data) == 0 || string(e.Data) == "null" {
		return e.Message
	}
	var s string
	if err := json.Unmarshal(e.Data, &s); err == nil {
		return s
	}
	return string(e.Data)
}
