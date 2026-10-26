# Spike: the SDK repository, licences, the platform tools and AVD homes (m3f step 1)

- Date: 2026-10-26
- Plan: [m3f step 1](../plans/m3f-sdk-provisioning.md#1-the-facts-the-steps-rest-on-spike)
- Host: macOS arm64, zulu-17 (17.0.11). Scratch roots under
  `~/Library/Caches/droidyard-dev/3f/spike/`, each run with its own `HOME`, `ANDROID_USER_HOME`
  and `ANDROID_HOME`, `PATH=/usr/bin:/bin:/usr/sbin:/sbin`. The owner's SDK, AVD files and
  processes were only read; the owner's `emulator` binary (36.6.11.0) ran only `-list-avds`
  under a scratch `HOME`.
- Evidence: the scripts and their outputs in `~/Library/Caches/droidyard-dev/3f/evidence/`.

## The finding that redrafts the plan: `sdkmanager` is now Android CLI

`cmdline-tools;23.0`, today's `cmdline-tools;latest` (stable channel), ships `bin/sdkmanager` as
a shell script that prints "The SDK Manager CLI tool (sdkmanager) is deprecated. Android CLI will
be used instead" and runs `bin/android --sdk=<root> sdk <args>`, the Android CLI binary
(3.6 MB, also in 22.0's archive). `lib/sdkmanager-classpath.jar` is gone, and `--licenses` prints
"no longer needed".

Run as `sdkmanager --sdk_root=<scratch> --install platform-tools` with stdin `/dev/null` and no
`licenses/` directory, it:

- downloaded Android CLI into `$HOME/.android/bin/android-cli` and a bundled JRE into
  `$HOME/.android/cli/bundles/`;
- printed the Terms of Service URL and a notice that it collects usage data;
- **wrote `licenses/android-sdk-license` itself, accepting the licence with no answer from
  anyone**;
- installed `platform-tools`, exiting 0;
- left a `.sdk/` directory in the SDK root (`lock`, `objects/`, `refs/`, 436 KB).

The plan's step 1 Check names this case: `sdkmanager` accepting with stdin closed stops the
milestone for the owner. ADR-0005 rejects Android CLI as a dependency.

`cmdline-tools;22.0` is the last archive with the classic `sdkmanager` (a Java launcher of
`SdkManagerCli`). With stdin `/dev/null` and no licence file it printed the licence, then
"Skipping following packages as the license is not accepted" and "The following packages can not
be installed since their licenses or those of the packages they depend on were not accepted",
**and exited 0**: the refusal shows only in the output. With the licence file written by the rule
below it installed `platform-tools` and exited 0. It writes `<root>/.temp/` and
`$HOME/.android/cache/` (44 entries).

`avdmanager` in 23.0 is still the classic Java launcher (`com.android.sdklib.tool.AvdManagerCli`).

## (a) The licence hash

The classic tool computes `sha1(UTF-8(value))` (`com.android.repository.api.License.getLicenseHash`).
`value` is the licence text after `com.android.repository.impl.meta.TrimStringAdapter.unmarshal`,
read with `javap` from `tools.repository.jar` (22.0), which applies four steps in order:

1. `replaceAll("(?<=\\s)[ \t]*", "")`
2. `replaceAll("(?<!\n)\n(?!\n)", " ")`
3. `replaceAll(" +", " ")`
4. `trim()`

The rule reproduces both of the owner's files: `android-sdk-license` holds
`24333f8a63b6825ea9c5514f83c2829b004d1fee` and `android-sdk-arm-dbt-license` holds
`859f317696f67ef3d7f30a50a5560e7834b43903`, each one line after a leading newline, 41 bytes, no
trailing newline. The raw text's sha1 (`efa68a6b…`) and the plainly trimmed text's sha1
(`9002c006…`) match nothing. Android CLI wrote the same `24333f8a…` line.

## (b) The repository

- **Index files.** `repository2-3.xml` (423 KB, 320 packages) and `addons_list-5.xml` (3.8 KB)
  are served. So are `repository2-4.xml` (424 KB), `addons_list-6.xml` (byte-identical to
  `-5`) and a `sys-img2-4.xml` beside each of the 16 `sys-img2-3.xml` lists. The `-4` versions
  are newer, and the `-3` versions are still current.
- **Lists.** `addons_list-5.xml` names 16 system-image lists (`sdk:sysImgSiteType`, 134–238 KB,
  2–92 packages each) and 2 add-on lists, every URL relative to the base. In total 741
  packages, 1,320 archives.
- **Elements.** Every `remotePackage` carries `path`, `obsolete` (optional), `type-details`,
  `revision` (`major`, `minor`, `micro`, `preview`), `display-name`, `uses-license ref`,
  `channelRef`, `dependencies/dependency[@path]/min-revision` (optional) and
  `archives/archive/complete/{size, checksum[@type], url}`, with optional `host-os` (`macosx`,
  `linux`, `windows`) and `host-arch` (`x64`, `aarch64`, `x86`).
  - Every checksum is `type="sha1"`.
  - Every archive URL is a bare file name, relative to its list's directory (for example
    `sys-img/android/arm64-v8a-28_r02.zip`).
  - Every package id matches step 2's pattern.
- **Duplicate ids.** One id can appear twice in one list on different channels: `emulator` is
  37.2.12 on `channel-0` and 37.3.5 on `channel-2`. The model picks the stable entry per id.
- **Licences.** 10 ids. Each id has one text across every list that declares it, and each list
  declares the licences its packages use, so `uses-license` always resolves in the package's
  own list. The (id, sha1) model holds even though one text per id is what was observed today.
- **System images.** 275 of 382 depend on `emulator` with a `min-revision`.
- **Command-line tools.** `cmdline-tools;latest` is 23.0 (`commandlinetools-mac_arm64-16111833_latest.zip`,
  155,384,151 bytes, sha1 `ad03dc49…e830`). Versioned ids from `cmdline-tools;1.0` to
  `cmdline-tools;23.0` are listed beside it.
- **Archives step 11 downloads.**
  - `platform-tools` 37.0.1: 16,110,554 bytes.
  - `emulator` 37.2.12 (darwin aarch64): 416,112,708 bytes.
  - The smallest stable arm64 image at API ≥ 28 is `system-images;android-28;default;arm64-v8a`
    r2: 426,760,297 bytes, licence `android-sdk-license`, depends on `emulator` ≥ 31.1.1.
- **Archive layout**, read from each zip's central directory with an HTTP range request, without
  a full download. Each archive has one top directory, and it is not the package's path
  (`build-tools;36.1.0` unpacks as `android-16/`). No archive has a symlink entry. Modes are
  `0755` or `0644`, or `0` (the image zip, DOS-made).

  | Archive | Entries | Uncompressed | Ratio to the archive |
  | --- | --- | --- | --- |
  | `platform-tools` | 14 | 38.8 MB | 2.4× |
  | `emulator` | 404 | 1.31 GB | 3.1× |
  | android-28 image | 14 | 2.81 GB | 6.6× |
  | `platform-36` | 11,342 | 106 MB | 1.6× |
  | `build-tools` 36.1 (macOS) | 197 | 201 MB | 2.5× |

  The plan's "twice the download" free-space rule is short for images. Step 6's caps of 1 GiB
  and 20,000 entries fit `cmdline-tools` only.

## (c) What the tools write

- **`package.xml`.** The classic tool's `platform-tools/package.xml` is `<ns2:repository>` with
  a fixed preamble of namespace prefixes `ns2`–`ns18` (common 01/02, generic 01/02,
  repository2 01–04, addon2 01–04, sys-img2 01–05). Inside it:
  - one `<license id type="text">` holding the served text;
  - one `<localPackage path obsolete="false">` with the remote package's `type-details`
    (`xsi:type` remapped to the preamble's prefix), `revision`, `display-name` and
    `uses-license`.

  Android CLI wrote the same shape.
- **Cancellation, the final path after a kill, and updates through the tool.** Not observed:
  they matter only if the plan keeps `sdkmanager` (see the end).
- **The unpacked `cmdline-tools` (e).** The top directory is `cmdline-tools/`. It has
  `source.properties` only, with `Pkg.Path=cmdline-tools;23.0` (not `;latest`) and
  `Pkg.Revision=23.0`. It has 328 entries, every one mode `0755`, no symlink, 180 MB
  uncompressed.

## (d) AVD homes and profiles

`emulator -list-avds` (36.6.11.0) with a distinct AVD in each of five homes and one name shared
by all of them lists **one directory only**. It takes the first of these that exists:

1. `ANDROID_AVD_HOME`
2. `ANDROID_SDK_HOME/.android/avd`
3. `HOME/.android/avd`

`ANDROID_EMULATOR_HOME/avd` and `ANDROID_USER_HOME/avd` were never listed, alone or in pairs. An
`ANDROID_AVD_HOME` that does not exist fell back to `HOME`. The shared name appeared once, from
the chosen directory.

`avdmanager list device` (23.0, stdin `/dev/null`) exited 0 with 96 profiles. Each profile is a
block like this:

```text
id: <n> or "<id>"
    Name: <name>
    OEM : <oem>
    Tag : <tag>
---------
```

`Tag` is optional. `avdmanager list device -c` prints the ids alone, one per line.

No owner emulator was running, so the lock a running emulator holds in its `.avd` directory was
not observed.

## (f) Processes and open files

The only SDK process running was the owner's adb server (pid 72901). `kern.procargs2` gave its
executable `/Users/jorgetortosa/Library/Android/sdk/platform-tools/adb` and its arguments
`adb -L tcp:5037 fork-server server --reply-fd 4`, while `ps -o comm=` says `adb`. No emulator,
Gradle, Kotlin or Studio process ran, so those were not observed.

Over the user's 316 processes, `kern.procargs2` failed in three cases, each with `EINVAL`
(errno 22):

| Process | `kern.proc.pid` shows | Rule |
| --- | --- | --- |
| Two setuid `login` processes | `P_stat` 2 (running), `P_svuid` 0 | Skipped: no executable inside a user's SDK is setuid to another user. Otherwise every check would be a permanent doubt. |
| A zombie | `P_stat` 5 (`SZOMB`) | Skipped |
| A reaped child | absent | Skipped |

The same errno (22) came back for pid 1 (root).

`/usr/sbin/lsof -n -P -w -F pfn -p 72901` exited 0 with 31 lines, all of them `p`, `f` or `n`,
and one file under the SDK: the `txt` entry `<SDK>/platform-tools/adb`. A sanitised sample is
kept for step 5's fake. For a vanished pid it exited 1 with no output and no stderr, with and
without `-w`. For a live pid it printed its `cwd` first.

## (g) AVD settings

Read only from two lane AVDs `avdmanager` created (`m2w31`, `m2w36`), whose lines are written
`key = value`:

| Setting | Key and format |
| --- | --- |
| RAM | `hw.ramSize = 1536M` |
| VM heap | `vm.heapSize = 228M` |
| Internal storage | `disk.dataPartition.size = 6442450944` (bytes) |
| SD card | `hw.sdCard = yes`, `sdcard.size = 512 MB` (`avdmanager`'s default without `--sdcard`) |
| CPU cores | `hw.cpu.ncore = 4` |
| Graphics | `hw.gpu.enabled = no`, `hw.gpu.mode = auto` |
| Orientation | `hw.initialOrientation = portrait` |
| Device frame | `showDeviceFrame = yes` |
| Boot | `fastboot.forceColdBoot = no`, `fastboot.forceFastBoot = yes`, `fastboot.forceChosenSnapshotBoot = no` |
| Keyboard | `hw.keyboard = no` |

`avd.ini.displayname` was absent: Android Studio writes it and `avdmanager` does not. It is taken
from the emulator's documentation, not observed.

## What this changes

`sdkmanager` can no longer be the writer of the SDK.

- Today's tools accept licences on their own.
- They are Android CLI, which ADR-0005 rejects.
- Pinning `cmdline-tools;22.0` keeps the plan's shape, but on a tool Google has deprecated, whose
  refusal exits 0 and which reads the `-3` lists that the `-4` ones already succeed.

What the spike shows Droidyard would need to install packages itself:

- the repository model;
- size and sha1 checks while streaming;
- an unpack of one top directory into the id-as-path directory;
- the `package.xml` form above;
- the hash rule in (a).

Each of these is observed above. The owner's brief also asks for provisioning "done by us".
The decision, and the plan's redraft, are ADR-0130's and the plan's §Changes.
