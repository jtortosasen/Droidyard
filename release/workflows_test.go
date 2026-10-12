package release

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// files maps a workflow's name to its text. Comments are not stripped: a rule is checked
// against what the file says, and a "# v7.0.1" after a pinned SHA is not a `uses:` value.
type files map[string]string

func workflows(t *testing.T) files {
	t.Helper()
	paths, err := filepath.Glob("../.github/workflows/*.yml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no workflow files found (glob error %v)", err)
	}
	out := files{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = string(b)
	}
	return out
}

var (
	usesRe   = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(\S+)`)
	runsOnRe = regexp.MustCompile(`^\s*runs-on:\s*(\S+)`)
	pinnedRe = regexp.MustCompile(`^[\w.-]+/[\w./-]+@[0-9a-f]{40}$`)
)

func lines(text string) []string { return strings.Split(text, "\n") }

func unpinned(fs files) (bad []string) {
	for name, text := range fs {
		for _, l := range lines(text) {
			if m := usesRe.FindStringSubmatch(l); m != nil {
				if !strings.HasPrefix(m[1], "./") && !pinnedRe.MatchString(m[1]) {
					bad = append(bad, name+": "+strings.TrimSpace(l))
				}
			}
		}
	}
	return bad
}

func nonLinux(fs files) (bad []string) {
	for name, text := range fs {
		for _, l := range lines(text) {
			if m := runsOnRe.FindStringSubmatch(l); m != nil && !strings.HasPrefix(m[1], "ubuntu-") {
				bad = append(bad, name+": "+strings.TrimSpace(l))
			}
		}
	}
	return bad
}

// The six commands .githooks/pre-commit runs over the tree (ADR-0068 §3).
var hookChecks = []string{
	"pnpm -C web install --frozen-lockfile",
	"pnpm -C web run verify",
	"go build ./...",
	`test -z "$(gofmt -l .)"`,
	"go vet ./...",
	"go test ./...",
}

func missingChecks(ci string) (missing []string) {
	have := map[string]bool{}
	for _, l := range lines(ci) {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(strings.TrimPrefix(l, "- "), "run: ")
		have[l] = true
	}
	for _, c := range hookChecks {
		if !have[c] {
			missing = append(missing, c)
		}
	}
	return missing
}

// checkoutsKeepingCredentials lists each actions/checkout step whose own block lacks
// `persist-credentials: false` (the block ends at the next step).
func checkoutsKeepingCredentials(fs files) (bad []string) {
	for name, text := range fs {
		ls := lines(text)
		for i, l := range ls {
			m := usesRe.FindStringSubmatch(l)
			if m == nil || !strings.HasPrefix(m[1], "actions/checkout@") {
				continue
			}
			indent := len(l) - len(strings.TrimLeft(l, " "))
			ok := false
			for _, n := range ls[i+1:] {
				ni := len(n) - len(strings.TrimLeft(n, " "))
				if strings.HasPrefix(strings.TrimSpace(n), "- ") && ni <= indent {
					break
				}
				if strings.TrimSpace(n) == "persist-credentials: false" {
					ok = true
				}
			}
			if !ok {
				bad = append(bad, name+": line "+strings.TrimSpace(l))
			}
		}
	}
	return bad
}

func writes(ci string) (bad []string) {
	for _, l := range lines(ci) {
		if strings.Contains(l, ": write") {
			bad = append(bad, strings.TrimSpace(l))
		}
	}
	return bad
}

func ci(t *testing.T) string {
	t.Helper()
	text, ok := workflows(t)["ci.yml"]
	if !ok {
		t.Fatal("ci.yml is missing")
	}
	return text
}

// Each test below checks the real files, then proves the check by running it on a
// mutated copy of ci.yml that must fail (a rule that cannot fail is not a rule).

// TestActionsPinnedBySHA: a tag can be moved, a SHA cannot (ADR-0068 §3).
func TestActionsPinnedBySHA(t *testing.T) {
	if bad := unpinned(workflows(t)); len(bad) > 0 {
		t.Errorf("actions not pinned by a 40-hex SHA: %v", bad)
	}
	m := strings.Replace(ci(t), "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e", "actions/setup-go@v7", 1)
	if len(unpinned(files{"ci.yml": m})) == 0 {
		t.Error("a tag-pinned uses: passes")
	}
}

// TestLinuxRunnersOnly: CI is Linux only; no macOS job (ADR-0068 §Consequences).
func TestLinuxRunnersOnly(t *testing.T) {
	if bad := nonLinux(workflows(t)); len(bad) > 0 {
		t.Errorf("runners that are not ubuntu-*: %v", bad)
	}
	m := strings.Replace(ci(t), "ubuntu-24.04", "macos-latest", 1)
	if len(nonLinux(files{"ci.yml": m})) == 0 {
		t.Error("a macos-latest runner passes")
	}
}

// TestCIRunsTheHookChecks: CI runs what the hook runs, over the whole tree (ADR-0068 §3).
func TestCIRunsTheHookChecks(t *testing.T) {
	if m := missingChecks(ci(t)); len(m) > 0 {
		t.Errorf("ci.yml does not run: %v", m)
	}
	m := strings.Replace(ci(t), "go vet ./...", "echo skipped", 1)
	if len(missingChecks(m)) == 0 {
		t.Error("a ci.yml without go vet passes")
	}
}

// TestCheckoutDropsCredentials: a checkout must not leave the token in .git/config.
func TestCheckoutDropsCredentials(t *testing.T) {
	if bad := checkoutsKeepingCredentials(workflows(t)); len(bad) > 0 {
		t.Errorf("checkout keeps credentials: %v", bad)
	}
	m := strings.Replace(ci(t), "persist-credentials: false", "fetch-depth: 1", 1)
	if len(checkoutsKeepingCredentials(files{"ci.yml": m})) == 0 {
		t.Error("a checkout without persist-credentials: false passes")
	}
}

// TestNoWriteInCI: ci.yml runs on pull requests, including from forks; it reads only.
func TestNoWriteInCI(t *testing.T) {
	if bad := writes(ci(t)); len(bad) > 0 {
		t.Errorf("ci.yml grants write: %v", bad)
	}
	m := strings.Replace(ci(t), "contents: read", "contents: write", 1)
	if len(writes(m)) == 0 {
		t.Error("contents: write passes")
	}
}
