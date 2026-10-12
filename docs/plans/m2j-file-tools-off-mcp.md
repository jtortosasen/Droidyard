# Plan: Milestone 2j — the file capabilities leave the MCP surface, and an agent's `rm` goes to the trash

- Status: Approved
- Date: 2026-10-12
- Approved: 2026-10-12, by the main session under the owner's standing instruction to apply the recommended option (Delegated); the owner asked for the milestone in full ("procede con quitar y hacer el hito 2j"), so the plan stays one milestone. Q1 (as [ADR-0060](../adr/0060-file-capabilities-not-mcp-tools.md)), Q2 (the hook denies and hands over the command; written as ADR-B at step 6) and Q3 applied, Delegated.
- Milestone: "2j — the file capabilities leave the MCP surface". The owner, 2026-10-12, in Spanish: "No quiero gastar tokens en definiciones para algo que todos los agentes a día de hoy soportan", then "quítalos, sí": all nine `file.*` capabilities stop being MCP tools, stay in the capability API for the web UI (tree, editor, trash) and the CLI, and agents read, edit and delete with their own tools. Then, the same day: "analiza cómo es con cursor para meterlo también": 2j also ships a delete-to-trash hook for Claude Code and Cursor, so an agent's deletes land in droidyard's trash, undoable in the UI, with no MCP tool. (`docs/README.md` §Where the work stands does not name 2j yet; the close adds the line.)
- Phase: `docs/roadmap/mvp.md` §Phase 2, a milestone after the Definition of done, by discussion (owner, 2026-10-10). No phase deliverable is added.
- Contract: Q1's ADR-A and Q2's ADR-B (the next free numbers, 0060 and 0061 today; the plan names them A and B until they exist, because `TestADRMentionsExist` refuses a number not on disk). ADR-A supersedes in part ADR-0053 §Decision and §Consequences, ADR-0036 §Decision (item 4), ADR-0058 §Consequences. ADR-B supersedes in part ADR-0037 §Decision (item 2) and §Consequences, ADR-0019 §Processes (items 3, 4), ADR-0024 §Decision. Also ADR-0015 §6, ADR-0045 §4 (audience `human`); ADR-0004 (hooks are optional adapters); ADR-0054 (the trash); `mcp.md` §What is exposed, §Results, §Annotations, §Features beyond tools; `capability-api.md` §Discovery and versioning; plan m1o step 1 (a) and §Changes (Cursor's hooks, observed).

**The owner should see:**
- Q1: the mechanism is audience `human` with no new marker. The four batch renderings go, because only MCP read them. `maxToolListBytes` drops to the new size. Allow rules for `file_list` and `file_read` from an earlier `init` stay in users' client configs. The "droidyard only" comparison arm can no longer be run as defined.
- Q2 departs from the coordinator's sketch: **the hook deletes nothing itself.** On a simple `rm` it denies the command and hands the agent the exact `droidyard call … file.delete` to run instead. A hook that deleted (to the trash) would act before the client's own approval, so an `rm` the user would have refused would still happen. The cost is one more agent turn per delete. The rejected option wins if you prefer that turn saved over the approval kept.
- Q3: **the UI cannot undo an agent's delete today.** Undo is the tab's own stack (`web/src/lib/files/undo.ts`), and the trash view is deferred (register). So 2j adds a Restore action to Activity's completed `file.delete` rows, from any client.
- An `rm` of build outputs (`build`, `.gradle`, `.kotlin`, `.cxx`) is not routed. Trashing them would evict your older trashed items under the trash's 2 GiB cap (ADR-0054 §3).
- Cursor's hook ships only if step 6 shows a pass-through answer that neither blocks nor approves Cursor's other shell commands.
- The milestone is 11 steps. The round-2 reviewer recommends two milestones, cut after step 5: steps 1–5 are ready, and steps 6–11 wait on step 6's facts. Kept as one plan because the coordinator asked for that; the cut stays open. The milestone can stop after step 5 with the tool removal complete.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| The nine `file.*` off every MCP profile, refused from an MCP session; unchanged for the UI and the CLI | yes | Q1, steps 2–5 |
| The core profile, golden-path list, tool counts, byte budget, skill map | yes | step 3 |
| No nextStep an MCP tool can return names a capability that is not a tool (in the scanned packages) | yes | step 2 |
| `droidyard hook` for Claude Code (`PreToolUse`, Bash) and Cursor (`beforeShellExecution`; its `Delete` tool if step 6 shows its input) | yes | Q2, steps 6–9 |
| `init` writes the hook entries into the project's files only, never `~/.claude` or `~/.cursor` | yes | Q2 §5, step 9 |
| Restore of a completed `file.delete` from Activity, whatever its client | yes | Q3, step 10 |
| An OpenCode hook (a generated plugin) | no | not asked; ADR-0037's format note; a register row |
| A trash view, emptying the trash by hand | no | register row 263 stays |
| Phase 0: "the same capability is reachable from the CLI and from MCP with identical semantics" | holds for every capability on both | human-audience capabilities are outside it since ADR-0015 §6 |
| Phase 2: the same-primitive check | unaffected | the golden path's table names no `file.*` |

## Starting point

At `9ec50b0` (2i merged), API `0.20.0`. `internal/domain/file/file.go` registers all nine `file.*` as `AudienceAll`. `move`, `copy`, `delete` and `restore` declare `Text` renderings (`moveText`, `copyText`, `deleteText`, `restoreText` over `batchText`, in `ops.go`); only `adapters/mcp` reads them. `file.watch` is streaming, so eight `file_*` tools are listed.

How audience works:
- `capability.AudienceHuman` is refused only to `ClientMCP`, in `Operations.prepare`. That check (`internal/capability/operation.go:862`) runs before the statefulness check (866).
- `mcpadapter.Exposed` leaves human-audience capabilities out of `tools/list`, so mcp-go answers `-32602 tool '…' not found` first (`TestMCPTranscript`).
- The CLI and HTTP have no audience gate (`http_test.go` `fixture.forhumans`; `TestTrashSurvivesADaemonRestart` drives `file.delete` and `file.restore` over the CLI after `workspace.open`).
- `init`'s allowlist and the skill map go through `Exposed` (ADR-0037 Decision 4).
- `internal/assistant` (`edit.go` `mergeInto`, `uninstallJSON`) merges by key, appends an array element only if no equal element is there, and uninstalls only what it would write now.

Tests that encode today's decision:
- `TestOpsAnnotations` and `TestTrashDescriptors` assert `AudienceAll`.
- The renderings: `TestMoveTextLeadsWithTheFailures` (`ops_test.go`), `TestCopyTextLeadsWithTheFailures` (`copy_test.go`), and two asserts in `TestDeleteItemsReportEach`.
- `TestTheRenderedCapabilitiesAreThePlansList` forbids a rendering on a human-audience capability.
- `TestFileWatchNeedsASession` (`ws_test.go:1122`): its MCP leg expects `SESSION_REQUIRED`.
- `TestFileOperationsAreNotCore`, `TestCoreProfileListsTheGoldenPath` (`goldenPath` holds `file_list`, `file_read`, `file_write`), `TestDefaultListsEverything` (39), `TestToolListModelFacingBytes` (39, `maxToolListBytes = 35000`), `TestMCPTranscript` (39).
- `TestCoreNextStepsNameCoreTools` scans `build`, `build/gradle`, `device`, `emulator`, `file`, `project`, `capability`, `workspace` and `artifact`, but not `internal/policy`. `POLICY_SHELL_DISABLED`'s nextStep (`policy.go:512`), which an MCP agent can receive from `shell_exec`, names "the human-only policy.update".
- `TestMCPBatchWithFailedItemsIsASuccessfulCall` (`mcp_fileops_test.go`).
- `TestOperationStateCarriesTheSession` uses `file_list`.

`mcpadapter.Instructions`, `SKILL.md` and the web UI name no file tool. No nextStep outside `internal/domain/file` names a `file.*` capability.

The UI's Undo is the tab's own stack (`web/src/lib/files/ops.ts`, `undo.ts`). Activity (`web/src/lib/ops/activity.ts`) lists every client's operations (ADR-0048) and offers no Undo.

Hooks: ADR-0037 adopted none. m1o step 1 (a) observed, with `cursor-agent` 2026.10.01, that `-p` runs project hooks, that a `beforeShellExecution` deny reaches the model as "Command execution was blocked by a hook: <user_message>", that `agent_message` does not reach it, and that a hook's environment has no `DROIDYARD_STATE_DIR`. Claude Code's hook facts are the coordinator's, from secondary sources (§Unverified).

Measured 2026-10-12:
- `go vet ./...` is clean. `go test ./...` passes in every package (31 with tests, 5 without). The only exception is `TestPlansIndexMatchesFiles`, until the main session adds this plan's index row.
- `TestToolListModelFacingBytes` logs 34,786 B. The core profile is 24,836 B over 25 tools. The eight file tools are about 6.4 KB (the main session's measure from the schema).
- The live corpus is 26,316 words against `liveBudget = 26317`.

## Acceptance

- [ ] A1. No MCP session lists a `file_*` tool: 31 tools by default and under `--tools all`, 22 under `core`. The `tools/list` bytes are recorded before (34,786 B) and after, and `maxToolListBytes` drops to the after size rounded up to the next 1,000 B. Source: the owner; Q1 §1, §4. Proven by: steps 3, 5.
- [ ] A2. An MCP `tools/call file_read` gets `-32602`, tool not found. The core refuses every `file.*` (`file.watch` included) from `ClientMCP` with `REQUEST_NOT_ALLOWED_HERE` and creates no operation. Source: Q1 §1. Proven by: step 3.
- [ ] A3. `droidyard call file.*` and the UI's calls work as before, at level 3 and, in a real browser, at level 5. Source: the owner. Proven by: steps 2, 3, 5.
- [ ] A4. The schema diff is the version, `0.21.0`, plus nine `"audience": "all"` → `"human"` lines, and nothing else. Source: Q1 §4. Proven by: step 3.
- [ ] A5. A test fails if a nextStep in the packages `TestCoreNextStepsNameCoreTools` scans (`internal/policy` added) names a published capability that is not a tool, outside a reasoned exception list. Source: the brief. Proven by: step 2.
- [ ] A6. The live documents and the register say it. `mcp.md`'s not-exposed table is checked both ways. Source: Q1, Q2 §Consequences. Proven by: steps 4, 9.
- [ ] A7. A simple `rm` inside the project, sent by Claude Code (and by Cursor, if step 6 clears it), is denied with the exact `droidyard call … file.delete` command. Anything else passes untouched, including when no daemon answers. Source: the owner; Q2 §1–§4. Proven by: steps 7, 8, 11.
- [ ] A8. `droidyard init --client claude|cursor` writes the hook entry into the project only, idempotently, and `--uninstall` removes it. Source: Q2 §5. Proven by: step 9.
- [ ] A9. A delete an agent made through that command is restored from the UI's Activity. Source: the owner ("undo in the UI"); Q3. Proven by: steps 10, 11.

## Steps

### 1. ADR-A

- Builds: `docs/adr/<A>-file-capabilities-not-mcp-tools.md` from Q1's draft, Status Accepted (the owner's decision, the mechanism Delegated). It gets header lines `- Superseded in part by: ADR-A (§Decision)` and `(§Consequences)` in ADR-0053, `(§Decision)` in ADR-0036, and `(§Consequences)` in ADR-0058, plus the index row. The main session writes it.
- Implements: `AGENTS.md` §Conventions; the `adr` skill.
- Check: `go test ./internal/doccheck` passes. With any one back-pointer removed it fails in `checkSupersession` ("The pointer must exist both ways").
- Schema: no change.
- Needs: Q1 as reviewed.
- Not here: ADR-B, which waits for step 6's evidence.
- Rests on: —
- [x] Done. Evidence: ADR-0060 from Q1's draft (the forward mention of ADR-B worded as "the delete hook (plan m2j Q2)" until it exists); back-pointers in ADR-0053 (×2), ADR-0036, ADR-0058; index row; this plan's row. `go test ./internal/doccheck` ok.

### 2. Groundwork that is green before and after

- Builds:
  - `cmd/droidyard/mcp_profile_test.go` `TestCoreNextStepsNameCoreTools`:
    - Add a second failure: a token naming a published capability that is not exposed (read `schema/capabilities.json` as `published(t)` in `mcp_test.go` does; not exposed means audience `human` or not stateless).
    - That check scans the existing packages plus `internal/policy`, whose `POLICY_SHELL_DISABLED` reaches agents through `shell_exec`. The core-profile check keeps its own package list: `shell_exec` is not a core tool.
    - Add a reasoned exception list, kept fresh by a loop like the existing one for `unreachable` (`mcp_profile_test.go:258-267`), which fails on an entry that is no longer named:
      - `policy.update`: "named in `POLICY_SHELL_DISABLED` as the user's channel: the agent asks the user";
      - `policy.get`: "only `policy.update`'s own `POLICY_REVISION_CONFLICT` (`update.go:296`); an MCP session cannot call `policy.update`".
    - The existing `len(found) < 10` guard stays.
  - `operation_session_test.go`: `file_list` → `workspace_describe` in the shim loop, `byCap["file.list"]` → `byCap["workspace.describe"]`, and the doc comments at lines 15–16 and 182 follow. Confirm nothing else in the test calls it.
  - New `TestCLIBatchWithFailedItemsCompletes` in `mcp_fileops_test.go`, beside the MCP test. It runs `callIn` on `workspace.open`, then `file.move` with one colliding item and one free item. Expected: exit 0, operation `completed`, item 1 `FILE_EXISTS` with the target's bytes kept, item 2 done. A source outside the root exits non-zero with `FILE_OUTSIDE_ROOT` (ADR-0053 §2, over the CLI).
  - `cmd/droidyard/trust.go`: "An agent that can edit them can run any command as you, …", with "(file.write)" dropped.
- Implements: A5; A3 (the CLI leg, before the change).
- Check:
  - The new nextStep failure passes on today's tree, and is seen failing in three ways (each mutation reverted):
    - put `"call file.watch"` into a `WithNextStep` in `internal/domain/build`;
    - remove `policy.update` from the exception list;
    - add a stale exception.
  - The CLI test passes before step 3 and after it, which is the point.
  - `go test ./...` is green.
- Schema: no change.
- Needs: step 1.
- Not here: anything that changes what MCP lists.
- Rests on: —
- [x] Done. Evidence: the nextStep check (`TestCoreNextStepsNameCoreTools`) also fails on a published capability that is not a tool, scanning `internal/policy` with `policy.update` and `policy.get` as reasoned exceptions; seen failing with a build nextStep naming `file.watch`, with `policy.update` dropped from the list, and with a stale entry (an MCP tool; one no longer named). `operation_session_test.go` uses `workspace_describe`; `TestCLIBatchWithFailedItemsCompletes` (exit 0, `completed`, item 1 `FILE_EXISTS`, item 2 done; outside the root `FILE_OUTSIDE_ROOT`); `trust.go` no longer names `file.write`. `go test ./...` green.

### 3. `file.*` audience human

Everything in this step lands together: the audience change breaks the counts, the core list and the renderings test at once.

- Builds:
  - `internal/domain/file/file.go`:
    - Set `Audience: capability.AudienceHuman` on all nine descriptors.
    - Drop `Text` from `move`, `copy`, `delete` and `restore`. Delete the four render functions and `batchText` with its line type once nothing uses them.
    - The comment at line 167 cites ADR-A.
  - Version and schema: `system.go` `Version = "0.21.0"`, then `go test ./internal/schema -update`.
  - Tests in `internal/domain/file`:
    - `TestOpsAnnotations` and `TestTrashDescriptors` assert `AudienceHuman`.
    - A new `TestEveryFileCapabilityIsHumanAudience` finds exactly nine `file.*` descriptors in the fixture registry and fails on any that is not `AudienceHuman`.
    - Delete `TestMoveTextLeadsWithTheFailures`, `TestCopyTextLeadsWithTheFailures`, and the two rendering asserts in `TestDeleteItemsReportEach`.
  - `internal/schema/text_test.go`: `rendered` loses the four names.
  - `adapters/mcp/mcp.go`: `CoreTools` loses `file_list`, `file_read` and `file_write`.
  - `mcp_profile_test.go`:
    - `goldenPath` loses the same three, and its comment says "m1o D3 without the file tools (ADR-A)".
    - Drop `"file"` from both package lists, the core check's and the new check's. After this step every `file.*` that `internal/domain/file`'s nextSteps name is not exposed, and no file error reaches an MCP session.
    - `TestFileOperationsAreNotCore` becomes `TestFileCapabilitiesAreNotTools`. With no flag, `all` and `core`, no listed name starts with `file_`. `tools/call file_read` answers `-32602`, "tool 'file_read' not found". `p.ops.Start` of `file.read` as `ClientMCP` on an opened workspace gets `REQUEST_NOT_ALLOWED_HERE` and no id (as `TestPolicyGetIsRefusedFromTheCoreOnMCP` does). After `workspace.open`, `callIn` of `file.read` completes, and `POST /v1/call` of `file.read` with the token completes.
    - `TestDefaultListsEverything`: 31.
  - `ws_test.go` `TestFileWatchNeedsASession`: the MCP leg expects `REQUEST_NOT_ALLOWED_HERE` and no operation. `prepare`'s order stays as it is: audience before statefulness.
  - `toollist_bytes_test.go`: 31, and `maxToolListBytes` = the logged size rounded up to the next 1,000 B. Its comment records 34,786 B at `9ec50b0`, the new size and ADR-A §4.
  - `mcp_test.go` `TestMCPTranscript`: 31 and 31.
  - Delete `TestMCPBatchWithFailedItemsIsASuccessfulCall`; step 2's CLI test keeps the batch on the wire.
  - `go test ./cmd/droidyard -update`: the skill map loses its eight `file_*` rows.
- Implements: ADR-A §1–§4.
- Check:
  - `TestFileCapabilitiesAreNotTools` and `TestEveryFileCapabilityIsHumanAudience` fail before the step, and fail with `file.read` reverted to `AudienceAll`.
  - `TestToolListModelFacingBytes` fails with any one file tool restored.
  - Then `go test ./...` and `pnpm -C web run verify` (the schema changed; ADR-0040) are green.
- Schema: `"version"` `0.20.0` → `0.21.0`, and nine `"audience": "all"` → `"human"` (`file.list`, `read`, `write`, `watch`, `create`, `move`, `copy`, `delete`, `restore`). Nothing else changes; renderings are not in the schema.
- Needs: step 2.
- Not here:
  - `REQUEST_NOT_ALLOWED_HERE`'s generic text stays as it is. No MCP client can reach the core with a `file.*` call, because mcp-go refuses the unlisted tool first.
  - `Instructions` stays unchanged; it names no file tool.
- Rests on: the UI and the CLI have no audience gate. Level 3 here; level 5 in step 5.
- [x] Done. Evidence: the nine descriptors `AudienceHuman`, the four batch renderings removed, `Version` 0.21.0, `CoreTools` 22 tools, `goldenPath` without file tools; `TestEveryFileCapabilityIsHumanAudience`, `TestFileCapabilitiesAreNotTools` (none listed under no flag, `all`, `core`; `tools/call file_read` → -32602 "tool 'file_read' not found"; `ClientMCP` → `REQUEST_NOT_ALLOWED_HERE`; the CLI's `callIn` and `POST /v1/call` with the token complete); `TestFileWatchNeedsASession`'s MCP leg → `REQUEST_NOT_ALLOWED_HERE`. Reversal (`file.read` back to `all`): both new tests and `TestToolListModelFacingBytes` (32 tools) fail, rerun by the Opus verifier. `tools/list` 34,786 → 28,032 B (39 → 31 tools); `maxToolListBytes` 35,000 → 29,000 (the 968 B slack alone may not catch one small tool; the count does). Schema diff: the version and nine audience lines. Opus verifier: proceed, no blocking or Important finding.

### 4. The documents and the register, for the removal

- Builds:
  - `mcp.md`:
    - §What is exposed gets a row for `file.list`, `file.read`, `file.write`, `file.create`, `file.copy`, `file.move`, `file.delete`, `file.restore` and `file.watch` (moved from the streaming row): "Audience `human` (ADR-A): agents have their own file tools; the UI and the CLI call them".
    - §Results loses its batch bullet.
    - §Annotations reads "`workspace_close` is idempotent *and* destructive".
  - `files.md` §Why this exists: `file.*` serves the UI and the CLI. Agents use their own tools, so their edits bypass the daemon: the tree sees them by polling, and the editor's `ifRevision` catches them.
  - `vision.md` §Principles #1 and `overview.md`: the exception is the human-audience capabilities (ADR-0015 §6, ADR-0045 §4, ADR-A).
  - `mvp.md` §MVP capability surface, `file` row: "(human-only; never over MCP)".
  - `known-limits.md`:
    - Row 143 is narrowed to the UI ("within an MCP client's patience" goes; the untimed 1,000-item batch stays).
    - Row 178: "it edits build scripts with its own file tools".
    - New row (limit, files): an agent's own edits are not contained, recorded or guarded by `ifRevision` (ADR-A).
    - New row (limit, MCP/agents): earlier `init` allow rules for `file_list` and `file_read` stay.
    - New row (limit, MCP/agents): the "droidyard only" comparison arm (m1l C2, m1o D1) can no longer be run as defined.
  - `internal/doccheck/capabilities_test.go`: the reverse check. Every published capability with audience `human` or not stateless must be named in `mcp.md`'s not-exposed table. If it finds any outside `file.*` today, they join the table in this step.
  - `liveBudget`: raised by the measured delta (the corpus has one word of headroom), with a comment naming 2j step 4.
- Implements: ADR-A §Consequences.
- Check:
  - The reverse check fails before the `mcp.md` row is written (it names the nine) and passes after.
  - `grep -rn 'file_' docs/interfaces docs/subsystems docs/architecture docs/vision docs/roadmap/mvp.md` prints `mcp.md:110` and `:175` before and nothing after.
  - `go test ./internal/doccheck` is green.
- Schema: no change.
- Needs: step 3.
- Not here: `docs/README.md` and `docs/plans/README.md` (the main session). Frozen records stay untouched.
- Rests on: —
- [x] Done. Evidence: `mcp.md` (the not-exposed row naming the nine, `file.watch` out of the streaming row, the batch bullet gone; the instructions block unchanged), `files.md`, `vision/vision.md`, `architecture/overview.md`, `mvp.md`'s row, register rows (three added; two reworded); the reverse doccheck (`checkNotExposed`, `TestNotExposedTableNamesEveryHiddenCapability`) seen failing with eight `file.*` missing before the row; `grep file_` over the live interface, subsystem, architecture and vision documents prints nothing; corpus 26,317 of 26,317 by cuts, `liveBudget` unchanged.

### 5. Level 5: the removal on the shipped binary

- Builds: nothing. Run by the main session.
- Implements: A1, A3 at level 5.
- Run limits:
  - **R1**: m2i K1. A copy of `samples/golden` made by `git archive`. State and config under a fresh `/tmp/dyj.*`. `pre-health` must fail. The binary is built after `pnpm -C web build`.
  - **R2**: m2i K2, whole. Every droidyard daemon this run did not start (the owner's pid 15130, and 17053 if alive) and the adb server on 5037 keep their pid and start time, recorded before and after. Nothing is killed by name. `droidyard stop` runs only under R1's environment. `ANDROID_SERIAL=droidyard-m2j-none`; no adb, device or Gradle.
  - **R3**: m2i K4. Headless Chrome with its own `--user-data-dir`, driven over CDP, with captures by CDP only and never of the owner's screen. Never "Open folder…" or `workspace.chooseRoot`.
  - **R4**: never `~/Documents/DopamineControl`. `verify-daemon` §Launch, §Doctor and §Cleanup apply.
- Check: any `file_` tool listed, or any UI or CLI file call refused, fails the step.
  1. `"$DY" schema | cmp - schema/capabilities.json` prints nothing.
  2. An MCP session bound to the copy (`verify-daemon` §Drive):
     - `tools/list` has 31 names, none starting with `file_`.
     - Its bytes (`jq -c '[.result.tools[] | del(.outputSchema)]' | wc -c`) are recorded beside the test's figure, as two figures (Go escapes `<>&`).
     - `--tools core` lists 22 names.
     - `tools/call file_read` answers `-32602`.
  3. CLI on the copy: `workspace.open`, `file.list`, `file.read`, `file.write` with that `ifRevision`, `file.delete`, and `file.restore` of its `trashId`. Each exits 0, and `daemon.log` shows each as `client=cli … completed`.
  4. Browser:
     - The tree lists the copy.
     - Editing a `.kt` file and saving changes it on disk.
     - Deleting a file in the tree and pressing Undo brings it back.
     - `daemon.log` shows these as `client=ui … completed`.
     - The console shows no error except the favicon's.
  5. `droidyard init --client claude --root <copy> --dry-run` writes no `file_` allow rule.
  6. Cleanup leaves zero orphans, and R2 holds.
- Schema: no change.
- Needs: steps 3, 4.
- Not here: a real agent (step 11).
- Rests on: —
- [x] Done. Evidence: `l4-s5-114506` (R1–R4; built at `aed8aba` in a detached worktree, since the main tree held step 8's work). (1) `droidyard schema` equals `schema/capabilities.json`. (2) MCP: 31 tools, none `file_`; `--tools core` 22; `tools/call file_read` → -32602 "tool 'file_read' not found"; bytes without `outputSchema` 28,022 (Python, `<>&` unescaped) against the test's 28,032 (Go escapes them); core 22,294. (3) CLI: `workspace.open`, `file.list`, `file.read`, `file.write` with that `ifRevision`, `file.delete`, `file.restore` of its `trashId`, each exit 0 and `client=cli … completed` in `daemon.log`. (4) Headless Chrome: the tree lists the copy; `MainActivity.kt` edited and saved with Cmd+S, the disk changed; `s5-del.txt` deleted from the tree's menu (notice "Deleted 1 item"), Undo brought it back; `client=ui` `file.write`, `file.delete`, `file.restore`, `file.watch` completed; the one failed `file.read` is a `FILE_NOT_FOUND` at load, not an audience refusal; `window.__csp` empty, the only console error the favicon's. (5) `init --client claude --dry-run`: no `file_` rule (17 allow rules). (6) zero orphans; R2 unchanged.

### 6. What the clients' hooks do, then ADR-B

- Builds: nothing in the repository but ADR-B.
- First, the main session:
  - **(a)** Fetches Claude Code's official hooks reference and records what Q2 rests on:
    - the pass-through answer (exit 0, no output);
    - the deny shape, and whether its reason reaches the model;
    - whether `PreToolUse` runs before the permission prompt;
    - the default timeout;
    - the hook's environment;
    - what `cwd` the event carries after an earlier, separate `cd sub` in the same session;
    - whether project hooks in `.claude/settings.json` run under `claude -p` in a folder never trusted;
    - whether a nested `claude -p` needs `CLAUDECODE` unset;
    - how an allow rule matches a command that starts with `VAR=… /abs/path`.
    The round-2 reviewer already fetched the reference on 2026-10-12. It found: exit 0 with no output continues through the normal permission flow; `allow` skips the prompt; deny and ask rules still apply; the deny's reason is shown to Claude; exit 2 blocks like a deny; exit 1 is a non-blocking error; the default timeout is 600 s; hooks inherit Claude Code's environment. (a) re-checks these and records them.
  - **(b)** If `cursor-agent` runs under m1o K2's isolation (a scratch `CURSOR_DATA_DIR`; `~/.cursor/*` byte-identical before and after; never `-f`, `--yolo` or `--approve-mcps`), it probes a scratch copy whose throwaway `.cursor/hooks.json` hooks `beforeShellExecution` and `preToolUse` (matcher `Delete`) to a script that records stdin and answers:
    - Does `{"permission":"allow"}` let through a command that `.cursor/cli.json` would otherwise ask about or deny? Its `deny` list holds `Shell(touch *)`.
    - Does `{}` or an empty output pass a command, or block it?
    - Asked to delete a file, which does Cursor's agent use: its shell or its `Delete` tool? In m1o step 2 it ran no shell command in five runs. If it is `Delete`, that branch is the one worth shipping.
    - What `tool_input` does the `Delete` tool carry?
    - What `cwd` does a shell event carry after an earlier `cd sub`?
    - What does a non-zero exit other than 2 do?
    - The recorded stdin of each hook becomes step 7's fixtures.
- Then the main session writes ADR-B from Q2's draft, amended by (a) and (b), Accepted and Delegated, with its back-pointers and index row.
- Implements: risk first. Every hook step rests on these facts.
- Check: each question has a recorded answer, or "not run" with the reason. Cursor's pass-through answer is one that neither approves a command `cli.json` denies nor blocks an innocent one. If none is shown, ADR-B §5 drops Cursor from `init` (the handler still decodes Cursor's JSON, tested on fixtures), and a register row records it. `go test ./internal/doccheck` passes with ADR-B.
- Schema: no change.
- Needs: step 1 (the format); nothing of the removal.
- Not here: a Claude Code run, which costs the owner's quota; step 11 is the one run.
- Rests on: Cursor's pass-through semantics, level 5 (a real `cursor-agent`), or else no Cursor entry.
- [x] Done. Evidence: run before steps 3–5 (it needs only step 1). (a) Claude Code's hooks reference fetched 2026-10-12 (code.claude.com/docs/en/hooks): exit 0 with no output "continues through the normal permission flow"; deny is `hookSpecificOutput.permissionDecision: "deny"` with a `permissionDecisionReason` that Claude is shown; `allow` skips the prompt but "deny and ask rules are still evaluated regardless"; exit 2 blocks like a deny, other non-zero exits and a timeout do not block; default timeout 600 s; the hook inherits the parent environment plus `CLAUDE_PROJECT_DIR`; stdin carries `cwd` ("the new directory after Claude runs `cd`"), `tool_input`, `permission_mode`; `updatedInput` replaces the whole input. Not stated in what was read: project hooks under `claude -p` in an untrusted folder, nested `claude -p` and `CLAUDECODE`, how an allow rule matches `VAR=… /abs/path` (step 11 settles what it needs). (b) `cursor-agent` 2026.10.01-e373342 under m1o K2 (`CURSOR_DATA_DIR=$B/cursor-data`; `-p --trust`, grok 4.6; never `-f`, `--yolo`, `--approve-mcps`), a throwaway `.cursor/hooks.json` and `cli.json` (`deny: ["Shell(touch *)"]`) in `$B/probe`, six runs: `{"permission":"allow"}` did not run the denied `touch` ("Command blocked by permissions configuration"); `{}`, an empty output and exit 1 each let `echo` run; asked to delete a file, the agent first used its `Delete` tool (`preToolUse`, `tool_name: "Delete"`, `tool_input: {"file_path": <absolute>}`), which `-p` without `--force` rejected whatever the hook said, then `rm -f … && echo`; `beforeShellExecution`'s `cwd` is `""` while the shell keeps an earlier `cd app` (`pwd` printed `…/probe/app`). Fixtures: `$B/fixtures/` (seven events). `~/.cursor/mcp.json` byte-identical; `~/.cursor/cli-config.json` was rewritten by cursor-agent (its caches, as m1l C1 records) — its `permissions` and `approvalMode` compared equal around the last run (a copy was kept only then; the earlier runs kept only a hash), today `allowlist`, `allow: ["Shell(ls)"]`, `deny: []`. ADR-0061 written from Q2 with these facts: Cursor's pass-through `{}`; Cursor routes absolute operands and its `Delete` tool's `file_path`; back-pointers in ADR-0037 (×2), ADR-0019, ADR-0024; index row; `go test ./internal/doccheck` ok.

### 7. The `rm` reader and the decision

- Builds: `internal/assistant/hook.go`, pure, with no I/O beyond `filepath.Glob` and `Lstat` under the root.
  - Decode one event: Claude Code's `PreToolUse` (`tool_name` `Bash`, `tool_input.command`, `cwd`); Cursor's `beforeShellExecution` (`command`, `cwd`); and Cursor's `preToolUse` `Delete` only in the shape step 6 recorded.
  - Recognise Q2 §2's simple `rm`:
    - a shell-word splitter for single and double quotes and backslashes;
    - flags `-r`, `-R`, `-f` and their combinations, `--`;
    - globs (`*`, `?` only) expanded relative to `cwd`, where a match never starts with a dot unless the pattern's component does, as in `sh` (Go's `filepath.Match` differs; a pattern holding `[` passes);
    - relative operands only if step 6 shows that the event's `cwd` follows a `cd`; otherwise only absolute operands route.
  - Map each target to a root-relative path.
  - Return pass, or route with the path list.
  - Render each client's answer:
    - the Claude deny JSON with its `permissionDecisionReason`;
    - Cursor's `permission` `deny` with the reason in `user_message` (m1o: `agent_message` does not reach the model);
    - each pass-through as step 6 settled.
  - The replacement command reaches the daemon the hook dialled, and no other:
    - the binary by the absolute path the hook itself was started as (`init` writes it by m1o D5's rule);
    - `call --no-autostart`;
    - `DROIDYARD_STATE_DIR='…'` inline when the hook's own state root came from that variable;
    - quoted for a POSIX shell. A path holding a single quote or a control character passes.
- Implements: Q2 §2, §3's message, §4.
- Check:
  - `TestRmFormsTable`, one row per form. Route: `rm a.kt`, `rm -rf build/x`, `rm -fr 'a b.kt'`, `rm -- -x.kt`, `rm src/*.kt`. Pass:
    - `rm -i a`, `rm -v a`, `sudo rm a`, `command rm a`, `/bin/rm a`, `\rm a`;
    - `rm a; ls`, `rm a && x`, `rm a | x`, `rm $X`, `` rm `x` ``, `rm $(x)`, `rm a > f`, `rm ~/a`, `rm {a,b}`;
    - `find . -delete`, `git rm a`;
    - `rm ../outside`, `rm /abs/outside`, a glob matching nothing, a symlink leaving the root, a newline in the command;
    - `rm dir` without `-r`, `rm -rf .`, `rm -rf a/..`, the root itself, `rm src/[ab].kt`;
    - `rm -rf build`, `rm -rf app/build/tmp`, `rm -rf .gradle` (Q2 §2's regenerable outputs).
    - A route row asserts that `rm src/*` leaves out `src/.hidden`.
  - Both clients' JSON from step 6's recordings (Claude's from its documented shape, labelled).
  - Malformed JSON → pass.
  - Seen failing:
    - with the operator check removed (`rm a; ls` routes);
    - with the root check removed (`rm ../outside` routes).
- Schema: no change.
- Needs: step 6.
- Not here: talking to the daemon (step 8).
- Rests on: what the event's `cwd` means after a `cd`, which step 6 must show at level 5 before relative operands route; a wrong reading would trash the wrong file. Also the clients' input shapes: level 5 for Cursor (step 6), level 2 for Claude Code until step 11.
- [x] Done. Evidence: `internal/assistant/hook.go` (`DecodeHookEvent`, `RouteRm`, `PassOutput`, `DenyOutput`, `Redirect.Message`), `TestRmFormsTable` (routes; passes for other commands and prefixes, operators and expansions, locations, build outputs) on a real temp tree, three recorded Cursor events and a labelled synthetic Claude Code event in `testdata/hooks`. Seen failing with a rule reversed: operator characters, the root check, the dot-name rule, the build names, the `-r` requirement, the link-target check, an absolute event `cwd`, a quoted or escaped `rm`; after the verifier's round, the case-folded build names (`Build`, `.Gradle` route) and BSD rm's flag order (`rm a.kt -r dir`, `rm a.kt -f`, `rm *.kt` over a `-x.kt` route). Opus verifier (with step 8): proceed.

### 8. `droidyard hook`

- Builds: `cmd/droidyard/hook.go`, wired as a subcommand (ADR-B §1; usage text): `droidyard hook --client claude|cursor --root <abs>`.
  - Read stdin, decide (step 7). On route:
    - dial the socket without auto-start;
    - call `workspace.open` on `--root` as the CLI does;
    - deny with the replacement.
  - On pass, a failed dial, an error, or 2 s elapsed: answer pass.
  - Never write to stdout anything but the one answer.
  - **Never exit 2**, which Claude Code reads as a deny:
    - a panic is recovered into the client's pass-through answer;
    - a usage error (an unknown `--client`, a missing `--root`) exits 1 with a note on stderr and nothing on stdout. Exit 1 is non-blocking for Claude Code by the reference, and for Cursor by step 6(b).
  - `mcp.md` §Features beyond tools, its paragraph and its table row (line 245): assistant-side hooks are adopted for one purpose, through `droidyard hook` (ADR-B).
- Implements: Q2 §1, §3, §4.
- Check: tests over a real daemon (`startInProcess`):
  - A routed `rm` gets the deny with the replacement. That exact string, run through `/bin/sh -c` with a `PATH` that lacks `droidyard` and with `DROIDYARD_STATE_DIR` unset in the shell, completes against the test daemon and moves the files to its trash. No other `daemon.lock` appears.
  - With no daemon on the state root, the same `rm` passes and no daemon is started (the lock is absent afterwards).
  - Pass when `workspace.open` is held past 2 s (the existing open hook).
  - Pass for a non-`rm` command, without dialling.
  - A panic forced through a seam, and an unknown `--client`, each leave stdout empty and the exit code not 2.
  - Seen failing:
    - with the dial switched to auto-start;
    - with `--no-autostart` dropped from the replacement (a daemon appears at the shell's default root);
    - with the panic recovery removed (exit 2).
- Schema: no change.
- Needs: step 7.
- Not here: writing the entries (step 9).
- Rests on: the hook never starts a daemon, never sends the agent to another daemon, and never blocks a command on its own failure. Level 3 here; level 5 in step 11.
- [x] Done. Evidence: `cmd/droidyard/hook.go`, `main.go`'s `hook`: decision in a goroutine under a 2 s bound, stdin capped at 1 MiB, the dial without auto-start, `workspace.open`, then the deny; every failure passes; exits only 0 or 1; a panic recovered. `hook_test.go` runs the re-executed binary with both clients' JSON: the deny's replacement run through `/bin/sh -c` with no `PATH` or state variable reaches the same daemon and trashes both files, no second lock anywhere; no daemon → pass and none started; 17 non-routed forms make no dial; an open held past 2 s and a stdin that never closes pass at about 2 s; misuse exits 1 with nothing on stdout. Seen failing: the dial with auto-start; `--no-autostart` dropped from the replacement; the recover removed (exit 2; rerun by the verifier). Opus verifier: proceed; N1 and N2 fixed by the main session (step 7's evidence); N3 below; N4 (Cursor's `Delete` deny reaching the model) stays Unverified.

### 9. `init` writes the hook entry; documents and register

- Builds:
  - `internal/assistant`:
    - `--client claude` adds to `.claude/settings.json` the element `hooks.PreToolUse[]` `{"matcher":"Bash","hooks":[{"type":"command","command":"<abs droidyard> hook --client claude --root '<root>'"}]}`.
    - `--client cursor` (if step 6 cleared it) writes `.cursor/hooks.json` `{"version":1,"hooks":{"beforeShellExecution":[{"command":"…"}]}}`, plus `preToolUse` `Delete` if step 6 showed its input. `version` is a `sole` key (`edit.go:43-46`, as `deny` is at `assistant.go:235`), so `--uninstall` keeps a user's `version` while their hooks remain.
    - Both use the existing merge and uninstall rules: project files only, never `~/.claude` or `~/.cursor`. `init` prints what it adds.
  - Register:
    - Row 167 is closed for the delete case by 2j and kept for the shell redirect and the session-start context.
    - New (limit, MCP/agents): the hook passes complex forms, so they bypass the trash, and it needs the default state root or a `DROIDYARD_STATE_DIR` in the hook's command (m1o: client hook environments do not carry it).
    - New (deferred): an OpenCode hook.
    - New (limit): after the binary's path changes, a later `init` adds a second hook entry, and `--uninstall` cannot remove the stale one (the rule ADR-0037 §2 already has for every entry).
    - Step 6's Cursor row, if any.
  - `liveBudget` is adjusted by the measured delta.
- Implements: Q2 §5; ADR-B §Consequences.
- Check:
  - `TestInitWritesTheHookEntry`, one test per client: first run Created or Updated; second run Unchanged; a user's own `PreToolUse` element survives both runs and `--uninstall`; `--uninstall` leaves the file as it was before `init`.
  - Seen failing with the hook element dropped from the client's spec, and with `containsJSON` bypassed (the second run appends a duplicate).
  - `go test ./internal/doccheck` is green.
- Schema: no change.
- Needs: step 8.
- Not here: user-level files (house rule).
- Rests on: —
- [x] Done. Evidence: `internal/assistant/hookentry.go` (`HookCommand`: the absolute binary, `--client`, `--root`, `DROIDYARD_STATE_DIR` inline when init's came from it; POSIX quoting round-tripped through `sh -c`), Claude's `hooks.PreToolUse` element (matcher `Bash`) in the project's `.claude/settings.json`, Cursor's `.cursor/hooks.json` (`version` 1 as a sole key, `beforeShellExecution`, `preToolUse` with matcher `Delete`); `init` prints the hook line. Tests: second run unchanged, the user's entries and keys kept, `--uninstall` restores the tree and removes only what init would write now, unmergeable files refused with the entry printed, `--dry-run` writes nothing, an OpenCode run writes neither file, a real `init` subprocess names this binary, the root and the state dir. Seen failing: `containsJSON` forced false; the Claude element, Cursor's sole `version` or the `Delete` matcher dropped. Documents: `mcp.md` §Features beyond tools; register rows (complex forms and `/bin/rm` pass; a moved binary leaves a stale entry; an OpenCode hook deferred); corpus at the budget by a cut. Main session's diff review.

### 10. Restore from Activity

- Builds: `web/src/lib/ops/`. Every completed `file.delete` row, from any client, offers Restore. Only on click does it read the operation's result (`operation.wait` with `timeoutMs` 0) and call `file.restore` with the done items' `trashId`s. The row then shows each item restored, or its failure code, or "nothing to restore" when no item was done. When the wait answers `OPERATION_NOT_FOUND` (past the 15 minutes or 200 operations it keeps, `operation.go:58-59`), the row says the result is past retention and names `droidyard call file.restore` with the trash ids from the CLI.
  - Before writing: the two vendored Svelte skills, then `pnpm -C web run verify` and the autofixer gate (`AGENTS.md`).
- Implements: Q3.
- Check: Vitest tests:
  - Restore is offered on a `client=cli` delete.
  - It calls `file.restore` with exactly the done ids.
  - A `FILE_NOT_IN_TRASH` item is shown, not swallowed.
  - `OPERATION_NOT_FOUND` shows the past-retention message.
  - Seen failing with the client filter set to `ui` only.
- Schema: no change.
- Needs: step 3 (the UI's calls unchanged), independent of 7–9.
- Not here: a trash view (register row 263).
- Rests on: `operation.wait` returning any operation's result by id, register row 183's limit, staying as it is.
- [ ] Done. Evidence:

### 11. Level 5: an agent's `rm` lands in the trash, and the UI restores it

- Builds: nothing. Run by the main session under R1–R4, in a fresh copy.
- Implements: A7 and A9 at level 5; Q2 §3 with a real client.
- Run: `droidyard init --client claude --root <copy>`. The run's copy of the hook command gets `DROIDYARD_STATE_DIR=…` inline, a kit edit recorded as m1o's was. One `claude -p` run (a second only if the first is void):
  - with the owner's model;
  - in the copy, with `--allowedTools` holding `Bash(rm:*)` and a rule derived from the replacement's rendered prefix (step 6(a) says how a rule matches an env-prefixed command), so that only the hook can stop the `rm`;
  - with `--max-turns 6`;
  - prompted to delete one named `.kt` file with `rm`;
  - with `~/.claude/settings*.json` byte-identical before and after. Claude Code writes its own transcripts under `~/.claude/projects/`, which is expected. The owner's own user-level hooks may fire, and are recorded.
  - Then, in the browser, Restore on Activity's row.
  - Cursor: the same with `cursor-agent` under m1o K2, if step 6 cleared it.
- Check:
  - The transcript shows the `rm` denied with the replacement, and the agent running it.
  - `daemon.log` has a `client=cli file.delete … completed`.
  - The file is in `<state>/ws/<id>/trash` and absent from the copy.
  - After Restore it is back with its bytes.
  - No `daemon.lock` other than the run's appears, and R2 holds. Zero orphans.
  - The evidence updates register rows 158 (`init --client claude` not run in Claude Code) and 162 (level-5 Claude Code hook runs) at the close. Pass-through is step 8's, at level 3.
- Schema: no change.
- Needs: steps 5, 8, 9, 10.
- Not here: the Opus verifier's review at the close (`milestone-workflow`; a contract change), which reruns what it chooses.
- Rests on: Claude Code shows the deny reason to the model, and the model runs the replacement (level 5, here).
- [ ] Done. Evidence:

## Not in this milestone

- An OpenCode hook. ADR-0037 records OpenCode's plugin shape; it was not asked for. A register row.
- A trash view, emptying the trash, Undo after a reload: register row 263.
- Removing stale `file_*` allow rules from users' configs: `init` only adds (ADR-0037). A register row.
- Re-running the golden path or the comparison with real agents: the Definition of done stands (m1l, m1m). A register row.
- Rewriting the command (`updatedInput`, `updated_input`): Q2's alternatives.

## Design questions

The substance is the owner's: removing the tools, and having an agent's deletes reach the trash. The mechanisms are drafted below for the main session to apply under the standing instruction (Delegated). None of them blocks.

### Q1. How do the file capabilities leave MCP?

- Blocks: steps 1–5.
- The documents say:
  - Audience `human` keeps a capability off MCP, enforced by client kind (ADR-0015 §6), and ADR-0045 §4 added a second reason.
  - m2g Q1 and ADR-0053 §Alternatives considered rejected it for the file operations: "neither reason applies, and the agent's shell already moves files, uncontained".
  - ADR-0053 §1 made them tools under `all`. ADR-0036 Decision 4's core profile is m1o D3, which includes three file tools. ADR-0058 §Consequences: "Agents get the protection by passing back `file_read`'s revision".
  - `capability-api.md` §Discovery and versioning: "a breaking change to one is a new name or a major version".
- Draft decision, a new ADR:

> **ADR-A: The file capabilities are not MCP tools**
>
> - Status: Accepted · Date: 2026-10-12
> - Supersedes: ADR-0053 §Decision (in part: item 1's "MCP tools under `--tools all`"; item 2's text rendering and "a successful MCP call")
> - Supersedes: ADR-0053 §Consequences (in part: the first bullet, an agent's moves and deletes through the daemon; the third, `tools/list` growing by five)
> - Supersedes: ADR-0036 §Decision (in part: item 4, the core profile without `file_list`, `file_read`, `file_write`)
> - Supersedes: ADR-0058 §Consequences (in part: the agents' protection through `file_read`'s revision)
> - Relates to: ADR-0015 §6, ADR-0045 §4, ADR-0037 Decision 4, ADR-0054; plan m2j Q1
> - Decided by the owner, 2026-10-12: "No quiero gastar tokens en definiciones para algo que todos los agentes a día de hoy soportan"; "quítalos, sí". The mechanism is Delegated.
>
> **Context.** Eight of the nine `file.*` are MCP tools (`file.watch` is streaming). They take about 6.4 KB of the 34,786 B of `tools/list` a model reads (`TestToolListModelFacingBytes` at `9ec50b0`), paid on every session. The agents the owner uses read, write, edit and delete with built-in tools (Claude Code's Read, Write, Edit, Bash; Cursor's; OpenCode's). That is the owner's statement, not measured. No usage data on agents and these tools exists. ADR-0053 kept them for agents because a delete through the daemon is contained and undoable. The owner puts the tokens first. An agent's own `rm` reaching the trash is ADR-B's concern.
>
> **Decision.**
> 1. All nine `file.*` have audience `human`. No MCP profile lists them, mcp-go answers a call "tool not found", and the core refuses them from an MCP session (`REQUEST_NOT_ALLOWED_HERE`, checked before statefulness, so `file.watch` gets it too). The UI (`client=ui`) and the CLI (`client=cli`, an agent's shell included) call them as before, with the same parameters, results and codes.
> 2. Audience `human` means "not on the agent surface", for any of three reasons: an agent must not authorise itself (ADR-0015 §6); a window on the user's screen (ADR-0045 §4); every agent has an equivalent already (here). No new audience value.
> 3. The renderings of `file.move`, `copy`, `delete` and `restore` go, since only MCP read them. The core profile is the golden path without file tools: 22 tools.
> 4. API `0.21.0`. `capability-api.md` says a breaking change is "a new name or a major version". Before 1.0 the major is the minor, as ADR-0036's changed `maxLines` default went out as 0.13.0. There is no `deprecated` period: no capability is removed or renamed, only the MCP projection of eight. `maxToolListBytes` drops to the new size rounded up to the next 1,000 B, so the saving is held.
>
> **Consequences.**
> - An agent's edits and deletes bypass the daemon: no containment, no Activity record, no `ifRevision`. The tree still sees them (`file.watch` polls), and the editor's `ifRevision` still refuses a save over an agent's write. ADR-B routes an agent's simple `rm` to the trash.
> - The "droidyard only" comparison arm (m1l C2, m1o D1) cannot be run as defined.
> - Allow rules from an earlier `init` stay (`init` only adds; `--uninstall` removes what it would write now). They name no tool.
> - Code: `internal/domain/file`, `adapters/mcp` `CoreTools`, the `cmd/droidyard` tests and budget, the skill map. Documents: `mcp.md` §What is exposed, §Results, §Annotations; `files.md` §Why this exists; `vision.md` §Principles #1; `overview.md`; `mvp.md` §MVP capability surface; the register.
>
> **Alternatives considered.**
> - **Keep them** (ADR-0053's reasoning): containment and Undo for an agent's delete, at about 6.4 KB per session for work agents do with their own tools. Wins if usage data shows agents calling `file_*`.
> - **Only out of `core`, or keep `file_read` and `file_write`**: the default `all` still pays, and the owner asked for all nine.
> - **An exclusion list in the MCP adapter, audience left `all`**: the published schema would say `all` for capabilities no session lists. `init`, the skill map and the schema would then need a second source.
> - **A new audience value (`ui`, `local`)**: every adapter would treat it exactly as `human`. Wins if a capability must one day reach the UI and not the CLI, or the reverse.

### Q2. How does an agent's `rm` reach the trash with no MCP tool?

- Blocks: steps 6–9, 11.
- The documents say:
  - `mcp.md` §Features beyond tools: assistant-side hooks are "not adopted", and "may return later as optional adapters for a specific assistant if a real need appears".
  - ADR-0037 §Alternatives considered: the shell-redirect `droidyard hook` was measured unnecessary.
  - ADR-0019 §Processes 3 and 4: the subcommand set and auto-start.
  - ADR-0004: optional adapters only.
  - Nothing on deletes.
- Draft decision, a new ADR (amended by step 6's facts before it is written):

> **ADR-B: An agent's `rm` is redirected to the trash by an optional assistant hook**
>
> - Status: Accepted · Date: 2026-10-12
> - Supersedes: ADR-0037 §Decision (in part: item 2, `init` also writes a hook entry for `claude` and `cursor`)
> - Supersedes: ADR-0037 §Consequences (in part: assistant-side hooks "not adopted")
> - Supersedes: ADR-0019 §Processes (in part: item 3, the subcommand set gains `hook`; item 4, `hook` never auto-starts)
> - Supersedes: ADR-0024 §Decision (in part: the subcommands row gains `hook`)
> - Relates to: ADR-0004, ADR-0015 §6, ADR-0054, ADR-A; plan m1o step 1 (a); plan m2j Q2
> - Decided by the owner, 2026-10-12 ("analiza cómo es con cursor para meterlo también"): an agent's deletes reach droidyard's trash with no MCP tool. The shape is Delegated.
>
> **Context.** After ADR-A, an agent deletes with its own tools. Claude Code has no delete tool, so a delete is a Bash `rm`. Cursor deletes through its shell or its `Delete` tool. Each client runs a project hook before a shell command, and a deny's text reaches the model ([obs] Cursor, m1o step 1 (a); Claude Code, step 6 (a)). ADR-0037 rejected a hook that redirects shell commands to tools for two reasons. No agent ran a redirectable command. And it would put three clients' formats and a command-line parser in the core. A delete is a different need: the owner asks for it, and no measurement says how often agents `rm`. The parser recognises one command word with three flags, and it lives in `internal/assistant` beside `init`, outside the core and the adapters.
>
> **Decision.**
> 1. `droidyard hook --client claude|cursor --root <abs>` reads one hook event on stdin and answers within 2 s. It never auto-starts the daemon. It is not a security boundary: `/bin/rm`, `sh -c` or `find -delete` pass.
> 2. It routes only a simple `rm` whose every target would be deleted by `rm` itself and lies inside `--root`:
>    - the command word exactly `rm`;
>    - flags among `-r`, `-R`, `-f`, and `--`;
>    - operands as plain or quoted words, or `*` and `?` globs expanded as `sh` does (no dot-names unless asked for);
>    - relative operands only if the event's `cwd` follows the shell's `cd` (step 6).
>
>    Anything else passes untouched:
>    - any shell operator or expansion, another flag, a prefix (`sudo`, `command`, a path);
>    - a target outside the root, matching nothing, `.`, ending in `..`, or the root itself;
>    - a directory without `-r`;
>    - a target that is or lies under `build`, `.gradle`, `.kotlin` or `.cxx`. These are regenerable outputs, and trashing them would evict the user's older items under ADR-0054 §3's 2 GiB cap.
> 3. For a routed `rm`, if a daemon answers on the socket, the hook opens the workspace and denies the command with one message: run `<abs droidyard> call --no-autostart --workspace '<root>' file.delete --params '{"paths":[…]}'` instead, which moves them to droidyard's trash, where the user can restore them. `DROIDYARD_STATE_DIR` is inline when the hook's root came from it, so the replacement reaches the daemon the hook dialled and never starts another.
>    - **The hook deletes nothing.** The agent's replacement goes through the client's own approval like any shell command, and the agent reads each item's outcome in its result.
>    - The hook's `workspace.open` happens before approval. It runs no project code (ADR-0015 §5).
>    - With no daemon, an error, a timeout or malformed input, the command passes (fail open).
> 4. Pass-through:
>    - Claude Code: exit 0 with no output.
>    - Cursor: the answer step 6 shows neither approving nor blocking another command, or no Cursor entry at all.
>    - A usage error exits 1 and a panic is recovered. The hook never exits 2, which Claude Code reads as a deny.
> 5. `droidyard init --client claude|cursor` writes the entry into the project's `.claude/settings.json` or `.cursor/hooks.json` only, never a user-level file, with ADR-0037's merge, keep and uninstall rules. It is on by default with the client's other entries.
>
> **Consequences.**
> - A routed `rm` costs the agent one more turn and one approval for the replacement. A complex form, `/bin/rm` or a build output bypasses the trash. ADR-0054's caps apply to what is routed.
> - The replacement must name the binary and the state root, so it reaches the daemon the hook dialled. The in-hook delete would have needed neither.
> - A client hook's environment lacks `DROIDYARD_STATE_DIR` [obs, m1o]: a custom state root needs it written into the command (register).
> - Two clients' hook formats live in `internal/assistant`. A client that changes its format loses the redirect silently, never the agent's work (fail open).
> - Documents: `mcp.md` §Features beyond tools; the register.
>
> **Alternatives considered.**
> - **The hook trashes the files itself, then denies** (the coordinator's sketch): one turn fewer. But `PreToolUse` runs before the permission prompt (Claude Code's reference: exit 0 "continues through the normal permission flow"), so an `rm` the user would have refused still happens (to the trash), in any permission mode. The hook must also own a partial failure and a long-running delete inside a hook's timeout. Wins if the owner prefers the turn saved and accepts the approval skipped.
> - **Rewriting the command** (Claude Code's `updatedInput`, Cursor `preToolUse`'s `updated_input`): a reported Claude Code bug ignores it in some versions, and `beforeShellExecution` cannot modify. Wins when both clients honour it.
> - **A `file_delete` MCP tool only**: the token cost ADR-A removes, and agents reach for `rm` anyway.
> - **A shell function or a `rm` shim on `PATH`**: it alters the user's shell for every program, invisibly.
> - **User-level hook files**: they act in every project, and the house rule forbids them.
> - **An OpenCode plugin**: not asked; a register row.

### Q3. How does the user undo an agent's delete in the UI?

- Blocks: steps 10, 11.
- The documents say: the UI's Undo is the tab's own stack (m2g; `undo.ts`). "A trash view, emptying the trash by hand, Undo after a reload" is deferred (register row 263). `operation.wait` returns any operation's result by id (register row 183).
- Draft decision (the plan's, no ADR: no contract changes): Activity offers Restore on every completed `file.delete` row, of any client. On click it reads the result through `operation.wait` and calls `file.restore` with the done ids. Past `operation.wait`'s retention (15 minutes or 200 operations) it names the CLI fallback. This rests on register row 183's limit staying. Rejected:
  - a trash view, which is larger, and its row stays deferred;
  - the CLI only (`droidyard call file.restore`), which misses the owner's "undo in the UI";
  - folding foreign deletes into the tree's Undo stack, which would undo another client's work from Ctrl+Z without the user seeing whose it was.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| Claude Code's hook facts: pass-through, deny shape, reason to the model, before the permission prompt, exit codes. Read from the official reference by the round-2 reviewer; not run | 7, 8, 9 | step 6 (a) re-reads them; step 11, one real run |
| What an event's `cwd` is after a `cd`, in each client | 7 (relative operands) | step 6; until shown, only absolute operands route |
| Project hooks run under `claude -p` in a copy never trusted; how an allow rule matches an env-prefixed command | 11 | step 6 (a) |
| Cursor's neutral answer, and whether `allow` overrides `cli.json` | 9 (Cursor half) | step 6 (b); without it, no Cursor entry and a register row |
| The `tool_input` of Cursor's `Delete` tool | 7 (that branch) | step 6 (b) |
| Agents run the replacement command rather than another way to delete | none (fail open) | step 11, one Claude Code run; more runs are a register row |
| Agents edit with their own tools without stumbling on a skill copy that predates 0.21.0 | none | a real session per client; a register row |
| The 6.4 KB is model tokens saved in each client (Cursor shows every description and `inputSchema`, ADR-0036; Claude Code not observed) | none | a client's token report; a register row |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | B1: step 2 omitted `TestFileWatchNeedsASession`'s MCP leg and `TestCopyTextLeadsWithTheFailures` | step 3 lists both; `prepare`'s order is kept and stated |
| 1 | revise | B2: A5 claimed more than the scan; `policy.go:512` names `policy.update` | `internal/policy` is scanned with a reasoned `policy.update` exception; A5 is restated |
| 1 | — | N3: ADR-0053 §Consequences is not superseded | Q1 supersedes bullets 1 and 3; step 1 adds the back-pointer |
| 1 | — | N4: versioning skipped the governing sentence | ADR-A §4 quotes it and says why there is no `deprecated` period |
| 1 | — | N5: nothing fails if the `mcp.md` row is dropped | step 4 adds the reverse doccheck |
| 1 | — | N6: one word of corpus headroom | steps 4 and 9 raise by the measured delta, with a comment |
| 1 | — | N7: run limits narrower than m2i K2 | R2 cites K2 whole: every daemon not this run's |
| 1 | — | N8: step 2 larger than needed | split into step 2 (green either side) and step 3 (atomic) |
| 1 | — | nits: `workspace.open` first; exactly nine; ADR-A in comments; `goldenPath` comment; ADR-0037 alternative; row 143 narrowed; comparison-arm row | all taken |
| — | — | Scope added after round 1 (owner, 2026-10-12): the delete-to-trash hook | steps 6–11, Q2, Q3; round 2 reviewed it in full |
| 2 | revise | B1 (B2 again): `policy.get` is named in `update.go:296`; step 3 did not say which list loses `file` | `policy.get` joins the exceptions with its reason, and a stale-exception loop is added; step 3 drops `file` from both lists |
| 2 | revise | B2: the replacement could reach another daemon or auto-start one (no `PATH`, no `DROIDYARD_STATE_DIR` in client environments, m1o) | absolute binary, `--no-autostart`, the state root inline (step 7, ADR-B §3); step 8 runs the string through `/bin/sh -c` with neither; step 11's allow rule is derived from it |
| 2 | revise | B3: a panic or usage error exiting 2 would block every Bash command | step 8: panic recovered into pass, usage error exit 1, never 2, with tests and mutations |
| 2 | revise | B4: the reader could trash dot-files, a directory without `-r`, `.`, `..` or the root | `sh` glob rules, `[` passes, and those operands pass (step 7 rows; ADR-B §2) |
| 2 | revise | B5: `cwd` after a `cd` is unproven, so the wrong file could be trashed | step 6 asks it of both clients; only absolute operands route until it is shown; step 7's `Rests on` |
| 2 | — | N6 the departure confirmed by the reference; N7 the trash cap; N8 retention; N9 `version` sole and the mutation; N10 the run limits; N11 Cursor's `Delete`; N12 split; nits | ADR-B costs added and 6(a) widened; build outputs pass; Restore on click with a past-retention message; `version` sole, real mutations, a register row; `settings*.json` byte-identical, the `ls` line dropped; 6(b) asks which tool; split offered to the owner; comments, `mcp.md:245`, rows 158/162, ADR-B's open-before-approval and parser answer |

## Changes

- Steps 7–8 (the verifier's N3, Delegated): Claude Code's relative operands route on its reference's statement that `cwd` follows a `cd` (ADR-0061 §2), not on a step 6 run; step 11 adds a `cd` into a subdirectory in one Bash call and a relative `rm` in the next, and checks the routed path.

- Step 7: `rm -rf build/x` passes, as ADR-0061 §2 says ("is or lies under `build`"); the Route list's `build/x` example was inconsistent with the pass list's `app/build/tmp`.

None yet.
