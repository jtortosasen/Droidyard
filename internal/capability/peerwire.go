package capability

import "errors"

// The peer protocol (ADR-0098 §4) is internal: both ends are this code, so it is not in
// schema/capabilities.json. A session is a TLS 1.3 connection with both certificates
// pinned, upgraded to a WebSocket on PeerPath; its first frame each way is a PeerHello,
// then the request and response envelopes of this package, requests matched by id and a
// subscription's events by subscriptionId. The two ends are adapters/peer (the host's
// listener) and internal/hosting (the client's link); this file is where they agree,
// beside the envelopes, because an adapter may import the core and nothing else
// (docs/architecture/overview.md §Adapter boundary).
const (
	// PeerProtocol is the version of this framing. Both ends must name the same one.
	PeerProtocol = 1
	// PeerPath is the route of the session.
	PeerPath = "/peer/v1/ws"
	// PeerScreenPath is the route of a live view's video (ADR-0078 §4 on the peer port):
	// a socket of its own, TLS-pinned like the session, whose first frame is
	// {"stream": <id>}, an id from a device.watchScreen of the same peer.
	PeerScreenPath = "/peer/v1/screen"
	// PeerUploadPath is the route of an APK upload (ADR-0099 §3): a PUT whose body is the
	// file, with a Content-Length of at most PeerMaxUpload, answered with a PeerUploadReply.
	PeerUploadPath = "/peer/v1/upload"
	// PeerArtifactsPath is the prefix of the route a peer fetches the bytes of an artifact
	// one of its own calls produced on (ADR-0099 §2): GET PeerArtifactsPath + <id>, the id
	// being the reference without its droidyard://artifact/ scheme. The answer is the
	// artifact's stored bytes with its media type and, for untrusted content, the header
	// X-Droidyard-Untrusted: true; an artifact the peer does not own is 404.
	PeerArtifactsPath = "/peer/v1/artifacts/"
	// PeerMaxUpload is the largest APK a host takes, in bytes (ADR-0099 §3).
	PeerMaxUpload = 512 << 20
	// PeerJoinPath is the one route a connection that offered PeerJoinALPN reaches
	// (ADR-0107 §3): a POST of a PeerJoinRequest, answered with a PeerJoinReply. It is not on
	// the port's main mux: only a join connection is served it, and only one request.
	PeerJoinPath = "/peer/v1/join"
	// PeerJoinALPN is the application protocol a TLS hello offers to join. A hello that
	// offers it may present a certificate that is not paired while a code is pending; every
	// other hello must present a paired one.
	PeerJoinALPN = "droidyard-join/1"
	// PeerMaxJoinBody is the largest body a join request or its answer may have, in bytes.
	PeerMaxJoinBody = 4 << 10
)

// PeerJoinRequest is the body of a join (ADR-0107 §3): the code's secret as unpadded
// base64url, the name the joining side proposes for itself and the API version it runs.
// The name is another machine's text: the host cleans it before it keeps it.
type PeerJoinRequest struct {
	Secret string `json:"secret"`
	Name   string `json:"name"`
	API    string `json:"api"`
}

// PeerJoinReply is the host's answer to a join: the name it proposes for itself and the
// device it lent. Both are another machine's text: the joining side cleans and checks them
// before anything is written (ADR-0100 §4).
type PeerJoinReply struct {
	Host   string `json:"host"`
	Device string `json:"device"`
}

// ErrPeerUploadBusy is what a store answers an upload with while the same peer's earlier
// one is still arriving: a peer has one slot, so its concurrent uploads cannot fill the
// host's disk. The listener answers it 409.
var ErrPeerUploadBusy = errors.New("this peer already has an upload arriving")

// PeerUploadReply is what a host answers a stored upload with: the id the peer names it by
// in device.installUpload, and the SHA-256 of the bytes the host stored, which the peer
// compares with the digest of what it sent.
type PeerUploadReply struct {
	Upload string `json:"upload"`
	SHA256 string `json:"sha256"`
}

// The WebSocket close codes of a peer session, in the application range.
const (
	// PeerCloseProtocol: the hello was wrong or named another peerProtocol.
	PeerCloseProtocol = 4400
	// PeerCloseNoHello: no hello arrived in time.
	PeerCloseNoHello = 4401
	// PeerCloseRevoked: the peer is no longer in hosting.peers (ADR-0098 §3).
	PeerCloseRevoked = 4403
)

// PeerHello is a session's first frame in each direction, inside a PeerHelloFrame: the
// framing version and the API version of the daemon that sends it. A different API
// version is a warning, not a refusal.
type PeerHello struct {
	PeerProtocol int    `json:"peerProtocol"`
	API          string `json:"api"`
}

// PeerHelloFrame is the hello as it travels.
type PeerHelloFrame struct {
	Hello *PeerHello `json:"hello"`
}
