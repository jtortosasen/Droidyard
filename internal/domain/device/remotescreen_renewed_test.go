package device

import (
	"errors"
	"strings"
	"testing"
)

// watchedEnd runs watchLink of a relayed source whose link channels are as given and returns
// the reason the source ended with.
func watchedEnd(t *testing.T, lost, marked bool) *ScreenEnd {
	t.Helper()
	s := (&ADB{}).newRelayedSource(&relayedScreens{}, &remoteTarget{remote: "far", id: "far/X1", serial: "X1"})
	down, mark := make(chan struct{}), make(chan struct{})
	if lost {
		close(down)
	}
	if marked {
		close(mark)
	}
	s.down, s.marked = down, mark
	s.wg.Add(1)
	s.watchLink()
	var end *ScreenEnd
	if !errors.As(s.hub.Reason(), &end) {
		t.Fatalf("the source ended with %v, want a ScreenEnd", s.hub.Reason())
	}
	return end
}

// Decision (ADR-0123 §2): a mark frame written to the host ends the relayed source with the
// fifth reason, renewed, and says whose mark changed.
func TestARelayedSourceEndsRenewedWhenTheMarkIsWritten(t *testing.T) {
	end := watchedEnd(t, false, true)
	if end.Reason != StopRenewed || !strings.Contains(end.Cause, "far") || !strings.Contains(end.Cause, "fast mark") {
		t.Errorf("a written mark ended the source with %+v; want renewed naming the remote and its fast mark", end)
	}
}

// Decision (ADR-0123 §2, Rests on): a renewed end is published for a mark change, never for a
// loss. A link lost alone is deviceGone; and when the mark channel is closed and the link is
// down at once (a loss that followed the write), the loss wins on every run: the select
// between two ready channels is random, so 200 runs would see renewed if the loss were not
// checked. Seen failing with the second check removed (the mark channel answered first):
// renewed on a lost link.
func TestARelayedSourceNeverEndsRenewedForALoss(t *testing.T) {
	if end := watchedEnd(t, true, false); end.Reason != StopDeviceGone {
		t.Errorf("a lost link ended the source with %+v; want deviceGone", end)
	}
	for i := range 200 {
		if end := watchedEnd(t, true, true); end.Reason != StopDeviceGone {
			t.Fatalf("run %d: a lost link whose mark channel was closed too ended the source with %+v; want deviceGone", i, end)
		}
	}
}
