# ADR-0130: Droidyard installs SDK packages itself; `avdmanager` stays for AVDs

- Status: Accepted
- Date: 2026-10-26
- Supersedes: ADR-0129 §4, §5, §6 (in part); ADR-0005 §Decision (in part: `sdkmanager`)
- Relates to: ADR-0129 §2, §3, §8; [spike m3f step 1](../research/spike-sdk-provisioning-2026-10.md); [plan m3f](../plans/m3f-sdk-provisioning.md) §Changes
- Decided by the main session under the owner's standing instruction to apply the recommended option, Delegated (2026-10-26), on the spike's evidence. The plan's step 1 named this case as one that stops the milestone for the owner; the owner's brief ("done by us", 2026-10-25) and ADR-0005 point the same way, so the recommendation was applied and is reported for the owner to revert.

## Context

ADR-0129 §4 had `sdkmanager` write every package so its `package.xml` files are the ones other
Android tools read, and §2 relied on the tool refusing a licence nobody accepted. The spike
found both premises broken:

- **Today's `sdkmanager` is Android CLI.** In `cmdline-tools;23.0`, today's
  `cmdline-tools;latest`, `bin/sdkmanager` is a script that runs Android CLI.
- **It accepts licences on its own.** Run with stdin closed and no licence file, Android CLI
  downloaded itself and a JRE into `$HOME/.android`, announced usage-data collection, wrote
  `licenses/android-sdk-license` with nobody's answer, and installed the package.
- **The classic tool is deprecated, and its refusal is silent in the exit status.**
  `cmdline-tools;22.0` is the last archive with the classic `SdkManagerCli`. It still refuses
  correctly, but it exits 0 when it refuses.
- **`avdmanager` is unchanged.** In 23.0 it is still the classic Java tool.

The spike also observed everything an installer needs:

- the repository's elements and archives (one top directory, no symlinks, sha1 checksums);
- the hash `sdklib` writes and checks (sha1 of the `TrimStringAdapter`-normalised text, which
  matches the owner's own files);
- the `package.xml` form the classic tool and Android CLI both write.

## Decision

1. **Droidyard installs, updates and removes SDK packages itself, in Go.** No `sdkmanager`,
   classic or Android CLI, and no `android` binary, is ever run. For each package of the
   closure (ADR-0129 §8), dependencies first:
   - **Download.** The `complete` archive for this host, from the URL the repository gives
     relative to its list, over HTTPS on the repository's host. Its sha1 is checked while
     streaming, and the download stops past the declared size. It goes to
     `<root>/.temp/droidyard-<random>/`.
   - **Unpack.** The entry names must stay inside the temporary directory, with exactly one top
     directory, regular files and directories only (no symlinks), and the entry count and
     uncompressed total bounded. Modes are `0755` when any execute bit is set, else `0644`.
   - **Write `package.xml`** into the top directory, in the form the spike recorded: the
     licence text as served, and a `localPackage` carrying the remote package's `type-details`,
     `revision`, `display-name`, `uses-license` and `dependencies`.
   - **Move into place.** Rename to `<root>/<id with ; as />` last. An update moves the old
     directory into the temporary one first and removes it after the rename.

   A killed daemon leaves only `.temp/droidyard-*`, which the next install sweeps once it is an
   hour old. Nothing reaches the final path half-written.
2. **Free space is checked twice:** before the downloads (the archives' total), then per
   package from its zip's central directory before unpacking (the uncompressed total plus a
   margin). An image unpacks to 6.6× its archive.
3. **The licence files are written by `sdk.acceptLicense` only** (ADR-0129 §2). The hash is
   sdklib's: `sha1(UTF-8(text))` after `TrimStringAdapter`'s four steps (the spike's §(a)).
   AGP, Android Studio and the classic tools read the same file.
4. **No bootstrap.** On a host with no SDK, `sdk.install` creates ADR-0129 §5's target root and
   installs into it like any other package. `cmdline-tools` is a package needed only for
   `avdmanager`.
5. **`avdmanager` (classic, from `cmdline-tools`) stays the writer of new AVDs and the remover
   of deleted ones**, under ADR-0129 §6's recorded `sdk-tool` process rule. Installs and
   uninstalls run in the daemon, cancelled through the call's context, and spawn nothing. A
   JVM is needed only for `avdmanager`.

## Consequences

- An install no longer needs a JVM or `cmdline-tools`. `SDK_JAVA_NOT_FOUND` and
  `SDK_TOOLS_NOT_FOUND` apply only to `sdk.profiles`, `emulator.create` and `emulator.delete`.
- `SDK_TOOL_FAILED` names an `avdmanager` failure only. A download, checksum or unpack failure
  is `SDK_DOWNLOAD_FAILED`.
- Droidyard now owns a writer of a format other tools parse. It is held by these checks:
  - a golden `package.xml` from the spike;
  - an opt-in test in which the classic `sdkmanager` (22.0) lists Droidyard's packages as
    installed;
  - at level 5, `avdmanager` creating an AVD from a Droidyard-installed image, the emulator
    booting it, and a Gradle build using a Droidyard-installed platform and build-tools.

  Android Studio's reading is not run (a register row).
- Patches (`archive/patches`) are not used. Every install downloads the complete archive.
- Google's own tools may change the format. A future change shows in these checks and in the
  repository's schema versions (`repository2-4.xml` is already served beside `-3`).
- Documents that follow: `sdk.md` §Provisioning; `process-lifecycle.md` (the `sdk-tool` kind
  for `avdmanager` only); `docs/adr/0005-reuse-platform-tools.md` and
  `docs/adr/0129-sdk-provisioning-by-droidyard.md` gain the `Superseded in part by` line.

## Alternatives considered

- **Drive Android CLI (`android sdk install`).** ADR-0005 rejects it: an opaque binary, and a
  moving target. It also accepts licences with nobody's answer, downloads itself and a JRE into
  the user's home, collects usage data by default, and leaves `.sdk/` in the SDK root. Wins if
  Google documents a stable, consent-respecting interface for it, and the owner reopens
  ADR-0005.
- **Pin the classic `sdkmanager` of `cmdline-tools;22.0`.** It keeps ADR-0129 §4's shape, but on
  a tool Google has deprecated. Droidyard would have to install a second `cmdline-tools` beside
  the user's own. The tool's refusal exits 0, so only its output would show it. And it reads the
  `-3` lists, which the `-4` ones already succeed, so one repository change ends it.
- **Keep `sdkmanager` (any version) and rely on Droidyard's licence pre-check alone.** With 23.0,
  the tool's own acceptance writes `licenses/` for whatever closure the tool computes, outside
  Droidyard's consent path. ADR-0129 §2 forbids any other writer of `licenses/`.
- **Reuse the archives' patches to save downloads.** That is a second unpacking path for a
  bandwidth saving that the stable channel's complete archives do not need.
