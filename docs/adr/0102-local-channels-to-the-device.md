# ADR-0102: Forwards to the device are sockets in `run/`, and the reader fences its reads

- Status: Accepted
- Date: 2026-10-19
- Supersedes: ADR-0030 §Decision (in part: "no socket authentication": reads are checked by the peer's uid); ADR-0035 §2 (in part: reads are fenced as acts are); ADR-0077 §1 (in part: the forward is `localfilesystem:`, not `tcp:0`)
- Relates to: ADR-0015, ADR-0019 §6, ADR-0021 §Layout, ADR-0091, ADR-0099 §4; plan m2t Q6 and step 8 (a); register rows "The reader's reads are unauthenticated" and "Between the screen server's `forward` and the connects"
- Decided by the owner, 2026-10-18: the reader-reads row joins 2t; ADR-0030's "no socket authentication" was the owner's. The mechanism is Delegated (plan m2t Q6, reviewed in two rounds; the spike of step 8 (a)), and the owner may revert it with step 8.

## Context

The reader (ADR-0030) and the screen server (ADR-0077) are reached through `adb forward
tcp:0 localabstract:…`: a loopback port any local account can connect to, first if it is
quick. ADR-0035 fenced the reader's acting verbs by the peer's uid; its reads stayed open,
as ADR-0030 judged no threat worth a check. Lending a phone (ADR-0099) puts someone's device
behind that reader on the host.

Spike (plan m2t step 8 (a), 2026-10-19, adb 1.0.41 / 36.0.0, macOS 26.6.2, the lane's API 31
AVD): `adb forward localfilesystem:<dir>/x localabstract:<reader>` succeeds; the socket file
is created mode 755 inside a 0700 directory; `ping` through it answers `ok <pid>` in 1 ms and
`dump` answers the hierarchy; `forward --list` shows it; `forward --remove` removes the
forward **but leaves the socket file**; a forward onto an existing file replaces it.

## Decision

1. **Forwards** to the reader and the screen server are `localfilesystem:` sockets in
   `<state>/run/fwd/` (0700), named `<kind>-<12 hex>`, within `sun_path` as ADR-0019 §6
   bounds the daemon's socket. The daemon unlinks a socket file after `forward --remove`
   (adb leaves it), and sweeps the directory's leftovers before its first forward of a run
   (ADR-0091), sparing a socket adb still lists, so a surviving reader's forward is adopted.
2. **The reader fences every request as it fences acts**: `dump`, `ping` and `quit` answer
   only a peer of uid 0 or 2000 (adbd, through which the daemon's forward arrives); any
   other gets `denied`. Checked on the lane's API 31 AVD: formapp's uid (10147) is
   answered `denied` for `ping` and `dump`, uid 2000 is served, and the previous reader
   served uid 10147.
3. **On macOS**, `droidyard ui` and `droidyard` hand the UI's URL to `osascript` on stdin,
   never on a command line; Linux keeps `xdg-open`.

## Consequences

- No other local account can connect to a forward or read the device's UI through it;
  an on-device app cannot read the reader's socket either.
- `reader.dex` is rebuilt and its digest re-pinned; `adb-device.md` §Transport to the device
  names the socket forwards (plan m2t step 9).
- adb prints a forward's local path raw in `forward --list`, spaces included (macOS's state
  root has one): a line is the serial, the local spec and the remote spec, split at the
  first and last field.
- Forwards a daemon before this one made over `tcp:` are no longer recognised; they stay
  in adb's table until adb restarts.
- The `open`/`xdg-open` row narrows to Linux.

## Alternatives considered

- **Keep `tcp:0`.** The race and the open reads stay, on a phone that may be someone
  else's.
- **A token on the socket.** ADR-0035 rejected it: visible to `ps` on the device.
- **An authentication handshake per connection.** scrcpy's protocol has none, and the
  reader's would duplicate what the peer uid already says.
