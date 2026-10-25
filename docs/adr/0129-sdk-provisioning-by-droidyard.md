# ADR-0129: Droidyard provisions the SDK and AVDs, with consent kept human and nothing in use replaced

- Status: Accepted
- Date: 2026-10-25
- Relates to: ADR-0005, ADR-0015 §1, §6, ADR-0020, ADR-0021 §Pid records, ADR-0036, ADR-0060 §2, §4, ADR-0099 §1, ADR-0108 §1; [plan m3f](../plans/m3f-sdk-provisioning.md) Q1–Q9
- Decided by the owner, 2026-10-25: SDK provisioning done by Droidyard itself (the SDK set up, packages and licences, system images, AVD creation), package updates and AVD hardware settings included ("metelo en 3f"); updates allowed to agents at the planner's call. The shape below is the main session's under the owner's standing instruction to apply the recommended option, Delegated.

## Context

`mvp.md` §Out of scope deferred SDK provisioning; `sdk.md` §Provisioning sketched it (never
accept a licence on the user's behalf; an interrupted download leaves nothing half-installed
silently) and `mcp.md` left open whether provisioning is an agent tool and how an agent
meets a licence. Today an agent that hits "platform android-35 is missing" can only hand the
user an `sdkmanager` command (`sdk.requirements`' note).

The forces: licences are a legal act of the user, and ADR-0015 §6 keeps consent and policy
off the agent's channel. `sdkmanager`, AGP and Android Studio share one SDK layout and its
files (`package.xml`, `licenses/<id>`, `config.ini`), so whatever Droidyard writes must be
what they read. Updating or removing a package replaces files that a running adb server, an
emulator or a Gradle daemon may be executing or holding open. SDK packages and AVDs are
host-wide state that other projects rely on, with no undo. The repository's XML and its
licence texts are text from outside (ADR-0015 §1).

Not yet observed, and settled by plan m3f step 1 before code rests on it: the licence
file's hash rule, `sdkmanager`'s behaviour with stdin closed, the launcher's AVD search order,
the `config.ini` keys and formats, and what each running process holds open.

## Decision

1. **Agent tools and human-only calls.** On MCP: `sdk.packages`, `sdk.install` (install or
   update), `sdk.profiles`, `emulator.create`. Audience `human` (ADR-0060 §2):
   `sdk.licenses`, `sdk.acceptLicense`, `sdk.uninstall`, `emulator.delete`,
   `emulator.configure`. None in `--tools core`; none `Lendable` (ADR-0099 §1). The tool-list
   budget grows by ADR-0060 §4's rule; the server instructions do not change.
2. **A licence is (id, sha1), accepted only by a person.** `sdk.acceptLicense {id, sha1}`
   carries the hash of the text the person was shown and is refused unless the repository
   serves that text for that id now (`SDK_LICENSE_UNKNOWN`). It writes the hash into
   `<root>/licenses/<id>` (atomic rename, no symlink), the file the Android tools read, and one
   `daemon.log` line; its scheduler key is its own, so it never waits on an install. No other
   code path writes `licenses/`. `sdkmanager` and `avdmanager` never get consent: stdin
   `/dev/null` (a fixed `no` for `avdmanager create`'s prompt). An agent meets
   `SDK_LICENSE_NOT_ACCEPTED`, naming the person's step.
3. **Listing is modelled from Google's repository XML**, not parsed from `sdkmanager --list`:
   a fixed HTTPS base, every body and count bounded, cached an hour in memory, the stable
   channel and this host's archives and ABI only. Display names and licence texts are tagged
   untrusted; package and licence ids are validated by pattern before they reach an argument
   or a path. A failed fetch still lists what is installed. Installed state is read at each
   repository id's id-as-path directory (`package.xml`, else `source.properties`),
   `cmdline-tools;latest` at the highest revision under `cmdline-tools/*`.
4. **Droidyard owns the flow; the platform tools write the SDK.** Droidyard decides what to
   install or update, checks consent, space and use, reports progress and cancels; every
   write inside the SDK and every AVD creation and deletion runs `sdkmanager` or `avdmanager`
   (ADR-0005). Go itself writes only the licence file (§2), the bootstrap of the tools (§5)
   and the allowlisted keys of an existing `config.ini` (§9).
5. **From nothing, `sdk.install` sets up the tools** into the host SDK if one is found, else
   `ANDROID_HOME`, else `ANDROID_SDK_ROOT`, else the per-OS default; an existing, non-empty
   directory that is not an SDK is refused (`SDK_NOT_WRITABLE`). The `cmdline-tools` archive
   comes from the repository over HTTPS, its size and checksum checked while streaming,
   unpacked in a temporary directory under the root with escapes and symlink entries refused,
   and renamed into place last. No separate capability.
6. **Tool processes are recorded.** Kind `sdk-tool`: its own session and group, the group
   ended SIGTERM then SIGKILL at every end of the call and after the tool exits, no death
   signal, a survivor's group ended by the next daemon's Reconcile. Short `avdmanager` runs are
   recorded too, beyond the exemption ADR-0021 §Pid records gives short commands. Bounds (working
   defaults): install 60 min and 10 min without output; anything else 5 min. One `sdk:<root>`
   key serialises installs and uninstalls on a root; an AVD's create, edit and delete take
   `emulator.boot`.
7. **AVDs are created in the launcher's first AVD directory** (the order observed by plan
   m3f step 1); a name present in any AVD home is refused (`EMULATOR_AVD_EXISTS`). Delete and
   edit refuse a name in two homes (`EMULATOR_AVD_AMBIGUOUS`) and a running AVD
   (`EMULATOR_AVD_RUNNING`): running under any adb server, by this daemon's listing, by an
   emulator process whose arguments name it, by a candidate process (§8) holding a file open
   in its `.avd` directory, or by the emulator's lock; any doubt counts as running.
8. **Updates go through `sdk.install`, never of a package in use.** An installed id with a
   newer revision is updated, one at the latest left alone, and a dependency below a
   package's `min-revision` updated with it, named on the argv; `sdk.install` is therefore
   destructive. Before any update or uninstall each package to be replaced or removed is
   checked: a process of the user running an executable inside it (the full executable path,
   never the 16-character name); a **candidate** process holding a file open inside it
   (`lsof` over the processes whose executable lies under the root or whose arguments name it,
   and those named `java`, `adb`, `emulator`, `studio` or `qemu-system-*`); or a Droidyard
   boot or build in flight that needs it. Any doubt (an unreadable live process, an `lsof`
   failure, a live candidate missing from its output) counts as in use; zombies and vanished
   processes are skipped. `SDK_PACKAGE_IN_USE` names what uses each package, and its next step
   asks the user, never the agent, to stop it.
9. **AVD hardware settings are typed, not keys:** display name, RAM, VM heap, internal
   storage, SD card (at creation only, through `avdmanager --sdcard`), CPU cores, graphics,
   orientation, device frame, boot (quick or cold; `emulator.start`'s `coldBoot` wins) and
   keyboard, each mapped to one `config.ini` key, with ranges bounded by the host. That map
   is an allowlist: Go writes only those keys, keeps every other line byte for byte, writes
   atomically, never through a symlink, never on a running or ambiguous AVD, and never
   shrinks internal storage. Creation with settings is all or nothing. `emulator.configure`
   edits a stopped AVD and is destructive (a hardware change discards the quick-boot
   snapshot). `emulator.list` reports the values.

## Consequences

- The contract grows by nine capabilities, a new `SDK_` error-code prefix and six
  `EMULATOR_` codes, in one minor version; the codes and their meanings live in the
  descriptors and `schema/capabilities.json`.
- MCP grows by four tools, about 4 KB of `tools/list`; `maxToolListBytes` follows.
- `sdk.install` carries the destructive hint even for a fresh install, so a client asks for
  approval each time; if that proves noisy, a separate `sdk.update` is the way back (below).
- The in-use check has known blind spots, each a register row: a process that starts using a
  package after the check, a non-candidate process holding an SDK file, a Droidyard boot or
  device call that starts during an update (one scheduler key per call), other users'
  processes.
- The daemon fetches from `dl.google.com` on `sdk.packages` and `sdk.install`: the first
  network reads Droidyard makes on its own behalf.
- The residual risk is ADR-0015 §6's: an agent's own shell can run
  `droidyard sdk accept-license`, as it can run `policy update`.
- Documents that follow: `sdk.md` §Provisioning and §Reading what is installed;
  `emulator.md` §AVD discovery, §Capabilities, §Flags and configuration; `mcp.md` §What is
  exposed and §Features beyond tools; `mvp.md` §MVP capability surface and §Out of scope;
  `process-lifecycle.md` (kind `sdk-tool`, `lsof`).

## Alternatives considered

- **Every provisioning call human-only.** The agent that hits a missing platform could only
  hand the user a command, which the owner's brief rules out.
- **Every call on MCP.** The agent would consent for the user and delete AVDs and packages it
  did not make, which other projects rely on.
- **Licences through MCP elicitation.** Client support is uneven, and a client that
  auto-approves turns it into consent by the agent; ADR-0015 rejected elicitation-gated
  policy for the same reason. Wins if clients guarantee a human answers an elicitation.
- **Answering `sdkmanager --licenses` prompts.** It accepts whatever the tool shows next,
  not a text the person was shown; kept only as the fallback writer should the hash rule not
  match the files the tools write.
- **Accepting a licence by id without a hash.** One id has several texts across the
  repository's lists, so the person would accept a text they never saw.
- **`sdkmanager --list`, verbatim or parsed.** No sizes, no licence texts, no stable format,
  and a JVM start per listing.
- **A package manager in Go.** It reimplements the formats AGP and Android Studio parse; one
  divergence corrupts an SDK they share.
- **Android CLI** as the runner: ADR-0005 rejected it.
- **A separate `sdk.setup`.** One more tool for a step `sdk.install` can take when the tools
  are absent.
- **A separate `sdk.update`.** One more tool and a second path to the same `sdkmanager` call;
  it wins if the destructive hint on every install proves noisy.
- **Updates human-only.** The owner allowed agents; consent is the licence's, and the in-use
  check guards the harm.
- **In use by process names (`ps`), or `lsof` over every process of the user.** A 16-character
  name misses `qemu-system-aarch64-headless` and cannot tell an adb from another SDK; `lsof`
  over every process is unbounded in output and time.
- **Refusing every update while any emulator or adb server runs.** It refuses updates of
  packages nothing uses.
- **Unrecorded tool runs, or a death signal.** A killed daemon would leave `sdkmanager`
  downloading; a death signal reaches the leader only (the `git-write` precedent).
- **`avdmanager`'s own AVD directory, or deleting the first match.** The first may not be
  where the launcher looks; the second deletes an AVD the user did not mean.
- **A raw key and value map for settings, or rewriting `config.ini` from a parsed map.** The
  first lets any key through and checks each format anyway; the second loses comments, order
  and unknown keys.
- **Launch flags (`-memory`, `-cores`) at each boot.** Settings that persist belong to the
  AVD (`emulator.md` §Flags and configuration), and Android Studio would not see them.
