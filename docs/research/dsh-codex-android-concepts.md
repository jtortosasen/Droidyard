# Research: Android runtime concepts in `dsh-android` and `codex-android-mcp`

Conceptual model of how both repositories handle **adb, devices, emulators,
streaming, automation, build/run, lifecycle and security** — and what is worth
extracting for `mydev`.

They share one lineage, so their **domain model is identical**; the differences are
almost entirely about *how the capability is exposed and governed*. Read together
they show the same Android runtime in two packaging modes.

> Status: draft. Complements [`prior-art.md`](prior-art.md) and feeds
> `architecture/process-lifecycle.md`, `subsystems/adb-device.md`,
> `subsystems/ui-automation.md`.

## 0. The conceptual differences in one table

| Concept | `dsh-android` | `codex-android-mcp` |
| --- | --- | --- |
| Purpose of the stream | A live panel the user watches | Coordinate mapping for input; screenshots on demand |
| Coordinates without a live frame | `wm size` | **Fresh screencap** (rotation-accurate) |
| Device governance | None in the domain layer; harness + HMAC on the *web* stream | `policy.ts`: emulator-by-default, allowlists, per-call re-check |
| Cancellation | `AbortSignal` threaded through adb calls (for the driver) | Removed from adb; `AbortSignal.any` at the server layer |
| Emulator handling | Same | Same |
| adb handling | Same | Same |
| Build/run input safety | Trusts its callers | Strict regex validation + project-root policy |
| Threat model | Untrusted *web clients* | Untrusted *devices/apps*, plus an operator policy |

Everything below is otherwise **shared**.

## 1. The central rule: adb-centric, serial is the only identity

Stated as "decision 0" in their own architecture notes: a device's one and only
identity is the **serial** reported by `adb devices -l`. Emulators, USB phones and
`ip:port` devices share a single code path. Nothing assumes a particular emulator
product; the `emulator` binary is only used by the explicit *boot an AVD* verb, and
**every other verb keeps working without it**.

**adb discovery order** (best-effort, with graceful degradation):

```text
ADB env var  ->  adb on PATH  ->  <sdk-root>/platform-tools/adb
                                  (ANDROID_HOME, ANDROID_SDK_ROOT,
                                   per-OS defaults: ~/Library/Android/sdk,
                                   %LOCALAPPDATA%\Android\Sdk, ~/Android/Sdk)
```

If nothing resolves, the server **still loads and registers its tools**; each call
then fails with an explanatory error. This "degrade, don't refuse" style is the
same one used for OCR and the emulator.

Device rows are parsed into `{serial, state, emulator, model, product, transportId}`
with states `device | offline | unauthorized | recovery | sideload | unknown`;
`emulator` is inferred from an `emulator-` prefix **or** goldfish/ranchu/sdk_gphone
product strings. Details come from `getprop` (model, manufacturer, release, sdk) and,
for emulators, `adb emu avd name` — the console auth token is injected by adb, so no
console port is touched.

**Target resolution** never guesses: explicit serial → the streamed serial → *only
if exactly one online device* → otherwise an error naming the candidates.

## 2. Emulators

Emulators are treated as **optional and unowned**:

- The launcher is resolved best-effort (SDK root → derived from adb's location →
  PATH). Absence is normal and explains itself.
- `listAvds()` uses `emulator -list-avds`, filtering launcher `INFO` chatter.
- `bootAvd()`:
  - takes a **before** snapshot of emulator serials,
  - spawns the launcher **detached and unreferenced** — the emulator deliberately
    outlives the conversation, exactly like a hand-booted one,
  - flags `-avd <name> -no-snapshot-save -no-boot-anim -no-audio`,
  - resolves the new serial by diffing before/after, falling back to a matching
    `avdName` when the launcher deduplicates a second boot of the same AVD,
  - then `waitForBoot()` polls `getprop sys.boot_completed` every 1 s (default
    180 s timeout).
- Shutdown is `adb emu kill` and **only** for a local `emulator-<port>` target;
  physical devices are refused honestly.

Conceptual takeaways: **the emulator is booted, not owned**; readiness is a device
property (`sys.boot_completed`), not a launcher property; and a booted device has no
serial until it appears, so boot is an explicit, long-running, non-idempotent verb.

## 3. The stream model (`AndroidHostController`)

A lifecycle manager for **one live stream slot**:

- **Start**: `ensureStreaming(serial)` serialises launches; concurrent callers share
  one in-flight promise; a request for a *different* serial retires the current loop
  first (one streamed device at a time).
- **Source**: one persistent child,
  `adb -s <serial> exec-out 'while :; do screencap -p; done'` — a PNG-per-frame
  stream far cheaper than spawning `screencap` per frame (~8 fps on an emulator,
  2–5 fps over USB).
- **Framing**: a signature-anchored PNG splitter handles concatenated PNGs, resyncs
  on garbage, and reads width/height straight from IHDR.
- **Fan-out**: subscribers receive frames; the latest frame is the shared source of
  truth for every consumer; a single broken consumer must not stall the fan-out.
- **Lifetime**: a consumer refcount (`acquire`/`release`) plus an **idle timeout**
  (5 min) stop the loop; a **keep-alive** tick (1 s, restart after ~5 s) restarts an
  unintentionally dead loop unless the stop was intentional.
- **Introspection**: `status()` exposes running, serial, restarts, last error, adb
  source, consumers, frame sequence/size and recent stderr.

**Coordinates are normalized 0..1 of the live frame** and mapped to `input` pixels
using the frame's own size. Because the frame follows display rotation and `input`
shares that space, **one mapping serves every rotation** — no inverse math anywhere.

This is the single most important conceptual difference between the two:
`dsh-android` maps coordinates with a live panel always on screen; `codex-android-mcp`
has no panel, so when no frame is live it takes a **fresh screencap** — because
`wm size` commonly reports the portrait *panel* size even in landscape, whereas the
screenshot reports the actual rotated dimensions.

## 4. Input and automation

**Input primitives** (all plain adb verbs):

- tap, drag/swipe (`input tap`, `input swipe` with duration; swipe band clamped to
  avoid the gesture-navigation strips), button (`input keyevent` with a named map or
  a raw `KEYCODE_*`).
- Text: `input text` is ASCII-only, so text is escaped (`%s` for spaces, shell
  metacharacters escaped). Non-ASCII (CJK, emoji) goes through the **ADBKeyboard**
  IME via an `ADB_INPUT_B64` broadcast if installed, otherwise it is **refused with
  an install hint — never silently mistyped**.
- Rotation pins `accelerometer_rotation=0` then sets `user_rotation` (auto-rotate
  would fight the explicit value).
- Device actions are plain adb: `cmd statusbar expand-notifications|expand-settings`,
  `KEYCODE_SLEEP/WAKEUP/ASSIST`, `wm dismiss-keyguard` (best-effort; a PIN-locked
  device stays locked and the stream just shows the lock screen).

**UI inspection** is accessibility-first:

- `uiautomator dump /dev/tty` with an `/sdcard` fallback and cleanup; a
  hand-written, quote-aware XML parser; a compact node model
  (`type, text, contentDesc, resourceId, bounds, enabled, focused, clickable,
  scrollable`) that emits boolean flags **only in the non-default state** and caps
  output (~40 KB) by pruning the deepest levels.
- Element resolution is **identity-first**: exact → case-insensitive contains,
  folding nested duplicate chains to the outermost control, with off-screen and
  disabled gates and an ambiguity list of candidates.

**Interaction closes the verify loop in one call**: every tap returns a post-settle
screenshot and can assert `expect_text` / `expect_gone` (OCR polled for a few
seconds) or, for lists, `expect_count` — with a **probe guard that refuses before
tapping** when the counter key is unknown.

**Lists/feeds** get a dedicated row abstraction: repeated isomorphic siblings
(≥3, same class, near-equal height) are detected as rows, with generic counter
parsing (number + classifier token; multipliers like 万/亿/k/m/w) — because list-item
controls are often unlabeled `ImageView`s with no resource-id or content-desc.

**OCR** is a separate, optional backend (Apple Vision via a Swift helper compiled at
runtime) used only when the UI tree exposes nothing (WebView/Flutter/canvas/games).
macOS-only; it degrades with an explanatory error elsewhere.

## 5. Build → install → launch

The pipeline is deliberate about never guessing:

- **Gradle root** is found by climbing ancestors to `settings.gradle[.kts]`;
  the wrapper (`./gradlew`) is preferred over PATH `gradle`.
- Task construction: `assemble<Variant>` or `:<module>:assemble<Variant>`, with
  `--console=plain --stacktrace` so the captured tail is free of progress-bar
  control sequences and plugin failures are actionable.
- **APK discovery**: newest `.apk` under any `build/outputs/apk/<variant>/`, bounded
  walk depth, skipping `.git/.gradle/.idea/node_modules/src/.kotlin/.cxx`.
- **applicationId** is resolved authoritatively from `output-metadata.json`, then the
  module build script (`applicationId`/`namespace`), then a legacy manifest
  `package=`. `aapt2` is deliberately avoided.
- **Install**: `adb install -r <apk>`, parsing `Failure [INSTALL_FAILED_…]` even
  though adb exits 0.
- **Launch**: `monkey -p <pkg> -c android.intent.category.LAUNCHER 1` (no manifest
  activity needed), inspecting output for "No activities found"; `am force-stop` for
  relaunch.
- **Cancellation**: SIGKILL on POSIX; on Windows `taskkill /PID <pid> /T /F` via a
  fixed System32 path with rollback.

`codex-android-mcp` adds a **strict validation layer** on top: variant, module,
assemble task and every Gradle argument are checked against narrow regexes; the
project root must contain a `settings.gradle[.kts]` **and an executable `gradlew`**,
resolved through symlink-aware, containment-checked paths.

## 6. Process and lifecycle discipline

- Long-lived children get explicit timeouts and bounded buffers (`maxBuffer`).
- `logcat` follow children are killed as a **process group** so their subtree dies.
- Combined client-abort + per-tool timeout (`AbortSignal.any`) at the tool layer.
- The emulator is detached/unref'd on purpose; **adb, not the launcher, is the
  contract**, and the adb server is left running for other tools.
- Everything degrades: a flaky device still lists; details just stay sparse.

## 7. Security and governance — where the two diverge most

They protect against **different threats**, which is why the packaging differs:

- `dsh-android` protects the **web stream**: HMAC capabilities with a short TTL, a
  triple loopback fence (peer address + loopback `Host` + same-origin metadata), and
  screenshot-directory containment (`lstat`/`realpath`/`O_NOFOLLOW`).
- `codex-android-mcp` protects **device operations**: a `policy.ts` loaded from env
  that governs what the agent may touch.

The policy model is the most extractable artefact:

- **Emulator-preferred by default.** A local `emulator-\d+` is allowed; physical
  devices require `allowPhysical` **and** an exact serial in an allowlist **and** the
  serial on **every** call (physical devices are never selected implicitly and are
  hidden from listings).
- **Allowlists** for serials, AVDs, packages and project roots.
- **`android_build_run` is opt-in** and requires trusted roots; it verifies the
  settings file and an executable wrapper, and rejects symlinks/escapes.
- **Authorization is a distinct boundary**, re-checked immediately before execution
  (`authorizeToolCall` re-reads the device list) — mitigating TOCTOU — and is
  explicitly **separate from tool annotations**, which are only hints for the
  client's approval UX.
- Byte caps for images/text and error **redaction** (`sanitizeToolError` strips the
  `text`, `device`, `serial`, `projectPath` arguments from messages).
- Emulator-only shutdown; pid-only backtrace disabled when packages are allowlisted.

Its limitations: policy is **env-var only and frozen at startup**, and errors still
leak the `dsh-android:` brand internally before sanitisation.

## 8. What to extract for `mydev`

1. **adb-centric, serial-as-identity, degrade-don't-refuse.** One adb path, an
   optional emulator, tools that load even when adb is absent.
2. **Boot the emulator, don't own it.** Detached launcher, readiness by
   `sys.boot_completed`, serial discovered by diff, explicit long-running verb.
3. **Normalized coordinates against the live frame** — and the codex lesson: when
   there is no live frame, use a **screencap**, not `wm size`.
4. **The stream controller is a special case of our process lifecycle**: refcount +
   idle timeout + keep-alive + fan-out with latest-wins backpressure. Our version
   generalises it to multiple devices with per-device locks.
5. **Accessibility-first automation with refs**, observe → act-with-assertion in one
   call, a row abstraction for unlabeled feeds, and OCR as an optional backend.
6. **Never guess in build/run**: wrapper-first, applicationId from
   `output-metadata.json`, `install` failure detection, `monkey` launch — plus
   strict argument validation.
7. **The policy model**: emulator-by-default, allowlists, authorization as a
   boundary re-checked before execution, annotations-as-hints, redaction — but
   **live-configurable**, not env-frozen
   ([ADR-0015](../adr/0015-untrusted-data-authorization.md)).
8. **Input text handling**: escape, and refuse non-ASCII rather than mistype.
9. **Honest degradation and bounded output** as cross-cutting defaults.

## Legal note

Both are MIT-licensed; porting algorithms to Go is permitted with attribution in a
third-party notices file.

## Verdict

Conceptually, `codex-android-mcp` = `dsh-android` **minus** harness/web/panel and
**plus** a device-authorization policy, strict validation, and a rotation-correct
coordinate fallback. The Android domain model is the same and is the part worth
extracting; the packaging and the policy are the parts we redesign.
