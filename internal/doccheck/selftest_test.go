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
	apiPath: `# Capability API

## Naming and namespaces

` + fence + `text
system.health     workspace.open
device.list       device.logs
policy.get        (human-only)
code.symbols      (post-MVP)
` + fence + `

## Classification of the MVP surface

| Capability | Scope |
| --- | --- |
| ` + "`system.health`" + ` | G |
| ` + "`workspace.open`" + ` | W |
| ` + "`device.list`, `device.logs`" + ` (bounded) | G |
| ` + "`policy.get`" + ` | G |

## Initial domains

| Domain | Kind |
| --- | --- |
| ` + "`workspace`" + ` | cmd |
| ` + "`device`" + ` | query |
| ` + "`system`" + ` | query |
| ` + "`policy`" + ` | query |
| ` + "`code`" + ` | query |
`,
	mcpPath: `# MCP

## What is exposed, and what is not

| Not exposed | Why |
| --- | --- |
| ` + "`policy.get`" + ` | human |

## The MVP tool surface

| Tool | RO |
| --- | --- |
| ` + "`system_health`" + ` | ✓ |
| ` + "`workspace_open`" + ` | |
| ` + "`device_list`" + ` | ✓ |
| ` + "`device_logs`" + ` | ✓ |
`,
}

func TestCapabilityCheckCatchesDrift(t *testing.T) {
	published := []string{"system.health"}
	expectClean(t, checkCapabilityNames(capabilityFixture.fs(), published))

	t.Run("published but not in the roadmap", func(t *testing.T) {
		expect(t, checkCapabilityNames(capabilityFixture.fs(), []string{"system.health", "system.version"}),
			mvpPath, "`system.version`", "Add it to the `system` row")
	})
	t.Run("missing from the name list", func(t *testing.T) {
		tr := capabilityFixture.with(t, apiPath, "device.list       device.logs", "device.list")
		expect(t, checkCapabilityNames(tr.fs(), published), apiPath, "§Naming and namespaces", "`device.logs`", mvpPath)
	})
	t.Run("a post-MVP name in the surface", func(t *testing.T) {
		tr := capabilityFixture.with(t, mvpPath, "`get` (human-only) | 1 |", "`get` (human-only) | 1 |\n| `code` | `symbols` | 1 |")
		expect(t, checkCapabilityNames(tr.fs(), published), apiPath, "`code.symbols`", "(post-MVP)")
	})
	t.Run("missing classification", func(t *testing.T) {
		tr := capabilityFixture.with(t, apiPath, "| `policy.get` | G |\n", "")
		expect(t, checkCapabilityNames(tr.fs(), published), apiPath, "§Classification", "`policy.get`", "has no classification row")
	})
	t.Run("a tool with no capability", func(t *testing.T) {
		tr := capabilityFixture.with(t, mcpPath, "| `device_logs` | ✓ |", "| `device_logs` | ✓ |\n| `device_logcat` | ✓ |")
		expect(t, checkCapabilityNames(tr.fs(), published), mcpPath, "`device_logcat`", "`device.logcat`")
	})
	t.Run("a capability with no tool and no exclusion", func(t *testing.T) {
		tr := capabilityFixture.with(t, mcpPath, "| `device_logs` | ✓ |\n", "")
		expect(t, checkCapabilityNames(tr.fs(), published), mcpPath, "`device.logs`", "no tool row `device_logs`")
	})
	t.Run("an excluded capability that still has a tool", func(t *testing.T) {
		tr := capabilityFixture.with(t, mcpPath, "| `device_logs` | ✓ |", "| `device_logs` | ✓ |\n| `policy_get` | ✓ |")
		expect(t, checkCapabilityNames(tr.fs(), published), mcpPath, "`policy_get`", "not exposed")
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
	t.Run("initial domains", func(t *testing.T) {
		tr := capabilityFixture.with(t, apiPath, "| `policy` | query |\n", "")
		expect(t, checkCapabilityNames(tr.fs(), published), apiPath, "§Initial domains", "`policy`")
	})
	t.Run("renamed section", func(t *testing.T) {
		tr := capabilityFixture.with(t, mcpPath, "## The MVP tool surface", "## Tools")
		expect(t, checkCapabilityNames(tr.fs(), published), mcpPath, `"The MVP tool surface"`, "capabilities_test.go")
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
	"docs/adr/0001-first.md":  adr("0001", "First", "Accepted", "- **Other.** Fails because."),
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
	t.Run("a gap", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0004-fourth.md", "", adr("0004", "Fourth", "Accepted", "Other."))
		expect(t, checkADRIndex(tr.fs()), "expected 0003, found 0004")
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
		expect(t, checkADRFormat(tr.fs()), "docs/adr/0001-first.md:19", "Alternatives considered", "placeholder")
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
	"docs/adr/0001-first.md": adrFixture["docs/adr/0001-first.md"],
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
