---
name: Prove a change
description: Use before claiming that any Droidyard change works (after a plan step, a fix or a descriptor edit) and when writing the report of what was verified. Covers running and reading the checks, reviewing the schema/capabilities.json diff, exercising behaviour that unit tests cannot reach, and labelling what could not be verified.
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

## The golden file

When a descriptor or the generator changes, `go test ./internal/schema` should fail first and print the new schema. That failure is expected, and it is evidence that the change reaches consumers. Regenerate with `go test ./internal/schema -update` only if the change is intended, then read `git diff schema/capabilities.json` line by line against the plan's declared schema impact. Lines that change what clients do:

- `annotations`: the hints clients use to decide on confirmation and retries;
- `longRunning`, and an `output` that became (or stopped being) the operation view;
- the error codes a capability lists: published codes are never renamed or repurposed;
- `required`, `additionalProperties` and property types in `input`: these can break existing callers;
- `version`, and `since` on new capabilities.

A changed line that the change does not explain is a bug in the change. When a step declared "no schema change", any diff is a finding.

## Beyond unit tests

A socket, `flock`, a spawned shim, a killed process or a real MCP client cannot be proven by a unit test with fakes. Exercise it for real where you can:

- Build outside the tree: `go build -o "$TMPDIR/droidyard" ./cmd/droidyard`. Never leave binaries in the repository.
- Isolate state from the owner's own daemon. ADR-0021 fixes `DROIDYARD_STATE_DIR` as the override tests use. Until the code honours it, say that a run used the real state directory.
- Prefer a durable form over a one-off session: a Go test that drives the process over pipes or the socket, or a recorded MCP transcript kept as a golden test (the pre-implementation review, §What I would add if I owned the project, item 8).
- Record what you ran, on what (OS, Go version, client and version for an MCP client), and quote the decisive line of output.

## Label what you did not prove

Every claim in a report falls into one of three kinds:

- **Verified**: the command or procedure, and what it showed.
- **Not verified**: why not, and what would settle it (a spike, a real device, a client run).
- **Assumed**: taken from a document, with the citation.

Partial evidence is stated as partial, with what it covered and what it did not, as `docs/research/spike-s1-error-formats.md` does in its status table. Never write "should work".
