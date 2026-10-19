package hosting

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// arriving starts a Put of the peer that stays in flight until the returned function ends
// it, and waits for its file to exist.
func arriving(t *testing.T, u *Uploads, dir, peer string) (end func() error) {
	t.Helper()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, _, err := u.Put(peer, 100, pr)
		done <- err
	}()
	pw.Write([]byte("half"))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		u.mu.Lock()
		on := u.arrives[peer]
		u.mu.Unlock()
		if on && len(files(t, dir)) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return func() error {
		pw.CloseWithError(errors.New("the sender is gone"))
		return <-done
	}
}

// Decision (plan m2t step 4, verifier B1): a peer has one slot, pending or arriving, so
// concurrent uploads of one peer cannot fill the host's disk. While an upload of the peer is
// arriving, a second is refused with ErrUploadBusy and writes nothing; its earlier pending
// upload is gone as the new one starts, so the peer never has two files here; another peer's
// upload is unaffected. Seen failing without the in-flight check (two files at once).
func TestAPeerHasOneSlot(t *testing.T) {
	u, dir := newUploads(t)
	put(t, u, "anna", []byte("pending from before"))
	if len(files(t, dir)) != 1 {
		t.Fatalf("setup: %v", files(t, dir))
	}
	end := arriving(t, u, dir, "anna")
	if got := files(t, dir); len(got) != 1 {
		t.Errorf("anna's new upload is arriving and her earlier one is still here: %v; want one file", got)
	}
	for range 3 {
		if _, _, err := u.Put("anna", 5, strings.NewReader("12345")); !errors.Is(err, ErrUploadBusy) {
			t.Errorf("a second upload while one is arriving: %v; want ErrUploadBusy", err)
		}
	}
	if got := files(t, dir); len(got) != 1 {
		t.Errorf("refused uploads wrote: %v; want only the arriving file", got)
	}
	put(t, u, "bob", []byte("bob's"))
	if got := files(t, dir); len(got) != 2 {
		t.Errorf("bob's upload beside anna's arriving one: %v; want two files", got)
	}
	if err := end(); err == nil {
		t.Fatal("the cut-off upload succeeded")
	}
	// The slot is free again once the upload ends.
	put(t, u, "anna", []byte("again"))
}

// Decision (verifier nit): an id that is not pending says why when the caller who named it
// asks: replaced by a newer upload, expired, or removed when the peer was unpaired; one never
// issued, or the peer's other's, says nothing. The expiry runs on the package's TTL.
func TestGoneSaysWhy(t *testing.T) {
	old := uploadTTL
	uploadTTL = 60 * time.Millisecond
	t.Cleanup(func() { uploadTTL = old })
	u, _ := newUploads(t)

	first := put(t, u, "anna", []byte("1"))
	second := put(t, u, "anna", []byte("2"))
	if why := u.Gone("anna", first); !strings.Contains(why, "replaced") {
		t.Errorf("Gone(replaced) = %q", why)
	}
	deadline := time.Now().Add(5 * time.Second)
	for u.Gone("anna", second) == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if why := u.Gone("anna", second); !strings.Contains(why, "expired") {
		t.Errorf("Gone(expired) = %q", why)
	}
	if why := u.Gone("bob", second); why != "" {
		t.Errorf("another peer is told why: %q", why)
	}

	uploadTTL = old
	third := put(t, u, "anna", []byte("3"))
	u.Forget("anna")
	if _, ok := u.Take("anna", third); ok {
		t.Error("an upload survived its peer being unpaired")
	}
	if why := u.Gone("anna", third); !strings.Contains(why, "unpaired") {
		t.Errorf("Gone(forgotten) = %q", why)
	}
}
