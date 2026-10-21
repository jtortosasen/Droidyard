# Spike: real apps' full dumps and events, and what a dump costs others (m2x step 1)

- Date: 2026-10-21
- Plan: [m2x step 1](../plans/m2x-ui-recording.md#1-spike-real-apps-dumps-and-events-and-what-a-dump-costs-others)
- Devices: the lane's scratch AVDs only, `m2x31` (API 31 `google_apis` arm64, `emulator-5590`,
  Chrome on its first-run version) and `m2x36` (API 36 `google_apis_playstore` arm64, a `user`
  build, `emulator-5592`, Chrome's and the notification prompt's first-run screens tapped once,
  Play Store signed out), on the lane's own adb server (port 5211) behind a guard that refuses any
  other serial. No 5037 server ran before or after; the owner's daemon (pid 41580) kept running
  untouched.
- Code: an isolated daemon (`-tags droidyard_nodump`) built at `5f83ffb`; the reader dex
  `b855f4a0…89ec` (2w's). Pages over the emulator's network (en.wikipedia.org).
- Evidence: `~/Library/Caches/droidyard-dev/m2x/spike.json`, `spike-flag.json` and the scripts
  beside them (`spike.py`, `order*.py`, `fresh.py`, `chromeflag*.py`).
- **Times are the CLI's wall clock** (`droidyard call`, one process per call), not
  `meta.durationMs`: the plan named a field the CLI's answer does not carry. The CLI's own cost,
  `system.health`, is 8 ms median, 27 ms max (10 calls).

## M1: full dumps on real screens (20 each, API 36)

| Screen | Nodes | Bytes | p50 ms | p95 ms | max ms | Failures |
| --- | --- | --- | --- | --- | --- | --- |
| Settings, main list | 72 | 35,703 | 27 | 33 | 76 | 0 |
| Settings › Apps › all apps | 48 | 20,833 | 27 | 93 | 100 | 0 |
| Chrome, a long Wikipedia article | 30 | 15,180 | 27 | 201 | 272 | 0 |
| the same, `invisible` | 35 | 17,568 | 28 | 33 | 66 | 0 |
| Play Store, first screen (sign-in) | 14 | 6,974 | 28 | 35 | 70 | 0 |
| **Chrome, the article, its page exposed** (below) | 688 | 375,908 | 130 | 306 | 1,803 | 2 of 20 past the bound |
| the same, `invisible` | 693 | 378,133 | 195 | 377 | 2,372 | 0 |

Play Store signed out is a light screen, not the heavy one the plan meant. The first Chrome rows
are the page hidden (a `WebView` leaf, below); the heavy screen is Chrome with its page exposed.

## When Chrome exposes a page's content

Chrome builds its page's accessibility tree only while the system reports an accessibility client,
and follows that state live. Observed on `m2x36` (`order2.py`, `fresh.py`):

| Case | `accessibility_enabled` when Chrome started | Dumps |
| --- | --- | --- |
| Chrome's data cleared, first run passed, the article opened, the reader connected throughout | 1 | 686 nodes from the first dump, 12 dumps over 60 s |
| the reader killed, Chrome force-stopped and started with no client | 0 | 30 nodes (the page a `WebView` leaf holding its title), then 3 dumps failing at the 2.5 s bound |
| Chrome force-stopped and restarted with the reader connected | 1 | 687 nodes, 4 of 4 |
| Chrome running with its tree, the reader killed (0), then the page reopened and dumped | 0, then 1 at the dump | 30 nodes, then 688 from the next dump 3 s later, 15 of 15 |

So a reader connecting while Chrome runs turns the tree on within seconds, and dumps may fail at
the bound while Chrome builds it. On a device with any accessibility service enabled (the
owner's phones) Chrome always has it.

**Not explained:** in M1 and in the first runs on both images, Chrome kept the page a leaf for
minutes with the reader connected (20 dumps, all 30 nodes; on API 31 Android's own
`uiautomator dump`, taken with the reader stopped, gave the same 26 nodes as `device.uiDump`). It
cleared when Chrome restarted while the reader was connected, or when the reader reconnected.
Chrome's `--force-renderer-accessibility` switch was tried then (`chrome-command-line`, with
`am set-debug-app` on the `user` build) and the page appeared (655 nodes on API 31, 687 on
API 36), but each try also restarted Chrome under a connected reader, so the switch is **not shown
to matter**; with it removed and the debug app cleared, Chrome kept exposing the page. The M1
rows with the page or web content exposed are the tree Chrome gives normally.

A page load seen through successive dumps (`order.py`: Chrome reopened on the article, a dump
every ~0.2 s after the previous answer):

```text
at ms   287  573  2462  2727  3022  3884  4688  5123 … 10887
nodes    23   23    31    32    30   635   685   688 …   688
dump ms 287   85  1687    62    90   660   599   231 …    90–190
```

The slow dumps fall during the load (1.7 s while the renderer built its tree); once loaded,
84–231 ms. No failure in this run; in M1's, 2 of 20 dumps failed at the 2.5 s bound, both while
Chrome was reloading.

## M2: a load read as events (`since`, `waitMs` 20000, `until: settle`, `quietMs` 1000)

| Load | Entries | Raw | By type (entries/raw) | Most in 100 ms | Span | `more` / gaps |
| --- | --- | --- | --- | --- | --- | --- |
| Settings cold start (force-stopped, from Home) | 18 | 19 | windowContent 14/15, windowState 3/3, scroll 1/1 | 4 | 4.6 s | 0 / none |
| Chrome page load (web content exposed) | 19 | 26 | windowContent 10/17, selected 5/5, focus 3/3, windowState 1/1 | 4 | 3.1 s | 0 / none |

The framework's throttle (2w's spike: at most about 10 content changes a second) keeps a load
small; one 200-entry page holds it many times over.

## M3: what dumps cause on a static screen (10 dumps 300 ms apart)

| Screen | Events the dumps caused | Distinct documents |
| --- | --- | --- |
| Settings | none | 1 of 10 |
| Chrome, web content exposed | 1 windowContent (3 raw) | 3: the first three differ, then 7 identical |

Dumps cause no stream of events, and documents stop changing within about a second.

## M4: an agent's `uiTree` while another client dumps back to back (20 each)

| Screen | `uiTree` alone p50 / p95 / max | while dumping p50 / p95 / max | dumps p50 / max | failures |
| --- | --- | --- | --- | --- |
| Chrome, web content exposed (688 nodes) | 76 / 120 / 1,256 | 143 / 214 / 240 | 129 / 283 | 0 |
| formapp `spinner` (each dump's idle wait runs out) | 134 / 205 / 214 | 218 / 336 / 379 | 221 / 299 | 0 |

## Gates

| Gate | Result | Value |
| --- | --- | --- |
| S1 busy pause = max(5 s, M1's slowest + 1 s) | slowest 2,372 ms | **5 s**; Chrome during a reload can pass 2.5 s (2 of 20), so `missed: bound` is a real case; R1 narrowed |
| S2 dumps cause events that keep documents changing over 5 s | no (M3) | no exclusion rule |
| S3 a load puts over 200 entries in 100 ms | no: 4 at most (M2) | tick unchanged |
| S4 `uiTree`'s median grows by 200 ms or more while dumping | no: +67 ms (Chrome), +84 ms (`spinner`) | gap stays **100 ms** |

R2: the 250 ms fold window and the 512-entry ring hold a Settings cold start and a Chrome page
load with no gap and no `more`; measured on two loads of one emulator.

## Not observed

API 36.1, physical devices, other OEMs; heavier native apps (a long RecyclerView feed); an
app's WebView; Chrome with its web content exposed on API 31 beyond one dump series.
