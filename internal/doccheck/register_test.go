package doccheck

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

const registerPath = "docs/roadmap/known-limits.md"

var (
	registerColumns = []string{"Item", "Kind", "Area", "Work", "Source", "Recorded", "Closed by"}
	// registerKinds are in the order rows are sorted within an Area.
	registerKinds = []string{"limit", "unverified", "deferred", "open question"}
	// registerAreas are in case-insensitive alphabetical order, which is the order
	// rows are sorted in.
	registerAreas = []string{"build", "CLI", "code", "configuration", "device", "device/UI", "emulator", "files", "git", "MCP/agents", "platform", "policy/security", "process", "SDK", "web UI"}

	// registerWork is the work that would close an open row, as the register's intro
	// lists it; a milestone such as 2p is also allowed.
	registerWork = []string{"C1", "C2", "C3", "C4", "C5", "C6", "git", "v1-code", "v1-test", "v1-device", "v1-sdk", "distribution", "platforms", "quick", "review", "trial"}

	milestoneRE = regexp.MustCompile(`^\d+[a-z]+$`)
	closedByRE  = regexp.MustCompile(`^\d+[a-z]+\b`)
	// registerLinkRE is a markdown link with a target, which Source must hold.
	registerLinkRE = regexp.MustCompile(`\[[^\]]+\]\([^)\s]+\)`)
)

// TestRegisterIsWellFormed checks docs/roadmap/known-limits.md, the one home of open
// items (AGENTS.md §Where each fact lives): seven columns; Kind one of four; Area from
// a closed set; Work empty, a milestone or from a closed set; Source a link; Recorded a milestone or "design"; Closed by empty or
// starting with a milestone; rows ordered by Area, then Kind, as its intro says.
func TestRegisterIsWellFormed(t *testing.T) {
	report(t, checkRegister(repo()))
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

func checkRegister(fsys fs.FS) []string {
	d, err := readDoc(fsys, registerPath)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	for i, l := range d.lines {
		if !d.inFence[i] && strings.HasPrefix(strings.TrimSpace(l), "|") {
			if h := splitRow(l); strings.Join(h, "|") != strings.Join(registerColumns, "|") {
				add("%s:%d: the table's columns are %q; they must be %q, in this order", registerPath, lineNo(i), h, registerColumns)
			}
			break
		}
	}
	rows, lines := d.table(0, len(d.lines))
	if len(rows) == 0 {
		add("%s: no table of rows found", registerPath)
	}
	prevArea, prevKind := -1, -1
	for r, row := range rows {
		at := fmt.Sprintf("%s:%d", registerPath, lineNo(lines[r]))
		if len(row) != len(registerColumns) {
			add("%s: the row has %d cells; it needs %d (%s). A literal | in the item goes inside a code span", at, len(row), len(registerColumns), strings.Join(registerColumns, ", "))
			continue
		}
		item, kind, area, work, source, recorded, closed := row[0], row[1], row[2], row[3], row[4], row[5], row[6]
		if item == "" {
			add("%s: the row has an empty Item", at)
		}
		k := indexOf(registerKinds, kind)
		if k < 0 {
			add("%s: Kind %q is not one of %s", at, kind, strings.Join(registerKinds, " | "))
		}
		a := indexOf(registerAreas, area)
		if a < 0 {
			add("%s: Area %q is not one of %s. A new area is a decision: add it to this check and to the register's intro", at, area, strings.Join(registerAreas, " | "))
		}
		if work != "" && indexOf(registerWork, work) < 0 && !milestoneRE.MatchString(work) {
			add("%s: Work %q is not a milestone such as 2p nor one of %s. A new kind of work is a decision: add it to this check and to the register's intro", at, work, strings.Join(registerWork, " | "))
		}
		if !registerLinkRE.MatchString(source) {
			add("%s: Source %q holds no link. Link the ADR, plan or research note that raised the item, or the area document", at, source)
		}
		if recorded != "design" && !milestoneRE.MatchString(recorded) {
			add("%s: Recorded %q must be a milestone such as 1n, or design", at, recorded)
		}
		if closed != "" && !closedByRE.MatchString(closed) {
			add("%s: Closed by %q must be empty or start with a milestone such as 1p", at, closed)
		}
		if k < 0 || a < 0 {
			continue
		}
		if a < prevArea || (a == prevArea && k < prevKind) {
			add("%s: the row (Area %s, Kind %s) comes after one that sorts later. Order rows by Area (%s), then Kind (%s)",
				at, area, kind, strings.Join(registerAreas, ", "), strings.Join(registerKinds, ", "))
		}
		prevArea, prevKind = a, k
	}
	return problems
}

// liveBudget is the most words the live documentation set may hold (wc -w, counted
// with strings.Fields). Milestone 1q fixed the measure at 42,775 words and aims for
// 0.6 of that; each of its merge steps lowers the constant to what the step reached. The set is
// every docs/**/*.md except docs/adr/, docs/plans/, docs/research/, docs/reviews/ and
// the register, which grows with what is open. Raised by 105 words (25,650 to 25,755) in
// milestone 1o step 3: the manual that left 31 tool descriptions (selectors, the golden
// order, never repeat an action, the shell and wrapper rules) is now the server
// instructions block in docs/interfaces/mcp.md, which `adapters/mcp` must equal verbatim
// and cannot be kept shorter than the points it carries (ADR-0036 Decision 1). Raised by 312
// (to 26,067) in milestone 2c step 1: the 2026-10-10 Phase 2 reorganisation (12 words net
// after cuts) and about 300 for the live documents 2c's steps 3, 4, 8, 9 and 11 change.
// Raised by 250 (to 26,317) in milestone 2g step 1: files.md's section on the five new
// operations, mcp.md's batch case and mvp.md's file row (ADR-0053, ADR-0054).
// Raised by 29 (to 26,346) in milestone 2k step 6: the `agent` domain's rows in mcp.md and
// mvp.md (ADR-0063). Raised by 31 (to 26,377) in milestone 2k step 7: mcp.md's hooks
// paragraph and table row name the observation (ADR-0063). Raised by 24 (to 26,401) in
// milestone 2l step 3: mvp.md's git row (`watch`, `read`, human-only) and mcp.md's
// not-exposed row for git.read (ADR-0066). Raised by 2 (to 26,403) in milestone 2l step 4:
// capability-api.md names git.changes among the ring-less topics (ADR-0066 §1). Raised by 23
// (to 26,426) at 2l's close: the README's status, mvp.md's Git line and process-lifecycle.md's
// git row (a watch's gits outlive its operation). Raised by 55 (to 26,481) at 2m's close: the
// README's install section and status, and mvp.md's Distribution line (ADR-0068). Raised by 4
// (to 26,485) in milestone 2n step 2: mvp.md's git row (`log`, `refs`, `show`) and mcp.md's
// not-exposed row for git.log (ADR-0069). Raised by 1 (to 26,486) in milestone 2n step 5:
// the same row names git.refs. Raised by 1 (to 26,487) in milestone 2n step 7: the same row
// names git.show. Raised by 59 (to 26,546) in milestone 2n step P2: a write git is a recorded
// kind, in process-lifecycle.md's per-operation row and layer 2, and mvp.md's git row and
// mcp.md's not-exposed row name git.fetch (ADR-0071). Raised by 5 (to 26,551) in milestone 2n
// step P3: the same two rows name git.checkout and git.createBranch. Raised by 3 (to 26,554) in
// milestone 2n step P4: the same two rows name git.submodules. Raised by 2 (to 26,556) in
// milestone 2n step P5: the same two rows name git.updateSubmodules. Raised by 2 (to 26,558)
// in milestone 2n step R3: mcp.md's not-exposed row and mvp.md's git row name git.pull (ADR-0074).
// Raised by 7 (to 26,565) at milestone 2n's close: README.md's §Where the work stands names 2n.
// Raised by 90 (to 26,655) at milestone 2o's close: workspace-state.md, gradle.md and
// process-lifecycle.md name the two roots, the register's five 2o rows, and README.md's
// status line (ADR-0075).
// Raised by 1,015 (to 27,670) on 2026-10-17: vision/beyond.md records the owner's vision
// beyond the MVP and the capabilities it needs, at the owner's request, with its two links.
// Raised by 14 (to 27,684) in milestone 2p step 6: mvp.md's device row and mcp.md's not-exposed row name
// device.tap, device.swipe and device.pressKey (ADR-0079).
// Raised by 283 (to 27,967) at milestone 2p's close: adb-device.md's screen viewing (the live view,
// the server's upgrade procedure, what it leaves on a device), ui-automation.md's live input,
// http-websocket.md's GET /v1/screen, capability-api.md's device.screen, the register's fourteen 2p
// rows and README.md's status line (ADR-0077 to ADR-0080).
// Raised by 166 (to 28,133, merged over 2p's) in milestone 2q step 4: mcp.md's server instructions block, which
// a test holds verbatim, and its two paragraphs on workspace_enter and the chat id
// (ADR-0082, ADR-0088), and mvp.md's workspace row.
// Raised by 15 (to 28,148) in milestone 2q step 6: session.watch's row and domain in mvp.md,
// its line in mcp.md's not-exposed table, which doccheck requires of a new capability, and
// session.state in capability-api.md's list of ring-less topics.
// Raised by 6 (to 28,154) in milestone 2q step 9: mvp.md's workspace row and mcp.md's
// not-exposed row name workspace.show and workspace.forget (ADR-0084, ADR-0086).
// Raised by 132 (to 28,286) at milestone 2q's close, Delegated: workspace-state.md's
// worktrees, the moving binding and inherited trust, overview.md's binding, the glossary's
// Session, http-websocket.md's one tab, mvp.md's human-only note and README.md's status
// line (ADR-0081 to ADR-0084, ADR-0088).
// Raised by 8 (to 28,294) at milestone 2r's close: README.md's status line names 2r (2r's
// edits of files.md, workspace-state.md and process-lifecycle.md add nothing in total).
// Raised by 282 (to 28,576) at milestone 2s's close, Delegated: workspace-state.md's occupied
// worktree and held device, adb-device.md's DEVICE_OCCUPIED, mcp.md's occupation paragraph and
// hooks row, files.md's recorded edits, the glossary's Occupation, the register's eight new 2s
// rows and three narrowed ones, and README.md's status line (ADR-0094, ADR-0095, ADR-0105);
// steps 4, 7 and 8 kept their rows within the budget by trimming.
// Raised by 14 (to 28,590) at milestone 2t step 4: mvp.md's device row and mcp.md's
// not-exposed table name device.installUpload, which the capability check requires
// (ADR-0099 §3).
// Raised by 710 (to 29,300) at milestone 2t steps 8–9, Delegated: interfaces/peer.md, the one
// document of the peer role (the routes, the two roles, what a peer may call), and the lines
// that name the peers in adb-device.md, http-websocket.md, ui-automation.md, workspace-state.md,
// vision.md, beyond.md, mvp.md and the glossary (ADR-0097 to ADR-0106).
// Raised by 5 (to 29,305) at milestone 2t's close: README.md's status line names 2t.
// Raised by 35 (to 29,340) at milestone 2u step 3: mvp.md's hosting row and domain, and
// mcp.md's not-exposed row, which the capability check requires (ADR-0108 §1).
// Raised by 2 (to 29,342) at milestone 2u step 4: `watchShare` in the same two lines.
// Raised by 216 (to 29,558) at milestone 2u step 7, Delegated: peer.md's sharing section
// (share and join, the join route, the listener on private addresses, every key live), and
// the lines that name them in adb-device.md and http-websocket.md (ADR-0107 to ADR-0109).
// Raised by 34 (to 29,592) at milestone 2u's close: the close verifier's corrections of
// peer.md, adb-device.md and http-websocket.md (the foreground share, hosting.listen), and
// README.md's status line names 2u.
// Raised by 4 (to 29,596) at milestone 2w step 6: mvp.md's device row names `uiEvents`, and
// mcp.md's list of renderings names the screen's events.
// Raised by 2 (to 29,598) at milestone 2w step 7: the same two tables name `watchUiEvents`.
// Raised by 203 (to 29,801) at milestone 2w's step 8: ui-automation.md's section on the full dump
// and the events between reads, adb-device.md's event ring and digest check, capability-api.md's
// `device.uiEvents` topic, and README.md's status line (ADR-0110 to ADR-0112).
// Raised by 6 (to 29,807) at milestone 2x step 3: peer.md says a peer's link holds four UI events
// streams per device too (ADR-0115).
// Raised by 38 (to 29,845) at milestone 2x step 5: mcp.md says a UI recording is the daemon's,
// not an operation's, and mvp.md's device row names recordUi and stopUiRecording (ADR-0113).
// Raised by 20 (to 29,865) at milestone 2x step 6: mcp.md's not-exposed table names
// device.watchUiRecordings and device.deleteUiRecording (a registry check requires it), mvp.md's
// device row the three new capabilities, and the skill says device_uiRecordings lists them (ADR-0113).
// Raised by 24 (to 29,889) at milestone 2x step 9: peer.md says a peer records a lent device on
// the host, sees only its own recordings and fetches its own finished timelines (ADR-0115).
// Raised by 6 (to 29,895) at the merge of 2y's close into 2x: README.md's status line names 2y; the
// steps' own additions (ADR-0117 to ADR-0121) were cut from the documents they touched.
// Raised by 159 (to 30,054) at milestone 2x's close: ui-automation.md says what a recording is and
// why Chrome's first dump may lack the page, adb-device.md that a peer's recording holds the lease,
// http-websocket.md that a growing text artifact is 400, README.md's line names 2x (ADR-0113 to
// ADR-0116).
// Raised by 13 (to 30,067) at milestone 2z step 7: capability-api.md, overview.md and files.md say a
// CLI connection is a session that carries streams and the CLI's stderr takes progress (ADR-0122).
// Raised by 14 (to 30,081) at milestone 2z's close: README.md's line names 2z, and capability-api.md
// §Statefulness is declared names the CLI connection.
const liveBudget = 30081

// TestLiveCorpusWithinBudget keeps the live documents from growing back. A document
// that needs more words has to say what it cuts, or the budget is raised in a change
// that explains why.
func TestLiveCorpusWithinBudget(t *testing.T) {
	n, problems := checkCorpusBudget(repo(), liveBudget)
	t.Logf("live documentation: %d words, budget %d", n, liveBudget)
	report(t, problems)
}

func liveCorpusFile(p string) bool {
	if !strings.HasPrefix(p, "docs/") || p == registerPath {
		return false
	}
	for _, dir := range []string{adrDir + "/", plansDir + "/", "docs/research/", "docs/reviews/"} {
		if strings.HasPrefix(p, dir) {
			return false
		}
	}
	return path.Ext(p) == ".md"
}

func checkCorpusBudget(fsys fs.FS, budget int) (int, []string) {
	files, err := markdownFiles(fsys)
	if err != nil {
		return 0, []string{err.Error()}
	}
	total := 0
	for _, f := range files {
		if !liveCorpusFile(f) {
			continue
		}
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			return 0, []string{err.Error()}
		}
		total += len(strings.Fields(string(raw)))
	}
	if total > budget {
		return total, []string{fmt.Sprintf("the live documentation holds %d words; the budget is %d. Cut %d words, or raise liveBudget in a change that says why", total, budget, total-budget)}
	}
	return total, nil
}
