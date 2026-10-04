# ADR-0029: UI reads through a one-shot dex reader that keeps accessibility services running

- Status: Accepted
- Date: 2026-10-04
- Relates to: ADR-0005, ADR-0015, `docs/subsystems/ui-automation.md` §The tree,
  `docs/research/device-speed-2026-10.md`
- Decided by the owner, 2026-10-04 ("utilicemos el .dex con el flag").
- Extended by [ADR-0030](0030-persistent-ui-reader.md): the reader stays running; this
  one-shot run becomes its fallback.
- Amended: 2026-10-04 — measured on the implementation (milestone 1e step 10): a read
  costs ~1.3 s end to end (push, `app_process` JVM start, the idle wait, removal), not
  the spike's ~150 ms, which was connect-to-read only; still faster than the dump (2–8 s
  observed). The dex is pushed and removed on every read.

## Context

`device.uiTree` reads the screen with `uiautomator dump /dev/tty`. That command opens
`UiAutomation` without `FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES`, so while it is
connected Android suspends every other accessibility service on the device (logcat:
`Registering UiTestAutomationService … flags=0x0`). An app that depends on its own
accessibility service sees it vanish. On the owner's app, Qustodio, a dump after about
three idle minutes killed the app twice: its malfunction check threw ~50 ms after the
registration, `FATAL EXCEPTION`, SIGKILL, restart. A screen-reader user would lose
TalkBack for the length of every read. The dump also costs ~2 s on the device (measured
on a Pixel_9a), most of it a JVM start and a fixed idle wait.

A 5 KB spike reader (a dex run with `app_process`, `UiAutomation` connected with the
flag) read the same Qustodio screen, 45 nodes, in ~150 ms, and the app survived six
reads, one after an idle period. Not established: that the flag alone prevents that
kill — an unflagged reader holding the connection ~150 ms did not trigger it either
(research note §Addendum). The flag is the documented way to keep other services
running (Appium's `disableSuppressAccessibilityService`; mobium ships such a dex for the
same reason, PR #101).

## Decision

- **The UI tree is read by Droidyard's own reader**, a small dex built from Java
  source in this repository, run with `app_process` on the device as the shell user,
  never installed: pushed to `/data/local/tmp/` for the read and removed after it.
- **It connects `UiAutomation` with `FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES`**, reads
  the active window's tree, disconnects, and exits. One read per run: no persistent
  on-device server in this decision.
- **Its output feeds the same snapshot** `device.uiTree` publishes today (flat `nodes[]`,
  compaction, the 40 KB cap, the untrusted mark): the published contract does not change.
- **The dex is a build artefact, checked in and pinned**: its source, a reproducible
  build script (the SDK's `javac` target, `android.jar` and `d8`; reproducible with the
  javac version recorded beside the digest), and the binary embedded in the daemon with
  its SHA-256 pinned by a test and checked at run time before each push. The Go build needs no Android
  SDK.
- **`uiautomator dump` stays only as a labelled fallback** when the reader cannot run
  (the push or `app_process` fails, or a platform below API 24, where the flag does not
  exist); the result then carries a `warnings[]` entry saying other accessibility
  services were suspended during the read.
- The per-device dump lock stays: one `UiAutomation` at a time per device still holds.

## Consequences

- No more suspended accessibility services, and the observed kill avoided (Qustodio
  survived the reader after 215 s idle; the dump killed it 3 of 3). Reads are faster but
  not by an order of magnitude: ~1.3 s end to end against 2–8 s for the dump, most of it
  the JVM start and the idle wait; only a persistent server removes those.
- The repository carries Java source and a binary artefact; changing the reader needs
  the Android SDK and a rebuild, and a checked hash keeps the binary honest.
- `UiAutomation` is still one per device: a user's own instrumented test holding it
  conflicts with a read, as `uiautomator dump` did.
- The reader runs hidden API (`UiAutomation`'s connection constructor) through
  reflection, as the platform's own `uiautomator` does; an Android release can break it,
  and the fallback with its warning covers that.
- Documents that change with it: `ui-automation.md` §The tree (the mechanism),
  `adb-device.md` (the pushed file and its cleanup), the 1e plan (a step implementing it
  before the level-5 run), `docs/README.md` (the ADR count).

## Alternatives considered

- **Keep `uiautomator dump`.** Suspends every other accessibility service on each read
  and killed the owner's app; ~2 s per read. Rejected on correctness.
- **A persistent on-device server** (mobile-mcp's `app_process` server over
  `adb forward`; agent-device's instrumentation session). Removes the JVM start as well,
  so reads fall to tens of milliseconds and input speeds up; at the cost of a lifecycle
  to manage (a wedged server, restarts, port allocation) and a long-held `UiAutomation`.
  Not rejected: the next step if one-shot reads prove too slow for `tapElement`'s polling,
  decided in its own ADR with this reader as its base.
- **An installed APK** (instrumentation or an accessibility service of our own). Needs an
  install, a signing key and, for a service, the user enabling it in settings; visible on
  the device. The pushed dex needs none of these.
- **UiAutomator2 or Appium.** Their servers open `UiAutomation` without the flag by
  default (Appium added an opt-in), and they bring a runtime of their own; ADR-0005 keeps
  the platform's tools and our own small code instead.
