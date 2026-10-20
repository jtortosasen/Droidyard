# Harness integration: would hooks and fine-grained controls make agents on Droidyard faster and cheaper?

Research note, 2026-10-07. Read-only; nothing in the repository changed. Labels:
**[doc]** a vendor document says so; **[obs]** observed on this machine (installed
files, our run logs); **[inf]** inferred, not checked. Versions on this machine:
Cursor app 3.23.23, `cursor-agent` 2026.10.01-e373342, OpenCode v2.0.18, Claude Code
2.1.292, Codex CLI 0.157.1.

## 0. Two corrections to the premise

1. **"~15k tokens of tool definitions re-sent every turn" holds for no client we care
   about as configured today.**
   - **Cursor IDE and CLI defer MCP tool definitions.** The static context holds only
     tool names. Each server's descriptors are written to files that the agent reads
     when it needs one. [doc] cursor.com/blog/dynamic-context-discovery (2026-01-06)
     reports "reduced total agent tokens by 46.9%". Cursor staff confirmed it in
     forum.cursor.com/t/166405 (2026-07-23). The CLI got the same deferral by
     2026-09-09 [doc] (forum.cursor.com/t/165642).
     [obs] the descriptor files are in `~/.cursor/projects/<ws>/mcps/<server>/`:
     `tools/<name>.json` (name, description, arguments and **outputSchema when the
     server sends one**, seen for 263 firebase tools), `INSTRUCTIONS.md` (the server's
     `instructions`) and `SERVER_METADATA.json`. The writer is in `cursor-agent`
     `index.js` near offset 5101873.
   - **Claude Code** defers MCP tools behind ToolSearch by default [doc]
     code.claude.com/docs/en/mcp. [obs] this session lists deferred tools by name only
     and carries each server's instructions in the system prompt.
   - **OpenCode v2.0.18** has a **Code Mode** that is on by default per MCP server.
     [obs] binary config schema: `codemode`, "Expose this server's tools through Code
     Mode. Defaults to true." MCP tools are reached through an `execute` tool with
     `search({query})`. Our comparison kit set `"codemode": false`
     (`~/Library/Caches/droidyard-dev/m1m/kit/reset.sh:19`).
   - [obs] What the comparison paid: the droidyard arm's **first** request was
     10.4 k input tokens and the shell arm's 11.7 k (`runs/*/out.jsonl`, step 1). The
     droidyard arm's extra input therefore builds up over the run, from tool results
     and longer runs (dy4 and dy5 had 31–32 steps). Either the 34 schemas were not
     sent in full, or they cost less than the built-in tools the droidyard arm had
     denied. The run logs cannot tell these apart. [inf]
2. **What we actually ship is 130 KB, not 61 KB.** [obs] `tools/list` in
   `runs/dy1/wire.s2c.jsonl` (commit f501c25) is 130.0 KB of compact JSON:
   - descriptions 32.8 KB;
   - inputSchemas 22.0 KB;
   - **outputSchemas 75.2 KB**;
   - 59.3 KB without the outputSchemas, which is the "61 KB" figure.

   The largest descriptors are `device_tapElement` (16.9 KB: description 1.7 KB,
   outputSchema 10.1 KB), `device_inputText` (16.7 KB) and `device_logs` (8.7 KB,
   with a 2.3 KB description). Our own rule is "One or two sentences"
   (`docs/interfaces/mcp-tool-surface.md` §Descriptions). The current descriptions
   break it: the longest run 1.2–2.3 KB.

   So the token lever is not "stop re-sending 15k every turn". It is **the size of each
   descriptor when it is read**: in Cursor a read is a file read that stays in the
   context, and it includes the outputSchema. The lever is also **the number of calls**.

## 1. Cursor (priority 1)

### What exists

| Mechanism | Facts | Source |
|---|---|---|
| Hooks file | `.cursor/hooks.json` (project) and `~/.cursor/hooks.json` (user), `{"version":1,"hooks":{event:[{command,type:"command"\|"prompt",matcher,timeout,failClosed,loop_limit}]}}`. Matchers are JS regex: on the command string for shell events, on the tool type (`Shell`, `Read`, `MCP: …`) for pre/postToolUse. Exit code 2 blocks; other failures fail open unless `failClosed`. | [doc] cursor.com/docs/agent/hooks (fetched 2026-10-07, undated); [obs] `~/.cursor/skills-cursor/create-hook/SKILL.md:28-229` (bundled with app 3.23.23) |
| Events | sessionStart/End, preToolUse, postToolUse, postToolUseFailure, subagentStart/Stop, before/afterShellExecution, before/afterMCPExecution, beforeReadFile, afterFileEdit, beforeSubmitPrompt, preCompact, stop, afterAgentResponse/Thought, workspaceOpen, plus Tab events | same |
| Outputs | beforeShell/MCPExecution: `permission` allow\|deny\|ask, `user_message`, `agent_message`. preToolUse: permission, `updated_input`, agent_message, and also `additional_context` [obs, not documented]. postToolUse: `additional_context` and `updated_mcp_tool_output`. sessionStart: `env`, `additional_context`. stop: `followup_message` (5 automatic follow-ups by default, set by `loop_limit`). **afterFileEdit and afterShell/MCPExecution can return nothing.** | [doc] hooks page; [obs] `cursor-agent` `index.js` near offset 4241539 |
| **Hooks in `cursor-agent`** | The docs say nothing on this. [obs] the 2026.10.01 bundle calls `executeHookForStep` for every agent event above and reads `.cursor/hooks.json`. In the CLI's shell path, **deny builds the error from `user_message`** (`190.index.js` ~offset 97000: `j("Command execution", i.user_message)`), so a redirect text belongs in **both** user_message and agent_message. In the MCP path only `deny` is handled (`190.index.js` 97275). | [obs] |
| Claude Code hooks | Cursor also loads hooks from `.claude/settings{,.local}.json` and `~/.claude/settings.json`. This needs the setting "Include Third-Party Plugins, Skills, and Other Configs". The mapping is `permissionDecisionReason` → **`user_message`**. Notification and PermissionRequest are not supported. | [doc] cursor.com/docs/reference/third-party-hooks; [obs] `190.index.js` offset 74040 |
| Rules | `.cursor/rules/*.mdc` with `description`, `globs` and `alwaysApply`, in four modes (always, intelligent, by file, manual). `AGENTS.md`, root and nested. The CLI also applies `AGENTS.md` and `CLAUDE.md` at the root. | [doc] cursor.com/docs/context/rules; cursor.com/docs/cli/using |
| Skills | `.agents/skills/` and `.cursor/skills/` (project and `~`), and `.claude/skills/` and `.codex/skills/` "legacy". Name and description sit in the context; the body loads on demand. Frontmatter takes `disable-model-invocation` and `paths` globs. | [doc] cursor.com/docs/context/skills |
| MCP | `.cursor/mcp.json` or `~/.cursor/mcp.json`, stdio `{type,command,args,env,envFile}`. Tools, Prompts, Resources, Roots, **Elicitation** and Apps are all marked "Supported". Approval is required by default. Servers can be toggled on and off. | [doc] cursor.com/docs/context/mcp |
| CLI permissions | `~/.cursor/cli-config.json` and `<project>/.cursor/cli.json`, with `permissions.allow` and `permissions.deny` lists of `Shell(cmd)`, `Read()`, `Write()`, `WebFetch()` and `Mcp(server:tool)` entries. **Deny wins.** | [doc] cursor.com/docs/cli/reference/permissions |
| CLI MCP management | `agent mcp list`, `list-tools <id>`, `enable` and `disable` (whole servers). `--approve-mcps`, `--plugin-dir`. | [obs] `cursor-agent --help` |
| Tool limits | The old cap of 40 tools is a warning now, not a limit [doc-forum, older threads]. Deferral makes the count matter little. No limit on description length is documented. | forum.cursor.com/t/67976, /t/165642 |
| MCP instructions | They are written to `INSTRUCTIONS.md`. The CLI's context sections include `mcp_instructions` and `mcp_file_system` (`7923.index.js`). The prompt is assembled on the server, so **whether the instructions are always in the context is [inf]**. | [obs] |

[obs] Droidyard has never been run in Cursor on this machine: no `mcps/*droidyard*`
directory exists. Every Cursor number below is an estimate.

### What each mechanism buys Droidyard

**(a) Tokens**

- In Cursor, cutting or disabling tools saves little, because names are cheap
  (34 names ≈ 0.5 KB).
- The cost is each **descriptor read**. The twelve golden-path descriptors come to
  68 KB with outputSchemas and 33 KB without [obs]. Cursor copies the outputSchema into
  the file.
  - Trimming descriptions to our own one-or-two-sentence rule takes the whole surface
    from 59 KB to about 32 KB.
  - For a golden path in Cursor that is about 15 KB, roughly 4 k tokens, once per
    session. Every later step then carries it cached. [inf]
- Moving the usage guidance into one skill costs about 100 tokens of name and
  description per session, plus the body (a few KB) when it is loaded.
- These are **server-side changes. No hook is needed**, and every client benefits.
- The outputSchemas (75 KB) are the largest single block. Shrinking them touches the
  contract (`mcp-tool-surface.md` §Schemas on the wire), so that is an ADR question,
  not a recommendation.

**(b) Wasted calls.** This is where hooks earn their place.

- A `beforeShellExecution` hook with a matcher such as `(^|[\s;&|])(adb|\./gradlew|gradlew|gradle)(\s|$)`:
  - it denies only the commands that have a droidyard equivalent, and says which one:
    `adb install` → `device_install`, `adb shell input`/`uiautomator dump`/`screencap`
    → `device_tapElement`/`device_uiTree`/`device_screenshot`, `adb logcat` →
    `device_logs`, `./gradlew assemble…` → `build_assemble`;
  - it allows everything else, and fails open.
  - It turns a fallback episode into a single denied call. In our shell arm a fallback
    episode (dump → parse → input → dump again) ran 3–5 calls, at 3.8 s per dump and
    3.4 s per input [obs, m1m medians].
- The always-on, zero-risk counterpart is two lines in `AGENTS.md`, which costs about
  40 tokens per session.
- A **`sessionStart` `additional_context`** with cheap project facts (modules,
  variants, AVDs and devices online, policy state) would save the 1–3 orientation calls
  seen in every droidyard run (`project_modules`, sometimes `emulator_list`,
  `device_list`, `system_capabilities`) [obs, runs dy1–dy5].
  - Those facts come from the project and are untrusted data
    (ADR-0015 (`docs/adr/0015-untrusted-data-authorization.md`)),
    so they are framed as data.
  - Nothing read from a device is injected.
- **A `stop` hook with `followup_message`** ("Kotlin files changed since the last build:
  run build_assemble") catches handing over code that does not compile. It costs one
  call when it fires, and it contradicts "build after editing should not be automatic"
  (`mcp-features.md` §8). Off by default.
- `afterFileEdit` cannot speak to the agent: it has no output fields [doc]. It is only
  useful for recording state for the `stop` hook.
- `postToolUse` on Shell could add "use build_lastResult for structured errors" after a
  raw `gradlew` that was allowed. It is minor.

**(c) Speed, said plainly.**

- Hooks do not make `device_inputText` (7.3 s), `device_tapElement` (5.5 s),
  `emulator_start` (17.6 s) or a Gradle sync (8.5 s) any faster. Those are device and
  Gradle time [obs, m1m medians]. Each hook also adds a process spawn (tens of ms).
- What can get faster:
  1. **Fewer model steps.** droidyard median: 148 s wall, of which 80 s was tool time,
     so about 68 s over about 20 steps, **≈3.4 s per step** (DeepSeek in OpenCode; Grok
     in Cursor will differ). Each avoided call saves that plus its own tool time.
     Estimated per run: 1–3 orientation calls, plus 0–N fallback episodes of 3–5
     calls. That is 5–25 s on a 150 s run [inf].
  2. **Hiding cold-start latency.** A `sessionStart` hook can start the daemon and
     begin Gradle configuration in the background (`droidyard call …&`), so the first
     `project_modules` does not pay 8.5 s. It must never boot an emulator unasked: that
     is a device policy, and the owner's emulators are at stake.
  3. **Human approval latency in the IDE.** Approval is on by default for MCP. A
     generated allowlist of the **read-only** droidyard tools (from `readOnlyHint`) in
     `.cursor/cli.json` `permissions.allow` (`Mcp(droidyard:device_uiTree)` and so on)
     removes the prompts on the calls made most often. This is likely the largest
     wall-clock gain in interactive use, and zero in `--force` runs. Destructive tools
     stay prompted.

## 2. OpenCode v2.x (priority 2)

| Mechanism | Facts | Source |
|---|---|---|
| Plugins | JS/TS modules in `.opencode/plugins/` or `~/.config/opencode/plugins/`, or npm packages. Hooks: `tool.execute.before` (can mutate arguments, or **throw to block**), `tool.execute.after` (can mutate output), `shell.env`, session/message/file events, `experimental.session.compacting` (injects context into compaction). No documented hook injects context at session start. | [doc] opencode.ai/docs/plugins (updated 2026-10-07) |
| Permissions | Keys `bash`, `edit`, `read`, `glob`, `grep`, `task`, `skill`, `webfetch`, `external_directory` and others, with allow/ask/deny. Bash globs, **last match wins**. Per-agent overrides. What the model is told on a deny is not documented. | [doc] opencode.ai/docs/permissions; [obs] our kit used `"*platform-tools/adb*": "deny"` and `kill *` patterns, which held under `--auto` (m1m step 5 a0) |
| MCP | `mcp.<name>` takes `{type:"local", command[], environment, enabled, timeout, cwd}`. `tools: {"droidyard_*": false}` turns tools off globally or per agent. **`codemode`** defaults to true [obs]. Server `instructions` are rendered into the context, with change diffs [obs: `@opencode/McpInstructions`, "New MCP server instructions are available…"]. | [doc] opencode.ai/docs/mcp-servers; [obs] binary |
| Skills | `.opencode/skills/`, **`.claude/skills/`** and **`.agents/skills/`**, project and global. Names and descriptions sit in the `skill` tool's description. Controlled by a `skill` permission. | [doc] opencode.ai/docs/skills |
| Instructions | `AGENTS.md`, and an `instructions` list in the config [obs: config field `instructions`]. | [obs] |

Findings from our own runs [obs]:

- **10 of 135 droidyard-arm calls (7 %) were `execute`/`search()` calls that returned
  `{"items":[]}`.** The agent looked for droidyard tools through Code Mode although
  `codemode:false` had exposed them directly. OpenCode's own catalog text says "Do not
  call `execute` unless there is at least one available Code Mode tool"; the model
  ignored it.
- Cheap fixes:
  - deny `execute` in that configuration;
  - or test `codemode: true`: a compact catalog plus search could also cut input
    tokens.
  - An A/B in the m1m kit settles which is better. One line of server instructions
    ("droidyard tools are called directly by name") is the client-agnostic hedge.
- Transfer of the Cursor plan:
  - the AGENTS.md lines and the skill transfer unchanged;
  - the shell redirect is a `permission.bash` deny pattern (no explanation reaches the
    model [inf]) or a 20-line plugin whose `tool.execute.before` throws with the
    mapping text (whether the thrown text reaches the model is [inf]: test it);
  - the facts at session start have no clean equivalent: put them in instructions or
    the skill.

## 3. Where clients converge (one integration, several clients)

| Artifact | Cursor | OpenCode | Claude Code | Codex |
|---|---|---|---|---|
| `AGENTS.md` | yes [doc] | yes [obs] | reads `CLAUDE.md` (AGENTS.md through an import [inf]) | yes |
| `.agents/skills/<name>/SKILL.md` | yes [doc] | yes [doc] | not verified; ship a copy in `.claude/skills/` (Cursor and OpenCode also read it) | yes [doc] developers.openai.com/codex/skills |
| MCP `instructions` | written to INSTRUCTIONS.md [obs]; in the context [inf] | rendered [obs] | in the system prompt [obs, this session] | not verified |
| Deferred tool loading | yes [doc] | Code Mode by default [obs] | ToolSearch by default [doc] | not verified |
| Hook schema | native camelCase (`permission`, `agent_message`); also loads Claude's format | JS plugin | `PreToolUse` `permissionDecision`/`additionalContext` | **same shape as Claude's** (`PreToolUse`, `permissionDecision`, `additionalContext`, `Stop` `decision:block`), in `.codex/hooks.json`, trust review required [doc] learn.chatgpt.com/docs/hooks |
| `tools/list_changed` | not verified | catalog diffs [obs] | yes [doc] | not verified |
| Tool annotations → approval | not verified | — | not verified | not verified |
| Elicitation | supported [doc] | not verified | supported [doc] | not verified |

So:

- **instructions + AGENTS.md + one `SKILL.md`** reach all four clients with no
  per-client code;
- **descriptor slimming** helps all four;
- the **hooks** need three thin configs (Cursor native, Claude and Codex in one schema,
  an OpenCode plugin) over **one handler**: `droidyard hook <client> <event>`, a Go
  subcommand that reads stdin JSON and takes the tool mapping from the capability
  registry. No jq or bash dependency, and no drift.

## 4. What an ADR must say (the MVP defers skills and distribution; §8 says hooks are "not adopted")

A new ADR, "Optional assistant integration files", which:

- amends ADR-0014 (`docs/adr/0014-agent-distribution-model.md`)
  (it rejected per-assistant *plugins*; this proposes generated per-assistant *config
  files*, still with no plugin);
- updates `mcp-features.md` §8;
- moves "one skill" out of `mvp.md` §Out of scope.

It must state:

1. **Optional, never required.** Every capability works without the files
   (ADR-0004 (`docs/adr/0004-no-own-harness.md`)).
2. **Hooks are UX, not security.** A deny hook can be bypassed (`sh -c`, aliases,
   scripts). Enforcement stays in the core
   (ADR-0015 (`docs/adr/0015-untrusted-data-authorization.md`)).
   Hooks fail open.
3. **Generated from the registry**, the single source of truth: the tool mapping, the
   read-only allowlist, the instructions and the skill.
4. **`droidyard init` is run by the user, never through MCP.** Writing hooks into a
   project makes the user's agent run code.
5. **Idempotent and marked.** It never overwrites the user's files, it merges JSON, and
   there is an uninstall.
6. **The client versions verified**, and a measurement: a Cursor arm in the m1m kit.
7. **What was rejected:** plugins and marketplaces; automatic builds by default; device
   facts in the context.

## 5. Ranked recommendation

| # | What | Clients | Gain (estimate) | Cost | Risk |
|---|---|---|---|---|---|
| 1 | **Enforce our own description rule** (≤ ~300 chars, a test in `internal/schema`); move the usage text into the skill and instructions | all | 59 → ~32 KB of definitions; ~4 k tokens per Cursor golden path read once; ~7 k tokens per request where nothing is deferred | S (descriptors + a test + schema diff) | Low; agents lose some guidance until the skill exists |
| 2 | **Server instructions** gain about 3 lines: the golden-path order; "adb/gradle actions all have tools: …"; "tools are called directly" | all | Prevents fallback and Code Mode searches (7 % of calls in OpenCode) | XS | None |
| 3 | **`AGENTS.md` block** (≤ 5 lines) + **`.agents/skills/droidyard-android/SKILL.md`** (+ a copy in `.claude/skills/`), per ADR-0011 | all | Fewer fallbacks; guidance paid only when loaded | S | Low; drift if not generated |
| 4 | **Cursor `beforeShellExecution` redirect** (+ the same handler for Claude/Codex `PreToolUse(Bash)` and an OpenCode plugin) | Cursor first | One denied call instead of a 3–5-call fallback | M (Go subcommand + 3 configs) | False denies; mitigated by denying only mapped commands, failing open, an opt-out file |
| 5 | **Allowlist for read-only tools** in `.cursor/cli.json` (+ the IDE allowlist, documented) | Cursor | Removes approval prompts on uiTree, logs, list and describe calls | XS | Low; destructive tools stay prompted |
| 6 | **`sessionStart` warm-up + facts** | Cursor, Claude, Codex | 1–3 calls, up to ~8 s of Gradle cold start | M | Hook timeouts; untrusted project data; measure first |
| 7 | `stop` build check | Cursor, Claude, Codex | Fewer non-compiling handovers | S | Reverses a recorded stance; off by default |
| — | Do **not**: disable tools per project (little gain under deferral), use `updated_mcp_tool_output` (the server should bound its own output), use prompt-type hooks (cost model calls) | | | | |

Before rows 4–7, **measure**: one Cursor arm in the m1m kit
(`cursor-agent -p --output-format stream-json --model cursor-grok-4.6-high --approve-mcps`;
whether stream-json reports tokens is unverified). Without that, every Cursor gain
above is an estimate.

### What `droidyard init [--client cursor|opencode|claude|codex|all]` would write

```text
AGENTS.md                                   # marked block, ≤5 lines (merge, never overwrite)
.agents/skills/droidyard-android/SKILL.md   # + references/ (golden path, tool map); copy in .claude/skills/
.cursor/mcp.json                            # {"mcpServers":{"droidyard":{"type":"stdio","command":"droidyard","args":["mcp"]}}} (merge)
.cursor/cli.json                            # permissions.allow: Mcp(droidyard:<each read-only tool>)
.cursor/hooks.json                          # beforeShellExecution → "droidyard hook cursor before-shell" (matcher above);
                                            # optional sessionStart → "droidyard hook cursor session-start"
.claude/settings.json / .codex/hooks.json   # PreToolUse(Bash) → "droidyard hook claude pre-tool" (one schema)
opencode.json + .opencode/plugins/droidyard.ts  # mcp entry (codemode per the A/B); tool.execute.before → droidyard hook opencode
```

The hook handler answers in each client's own shape. For Cursor it fills `permission`,
**both** `user_message` and `agent_message`, and the droidyard tool to use instead.

## Sources (fetched 2026-10-07 unless noted)

- cursor.com/docs/agent/hooks
- cursor.com/docs/reference/third-party-hooks
- cursor.com/docs/context/mcp, /context/skills, /context/rules
- cursor.com/docs/cli/using, /cli/reference/permissions
- cursor.com/blog/dynamic-context-discovery (2026-01-06)
- forum.cursor.com/t/166405 (2026-07-23), /t/165642 (2026-07-14 and 2026-09-09), /t/67976
- opencode.ai/docs/plugins, /permissions, /mcp-servers, /skills (each "updated 2026-10-07")
- code.claude.com/docs/en/mcp
- learn.chatgpt.com/docs/hooks (redirected from developers.openai.com/codex/hooks)
- developers.openai.com/codex/skills (from search results)
- Local, all [obs]:
  - `~/.cursor/skills-cursor/create-hook/SKILL.md`
  - `~/.local/share/cursor-agent/versions/2026.10.01-e373342/{index,190.index,7923.index}.js`
  - `~/.cursor/projects/*/mcps/`
  - `~/.opencode/bin/opencode` (v2.0.18 strings)
  - `~/Library/Caches/droidyard-dev/m1m/{kit,runs}`
