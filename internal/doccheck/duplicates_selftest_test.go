package doccheck

import "testing"

// Self-tests for duplicates_test.go: each check must be seen failing on the drift it
// exists to catch (plan m1q step 3).

var fixtureAmended = map[string]int{"0001-first.md": 1, "0002-second.md": 1}

func TestSupersessionAndAmendedCatchDrift(t *testing.T) {
	expectClean(t, checkADRFormat(adrFixture.fs()))
	expectClean(t, checkAmendedFrozen(adrFixture.fs(), fixtureAmended))

	// Decision (D2): an accepted ADR gets no new Amended line, whatever its date.
	t.Run("an extra Amended line with an old date", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0001-first.md", "- Amended: 2026-09-29 —", "- Amended: 2026-09-01 — old.\n- Amended: 2026-09-29 —")
		expect(t, checkAmendedFrozen(tr.fs(), fixtureAmended), "docs/adr/0001-first.md", "2 \"Amended:\" lines, 1 allowed", "D2")
	})
	t.Run("a new ADR with an Amended line", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0003-third.md", "", adr("0003", "Third", "Accepted", "Other."))
		expect(t, checkAmendedFrozen(tr.fs(), fixtureAmended), "docs/adr/0003-third.md", "1 \"Amended:\" lines, 0 allowed")
	})
	// Decision (D2): the supersession pointer is checked both ways.
	t.Run("a one-sided Supersedes", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0001-first.md", "- Supersedes: ADR-0002\n", "")
		expect(t, checkADRFormat(tr.fs()), "docs/adr/0002-second.md", "Superseded by ADR-0001", "no \"- Supersedes: ADR-0002\"")
	})
	t.Run("Supersedes whose target is not marked", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0002-second.md", "Superseded by ADR-0001", "Accepted")
		expect(t, checkADRFormat(tr.fs()), "docs/adr/0001-first.md", "Supersedes: ADR-0002", "does not have")
	})
	t.Run("a partial Supersedes with no pointer back", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0002-second.md", "Superseded by ADR-0001", "Accepted").
			with(t, "docs/adr/0001-first.md", "- Supersedes: ADR-0002", "- Supersedes: ADR-0002 §Decision")
		expect(t, checkADRFormat(tr.fs()), "docs/adr/0001-first.md", "ADR-0002 §<Section>", "Superseded in part by")
	})
	t.Run("a partial pointer both ways is clean", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0002-second.md", "Superseded by ADR-0001", "Accepted").
			with(t, "docs/adr/0002-second.md", "- Date:", "- Superseded in part by: ADR-0001 (§Decision)\n- Date:").
			with(t, "docs/adr/0001-first.md", "- Supersedes: ADR-0002", "- Supersedes: ADR-0002 §Decision").
			with(t, adrIndex, "Superseded by ADR-0001", "Accepted")
		expectClean(t, checkADRFormat(tr.fs()))
	})
	t.Run("a partial pointer with no Supersedes", func(t *testing.T) {
		tr := adrFixture.with(t, "docs/adr/0001-first.md", "- Supersedes: ADR-0002\n", "").
			with(t, "docs/adr/0002-second.md", "Superseded by ADR-0001", "Accepted").
			with(t, "docs/adr/0002-second.md", "- Date:", "- Superseded in part by: ADR-0001 (§Decision)\n- Date:")
		expect(t, checkADRFormat(tr.fs()), "docs/adr/0002-second.md", "Superseded in part by", "no \"- Supersedes: ADR-0002 §<Section>\"")
	})
}

var codeFixture = tree{
	"internal/capability/error.go": "package capability\n\nconst (\n\tErrA ErrorCode = \"DEVICE_NOT_FOUND\"\n\tErrB ErrorCode = \"BUILD_FAILED\"\n)\n",
	"docs/a.md":                    "# A\n\n`DEVICE_NOT_FOUND` and BUILD_FAILED, and BUILD_QUEUE_FULL (planned). SOME_OTHER_NAME is not a code.\n",
	"docs/plans/m1x.md":            "# plan\n\nDEVICE_MISSPELT is a frozen record's business.\n",
	"AGENTS.md":                    "# Agents\n",
}

// Decision (D1): a code named in a live document, the register, AGENTS.md or .claude/
// is a constant or explicitly planned; records are not checked.
func TestErrorCodeCheckCatchesDrift(t *testing.T) {
	planned := map[string]bool{"BUILD_QUEUE_FULL": true}
	expectClean(t, checkErrorCodes(codeFixture.fs(), planned))

	t.Run("a misspelt code in a document", func(t *testing.T) {
		tr := codeFixture.with(t, "docs/a.md", "`DEVICE_NOT_FOUND`", "`DEVICE_NOT_FOUNT`")
		expect(t, checkErrorCodes(tr.fs(), planned), "docs/a.md:3", "DEVICE_NOT_FOUNT", "plannedCodes")
	})
	t.Run("a misspelt code in AGENTS.md", func(t *testing.T) {
		tr := codeFixture.with(t, "AGENTS.md", "# Agents", "# Agents\n\nBUILD_FAILD")
		expect(t, checkErrorCodes(tr.fs(), planned), "AGENTS.md:3", "BUILD_FAILD")
	})
	t.Run("a misspelt code in .claude", func(t *testing.T) {
		tr := codeFixture.with(t, ".claude/skills/x/SKILL.md", "", "returns BUILD_FAILD")
		expect(t, checkErrorCodes(tr.fs(), planned), ".claude/skills/x/SKILL.md:1", "BUILD_FAILD")
	})
	t.Run("a planned code that is not listed", func(t *testing.T) {
		expect(t, checkErrorCodes(codeFixture.fs(), nil), "BUILD_QUEUE_FULL")
	})
	t.Run("no constants at all", func(t *testing.T) {
		expect(t, checkErrorCodes(codeFixture.without("internal/capability/error.go").fs(), planned), "no ErrorCode constants")
	})
}

var countFixture = tree{
	"docs/a.md": "# A\n\nWords here.\n",
	"AGENTS.md": "# Agents\n",
}

// Decision (D1): counts and the API version have one home; a copy is a finding in the
// live documentation and AGENTS.md, and not in a frozen record.
func TestRestatedCountsCatchDrift(t *testing.T) {
	expectClean(t, checkRestatedCounts(countFixture.fs()))
	for _, c := range []string{"39 capabilities", "35 ADRs", "35 decisions", "34 MCP tools", "API 0.10.0"} {
		t.Run(c, func(t *testing.T) {
			tr := countFixture.with(t, "docs/a.md", "Words here.", "There are "+c+" now.")
			expect(t, checkRestatedCounts(tr.fs()), "docs/a.md:3", c)
		})
	}
	t.Run("AGENTS.md", func(t *testing.T) {
		tr := countFixture.with(t, "AGENTS.md", "# Agents", "# Agents\n\n35 decisions")
		expect(t, checkRestatedCounts(tr.fs()), "AGENTS.md:3", "35 decisions")
	})
	t.Run("a record may keep its numbers", func(t *testing.T) {
		tr := countFixture.with(t, "docs/research/r.md", "", "39 capabilities at API 0.1.0\n")
		expectClean(t, checkRestatedCounts(tr.fs()))
	})
}
