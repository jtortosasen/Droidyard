package sdk

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// ownerLicenceHash is the line in the owner's licenses/android-sdk-license (the spike's
// §(a)): sdklib's hash of Google's current android-sdk-license text.
const ownerLicenceHash = "24333f8a63b6825ea9c5514f83c2829b004d1fee"

// TestLiveRepository (level 4, opt-in: DROIDYARD_SDK_TESTS=1, network) reads Google's
// repository as the daemon does and checks what every later step rests on: this host's
// cmdline-tools and platform-tools archives, android-sdk-license hashing to the line the
// owner's file holds (so licenceHash is sdklib's against the live text), and nothing
// dropped (every live id and type-details fits the patterns). A changed licence text
// fails it, as it should: that is the register row on licence texts that change.
func TestLiveRepository(t *testing.T) {
	if os.Getenv("DROIDYARD_SDK_TESTS") != "1" {
		t.Skip("level 4: set DROIDYARD_SDK_TESTS=1 to read Google's repository over the network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	snap := NewRepository(RepositoryOptions{}).Load(ctx, true)
	if snap.Model == nil {
		t.Fatalf("the live repository did not load: %s %s", snap.State, snap.Cause)
	}
	m := snap.Model
	t.Logf("%d packages, %d licences, warnings %v", len(m.Packages), len(m.Licences), m.Warnings)

	for _, id := range []string{"cmdline-tools;latest", "platform-tools"} {
		p, ok := m.Package(id)
		if !ok {
			t.Errorf("%s is not available for this host", id)
			continue
		}
		if !strings.HasPrefix(p.Archive.URL, DefaultRepositoryBase) || p.Archive.Size <= 0 || p.Archive.ChecksumType != "sha1" || len(p.Archive.Checksum) != 40 {
			t.Errorf("%s archive = %+v", id, p.Archive)
		}
		t.Logf("%s %s: %s (%d bytes)", id, p.Revision, p.Archive.URL, p.Archive.Size)
	}

	pt, _ := m.Package("platform-tools")
	if pt != nil && (pt.License.ID != "android-sdk-license" || pt.License.SHA1 != ownerLicenceHash) {
		t.Errorf("platform-tools licence = %+v, want android-sdk-license %s, the line in the owner's file", pt.License, ownerLicenceHash)
	}
	if _, ok := m.Licences[LicenceRef{ID: "android-sdk-license", SHA1: ownerLicenceHash}]; !ok {
		t.Errorf("no android-sdk-license text hashes to %s; the repository's text changed or licenceHash is not sdklib's", ownerLicenceHash)
	}
	if warningsContain(m.Warnings, "dropped") || len(m.Warnings) != 0 {
		t.Errorf("warnings = %v, want none: every live id, archive URL and type-details should fit", m.Warnings)
	}
}
