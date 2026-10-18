# ADR-0032: The configuration file, version 1: policy and tool keys

- Status: Accepted
- Superseded in part by: ADR-0081 (§2)
- Superseded in part by: ADR-0098 (§5)
- Date: 2026-10-07
- Relates to: ADR-0031 (the file and its trust section, which this extends), ADR-0015
  §4, §6, §7 (device policy, human-only policy, `shell.exec`), ADR-0018 rule 3 (the
  below-floor escape hatch), ADR-0019 §8 (a GUI-launched daemon's environment),
  `capability-api.md` §Open questions (config file spec), `gradle.md` §Sidecar JVM
  discovery, `adb-device.md` §adb resolution and degradation
- Decided by the main session under the owner's standing instruction to apply the
  recommended option (Delegated, 2026-10-07, so milestone 1k can plan `policy.*` and
  `shell.exec`); the owner can revert it.
- Amended: 2026-10-07 (Delegated, plan m1k Q1). Enabling `shell.exec`, globally or for one
  root, gives the agent the user account's full reach, not confined to that root:
  droidyard's configuration and trust included (`droidyard trust`, `droidyard call
  policy.update` as a `cli` client, or an edit of `config.json`). No fence is attempted;
  a same-uid process can always write the file. The human is told before enabling (the
  `shell.exec` description and `POLICY_SHELL_DISABLED`'s message), and the description
  tells the agent never to change droidyard's configuration or trust through it.
  Rejected: a marker variable that `droidyard trust`/`call` refuse (bypassed by editing
  the file); a sandbox (platform-specific, no MVP item needs it).
- Amended: 2026-10-07 (Delegated, plan m1k Q2). Version 1 also defers the HTTP port
  (7317 with its ephemeral fallback stays), the environment source (inherit), idle
  periods and build retention, to later sections. `tools.adb` precedes the `ADB`
  variable, the reverse of `adb-device.md`'s order (§3 makes the key the first
  candidate). `gradle.allowBelowFloor` is one global key read at start, not per
  workspace (ADR-0018 rule 3 amended). The Alternatives' claim holds only for a patch
  that does not carry `trust.roots`: Merge Patch replaces arrays whole, so such a patch
  racing a `droidyard trust` can lose a root.
- Amended: 2026-10-07 (Delegated, m1k close review B1). Consequences' second bullet
  overstates: an agent cannot change policy through droidyard's agent-facing surface,
  but on a trusted root it can through the build (ADR-0015, second 2026-10-07
  `Amended:` line), shell or no shell. `droidyard trust` now says so when it trusts.
- Amended: 2026-10-08 (Delegated, plan m1p Q2). §5 gains an optimistic check:
  `policy.get` returns `revision`, the hex SHA-256 of the bytes its policy was parsed from
  (`"absent"` when there is no file; an invalid file has the hash of its bytes);
  `policy.update` takes an optional `ifRevision`, compared under the lock with the bytes
  just re-read: a difference fails the new code `POLICY_REVISION_CONFLICT` (nextStep: call
  `policy.get` and apply the change again) and writes nothing. Without `ifRevision` the
  behaviour is unchanged. Any writer, a hand edit included, changes the revision. It
  closes the lost `trust.roots` entry when a patch races `droidyard trust` (plan m1k D3)
  for a client that sends it. Rejected: an HTTP `ETag` only (the CLI writes too); merging
  arrays (not RFC 7386); a lock held from `get` to `update` (spans calls); a counter in the
  file (a hand edit forgets it).

## Context

ADR-0031 started `config.json` with trusted roots only and left the rest "to the
configuration specification". Phase 1 still lacks `policy.get`/`update` (human-only)
and `shell.exec` (off by default), and three documents point at config keys that do
not exist: the physical-device allowlist (ADR-0015 §4), the sidecar's Java home
(`gradle.sidecarJavaHome`) and the below-floor hatch (`gradle.allowBelowFloor`). A GUI-
launched daemon has no shell environment, so these keys are the only fix there.

## Decision

1. **One file, `config.json`, `schemaVersion: 1`**, where ADR-0031 put it. Every key is
   optional; a missing key is its default. Unknown keys are kept on rewrite and ignored,
   so a newer droidyard's keys survive an older one's edit.

   ```json
   {
     "schemaVersion": 1,
     "trust":   { "roots": [], "prefixes": [] },
     "devices": { "allowPhysical": false, "allowlist": [] },
     "shell":   { "enabled": false, "roots": [] },
     "gradle":  { "sidecarJavaHome": null, "allowBelowFloor": false },
     "tools":   { "adb": null, "emulator": null }
   }
   ```

2. **Policy** (`trust`, `devices`, `shell`), ADR-0015:
   - `devices.allowPhysical` and `devices.allowlist` (exact serials): a physical device is
     usable only when both admit it, and its serial is named on every call (§4).
     Emulators need no entry.
   - `shell.enabled` turns `shell.exec` on everywhere; `shell.roots` (absolute, canonical,
     prefix at a path-component boundary as `trust.prefixes`) turns it on for those
     workspaces only. Off by default; a call otherwise fails `POLICY_SHELL_DISABLED`
     (§7). `shell.exec` also needs a trusted root.
3. **Tool keys** (not policy; read at daemon start, ADR-0019 §8):
   `gradle.sidecarJavaHome` is the first JVM candidate (`gradle.md` step 1);
   `gradle.allowBelowFloor` is ADR-0018 rule 3's hatch (the conservative profile,
   `validation: unsupported`); `tools.adb` and `tools.emulator` are the first binary
   candidates. Every path is absolute; a relative one makes the file unparseable.
4. **Validation and failure** follow ADR-0031: a file that does not parse, has a wrong
   type, a relative path or a newer `schemaVersion` trusts nothing, admits no physical
   device, keeps `shell.exec` off and uses no tool key; the error names the file and is
   reported by `system.health` and by every capability the bad file affects.
5. **`policy.get` and `policy.update`** (audience `human`; the core refuses an MCP session
   by its client kind, ADR-0015 §6): `get` returns the policy section as read, the file's
   path and whether it parsed. `update` takes a JSON Merge Patch (RFC 7386) limited to
   `trust`, `devices` and `shell`, validates the result, and writes it as `droidyard
   trust` does (exclusive lock, re-read, atomic write, unknown keys kept). Tool keys are
   edited by hand. `droidyard trust` may stay a direct write.
6. **Reload.** The daemon re-reads the file by size and mtime at each policy check
   (ADR-0031 §4); tool keys take effect at the next daemon start.

## Consequences

- Every config key the documents promised now has a defined place, type and default.
- An agent still cannot change policy: `policy.update` is human-only, and `shell.exec`
  needs a human to enable it and to trust the root.
- The file stays hand-editable; `policy.update` is a convenience for the UI and the
  CLI, not the only writer.
- Timeouts stay constants; a later version may add keys under a new section.

## Alternatives considered

- **Separate files per concern** (`trust.json`, `devices.json`). Simpler merges, but
  several locks and several "which file is wrong" errors for a handful of keys.
  Rejected.
- **`policy.update` replacing whole sections.** Easier to implement; a UI toggling one
  flag would have to read, edit and send the whole section and could lose a concurrent
  `droidyard trust`. Merge Patch under the lock avoids both. Rejected.
- **Environment variables for the tool keys only.** They already exist (`JAVA_HOME`,
  `ADB`), but a GUI-launched daemon does not see them. Rejected as the only mechanism.
