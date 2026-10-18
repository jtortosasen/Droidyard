package doccheck

import (
	"strings"
	"testing"
	"testing/fstest"
)

// These tests run each checker on a small synthetic repository: once as written,
// where it must find nothing, and once per seeded drift, where it must name the file
// and the offending value. They are what shows the checks against the real
// repository can fail at all.

type tree map[string]string

func (tr tree) fs() fstest.MapFS {
	m := fstest.MapFS{}
	for p, s := range tr {
		m[p] = &fstest.MapFile{Data: []byte(s)}
	}
	return m
}

// with returns a copy of the tree with one edit applied. The edit must apply, so a
// fixture change cannot silently turn a seeded drift into no drift.
func (tr tree) with(t *testing.T, file, old, new string) tree {
	t.Helper()
	out := tree{}
	for k, v := range tr {
		out[k] = v
	}
	if old == "" {
		out[file] = new
		return out
	}
	if !strings.Contains(out[file], old) {
		t.Fatalf("fixture %s does not contain %q", file, old)
	}
	out[file] = strings.Replace(out[file], old, new, 1)
	return out
}

func (tr tree) without(file string) tree {
	out := tree{}
	for k, v := range tr {
		if k != file {
			out[k] = v
		}
	}
	return out
}

func expectClean(t *testing.T, problems []string) {
	t.Helper()
	for _, p := range problems {
		t.Errorf("the well-formed fixture was reported: %s", p)
	}
}

// expect asserts that some problem contains every fragment: the file, the value, and
// whatever says what to change.
func expect(t *testing.T, problems []string, fragments ...string) {
	t.Helper()
	for _, p := range problems {
		all := true
		for _, f := range fragments {
			if !strings.Contains(p, f) {
				all = false
				break
			}
		}
		if all {
			return
		}
	}
	t.Errorf("no problem mentions all of %q; got:\n  %s", fragments, strings.Join(problems, "\n  "))
}

const fence = "```"

var capabilityFixture = tree{
	mvpPath: `# MVP

## The golden path

| # | Step | Capability |
| --- | --- | --- |
| 1 | Open | ` + "`workspace.open`" + ` |
| 2 | See | ` + "`device.list`, `device.logs`" + ` |

## Scope

### In scope

- **Capability domains**: ` + "`system`, `workspace`," + `
  ` + "`device`, `policy`" + `.

### Out of scope (explicit)

- Nothing.

## Phases

### Phase 1 — Agent loop

- ` + "`device.*`: `list`, `logs` (snapshot), and the `outputs[]` that `device.list` reads" + `.

## MVP capability surface

| Domain | Capabilities | Phase |
| --- | --- | --- |
| ` + "`system` | `health`" + ` | 0 |
| ` + "`workspace` | `open`" + ` | 0 |
| ` + "`device` | `list`, `logs`" + ` | 1 |
| ` + "`policy` | `get`" + ` (human-only) | 1 |
`,
	mcpPath: `# MCP

## What is exposed, and what is not

| Not exposed | Why |
| --- | --- |
| ` + "`policy.get`" + ` | human |
`,
}

// The reverse check holds a decision: a capability no agent can call (audience human,
// or not stateless) must be in the not-exposed table, or the document hides it.
func TestNotExposedTableNamesEveryHiddenCapability(t *testing.T) {
	expectClean(t, checkNotExposed(capabilityFixture.fs(), []string{"policy.get"}))
	expect(t, checkNotExposed(capabilityFixture.fs(), []string{"policy.get", "file.read"}),
		mcpPath, "`file.read`", "not-exposed table")
}

func TestCapabilityCheckCatchesDrift(t *testing.T) {
	published := []string{"system.health", "policy.get"}
	expectClean(t, checkCapabilityNames(capabilityFixture.fs(), published))

	t.Run("published but not in the roadmap", func(t *testing.T) {
		expect(t, checkCapabilityNames(capabilityFixture.fs(), []string{"system.health", "policy.get", "system.version"}),
			mvpPath, "`system.version`", "Add it to the `system` row")
	})
	t.Run("golden path names an unknown capability", func(t *testing.T) {
		tr := capabilityFixture.with(t, mvpPath, "`device.list`, `device.logs` |", "`device.list`, `device.logcat` |")
		expect(t, checkCapabilityNames(tr.fs(), published), mvpPath+":8", "golden path", "`device.logcat`")
	})
	t.Run("phase 1 deliverable in another phase", func(t *testing.T) {
		tr := capabilityFixture.with(t, mvpPath, "`device` | `list`, `logs` | 1 |", "`device` | `list`, `logs` | 2 |")
		expect(t, checkCapabilityNames(tr.fs(), published), mvpPath, "Phase 1", "`device.list`", `phase "2"`)
	})
	t.Run("in-scope domain list", func(t *testing.T) {
		tr := capabilityFixture.with(t, mvpPath, "`device`, `policy`", "`device`")
		expect(t, checkCapabilityNames(tr.fs(), published), mvpPath, "**Capability domains**", "`policy`")
	})
	t.Run("a stale exclusion", func(t *testing.T) {
		// The not-exposed list names a capability the registry no longer publishes.
		tr := capabilityFixture.with(t, mcpPath, "| `policy.get` | human |", "| `policy.get` | human |\n| `policy.gone` | human |")
		expect(t, checkCapabilityNames(tr.fs(), published), mcpPath, "`policy.gone`", "not exposed", "not published")
	})
	t.Run("renamed section", func(t *testing.T) {
		tr := capabilityFixture.with(t, mcpPath, "## What is exposed, and what is not", "## Exposure")
		expect(t, checkCapabilityNames(tr.fs(), published), mcpPath, `"What is exposed, and what is not"`, "capabilities_test.go")
	})
}

func adr(n, title, status, alternatives string) string {
	return "# ADR-" + n + ": " + title + `

- Status: ` + status + `
- Date: 2026-09-27
- Amended: 2026-09-29 — something changed.

## Context

Why.

## Decision

What.

## Consequences

So.

## Alternatives considered

` + alternatives + "\n"
}

var adrFixture = tree{
	adrIndex: `# ADRs

## Index

| ADR | Title | Status |
| --- | --- | --- |
| [0001](0001-first.md) | First | Accepted |
| [0002](0002-second.md) | Second | Superseded by ADR-0001 |
`,
	"docs/adr/0001-first.md":  strings.Replace(adr("0001", "First", "Accepted", "- **Other.** Fails because."), "- Amended:", "- Supersedes: ADR-0002\n- Amended:", 1),
	"docs/adr/0002-second.md": adr("0002", "Second", "Superseded by ADR-0001", "- **Other.** Fails because."),
}

func TestADRChecksCatchDrift(t *testing.T) {
	expectClean(t, checkADRIndex(adrFixture.fs()))
	expectClean(t, checkADRFormat(adrFixture.fs()))

	t.Run("a file with no row", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0003-third.md", "", adr("0003", "Third", "Accepted", "Other."))
		expect(t, checkADRIndex(tr.fs()), adrIndex, "0003-third.md", "has no row")
	})
	t.Run("a row with no file", func(t *testing.T) {
		tr := adrFixture.with(t, adrIndex, "| [0002](0002-second.md)", "| [0002](0002-secnd.md)")
		expect(t, checkADRIndex(tr.fs()), adrIndex+":8", "0002-secnd.md", "does not exist", "the file is 0002-second.md")
	})
	t.Run("a gap is allowed", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0004-fourth.md", "", adr("0004", "Fourth", "Accepted", "Other.")).
			with(t, adrIndex, "| Second | Superseded by ADR-0001 |", "| Second | Superseded by ADR-0001 |\n| [0004](0004-fourth.md) | Fourth | Accepted |")
		expectClean(t, checkADRIndex(tr.fs()))
	})
	t.Run("a duplicate number", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0002-again.md", "", adr("0002", "Again", "Accepted", "Other."))
		expect(t, checkADRIndex(tr.fs()), "0002", "used by 2 files", "0002-again.md")
	})
	t.Run("index status disagrees", func(t *testing.T) {
		tr := adrFixture.with(t, adrIndex, "| First | Accepted |", "| First | Proposed |")
		expect(t, checkADRIndex(tr.fs()), adrIndex+":7", `"Proposed"`, "Status: Accepted")
	})
	t.Run("no alternatives section", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0001-first.md", "## Alternatives considered", "## Options")
		expect(t, checkADRFormat(tr.fs()), "docs/adr/0001-first.md", `"## Alternatives considered"`)
	})
	t.Run("placeholder alternatives", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0001-first.md", "- **Other.** Fails because.", "TBD.")
		expect(t, checkADRFormat(tr.fs()), "docs/adr/0001-first.md:20", "Alternatives considered", "placeholder")
	})
	t.Run("unknown status", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0001-first.md", "- Status: Accepted", "- Status: Approved")
		expect(t, checkADRFormat(tr.fs()), "docs/adr/0001-first.md", `"Status: Approved"`)
	})
	t.Run("superseded by a missing ADR", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0002-second.md", "Superseded by ADR-0001", "Superseded by ADR-0009")
		expect(t, checkADRFormat(tr.fs()), "docs/adr/0002-second.md", "ADR-0009", "does not exist")
	})
	t.Run("title number", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0002-second.md", "# ADR-0002:", "# ADR-0003:")
		expect(t, checkADRFormat(tr.fs()), "docs/adr/0002-second.md:1", "ADR-0003", "0002")
	})
	t.Run("sections out of order", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0001-first.md", "## Decision\n\nWhat.\n\n## Consequences\n\nSo.", "## Consequences\n\nSo.\n\n## Decision\n\nWhat.")
		expect(t, checkADRFormat(tr.fs()), "docs/adr/0001-first.md", "comes before")
	})
	t.Run("undated amendment", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0001-first.md", "- Amended: 2026-09-29 —", "- Amended: last week —")
		expect(t, checkADRFormat(tr.fs()), "docs/adr/0001-first.md", "Amended: last week", "YYYY-MM-DD")
	})
}

var linkFixture = tree{
	"docs/adr/0001-first.md": adr("0001", "First", "Accepted", "- **Other.** Fails because."),
	"docs/a.md": `# A

## Scope and workspace addressing

1. **Environment is fixed.** Text.
2. Two.

See [b](b.md) and [the rule](b.md#naming-and-namespaces), [up](../README.md) and ` + "`[not a link](nowhere.md)`" + `.
[b](b.md) §Naming and §Error model, and ADR-0001 §Decision, and
[A](a.md) §2, and ` + "`docs/b.md`" + ` §Error model, per ADR-0001 §Alternatives.

` + fence + `
[fenced](nowhere.md) ADR-0009 §Nothing
` + fence + `
`,
	"docs/b.md": `# B

## Naming and namespaces

## Error model (details)
`,
	"README.md": "# Root\n",
	"internal/x/x.go": `package x

// Reversible (docs/b.md §Naming and namespaces), see ADR-0001 §Consequences.
`,
}

func TestLinkChecksCatchDrift(t *testing.T) {
	expectClean(t, checkLinks(linkFixture.fs()))
	expectClean(t, checkSectionRefs(linkFixture.fs()))
	expectClean(t, checkADRMentions(linkFixture.fs()))

	t.Run("a missing file", func(t *testing.T) {
		tr := linkFixture.with(t, "docs/a.md", "[b](b.md) and", "[b](../b.md) and")
		expect(t, checkLinks(tr.fs()), "docs/a.md:8", `"../b.md"`, "does not exist")
	})
	t.Run("a missing anchor", func(t *testing.T) {
		tr := linkFixture.with(t, "docs/a.md", "b.md#naming-and-namespaces", "b.md#naming")
		expect(t, checkLinks(tr.fs()), "docs/a.md:8", "#naming", "Naming and namespaces")
	})
	t.Run("a section that never existed", func(t *testing.T) {
		tr := linkFixture.with(t, "docs/a.md", "ADR-0001 §Decision", "ADR-0001 §Naming")
		expect(t, checkSectionRefs(tr.fs()), "docs/a.md:9", "ADR-0001 §Naming", "docs/adr/0001-first.md")
	})
	t.Run("the second of a chain", func(t *testing.T) {
		tr := linkFixture.with(t, "docs/a.md", "and §Error model,", "and §Errors,")
		expect(t, checkSectionRefs(tr.fs()), "docs/a.md:9", "§Errors", "docs/b.md")
	})
	t.Run("a new citation ends a chain", func(t *testing.T) {
		tr := linkFixture.with(t, "docs/a.md", "ADR-0001 §Decision, and", "ADR-0001 §Decision and `docs/b.md` §Naming and namespaces, and")
		expectClean(t, checkSectionRefs(tr.fs()))
		tr = linkFixture.with(t, "docs/a.md", "ADR-0001 §Decision, and", "ADR-0001 §Decision and `docs/b.md` §Nope, and")
		expect(t, checkSectionRefs(tr.fs()), "docs/a.md:9", "§Nope", "docs/b.md")
	})
	t.Run("a numbered clause", func(t *testing.T) {
		tr := linkFixture.with(t, "docs/a.md", "[A](a.md) §2", "[A](a.md) §3")
		expect(t, checkSectionRefs(tr.fs()), "docs/a.md:10", "§3")
	})
	t.Run("a backticked path", func(t *testing.T) {
		tr := linkFixture.with(t, "docs/a.md", "`docs/b.md` §Error model", "`docs/b.md` §Errors model")
		expect(t, checkSectionRefs(tr.fs()), "docs/a.md:10", "§Errors model")
	})
	t.Run("a Go comment", func(t *testing.T) {
		tr := linkFixture.with(t, "internal/x/x.go", "ADR-0001 §Consequences", "ADR-0001 §Discovery")
		expect(t, checkSectionRefs(tr.fs()), "internal/x/x.go:3", "§Discovery")
	})
	t.Run("an ADR that does not exist", func(t *testing.T) {
		tr := linkFixture.with(t, "internal/x/x.go", "see ADR-0001", "see ADR-0002")
		expect(t, checkADRMentions(tr.fs()), "internal/x/x.go:3", "ADR-0002 does not exist")
	})
	t.Run("reviews are records", func(t *testing.T) {
		tr := linkFixture.with(t, "docs/reviews/r.md", "", "[b](../b.md) §Nothing at all\n")
		expectClean(t, checkSectionRefs(tr.fs()))
	})
}

// Decision (plan m2a step 1): the vendored third-party skills are not read by any
// check. Their markdown cites documents that do not exist here, and a link or
// section reference in them is not ours to check; the same text in a skill of ours is.
func TestThirdPartyDirectoriesAreSkipped(t *testing.T) {
	text := "# Vendored\n\nSee [x](../nowhere.md) and `docs/missing.md` §Nothing.\n"
	tr := linkFixture.with(t, ".claude/skills/svelte-core-bestpractices/references/x.md", "", text).
		with(t, ".claude/skills/svelte-code-writer/SKILL.md", "", text)
	files, err := markdownFiles(tr.fs())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasPrefix(f, ".claude/skills/svelte-") {
			t.Errorf("markdownFiles lists vendored %s", f)
		}
	}
	expectClean(t, checkLinks(tr.fs()))
	expectClean(t, checkSectionRefs(tr.fs()))

	// The same text in a skill of ours is checked.
	own := linkFixture.with(t, ".claude/skills/own/SKILL.md", "", text)
	expect(t, checkLinks(own.fs()), ".claude/skills/own/SKILL.md", "does not exist")
}

const goodPlan = `# Plan: Milestone 1b — Daemon

- Status: Approved
- Date: 2026-09-29

## Acceptance

- [ ] A1. It runs. Source: ADR-0019. Proven by: step 2.
- [x] A2. It stops. Proven by: steps 1 and 2.

## Steps

### 1. Socket

- Builds: internal/daemon
- Implements: ADR-0019 §5
- Check: TestSocket
- Schema: no change
- Needs: nothing
- Not here: idle exit
- [x] Done. Evidence: go test ./internal/daemon -run TestSocket: ok

### 2. Shim

- Builds: cmd/droidyard
- Implements: ADR-0019 §2
- Check: TestShim
- Schema: no change
- Needs: step 1
- Not here: HTTP
- [ ] Done. Evidence: <command and decisive output>
`

var planFixture = tree{
	"docs/plans/m1b-daemon.md": goodPlan,
	plansIndex: `# Plans

## Index

| Plan | Milestone | Status |
| --- | --- | --- |
| [m1b-daemon.md](m1b-daemon.md) | 1b | Approved |
`,
}

func TestPlanChecksCatchDrift(t *testing.T) {
	expectClean(t, checkPlans(planFixture.fs()))
	expectClean(t, checkPlansIndex(planFixture.fs()))
	expectClean(t, checkPlans(planFixture.without("docs/plans/m1b-daemon.md").fs()))

	const p = "docs/plans/m1b-daemon.md"
	t.Run("a status the template does not allow", func(t *testing.T) {
		tr := planFixture.with(t, p, "- Status: Approved", "- Status: In progress")
		expect(t, checkPlans(tr.fs()), p, `"Status: In progress"`)
	})
	t.Run("a ticked step with no evidence", func(t *testing.T) {
		tr := planFixture.with(t, p, "- [ ] Done. Evidence: <command and decisive output>", "- [x] Done. Evidence: <command and decisive output>")
		expect(t, checkPlans(tr.fs()), p+":31", "step 2 is ticked", "Evidence is empty")
	})
	t.Run("a missing field", func(t *testing.T) {
		tr := planFixture.with(t, p, "- Not here: HTTP\n", "")
		expect(t, checkPlans(tr.fs()), p, "step 2", `"- Not here:"`)
	})
	t.Run("a missing Done line", func(t *testing.T) {
		tr := planFixture.with(t, p, "- [ ] Done. Evidence: <command and decisive output>\n", "")
		expect(t, checkPlans(tr.fs()), p, "step 2", "Done. Evidence")
	})
	t.Run("proven by a step that does not exist", func(t *testing.T) {
		tr := planFixture.with(t, p, "Proven by: step 2.", "Proven by: step 3.")
		expect(t, checkPlans(tr.fs()), p+":8", "step 3", "does not exist")
	})
	t.Run("an acceptance item with no step", func(t *testing.T) {
		tr := planFixture.with(t, p, " Proven by: step 2.", "")
		expect(t, checkPlans(tr.fs()), p+":8", "Proven by: step <n>")
	})
	t.Run("steps out of sequence", func(t *testing.T) {
		tr := planFixture.with(t, p, "### 2. Shim", "### 3. Shim")
		expect(t, checkPlans(tr.fs()), p, "says 3", "heading 2")
	})
	t.Run("a plan with no index row", func(t *testing.T) {
		tr := planFixture.with(t, "docs/plans/m2-device.md", "", goodPlan)
		expect(t, checkPlansIndex(tr.fs()), plansIndex, "docs/plans/m2-device.md", "has no row")
	})
	t.Run("index status disagrees", func(t *testing.T) {
		tr := planFixture.with(t, p, "- Status: Approved", "- Status: Done")
		expect(t, checkPlansIndex(tr.fs()), plansIndex+":7", `"Approved"`, "Status: Done")
	})
}

// frozenFixture holds the same drift, made by ref (given the prefix that reaches
// docs/ from the file: "" or "../"), in every kind of file isFrozen knows, and in the
// two kinds it does not: a plan that is not Done, and a live document.
func frozenFixture(ref func(up string) string) tree {
	plan := func(status string) string {
		return "# Plan: x\n\n- Status: " + status + "\n- Date: 2026-10-08\n\n## Steps\n\n" + ref("../") + "\n"
	}
	return tree{
		"docs/b.md":              "# B\n\n## Real section\n",
		"docs/plans/done.md":     plan("Done"),
		"docs/plans/approved.md": plan("Approved"),
		"docs/research/note.md":  "# Note\n\n" + ref("../") + "\n",
		"docs/reviews/review.md": "# Review\n\n" + ref("../") + "\n",
		"docs/adr/0001-first.md": "# ADR-0001: First\n\n- Status: Accepted\n\n" + ref("../") + "\n",
		"docs/live.md":           "# Live\n\n" + ref("") + "\n",
	}
}

// TestFrozenFilesAreExempt shows the decision of D3 and what it does not exempt: the
// same dead section reference and dead #fragment link pass in a Done plan, a research
// note, a review and an ADR, and fail in an Approved plan and in a live document.
func TestFrozenFilesAreExempt(t *testing.T) {
	frozen := []string{"docs/plans/done.md", "docs/research/note.md", "docs/reviews/review.md", "docs/adr/0001-first.md"}
	live := []string{"docs/plans/approved.md", "docs/live.md"}

	cases := []struct {
		name  string
		ref   func(up string) string
		check func(tree) []string
		say   string
	}{
		{"section reference", func(up string) string { return "See [b](" + up + "b.md) §Nothing at all." }, func(tr tree) []string { return checkSectionRefs(tr.fs()) }, "§Nothing at all"},
		{"fragment link", func(up string) string { return "See [b](" + up + "b.md#nothing)." }, func(tr tree) []string { return checkLinks(tr.fs()) }, "#nothing"},
	}
	for _, c := range cases {
		problems := c.check(frozenFixture(c.ref))
		for _, f := range frozen {
			for _, p := range problems {
				if strings.HasPrefix(p, f+":") {
					t.Errorf("%s: the frozen file %s was reported: %s", c.name, f, p)
				}
			}
		}
		for _, f := range live {
			expect(t, problems, f, c.say)
		}
	}

	adrFile := "docs/adr/0001-first.md"
	adrOnly := tree{adrFile: "# ADR-0001: First\n\n- Status: Accepted\n"}
	t.Run("a bare dead section in a Go comment fails", func(t *testing.T) {
		tr := adrOnly.with(t, "docs/b.md", "", "# B\n\n## Real section\n")
		tr = tr.with(t, "internal/x/x.go", "", "package x\n\n// Cites docs/b.md §Nothing at all, and b.md §Real section.\n")
		got := checkSectionRefs(tr.fs())
		expect(t, got, "internal/x/x.go:3", "§Nothing at all")
		if len(got) != 1 {
			t.Errorf("only the dead section should be reported, got %v", got)
		}
	})
	t.Run("a bare name resolved by its unique base name", func(t *testing.T) {
		tr := adrOnly.with(t, "docs/sub/only.md", "", "# Only\n\n## Real section\n")
		tr = tr.with(t, "internal/x/x.go", "", "package x\n\n// Cites only.md §Real section, and only.md §Gone section.\n")
		got := checkSectionRefs(tr.fs())
		expect(t, got, "internal/x/x.go:3", "§Gone section", "docs/sub/only.md")
		if len(got) != 1 {
			t.Errorf("only the dead section should be reported, got %v", got)
		}
	})
	t.Run("a citation of a file that does not exist fails", func(t *testing.T) {
		tr := adrOnly.with(t, "internal/x/gone.go", "", "package x\n\n// Cites gone.md §Anything.\n")
		expect(t, checkSectionRefs(tr.fs()), "internal/x/gone.go:3", `"gone.md"`, "no file")
	})
	t.Run("a path to a file that does not exist fails", func(t *testing.T) {
		tr := adrOnly.with(t, "internal/x/path.go", "", "package x\n\n// Cites docs/missing/gone.md §Anything.\n")
		expect(t, checkSectionRefs(tr.fs()), "internal/x/path.go:3", "docs/missing/gone.md")
	})
	t.Run("an ambiguous bare name is not guessed", func(t *testing.T) {
		tr := adrOnly.with(t, "docs/b.md", "", "# B\n\n## Real section\n")
		tr = tr.with(t, "docs/other/b.md", "", "# Other B\n\n## Different\n")
		tr = tr.with(t, "internal/x/x.go", "", "package x\n\n// Cites b.md §Whatever.\n")
		expectClean(t, checkSectionRefs(tr.fs()))
	})
	t.Run("a citation wrapped over two comment lines", func(t *testing.T) {
		tr := adrOnly.with(t, "docs/only.md", "", "# Only\n\n## Real section name\n")
		tr = tr.with(t, "internal/x/x.go", "", "package x\n\n// Cites only.md §Real section\n// name for its rule.\n")
		expectClean(t, checkSectionRefs(tr.fs()))
	})
}

var registerFixture = tree{registerPath: `# Known limits

Intro.

| Item | Kind | Area | Work | Source | Recorded | Closed by |
| --- | --- | --- | --- | --- | --- | --- |
| One | limit | build |  | [x](x.md) | 1n | |
| Two | deferred | build |  | [x](x.md) | design | 1p (step 2) |
| Three | open question | CLI | C3 | [x](x.md) | 1d | |
| Four | limit | device | 2p | [x](x.md) | 1e | |
| Five | limit | device/UI | quick | [x](x.md) | 1e | |
| Six | limit | web UI |  | [x](x.md) | 1e | |
`}

func TestRegisterCheckCatchesDrift(t *testing.T) {
	expectClean(t, checkRegister(registerFixture.fs()))

	t.Run("a fifth Kind", func(t *testing.T) {
		tr := registerFixture.with(t, registerPath, "| One | limit |", "| One | risk |")
		expect(t, checkRegister(tr.fs()), registerPath+":7", `Kind "risk"`)
	})
	t.Run("an unknown Area", func(t *testing.T) {
		tr := registerFixture.with(t, registerPath, "| Six | limit | web UI |", "| Six | limit | desktop |")
		expect(t, checkRegister(tr.fs()), registerPath+":12", `Area "desktop"`)
	})
	t.Run("no Source link", func(t *testing.T) {
		tr := registerFixture.with(t, registerPath, "| One | limit | build |  | [x](x.md) |", "| One | limit | build |  | x.md |")
		expect(t, checkRegister(tr.fs()), registerPath+":7", "Source", "no link")
	})
	t.Run("out of order by Area", func(t *testing.T) {
		tr := registerFixture.with(t, registerPath, "| Four | limit | device |", "| Four | limit | build |")
		tr = tr.with(t, registerPath, "| Five | limit | device/UI |", "| Five | limit | CLI |")
		expect(t, checkRegister(tr.fs()), registerPath+":10", "sorts later")
	})
	t.Run("out of order by Kind", func(t *testing.T) {
		tr := registerFixture.with(t, registerPath, "| One | limit | build |", "| One | deferred | build |")
		tr = tr.with(t, registerPath, "| Two | deferred | build |", "| Two | limit | build |")
		expect(t, checkRegister(tr.fs()), registerPath+":8", "sorts later")
	})
	t.Run("Recorded", func(t *testing.T) {
		tr := registerFixture.with(t, registerPath, "| [x](x.md) | 1n |", "| [x](x.md) | last week |")
		expect(t, checkRegister(tr.fs()), registerPath+":7", `Recorded "last week"`)
	})
	t.Run("Closed by", func(t *testing.T) {
		tr := registerFixture.with(t, registerPath, "| 1p (step 2) |", "| fixed |")
		expect(t, checkRegister(tr.fs()), registerPath+":8", `Closed by "fixed"`)
	})
	t.Run("an unknown Work", func(t *testing.T) {
		tr := registerFixture.with(t, registerPath, "| CLI | C3 |", "| CLI | someday |")
		expect(t, checkRegister(tr.fs()), registerPath+":9", `Work "someday"`)
	})
	t.Run("a missing cell", func(t *testing.T) {
		tr := registerFixture.with(t, registerPath, "| One | limit | build |  | [x](x.md) | 1n | |", "| One | limit | build |  | [x](x.md) | 1n |")
		expect(t, checkRegister(tr.fs()), registerPath+":7", "6 cells")
	})
	t.Run("the columns", func(t *testing.T) {
		tr := registerFixture.with(t, registerPath, "| Item | Kind | Area |", "| Item | Kind | Where |")
		expect(t, checkRegister(tr.fs()), registerPath+":5", "columns")
	})
}

func TestCorpusBudgetCatchesGrowth(t *testing.T) {
	tr := tree{
		"docs/a.md":                "# A\n\none two three\n",   // 5 words
		"docs/adr/0001-x.md":       "many many many many many", // not live
		"docs/plans/p.md":          "many many many many many", // not live
		"docs/research/r.md":       "many many many many many", // not live
		"docs/reviews/r.md":        "many many many many many", // not live
		registerPath:               "many many many many many", // not live
		"AGENTS.md":                "many many many many many", // outside docs/
		"docs/vision/new-place.md": "four more words here",     // counted wherever it sits
	}
	n, problems := checkCorpusBudget(tr.fs(), 9)
	if n != 9 {
		t.Errorf("counted %d words; the live set holds 9 (a file moved to a new place still counts)", n)
	}
	expectClean(t, problems)
	_, problems = checkCorpusBudget(tr.fs(), 8)
	expect(t, problems, "9 words", "budget is 8", "Cut 1 words")
}
