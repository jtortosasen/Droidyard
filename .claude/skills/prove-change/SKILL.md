---
name: prove-change
description: Use before claiming that any Droidyard change works (after a plan step, a fix or a descriptor edit) and when writing the report of what was verified. Covers running and reading the checks, reviewing the schema/capabilities.json diff, exercising behaviour that unit tests cannot reach, grading how far each claim was proven, and labelling what could not be verified.
---

# Proving a change

The four commands are in `AGENTS.md` §Build and test. This skill covers what they do not tell you: how to read their output, and what to do beyond them.

## Read the output, not the exit code

- **`gofmt -l .` exits 0 even when it lists files.** Any output is a failure.
- **A `-run` filter that matches nothing passes.** `go test ./internal/x -run TestTypo` prints `ok … [no tests to run]`. When you name a step's check, run it with `-v` and see its name next to `PASS`.
- **`(cached)` is a pass from an earlier run.** Go's test cache tracks source, flags, environment variables and files the test opens, but not sockets, spawned binaries or external tools. When a test touches any of those, run it with `-count=1`.
- **See a new test fail once.** Where it is cheap, reverse the decision the test asserts, watch it fail for the reason in its comment, then restore. A test you have never seen fail proves only that it runs.
- **Concurrent code gets the race detector.** The daemon, sessions, operations and the supervisor share state across goroutines; also run `go test -race` on their packages. This is this skill's recommendation, not a documented project rule.
- **A result from before your last edit is stale.** Re-run after every fix: fixes are where the new problems come from.

## Tests that hold under load

A test that spawns a process, polls, or bounds a time must pass on a loaded machine, not only alone.

- **The kinds of time** are in `internal/testtime`'s package comment: a hang guard is `testtime.Bound`; a product claim is scaled only with its level-5 run named beside it; a window the test must meet is redesigned (a seam, a line the process prints, a precondition retried only while absent), never scaled; an absence is not scaled. Poll with `testtime.Eventually`.
- **A failure under load is evidence, not noise.** Trace it to its class before raising any bound; a bound raised to pass a run is a weaker test.
- **To chase one**, `stress.go` (this directory; its header gives the flags) runs a package repeatedly under chosen load and reports each failing test's rate and first lines. It is a diagnostic, not a gate (owner, 2026-10-20).

## The golden file

When a descriptor or the generator changes, `go test ./internal/schema` should fail first and print the new schema. That failure is expected, and it is evidence that the change reaches consumers. Regenerate with `go test ./internal/schema -update` only if the change is intended, then read `git diff schema/capabilities.json` line by line against the plan's declared schema impact. Lines that change what clients do:

- `annotations`: the hints clients use to decide on confirmation and retries;
- `longRunning`, and an `output` that became (or stopped being) the operation view;
- the error codes a capability lists: published codes are never renamed or repurposed;
- `required`, `additionalProperties` and property types in `input`: these can break existing callers;
- `version`, and `since` on new capabilities.

A changed line that the change does not explain is a bug in the change. When a step declared "no schema change", any diff is a finding.

## Beyond unit tests

A socket, `flock`, a spawned shim, a killed process or a real MCP client cannot be proven by a unit test with fakes: that is level 5 below, and only a real run reaches it.

- Build outside the tree: `go build -o "$TMPDIR/droidyard" ./cmd/droidyard`. Never leave binaries in the repository.
- For the daemon, follow the `verify-daemon` skill. It launches an isolated instance, proves that instance is the one being driven, and stops only what it started. When you need something it does not cover, add it there instead of improvising.
- Prefer a durable form over a one-off session: a Go test that drives the process over pipes or the socket, or a recorded MCP transcript kept as a golden test (the pre-implementation review, §What I would add if I owned the project, item 8).
- Record what you ran, on what (OS, Go version, client and version for an MCP client), and quote the decisive line of output.

## Grade what you proved

For each fact a claim rests on, say how far its proof got:

1. **Said**: stated, with nothing behind it.
2. **Pointed**: you cited the line of code that does it.
3. **Traced**: you walked the failure path through the code, and it does not reach.
4. **Tested**: a test ran the real code, faking only the external boundary (`nothingInstalled`, a fake connection).
5. **Reproduced**: with real processes, such as the built binary on a real socket, or a real client or tool.

Most steps that look risky are safe because of one fact. Name it, and aim the highest level you can reach at it; evidence about everything else does not substitute. "Killing a shim does not stop an operation it started" (`docs/roadmap/mvp.md` Phase 0) rests on one fact: an operation's lifetime does not depend on the connection that started it (ADR-0019 §Sessions). A test against a fake connection proves that at level 4. Only killing a real `droidyard mcp` reaches level 5.

## Label what you did not prove

Every claim in a report falls into one of three kinds:

- **Verified (L2 to L5)**: the command or procedure, what it showed, and its level. Level 1 is never Verified.
- **Not verified**: why not, and what would settle it (a spike, a real device, a client run). The gap between the level a fact needs and the level you reached belongs here.
- **Assumed**: taken from a document, with the citation.

Partial evidence is stated as partial, with what it covered and what it did not, as `docs/research/spike-s1-error-formats.md` does in its status table. Never write "should work".
