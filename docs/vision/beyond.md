# Beyond the MVP: the owner's vision and the capabilities it needs

The owner's vision (2026-10-17) of what Droidyard should become, and the main session's
analysis of the capabilities that vision needs. It is not a roadmap: it names no order
and no milestone. Each capability becomes milestones, each with its plan and ADRs, when
the owner picks it. [`vision.md`](vision.md) stays the statement of what exists; where
this document departs from it, the departure is named under §Decisions it reopens.

## The vision

Droidyard started as an MCP surface that abstracts Android, and grew into a runtime with
a UI. The owner wants it to become their **development centre for any language and
project**, mainly to see what is happening, and to cover needs no current tool covers:

- **Agents that work in worktrees.** An agent given a task, from the UI or from an
  external assistant, decides whether the task is new or already exists, takes or
  creates a worktree for it (reusing them), and keeps a context per worktree. The UI
  shows which session works where; it never moves a session to another worktree. Only
  the agent's workflow does, and the UI follows.
- **Not only Android.** A Python project or a plain JVM project gets the generic
  capabilities, not Android's.
- **A remote device, as if it were local.** A QA colleague's phone, with a build or a
  state the owner cannot reproduce: the colleague's machine runs Droidyard with the
  phone attached, a Droidyard service exposes the device, and the owner's Droidyard
  connects and sees and drives it transparently: screen, input, logs, install, debug.
  Over the company VPN first; over the internet later.
- **Recorded flows.** A person's interaction, recorded once (or an agent's exploration),
  is persisted as a graph of actions that replays semi-deterministically, without an
  agent reasoning each step. The same graph drives an **end-to-end test engine**, which
  needs no development environment, only devices.
- **A device farm.** Remote devices plus the e2e engine: a bank of devices reachable
  from the internet.

## The capabilities

**C1. Language-neutral workspaces.** Today the domains split into generic ones
(`workspace`, `file`, `git`, `shell`, `agent`) and Android ones (`device`, `emulator`,
`build` through Gradle, `sdk`, `project`). Principle 9 of [`vision.md`](vision.md)
already asks for separable modules. Needed: a project *kind* detected per workspace
(Android, JVM, Python, other), the domains enabled by kind, and the MCP tool list, the
skill and the UI panels following them. Recommended: one binary with domains enabled
per workspace; a language extension out of process (the Gradle sidecar's pattern) only
when a language needs a toolchain process. Separate MCP servers per domain are the
rejected alternative: more to install and configure, for the same split.

**C2. Sessions that own worktrees.** A worktree becomes a workspace of its own (its
root, id and trust) that shares a repository, an extension of the two roots of
[ADR-0075](../adr/0075-project-root-and-gradle-root.md). A session acquires a worktree
for a task (match an existing one or create one), owns it, and carries its context; the
Git side panel shows worktrees by session, read-only. Builds on the sessions of
[ADR-0048](../adr/0048-operation-session.md).

**C3. A live device view.** The screen streamed to the UI, with input. Already the next
planned milestone, and the base of C4 (seeing a remote phone) and C5 (recording what a
person does).

**C4. Device hosting.** A Droidyard in a *host* role exposes its attached devices to
other Droidyards: screen, input, logs, install, and adb or JDWP forwarded inside the
connection for debugging. The owner's Droidyard lists the remote device beside its
local ones. Authenticated, encrypted, audited, every action a capability call.
Transport: the company VPN first; internet reach later, through a relay or an overlay
network rather than a protocol of our own. Exposing raw adb to the network is rejected:
adb grants full control with no authentication, and does not survive NAT or version
skew.

**C5. Flow recording and replay.** Nodes are screens, identified by a signature of
their UI tree; edges are actions with a semantic selector and a postcondition, as
`device.tapElement` already verifies one. Recording comes from a person in the live view
or from an agent exploring. Replay matches the current screen to a node, runs the edge
and checks the postcondition; only a divergence calls an agent.

**C6. An e2e runner and a farm.** A runner with the device domain and the flow store,
without the development parts (C1 makes that a configuration, not a fork). A farm adds
many hosts (C4), many devices at once, scheduling and results.

## How they depend on each other

```text
C1 language-neutral ──┬──> C6 e2e runner / farm
                      │          ^
C3 live view ──┬──> C4 hosting ──┤
               └──> C5 flows ────┘
C2 sessions + worktrees  (independent; builds on C1 for non-Android projects)
```

## Decisions it reopens

Each is an owner decision, recorded as an ADR when its capability starts:

- "A cloud service. Droidyard is local-first" ([`vision.md`](vision.md) §Non-goals) and
  "multi-user or cloud use" ([mvp](../roadmap/mvp.md) §Out of scope): C4 and C6 reach
  other machines and other people.
- Remote access, TLS and authentication "after v1" (ratified 2026-09-29): C4 needs them.
- Multi-device concurrency, out of the MVP: C6 needs it. The core was built without a
  single device slot, so this is work, not a redesign.
- "An Android development runtime": C1 makes Android one kind of project among several.

## One repository or several

Recommended: **one repository, several programs**. C4's host role and the farm's device
nodes are the same code as the developer's daemon (the device domain, the capability
contract, the transport), so separate repositories would version one contract in two
places and pay for it at every change. Features are chosen at two levels: per workspace
at run time (C1), and per program at build time (the developer's `droidyard`, and later
a runner or a farm controller as further binaries of the same module). A part moves to
its own repository when it gains users, contributors, a language or a release cadence
of its own.
