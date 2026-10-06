package build

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/store"
)

// installHistory is a build registry over a temp state root, the store and artifact
// store a resolved output needs. No sidecar: these tests hand the records in.
func installHistory(t *testing.T) *Registry {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	arts, err := artifact.New(artifact.Options{Epoch: "e1", StateRoot: st.Root()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(arts.Close)
	return NewRegistry(RegistryOptions{Store: st, Artifacts: arts, Log: testLog{t}})
}

// apkOnDisk writes an APK and returns its path, size and mtime, the facts the record's
// output-path index stores.
func apkOnDisk(t *testing.T, name string, body []byte) (string, int64, time.Time) {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, fi.Size(), fi.ModTime()
}

// storeBuild persists a finished build whose one output names apk, with the
// output-path index entry made from path, size and mod (filters as given).
func storeBuild(t *testing.T, h *Registry, buildID, module, variant, appID string, version *int, apk, path string, size int64, mod time.Time, filters []string, finished time.Time) {
	t.Helper()
	rec := record{
		BuildID:    buildID,
		Outcome:    "success",
		FinishedAt: finished,
		Outputs: []Output{{
			Module: module, Variant: variant, ApplicationID: appID, VersionCode: version,
			APKs: []OutputAPK{{Name: apk, Filters: filters, SizeBytes: size, Supported: len(filters) == 0}},
		}},
		OutputPaths: []OutputPath{{
			Module: module, Variant: variant, APK: apk, Path: path, Filters: filters,
			SizeBytes: size, ModTime: mod,
		}},
	}
	if err := h.persist(testWS, rec); err != nil {
		t.Fatal(err)
	}
}

// Decision (plan m1g step 8, "Install reference"): with no buildId the resolver takes
// the newest finished build with an output for module and variant; a buildId names one;
// the result carries the record's applicationId, versionCode and APK name, and its path
// stays inside the core. Seen failing with the newest-first walk reversed.
func TestResolveBuildPicksTheNewestMatchingOutput(t *testing.T) {
	h := installHistory(t)
	oldPath, oldSize, oldMod := apkOnDisk(t, "app-old.apk", []byte("old"))
	newPath, newSize, newMod := apkOnDisk(t, "app-new.apk", []byte("new"))
	older := time.Now().Add(-time.Hour)
	storeBuild(t, h, "op-00000000000000e1", ":app", "debug", "com.example.app", intPtr(1), "app-old.apk", oldPath, oldSize, oldMod, nil, older)
	storeBuild(t, h, "op-00000000000000e2", ":app", "debug", "com.example.app", intPtr(7), "app-new.apk", newPath, newSize, newMod, nil, time.Now())

	got, err := h.ResolveBuild(testWS, "", ":app", "debug")
	if err != nil {
		t.Fatalf("ResolveBuild: %v", err)
	}
	if got.BuildID != "op-00000000000000e2" || got.APK != "app-new.apk" || got.Path != newPath {
		t.Errorf("ResolveBuild = %+v, want the newest build's APK", got)
	}
	if got.ApplicationID != "com.example.app" || got.VersionCode == nil || *got.VersionCode != 7 {
		t.Errorf("applicationId/versionCode = %q/%v, want com.example.app/7", got.ApplicationID, got.VersionCode)
	}

	old, err := h.ResolveBuild(testWS, "op-00000000000000e1", ":app", "debug")
	if err != nil {
		t.Fatalf("ResolveBuild(build-old): %v", err)
	}
	if old.BuildID != "op-00000000000000e1" || old.APK != "app-old.apk" {
		t.Errorf("ResolveBuild(build-old) = %+v, want the named build", old)
	}
}

// Decision (plan m1g step 8): the newest build is chosen only if it has a matching
// output; a build of another variant is skipped for an older one that has it.
func TestResolveBuildSkipsBuildsWithoutTheVariant(t *testing.T) {
	h := installHistory(t)
	path, size, mod := apkOnDisk(t, "app-debug.apk", []byte("x"))
	storeBuild(t, h, "op-00000000000000f1", ":app", "release", "com.example.app", intPtr(1), "app-release.apk", path, size, mod, nil, time.Now().Add(-time.Hour))
	storeBuild(t, h, "op-00000000000000d1", ":app", "debug", "com.example.app", intPtr(2), "app-debug.apk", path, size, mod, nil, time.Now().Add(-2*time.Hour))

	got, err := h.ResolveBuild(testWS, "", ":app", "debug")
	if err != nil {
		t.Fatalf("ResolveBuild: %v", err)
	}
	if got.BuildID != "op-00000000000000d1" {
		t.Errorf("ResolveBuild = build %s, want build-debug", got.BuildID)
	}
}

// Decision (plan m1g step 8, check): no build, no output for the variant, and an
// unknown buildId each have their own code.
func TestResolveBuildNotFound(t *testing.T) {
	h := installHistory(t)
	if _, err := h.ResolveBuild(testWS, "", ":app", "debug"); !hasCode(err, ErrBuildOutputNotFound) {
		t.Errorf("no build = %v, want BUILD_OUTPUT_NOT_FOUND", err)
	}
	path, size, mod := apkOnDisk(t, "app.apk", []byte("x"))
	storeBuild(t, h, "op-0000000000000001", ":app", "release", "com.example.app", nil, "app.apk", path, size, mod, nil, time.Now())
	if _, err := h.ResolveBuild(testWS, "", ":app", "debug"); !hasCode(err, ErrBuildOutputNotFound) {
		t.Errorf("wrong variant = %v, want BUILD_OUTPUT_NOT_FOUND", err)
	}
	if _, err := h.ResolveBuild(testWS, "nope", ":app", "release"); !hasCode(err, capability.ErrBuildNotFound) {
		t.Errorf("unknown buildId = %v, want BUILD_NOT_FOUND", err)
	}
}

// Decision (plan m1g step 8, check): an APK rewritten after the build, and one removed,
// are BUILD_OUTPUT_STALE — nothing is installed. Seen failing with the re-check removed.
func TestResolveBuildStale(t *testing.T) {
	h := installHistory(t)
	path, size, mod := apkOnDisk(t, "app.apk", []byte("original"))
	storeBuild(t, h, "op-0000000000000001", ":app", "debug", "com.example.app", nil, "app.apk", path, size, mod, nil, time.Now())

	// Rewritten, so its size and mtime differ from the record's.
	if err := os.WriteFile(path, []byte("a longer replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ResolveBuild(testWS, "", ":app", "debug"); !hasCode(err, ErrBuildOutputStale) {
		t.Errorf("rewritten APK = %v, want BUILD_OUTPUT_STALE", err)
	}

	// Gone.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ResolveBuild(testWS, "", ":app", "debug"); !hasCode(err, ErrBuildOutputStale) {
		t.Errorf("removed APK = %v, want BUILD_OUTPUT_STALE", err)
	}
}

// Decision (plan m1g step 8, check; gradle.md §Build outputs "MVP scope"): an output
// that is not exactly one unfiltered APK is BUILD_OUTPUT_UNSUPPORTED.
func TestResolveBuildUnsupported(t *testing.T) {
	h := installHistory(t)
	path, size, mod := apkOnDisk(t, "app-arm64.apk", []byte("x"))
	storeBuild(t, h, "op-0000000000000001", ":app", "debug", "com.example.app", nil, "app-arm64.apk", path, size, mod, []string{"arm64-v8a"}, time.Now())
	if _, err := h.ResolveBuild(testWS, "", ":app", "debug"); !hasCode(err, ErrBuildOutputUnsupported) {
		t.Errorf("filtered output = %v, want BUILD_OUTPUT_UNSUPPORTED", err)
	}
}

// Decision (gradle.md §Build outputs: missing is reported, not guessed): an output
// without an applicationId is not one, so it resolves nothing.
func TestResolveBuildWithoutApplicationID(t *testing.T) {
	h := installHistory(t)
	rec := record{BuildID: "op-0000000000000001", Outcome: "success", FinishedAt: time.Now(),
		Outputs:     []Output{{Module: ":app", Variant: "debug", Reason: "the output metadata names no applicationId"}},
		OutputPaths: []OutputPath{{Module: ":app", Variant: "debug", APK: "app.apk", Path: "/nowhere"}}}
	if err := h.persist(testWS, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ResolveBuild(testWS, "", ":app", "debug"); !hasCode(err, ErrBuildOutputNotFound) {
		t.Errorf("no applicationId = %v, want BUILD_OUTPUT_NOT_FOUND", err)
	}
}

// Decision (plan m1g step 8): a descriptor-only registry (no stores) resolves nothing
// rather than panicking.
func TestResolveBuildWithoutState(t *testing.T) {
	h := NewRegistry(RegistryOptions{})
	if _, err := h.ResolveBuild(testWS, "", ":app", "debug"); !hasCode(err, ErrBuildOutputNotFound) {
		t.Errorf("no state = %v, want BUILD_OUTPUT_NOT_FOUND", err)
	}
}

func hasCode(err error, code capability.ErrorCode) bool {
	var e *capability.Error
	return errors.As(err, &e) && e.Code == code
}

func intPtr(n int) *int { return &n }

// Decision (step 8 review B1): a buildId is an operation id; one holding a path names
// no build, so an install cannot reach another workspace's record. Seen failing before
// load checked the id: it resolved the other workspace's APK.
func TestResolveBuildRefusesAPathInTheBuildID(t *testing.T) {
	h := installHistory(t)
	path, size, mod := apkOnDisk(t, "app.apk", []byte("x"))
	storeBuild(t, h, "op-00000000000000aa", ":app", "debug", "com.other.app", nil, "app.apk", path, size, mod, nil, time.Now())
	if _, err := h.ResolveBuild("zz-ffffffffffff", "../../"+testWS+"/builds/op-00000000000000aa", ":app", "debug"); !hasCode(err, capability.ErrBuildNotFound) {
		t.Fatalf("a path in buildId = %v, want BUILD_NOT_FOUND", err)
	}
}

// Decision (step 8 review N2): the stale check compares the mtime as well as the size,
// so an APK rewritten with the same size is stale too.
func TestResolveBuildStaleOnASameSizeRewrite(t *testing.T) {
	h := installHistory(t)
	path, size, mod := apkOnDisk(t, "app.apk", []byte("original"))
	storeBuild(t, h, "op-0000000000000001", ":app", "debug", "com.example.app", nil, "app.apk", path, size, mod, nil, time.Now())
	if err := os.WriteFile(path, []byte("replaced"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod.Add(time.Minute), mod.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ResolveBuild(testWS, "", ":app", "debug"); !hasCode(err, ErrBuildOutputStale) {
		t.Errorf("same-size rewrite = %v, want BUILD_OUTPUT_STALE", err)
	}
}
