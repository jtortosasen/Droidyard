# ADR-0122: A CLI for people: commands per domain from the descriptors, and CLI connections that carry streams

- Status: Accepted
- Date: 2026-10-21
- Supersedes: ADR-0019 §Sessions (in part: a CLI connection can subscribe); ADR-0010 §Decision (in part: a CLI connection is a session that carries events); ADR-0041 §7 (in part: the CLI no longer gets `SESSION_REQUIRED`); ADR-0113 §6 (in part: `device.watchUiRecordings` is not web only)
- Relates to: ADR-0015 §1, §6; ADR-0020 §Adapter projections; ADR-0036 Decision 2; ADR-0037; ADR-0055; ADR-0090; ADR-0110 Decision 2; [plan m2z](../plans/m2z-cli-for-people.md) D1–D16 (the details and the tests); [`beyond.md`](../vision/beyond.md) §One repository or several
- Decided by the owner, 2026-10-21: the CLI and its command surface must be usable coherently by a person; shell completion and a split into several binaries are later. The shape is Delegated (plan m2z, reviewed in two rounds).

## Context

The owner wanted to see their devices and had to be told `droidyard call device.list`:
`droidyard --help` is one 120-line usage of 22 hand-written commands, and the 87
capabilities are reachable only through `call <name> --params <json>`, with their names in
`droidyard caps`, a flat list nothing in the help points to. `call` prints the response
envelope, which suits a script, not a person. Three register rows wait on the CLI: progress on
stderr and following a stream (a CLI connection has no event session, so every streaming
capability answers `SESSION_REQUIRED`), `--device` with `ANDROID_SERIAL` (the 2026-09-29
ratification), and `artifact cat` reading a large artifact as one `artifact.read` operation per
page. The descriptors already carry every name, parameter schema, description and, for 13
capabilities, a text renderer (`Descriptor.Text`).

## Decision

1. **Commands derived from the registry.** Every registered capability is
   `droidyard <domain> <verb>`, the verb in kebab case (`ui-tree`); matching ignores case and
   hyphens, and the canonical name (`droidyard device.list`) runs the same command. The tree
   is built from the binary's own registry, so a build that registers fewer domains gets a
   smaller, coherent tree with no fork. No descriptor field is added: what cannot be derived
   (help sections, hidden commands, hand verbs) is written in the CLI.
2. **Flags from parameters.** Scalars and enums are `--<kebab name>` with one value, typed
   and checked; arrays of scalars are a repeated flag, never comma-split; objects of scalars
   get dotted flags one level deep; anything deeper, arrays of objects, open maps and unions
   only through `--params <json>`, which is the base the flags add to, a key set both ways
   being a usage error. No positional arguments. The CLI's own flags: `--json`, `--params`,
   `--no-autostart`, `--help` everywhere, `--workspace` on workspace-scoped commands,
   `--detach` on long-running ones; a test refuses any collision with a parameter.
3. **`ANDROID_SERIAL`** fills a **required** `device` left unset; an optional one is never
   filled, and `call` stays verbatim.
4. **Output.** Human by default: the descriptor's `Text` renderer when it has one, else a
   generic renderer from the result's JSON (`key value` lines, a table for an array of flat
   objects, nested values summarised); strings with a control, format or line-break character
   are quoted, and in a response marked untrusted every string is, with one fixed note on
   stderr. Errors as `CODE: message`, cause and next step on stderr; exit statuses as
   `call`'s. `--json` prints the envelope `call` prints.
5. **Help in levels**: `droidyard help` is an index (where to start, the hand commands by
   section, the domains with their verbs, the raw door), then `<domain> --help` and
   `<domain> <verb> --help` from the descriptors. Listings hide `Binding` capabilities,
   stateful ones that do not stream, the `agent` domain and `device.watchScreen`; hidden
   commands still run.
6. **The hand-written commands keep their names**, so no alias is needed and `mcp`, `hook`
   and the entries `init` writes are untouched; their prose moves to their own help.
   `device allow|disallow` and `artifact cat` stay hand verbs of their domains, and a test
   refuses a capability that would shadow one. `call`, `caps` and `schema` are unchanged as
   the raw door.
7. **A CLI connection carries streams.** Each native socket connection gets an event session,
   as a WebSocket does. A streaming capability called over it answers its envelope, then its
   events as `{"subscriptionId","event"}` lines (ADR-0055's frame), never before that
   envelope; closing the connection releases every stream it owns. MCP and `POST /v1/call`
   keep `SESSION_REQUIRED`. A streaming command follows by its nature, with no `--follow`
   flag: exit 0 after its `stopped` event, 130 on Ctrl-C, 1 when the daemon closes.
8. **Progress from the bus.** A long-running call whose stderr is a terminal subscribes to
   `operation.progress` on the same connection and prints its own operation's reports, at
   most one a second, holding back and never dropping the latest; off a terminal nothing is
   subscribed or printed. The subscription is one more Activity row per such call.
9. **`artifact cat` over `GET /v1/artifacts/{id}`**: one request, one `artifact.read`
   operation, with the token and address a running daemon wrote; it never starts a daemon.
   An image is written when stdout is not a terminal. The Activity row shows client `ui`, as
   every HTTP request does.

## Consequences

- A person reaches every capability from `droidyard help`, and output reads without `jq`;
  scripts keep `--json` and `call`, byte for byte but for the operation id.
- An agent's own shell can now run a streaming command, which returns only at its `stopped`
  or when the shell tool's timeout kills it; agents keep `device.logs`, and a killed CLI
  releases its stream.
- Shell completion can walk the tree later; a second binary gets its CLI from its registry.
- The streaming descriptors' "WebSocket session" wording becomes "the calling session" in
  `schema/capabilities.json`; no field, code or enum changes.
- Documents to change: `docs/architecture/capability-api.md` §Statefulness is declared and
  §Subscriptions, `docs/architecture/overview.md` §Events,
  `docs/architecture/process-lifecycle.md` and `docs/subsystems/files.md` where they say a CLI
  call cannot subscribe or `file.watch` is WebSocket only.

## Alternatives considered

- **Hand-written commands for the common capabilities only**: every one restates a
  descriptor's names, parameters and description, which drift (AGENTS.md: one fact in one
  place), and the rest stay behind `call`. It wins only for a capability whose derived form
  is unusable, which none of the 87 is.
- **A descriptor field for CLI nouns, short flags and domain descriptions**: contract data
  for one adapter, bumping the published schema for presentation; reopened if a derived name
  proves unreadable in use.
- **Renaming the hand commands into the tree (`droidyard host share`) with aliases**: breaks
  nothing only while every alias lives, and `mcp`, `hook` and `init`'s entries are written into
  assistants' configurations; the gain is symmetry alone.
- **The CLI as a WebSocket client for streams**: the upgrade's mandatory `Origin` would have
  to be forged and every call would show as `ui`.
- **Polling `operation.wait` for progress**: `overview.md` §Events says no adapter polls; the
  bus is how progress reaches an adapter, and §7 gives the CLI a session that can subscribe.
- **A `--follow` flag joining a build with its console (`build.run` and `build.watchOutput`)**:
  domain logic in an adapter; the build's own progress already reaches the terminal by §8.
- **A larger `artifact.read` page, or coalescing pages in Activity**: the page is a contract
  limit sized for models, and coalescing is a UI change for one client.
- **Separate binaries per domain against one daemon** (raised by the owner): the shim and the
  daemon are one binary so their versions cannot skew (ADR-0019); an unused domain costs
  little at rest. A build with fewer domains, for a host or a farm, stays in `beyond.md`, and §1
  is what lets it have a CLI.
