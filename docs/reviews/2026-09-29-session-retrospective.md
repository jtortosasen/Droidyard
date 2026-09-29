# Session retrospective: designing Droidyard

- **Date**: 2026-09-29
- **Scope**: one working session that took Droidyard from a brainstorm to a closed design
  and the first code. This is the record of **how** it was done, what went wrong, and what
  now prevents each failure. Decisions live in [`../adr/`](../adr/README.md); this does not
  repeat them.
- **Reader**: whoever continues the work, and whoever changes the process.

## What the session produced

| | |
| --- | --- |
| Decisions | 24 ADRs, all accepted, several amended |
| Vision and architecture | 5 + 6 documents |
| Subsystems and interfaces | 6 + 3 |
| Research | 11 documents: five prior-art repositories, a conceptual comparison, Gradle version compatibility, a spike, and pstack |
| Reviews | A pre-implementation review with 12 blocking findings, the owner's 19 decisions, and this |
| Code | `internal/capability`, `internal/schema`, `internal/doccheck`, `internal/domain/system`, `cmd/droidyard`, and the published `schema/capabilities.json` |
| Process | 4 agents and 5 skills, all committed with the project |

## The recurring failure mode, and the only real lesson

**A fact maintained in two places drifts.** It is the whole session in one sentence. It
showed up at least seven times in seven different disguises:

| Disguise | What it cost |
| --- | --- |
| Capability names in four hand-written lists | `sdk` missing from one, `git.log` from another, `file.watch` from a third, `build.test` in a table but nowhere else |
| The socket path stated in ADR-0024 as well as ADR-0021 | Two ADRs describing the same path differently |
| A citation to `gradle.md §Unverified assumptions` | A section that never existed, cited in three documents |
| Four Go comments citing `ADR-0020 §Error model`, `§Naming`, `§Discovery` | Sections that live in `capability-api.md`, not in ADR-0020 |
| A spike declared "verified" in the documentation map | It ran one version combination, on the command line, with the Problems API never observed |
| `MVDEV_` left behind by a rename | Caught by reading, not by a check |
| The MVP's phase lists against its own capability table | Three capabilities in the table, absent from the list |

The response is not care. **The response is structure, and it has exactly two forms:
generate the fact, or check it.** `schema/capabilities.json` is generated; capability
names, the ADR index, ADR shape, links, section references and the plan template are now
checked by `internal/doccheck`. What remains in prose is what needs judgement.

## Mistakes made here, by the assistant, and what each produced

Recorded because the reviews of this project are adversarial by design and the same
standard applies to the session that ran them.

| Mistake | What produced it |
| --- | --- |
| Marked a partial spike as verified in the documentation map | A `Status: partial` block in the spike, and the house rule: **label what you did not verify** |
| Cited a section that did not exist, three times | The section-reference check, now part of `go test` |
| Restated a fact already stated in another ADR | The rule that the second statement defers to the first |
| Edited a bold heading and orphaned its paragraph | Nothing structural. A reminder that a small edit needs a re-read after it |
| Over-promised a spike as "half an hour" | Corrected in writing; a correction is cheaper than a missed expectation |
| Recommended one daemon per project root | Refuted by a better argument — device serials and emulator boot diffs are global — which is now ADR-0019. **Give an agent the reasoning, not only the conclusion**, and it will improve on it |
| Left a domain in a table but not in the name list | The capability-name check |
| Wrote a checker for the wrong thing twice while wiring the checks | The `selftest_test.go` idea: every check is run against a small repository with seeded drifts, so a check that cannot fail is caught |

## What worked, and is worth keeping

- **Research before design.** Five repositories were read before a decision was taken. The
  payoff is traceable: the capability API came from comparing three implementations; the
  console extractor came from `gradle-mcp`'s lossy problems adapter; the skills format came
  from Google's repository; the process came from pstack. None of it was invented here.
- **The adversarial review before any code.** It found **six decisions that did not
  exist** — process topology, workspace addressing, the invocation contract, the descriptor,
  persistence, and where agents read artifacts. None of those was a wording problem. All
  six would have been discovered in code, expensively.
- **Verify what the fixes broke.** A second, adversarial pass over the fix round found four
  contract-level blockers that the fixes themselves had introduced, including a path where
  a client timeout would cancel a running build. Checking the fix is a different job from
  making it, and it is where the new bugs live.
- **Disjoint file ownership for parallel agents.** Four agents edited the corpus at once
  without a single conflicting edit, because each owned an explicit, non-overlapping set of
  files and the orchestrator kept the contested ones. This is what made parallelism safe.
- **Spikes before assumptions become load-bearing.** The Kotlin plugin finding — compiler
  messages reach the Problems API only from KGP 2.4, and a real project in 2026 runs 2.0.21 —
  changed the design. No amount of document reading would have found it. It came from
  running a build on a real project and looking at the output.
- **Recording rejected alternatives.** It is what lets a later session say "that was
  considered and rejected, here is why" instead of re-arguing it.
- **A plan before a milestone.** Added late in the session, after the first milestone was
  built without one. The decomposition of a milestone is a decision, and it was being made
  implicitly, in code.

## What is still not protected

Honest list, so the next session does not assume coverage the project does not have.

- **A citation that names a real but wrong section still passes.** The check confirms the
  section exists. `registry.go` cited `ADR-0015 §Annotations` — a real clause — while the
  rule it described lives in `mcp-tool-surface.md`. Fixed by hand; the class remains.
- **Contradictions between two prose passages.** The socket path was one. Two documents
  disagreeing in prose needs the `verifier`; no check catches it.
- **The reverse direction of the phase lists.** Everything in the Phase 1 list is in the
  table; the table may hold more.
- **Nothing forces a session to read the ADRs.** `AGENTS.md` asks, and that is the whole
  mechanism. The defence is that a rejected option has its reason written down, so the
  conversation can be "see ADR-0017" rather than a re-litigation.
- **The checks confirm names and shapes, not meanings.** Whether a description matches a
  behaviour is judgement, and it stays with the verifier.
- **The daemon verification recipe has never been run.** `verify-daemon` is marked draft,
  and its own rule is that it counts only after one end-to-end run on one platform.

## Carried forward, not lost

- **Five mechanisms from pstack**, in the order the research recommends: the daemon recipe
  (written, unrun), the structural checks (done), the proof ladder (done), dismissed
  findings in the report (done), blinding for the MVP comparison (**due when Phase 1
  closes**).
- **Nine spikes**, S1 to S9. The one that matters most, S1 run 2, needs a Tooling API
  client, which is the first sidecar code: the spike and the milestone are the same
  artifact.
- **One blocking decision for the next milestone**: no document chooses the Go MCP library.
  The planner will surface it, and it is an owner decision.
- **Deliberately deferred, with reasons on record**: code intelligence, the live screen
  stream, OCR, SDK provisioning, AVD creation, multi-device concurrency, Windows, LAN
  exposure, and the AAPT2 merged-path decision, which must be settled before `build.errors`
  is implemented.

## The state it was left in

The design is closed and verified twice; the first code milestone is done and the suite is
green; the planning half of the process exists and has never been used. The next step is to
run the `planner` on milestone 1b, which is the first real test of the process rather than
of the design.
