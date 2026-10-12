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

// writeRe matches a write grant however YAML spells it: `contents: write`,
// `contents:  write`, `contents: 'write'`, `{contents: write}`, `permissions: write-all`.
var writeRe = regexp.MustCompile(`:\s*['"]?write\b`)

func writes(ci string) (bad []string) {
	for _, l := range lines(ci) {
		if writeRe.MatchString(l) {
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
	// Other spellings of the same grant (the rule is the grant, not one way to write it).
	for _, grant := range []string{
		"permissions:\n  contents: 'write'",
		"permissions:\n  contents: \"write\"",
		"permissions:\n  contents:  write",
		"permissions: {contents: write}",
		"permissions: write-all",
	} {
		m := strings.Replace(ci(t), "permissions:\n  contents: read", grant, 1)
		if len(writes(m)) == 0 {
			t.Errorf("the grant %q passes", grant)
		}
	}
}

// indent is the number of leading spaces of a line.
func indent(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

// code drops blank and comment-only lines, so a rule reads what the workflow does and
// not what a comment says.
func code(ls []string) (out []string) {
	for _, l := range ls {
		if s := strings.TrimSpace(l); s != "" && !strings.HasPrefix(s, "#") {
			out = append(out, l)
		}
	}
	return out
}

// jobKeyRe takes a job key with or without a trailing comment (`  publish: # why`).
var jobKeyRe = regexp.MustCompile(`^  ([\w-]+):\s*(#.*)?$`)

// jobs returns each job's lines (its key line included), by name.
func jobs(text string) map[string][]string {
	out := map[string][]string{}
	in, cur := false, ""
	for _, l := range code(lines(text)) {
		if indent(l) == 0 {
			in = strings.TrimSpace(l) == "jobs:"
			cur = ""
			continue
		}
		if !in {
			continue
		}
		if m := jobKeyRe.FindStringSubmatch(l); m != nil {
			cur = m[1]
		}
		if cur != "" {
			out[cur] = append(out[cur], l)
		}
	}
	return out
}

func releaseYML(t *testing.T) string {
	t.Helper()
	text, ok := workflows(t)["release.yml"]
	if !ok {
		t.Fatal("release.yml is missing")
	}
	return text
}

// mutate replaces the first old by new and fails the test when old is not there, so a
// mutation that silently did nothing cannot make a "must fail" check pass for the wrong reason.
func mutate(t *testing.T, text, old, new string) string {
	t.Helper()
	if !strings.Contains(text, old) {
		t.Fatalf("mutation target %q is not in the workflow", old)
	}
	return strings.Replace(text, old, new, 1)
}

// mutateIn is mutate restricted to what follows the job's key line.
func mutateIn(t *testing.T, text, job, old, new string) string {
	t.Helper()
	i := strings.Index(text, "\n  "+job+":")
	if i < 0 {
		t.Fatalf("job %q is not in the workflow", job)
	}
	return text[:i] + mutate(t, text[i:], old, new)
}

// ancestors lists the trimmed lines that enclose line i: the nearest earlier line of
// each smaller indent, innermost first.
func ancestors(ls []string, i int) (out []string) {
	at := indent(ls[i])
	for j := i - 1; j >= 0 && at > 0; j-- {
		if n := indent(ls[j]); n < at {
			out = append(out, strings.TrimSpace(ls[j]))
			at = n
		}
	}
	return out
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// stepOf returns the lines of the step that encloses line i (from its `- ` line to the
// next step).
func stepOf(ls []string, i int) []string {
	start := -1
	for j := i; j >= 0; j-- {
		if strings.HasPrefix(strings.TrimSpace(ls[j]), "- ") && indent(ls[j]) < indent(ls[i]) {
			start = j
			break
		}
	}
	if start < 0 {
		return nil
	}
	end := len(ls)
	for j := start + 1; j < len(ls); j++ {
		if indent(ls[j]) <= indent(ls[start]) {
			end = j
			break
		}
	}
	return ls[start:end]
}

// writeScopes reports where each write grant sits: "<file> job <name>", or
// "<file> (workflow level)" when it is above every job.
func writeScopes(fs files) (scopes []string) {
	for name, text := range fs {
		for _, l := range code(lines(text)) {
			if indent(l) == 0 && strings.TrimSpace(l) == "jobs:" {
				break
			}
			if writeRe.MatchString(l) {
				scopes = append(scopes, name+" (workflow level)")
			}
		}
		for job, ls := range jobs(text) {
			for _, l := range ls {
				if writeRe.MatchString(l) {
					scopes = append(scopes, name+" job "+job)
				}
			}
		}
	}
	return scopes
}

var downloadRe = regexp.MustCompile(`^actions/download-artifact@[0-9a-f]{40}$`)

// publishLeaks lists what the publish job may not contain. Its steps are an allowlist:
// the pinned actions/download-artifact, and run: steps; any other `uses:` (a checkout, a
// setup action, a local action, a docker image) is a finding, and so is a container or
// services. What a run: step executes cannot be policed by reading text (a
// `curl ... | sh` is a run: step like any other); the review of release.yml is what
// keeps that block to the sha256sum and gh lines.
func publishLeaks(text string) (bad []string) {
	pub, ok := jobs(text)["publish"]
	if !ok {
		return []string{"no publish job"}
	}
	for _, l := range pub {
		s := strings.TrimSpace(l)
		if m := usesRe.FindStringSubmatch(l); m != nil && !downloadRe.MatchString(m[1]) {
			bad = append(bad, s)
		}
		if strings.HasPrefix(s, "container:") || strings.HasPrefix(s, "services:") {
			bad = append(bad, s)
		}
	}
	return bad
}

// TestOnlyPublishWrites: the one write grant in the workflows, in any spelling, is
// `publish`'s, and that job takes no action but download-artifact (ADR-0068 §3).
func TestOnlyPublishWrites(t *testing.T) {
	want := []string{"release.yml job publish"}
	if got := writeScopes(workflows(t)); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("write permissions at %v, want only %v", got, want)
	}
	if bad := publishLeaks(releaseYML(t)); len(bad) > 0 {
		t.Errorf("publish runs more than download-artifact and run: steps: %v", bad)
	}
	r := releaseYML(t)

	// Each grant is the whole permissions block, as YAML lets it be written.
	for _, grant := range []string{
		"contents: write", "contents: 'write'", "contents:  write", "{contents: write}", "write-all",
	} {
		block := func(pad string) string {
			if grant == "write-all" {
				return pad + "permissions: write-all"
			}
			if strings.HasPrefix(grant, "{") {
				return pad + "permissions: " + grant
			}
			return pad + "permissions:\n" + pad + "  " + grant
		}
		top := mutate(t, r, "permissions:\n  contents: read", block(""))
		if len(writeScopes(files{"release.yml": top})) < 2 {
			t.Errorf("%q at the workflow level passes", grant)
		}
		build := mutateIn(t, r, "build", "    permissions:\n      contents: read", block("    "))
		if len(writeScopes(files{"release.yml": build})) < 2 {
			t.Errorf("%q on the build job passes", grant)
		}
	}

	for _, extra := range []string{
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1",
		"actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1 # v7.1.0",
		"./.github/actions/anything",
		"docker://alpine",
		"actions/download-artifact@v8",
	} {
		pub := mutateIn(t, r, "publish", "    steps:\n", "    steps:\n      - uses: "+extra+"\n")
		if len(publishLeaks(pub)) == 0 {
			t.Errorf("uses: %s in publish passes", extra)
		}
	}

	// jobs() must still find a job whose key carries a comment, or a rule would
	// silently stop looking at it.
	commented := mutate(t, r, "\n  publish:\n", "\n  publish: # the only writer\n")
	if got := writeScopes(files{"release.yml": commented}); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("a commented job key hides the job: write permissions at %v", got)
	}
	if len(publishLeaks(mutateIn(t, commented, "publish", "    steps:\n", "    steps:\n      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1\n"))) == 0 {
		t.Error("a checkout in a job with a commented key passes")
	}
}

var (
	tokenRe    = regexp.MustCompile(`^\s*(GH_TOKEN|GITHUB_TOKEN)\s*:`)
	tokenValRe = regexp.MustCompile(`github\.token|secrets\.GITHUB_TOKEN`)
	inheritRe  = regexp.MustCompile(`^\s*secrets:\s*['"]?inherit\b`)
)

// tokenSites lists every line that sets a token variable or passes the job token by
// value (an env, or a with: such as `github-token:`), and those that are not an env of
// the step that runs `gh release create`. `secrets: inherit` is always a finding.
func tokenSites(fs files) (all, bad []string) {
	for name, text := range fs {
		ls := code(lines(text))
		for i, l := range ls {
			site := name + ": " + strings.TrimSpace(l)
			if inheritRe.MatchString(l) {
				bad = append(bad, site)
			}
			if !tokenRe.MatchString(l) && !tokenValRe.MatchString(l) {
				continue
			}
			all = append(all, site)
			anc := ancestors(ls, i)
			if len(anc) == 0 || anc[0] != "env:" || !has(anc, "steps:") ||
				!strings.Contains(strings.Join(stepOf(ls, i), "\n"), "gh release create") {
				bad = append(bad, site)
			}
		}
	}
	return all, bad
}

// TestTokenOnOneStep: the job token is named once in the workflows, in the env of the one
// step that runs `gh release create` (ADR-0068 §3). It is not on a job, on the workflow,
// on another step, passed to an action's input, or inherited by a called workflow. This
// proves what the file says; what a step receives by default is the action's: today
// download-artifact's github-token input has no default in its action.yml (observed at the
// pinned SHA, 2026-10-12), so it gets no token.
func TestTokenOnOneStep(t *testing.T) {
	all, bad := tokenSites(workflows(t))
	if len(all) != 1 {
		t.Errorf("token named %d times, want once: %v", len(all), all)
	}
	if len(bad) > 0 {
		t.Errorf("token or inherited secrets outside the gh release create step's env: %v", bad)
	}
	r := releaseYML(t)
	for name, m := range map[string]string{
		"on the publish job": mutateIn(t, r, "publish", "    env:\n", "    env:\n      GH_TOKEN: ${{ github.token }}\n"),
		"on the workflow":    mutate(t, r, "permissions:\n  contents: read", "env:\n  GH_TOKEN: ${{ github.token }}\n\npermissions:\n  contents: read"),
		"on a second step": mutate(t, r, "      - name: Verify checksums\n",
			"      - name: Verify checksums\n        env:\n          GH_TOKEN: ${{ github.token }}\n"),
		"as the download action's github-token": mutate(t, r, "          name: release\n          path: release\n",
			"          name: release\n          path: release\n          github-token: ${{ github.token }}\n"),
		"as secrets.GITHUB_TOKEN in a with:": mutateIn(t, r, "build", "          if-no-files-found: error\n",
			"          if-no-files-found: error\n          github-token: ${{ secrets.GITHUB_TOKEN }}\n"),
		"inherited by the called workflow": mutate(t, r, "    uses: ./.github/workflows/ci.yml\n", "    uses: ./.github/workflows/ci.yml\n    secrets: inherit\n"),
		// Moved, not added: still once, but on a step that does not run gh release create.
		"moved to the checksum step": mutate(t, mutate(t, r, "        env:\n          GH_TOKEN: ${{ github.token }}\n", ""),
			"      - name: Verify checksums\n", "      - name: Verify checksums\n        env:\n          GH_TOKEN: ${{ github.token }}\n"),
	} {
		if _, bad := tokenSites(files{"release.yml": m}); len(bad) == 0 {
			t.Errorf("the token %s passes", name)
		}
	}
}

var (
	runRe = regexp.MustCompile(`^(\s*)(?:-\s+)?run:\s*(.*)$`)
	tagRe = regexp.MustCompile(`\$\{?TAG\b`)
)

// runBlocks returns, per job, the text of its run: values (inline or block scalar).
func runBlocks(text string) map[string]string {
	out := map[string]string{}
	for job, ls := range jobs(text) {
		for i, l := range ls {
			m := runRe.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			body := m[2]
			at := len(m[1])
			if strings.HasPrefix(strings.TrimLeft(l, " "), "- ") {
				at += 2
			}
			for _, n := range ls[i+1:] {
				if indent(n) <= at {
					break
				}
				body += "\n" + n
			}
			out[job] += body + "\n"
		}
	}
	return out
}

// jobDeclaresTag: the job has `TAG:` under an env: of its own.
func jobDeclaresTag(ls []string) bool {
	for i, l := range ls {
		if strings.HasPrefix(strings.TrimSpace(l), "TAG:") {
			if anc := ancestors(ls, i); len(anc) > 0 && anc[0] == "env:" {
				return true
			}
		}
	}
	return false
}

func runProblems(fs files) (bad []string) {
	for name, text := range fs {
		js := jobs(text)
		for job, body := range runBlocks(text) {
			if strings.Contains(body, "${{") {
				bad = append(bad, name+" job "+job+": an expression inside run:")
			}
			if tagRe.MatchString(body) && !jobDeclaresTag(js[job]) {
				bad = append(bad, name+" job "+job+": uses $TAG without its own env: TAG")
			}
		}
	}
	return bad
}

// TestNoExpressionInRun: a ${{ }} inside run: is spliced into the script before the
// shell reads it, so a crafted tag name becomes code; the value goes through env and
// the shell reads "$TAG" (ADR-0068 §3). A job's env does not reach another job.
func TestNoExpressionInRun(t *testing.T) {
	if bad := runProblems(workflows(t)); len(bad) > 0 {
		t.Errorf("%v", bad)
	}
	r := releaseYML(t)
	inline := mutate(t, r, `release/build.sh "$TAG"`, `release/build.sh "${{ github.ref_name }}"`)
	if len(runProblems(files{"release.yml": inline})) == 0 {
		t.Error("${{ github.ref_name }} inside run: passes")
	}
	block := mutate(t, r, `sha256sum -c checksums.txt`, `echo ${{ github.ref_name }}`)
	if len(runProblems(files{"release.yml": block})) == 0 {
		t.Error("an expression in a block run: passes")
	}
	noEnv := mutateIn(t, r, "publish", "    env:\n      TAG: ${{ github.ref_name }}\n", "")
	if len(runProblems(files{"release.yml": noEnv})) == 0 {
		t.Error("publish using $TAG without declaring it passes")
	}
}
