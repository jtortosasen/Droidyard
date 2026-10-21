# ADR-0114: A patient events reach for the UI events stream and the recorder

- Status: Accepted
- Date: 2026-10-21
- Supersedes: ADR-0112 §Decision 3 (in part: how the stream meets a held read lock)
- Relates to: ADR-0034 §1; ADR-0111 §8; ADR-0113; plan m2x Q5
- Delegated (plan m2x Q5, reviewed in three rounds; the main session under the owner's standing instruction to apply the recommended option).

## Context

ADR-0111 §8 lets one `device.uiEvents` call recover a dead reader twice, each time waiting at most
2 s for the read lock. ADR-0112 §3's poller used the same reach, so a stream behind another call
that held the lock past both waits (about 4 s: a read chain may hold it up to 22 s, an act up to
its 60 s host bound) published `stopped failed` though nothing had failed. A recording (ADR-0113)
polls the same ring for minutes beside an agent's reads and acts, so it would end the same way.

## Decision

1. **Two reaches.** A call keeps ADR-0111 §8 unchanged: two recoveries, 42 s at worst. A
   **patient** reach, used by the stream's poller and the recorder, treats a lock held by
   another caller as an empty tick: no recovery spent, no failure, the next tick tries again.
   Every lock holder is bounded by its own deadline, so a held lock is never a failure.
2. **Recovery stays bounded.** With the lock, a patient reach still `ping`s and restarts a dead
   reader once; it fails only when that restart fails, and then the stream publishes `stopped
   failed` (or the recording ends `failed`), never retrying in a loop.
3. **No busy bound.** No value below an act's 60 s host bound can tell a legitimate holder from a
   wedged one; the holders' own deadlines are the bound.

## Consequences

- The UI events panel no longer stops while an agent's long read or act holds the reader; it
  resumes when the holder releases, with a gap only when events were really lost.
- A stream or recording behind a held lock reports nothing for that time; the recorder counts
  the waits (`lockWaits`, ADR-0113).
- No contract change: the topic's `stopped failed` keeps its meaning, now only for a failed
  restart.

## Alternatives considered

- **A longer busy bound for streams** (15 s was proposed). Shorter than legitimate holds; any
  value is a guess that fails a stream behind a valid act.
- **Holding the read resource for the stream's life.** Would block every read and act on the
  device for as long as a panel is open.
