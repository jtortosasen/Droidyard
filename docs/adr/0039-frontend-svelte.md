# ADR-0039: Frontend built with Svelte 5 and TypeScript

- Status: Accepted
- Date: 2026-10-09
- Supersedes: ADR-0008
- Relates to: ADR-0006, ADR-0009, ADR-0003
- Decided by the owner, 2026-10-09.

## Context

ADR-0008 chose React for the Phase 2 web UI mainly because the agents that write most of
the UI generate React most reliably, and secondarily for its editor wrappers. No frontend
code exists yet. Before Phase 2 was planned the owner set two priorities for the UI:
**immediacy** (the UI reacts at once, also while Logcat or a build streams lines) and
**lightness** (little to download, parse and keep in memory).

Svelte compiles each component to direct DOM updates driven by signals (*runes*), with no
virtual DOM to diff; React re-runs a component and diffs its output, so a high-frequency
state placed too high re-renders a large subtree unless the code is disciplined about it.
Svelte is fast by default, React fast with care, and the code here is written by agents.
Svelte's runtime is a few kilobytes against React and ReactDOM's ~50 KB gzipped
(assumption: approximate public figures, not measured here). Both differences are small
next to virtualising long lists and batching stream updates, which either framework needs.

ADR-0008's first reason still holds in part: models were trained on far more React, and
Svelte 5 (October 2024) replaced Svelte 4's reactivity syntax, so a model can mix the two.
Svelte's maintainers now ship countermeasures for exactly this (observed 2026-10-09):
`llms.txt` documentation (`svelte.dev/docs/svelte/llms-small.txt`, 15 KB), two agent
skills (`svelte-core-bestpractices`, `svelte-code-writer`, MIT, `sveltejs/ai-tools`), and
a static analyser, `svelte-autofixer`, runnable as a CLI
(`@sveltejs/mcp svelte-autofixer <file>`, 0.1.26). ADR-0008's second reason no longer
holds: CodeMirror 6 (ADR-0009) and the generated client are framework-agnostic.

## Decision

1. The web UI is **Svelte 5 with TypeScript**, a single-page app built by Vite into static
   assets the daemon serves (ADR-0006). **No SvelteKit**: there is no server-side
   rendering or routing to do, and the daemon is the only server.
2. **Runes mode is enforced for the whole app** (`compilerOptions.runes: true`), so legacy
   Svelte 4 syntax is a compile error, not a silent fallback.
3. **Two gates refuse a change** to the frontend: `svelte-check` with no errors and no
   warnings, and `svelte-autofixer` with no issues on every changed `.svelte`,
   `.svelte.ts` file. Both run from pinned dev dependencies, not from `npx` fetching the
   latest, and the pre-commit hook runs them when a commit touches the frontend.
4. **The implementer is briefed with Svelte's own guidance**: the two official skills,
   vendored at a pinned commit with their licence, and the Svelte 5 `llms-small.txt`.
5. Unchanged from ADR-0008: TypeScript is mandatory; the TypeScript client is generated
   from `schema/capabilities.json`; the bundler is Vite.

## Consequences

- The owner's two priorities become measurable acceptance items in the Phase 2 plan (a
  bundle budget and a frame-time check under a log burst), so the choice is checked, not
  assumed.
- Every frontend step costs the two gates; an implementer run that fails them is sent back
  like a failing `go test`.
- Component libraries are Svelte ones (for example Bits UI, shadcn-svelte, Ark UI) or
  framework-agnostic ones; CodeMirror 6 is wrapped with an `{@attach}`, not a third-party
  wrapper. The choice of each is the Phase 2 plan's.
- Svelte changed its reactivity model once (4 to 5); another such change would cost a
  migration. Assumption: none is announced as of 2026-10-09.
- Documents to change with this decision: `docs/roadmap/mvp.md` §Phase 2 (the deliverable
  line); `docs/adr/README.md` (the index); ADR-0008's Status.

## Alternatives considered

- **React (ADR-0008)**: the most reliable for agent-written code and the deepest
  ecosystem of panels and accessible primitives. It loses on the owner's two priorities,
  where it is fast and light only with discipline the agents must apply every time. It
  wins back if the first Svelte panels show the implementer still under-delivering with
  the gates in place: then reopen this ADR with that evidence.
- **Preact with signals**: React's API in ~4 KB with fine-grained updates. It keeps
  React's training-data advantage but loses the compile-time checks Svelte's compiler and
  `svelte-check` give, and mixes two reactivity models (hooks and signals) for the agents
  to keep apart.
- **SolidJS**: the finest-grained updates and a small runtime, but the least training data
  of the four and no official agent tooling; the codegen risk is Svelte's without its
  countermeasures.
- **SvelteKit**: brings routing, server rendering and a server of its own; the daemon
  already serves the assets and there is one page. It wins if the UI ever needs
  server-side rendering, which a loopback tool does not.
- **Svelte's Claude Code plugin** (`/plugin install svelte`): installs the same skills
  and an MCP server, but into the owner's user-level configuration, and runs `npx` on the
  latest package. Vendored skills and a pinned CLI give the same checks, reproducibly, in
  the hook.
