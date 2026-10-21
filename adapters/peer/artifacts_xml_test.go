package peer_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/jtortosasen/droidyard/adapters/peer"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// Decision (ADR-0115 Decision 1): the peer route serves a UI dump, application/xml, as
// itself: the bytes, the media type without a charset or a rewrite, and the untrusted mark,
// and the client's fetch accepts the type. The route stays closed to other types
// (TestArtifactRouteServesWhatTheReaderGives). Seen failing with the type left out of the
// route's list (a 500 for the stored dump) and out of the client's (ErrArtifactReply).
func TestPeerArtifactRouteServesXML(t *testing.T) {
	doc := []byte("<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation=\"0\"><node text=\"a &amp; b\" /></hierarchy>\n")
	host, client, link := pairServing(t, func(name, id string) (peer.Artifact, error) {
		if id == ownedID {
			return peer.Artifact{MediaType: "application/xml", Untrusted: true, Data: doc}, nil
		}
		return peer.Artifact{}, capability.NewError(capability.ErrArtifactNotFound, "no")
	})

	got, err := link.Artifact(context.Background(), ownedID, 1<<20)
	if err != nil || got.MediaType != "application/xml" || !got.Untrusted || !bytes.Equal(got.Data, doc) {
		t.Fatalf("the owner's dump: %q untrusted %v, %d bytes, %v; want application/xml, untrusted, the %d bytes", got.MediaType, got.Untrusted, len(got.Data), err, len(doc))
	}

	// The header as the route sets it, over the same pinned connection the client uses.
	hostFP := host.host.Identity().Fingerprint()
	cfg := hosting.PinnedConfig(client.host.Identity(), func(fp string) bool { return fp == hostFP })
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}
	resp, err := hc.Get("https://" + host.host.Health().Hosting.Listen + capability.PeerArtifactsPath + ownedID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/xml" || resp.Header.Get("X-Droidyard-Untrusted") != "true" || !bytes.Equal(body, doc) {
		t.Errorf("GET: %d %q untrusted %q, %d bytes; want 200 application/xml true and the dump", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("X-Droidyard-Untrusted"), len(body))
	}
}
