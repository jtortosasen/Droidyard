# ADR-0110: A full UI dump for parsers, as an artifact in `uiautomator dump`'s format

- Status: Accepted
- Date: 2026-10-20
- Relates to: ADR-0012 §Durability classes, §Reading artifacts; ADR-0015 §1, §4; ADR-0029; ADR-0034 §1; ADR-0036; ADR-0099 §2; `ui-automation.md` §The tree; plan m2w Q1–Q3, Q9; [`spike-ui-dump-events-2026-10.md`](../research/spike-ui-dump-events-2026-10.md)
- Decided by the owner, 2026-10-20: a mode exposing everything the accessibility tree holds, so an agent can write a complete parser. The shape is Delegated (plan m2w Q1–Q3, Q9, reviewed in two rounds; the spike's S3 took `windows` out, the plan's Changes).

## Context

The owner writes parsers that extract data from Android screens through accessibility nodes.
`device.uiTree` is built for an agent that acts: it folds layout-only nodes into their parent,
caps the snapshot at 40 KB by pruning the deepest levels, and drops attributes the reader already
reads (package, index, focusable, long-clickable, drawing order, hint). A parser written against
it misses structure and data.

The spike (step 1, API 31 and 36) settled what the reader itself reads: the connection includes
not-important views at connect, and the reader's plain dump holds the same nodes as `uiautomator
dump` in the same order (identical on API 36; on API 31 two bounds differ, the reader writing the
real display's). The loss is in the compaction, not the read. `AccessibilityNodeInfo` holds more
than either dump writes: state description, range and collection info, the action list, error,
heading, pane title, tooltip, input type, maximum length, URL spans. A full dump of the widgets
screen took 41–67 ms. `getWindows()` lists only interactive windows (the app under a modal dialog
is not listed) and, during a heads-up, a SystemUI window holds the notification's text.

## Decision

1. **A capability of its own**, `device.uiDump {device, invisible?}`: kind query, stateless,
   idempotent, not long-running, the same per-device read resource and error list as
   `device.uiTree`, under the same device policy (ADR-0015 §4). Not in the `core` tool profile.
2. **Delivered as an artifact**: an ephemeral `application/xml` artifact (ADR-0012), marked
   untrusted, one element per line so `artifact.read` pages it by whole lines. The result carries
   the reference, the node count and the foreground package, never the XML. `droidyard artifact
   cat <ref>` writes the bytes to stdout, with one fixed line on stderr saying they are device data,
   so the document reaches a parser without passing through the model. The daemon writes no host
   file.
3. **The format is a superset of `uiautomator dump`'s**: `<hierarchy rotation>` and `<node>` with
   every attribute `uiautomator dump` writes, under its names and in its order, on every node,
   layout-only ones included; then, only when not default, `editable`, `visible-to-user`,
   `state-description`, `error`, `heading`, `pane-title`, `tooltip`, `input-type`,
   `max-text-length`, `actions` (standard names, comma-separated), `custom-actions` (`id:label`,
   newline-separated, escaped), `collection-rows`, `-columns`, `-hierarchical`, `-selection-mode`,
   `collection-item-row`, `-column`, `-row-span`, `-column-span`, `-heading`, `-selected`,
   `range-type`, `-min`, `-max`, `-current`, `text-links` (`start-end=url`, the URL's spaces as
   `%20`), `dismissable`, `multi-line`, `content-invalid`, `context-clickable`, `important`. Each
   getter behind its API level. No getter that queries another node.
4. **The active window only.** `invisible: true` adds the children not visible to the user (laid
   out but off screen, such as a scroll view's content below the fold), marked
   `visible-to-user="false"`. There is no every-window option.
5. **Never cut, never `uiautomator dump`.** The reader's full dump is tried persistent, then
   one-shot; then the call fails with `DEVICE_UI_DUMP_FAILED`. A document over the 8 MiB read cap
   fails naming the cap and stores no artifact. A full dump that misses ADR-0034 §1's 2.5 s bound
   fails with a cause of its own ("the full dump did not complete within the reader's bound"),
   not "the foreground app did not answer".
6. **Not lendable** (ADR-0099 §2 unchanged): a remote device id is `DEVICE_NOT_FOUND`.

## Consequences

- One new capability and a CLI subcommand; API `0.32.0`. `uiTree`, its cap and its rendering are
  unchanged.
- A parser gets every node of the active window with its full data, but nothing of a window
  beside it: a dialog's dump does not hold the app under it, and IME, popup and SystemUI windows
  are not read. That part of the owner's request is a register row.
- Device text reaches the agent only when it reads the artifact, and is marked untrusted there.
- The full dump costs one traversal of the active window (41–67 ms on the spike's screen);
  extremely large trees are bounded by the 2.5 s bound and the 8 MiB cap, both failing cleanly.
- Documents: `ui-automation.md` §The tree; the register (the every-window row; R1 closed in part).

## Alternatives considered

- **A mode of `uiTree`** (`uiTree {full: true}`), or raising its cap. `uiTree` is the most-called
  tool, compact, ref-bearing and budgeted for tokens (ADR-0036); a mode gives it two result shapes
  and extra schema in every session, and an inline full tree spends the tokens the artifact saves.
- **Inline XML in the result.** A 30–90 KB document per call goes through the model's context
  when its consumer is a program.
- **Writing the file into the workspace.** A device tool would write project files after 2j moved
  file tools off MCP, and a device read would need a workspace scope.
- **JSON, or child elements for collection and range data, or a `<window>` wrapper.** Each breaks
  the parsers the owner already has, which walk `hierarchy/node` and read attributes.
- **Every window as a root node** (as androidx UiAutomator's window dump does). The spike showed it
  does not deliver what it promises: `getWindows()` omits the app under a modal dialog, and a
  SystemUI window exposes notification text, which would need a redaction rule of its own. It wins
  if a reader can read non-interactive windows and the SystemUI rule is decided.
- **Falling back to `uiautomator dump`.** It lacks the extras, so the document would silently
  change shape, and it suspends the device's accessibility services (ADR-0029).
