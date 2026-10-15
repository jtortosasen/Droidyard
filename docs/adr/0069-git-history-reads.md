# ADR-0069: The history reads: `git.log`, `git.refs` and `git.show`, addressed by oid, with the history fence

- Status: Accepted
- Date: 2026-10-12
- Supersedes: ADR-0066 §Decision (in part: item 2's invocation, which gains `GIT_NO_REPLACE_OBJECTS=1`, `GIT_GRAFT_FILE=/dev/null` and `-c advice.graftFileDeprecated=false` for every git the daemon runs)
- Superseded in part by: ADR-0072 (§Decision)
- Relates to: ADR-0015 §1; ADR-0020 §Retention; ADR-0060 §2; ADR-0066 §1, §2, §4, §6; ADR-0070; plan m2n Q1
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated, except the audience, which the owner decided on 2026-10-12: the history is for the UI, "the AI already uses git on its own".

## Context

The owner asked for a commit history with a branch graph, refs and an expandable commit (plan m2n, items 1–11). ADR-0066 §1 left `git.read`'s `base` an enum of `HEAD` and promised that commits widen it "and break nothing"; `mvp.md` names `log` and `branches`. Nothing said how a page of history is addressed while refs move, which commands a history read runs, or what a merge commit's files are compared against.

A history read runs more of git than ADR-0066's status and `cat-file` do: `log` can verify signatures through `gpg.program`, rewrite authors through a mailmap and show notes; a diff can run `diff.external`, a `diff.<d>.command` or a textconv selected by `.gitattributes`; the pager and fsmonitor are configurable. On an untrusted root every one of those is the repository's command.

Step 1 of plan m2n tried each attack against real git, on git 2.51.2 (Homebrew) and 2.50.1 (Apple), on an untrusted root with a dirty work tree, through the runner and through each command a history read runs (`log`, `for-each-ref`, `rev-parse`, `worktree list`, `diff-tree`, `cat-file --batch`), with a control that shows plain git running each attack (`internal/domain/git/history_harden_test.go`):
- a signature program (`gpg.program`, `gpg.x509.program`, `gpg.ssh.program`) under `log.showSignature` runs from `log` unless `--no-show-signature` is given;
- `log.mailmap` and `notes.displayRef` change only what is shown, and a format of raw `%an` and `%ae` without `%N` shows neither, so `--no-use-mailmap` and `--no-notes` are a belt that no test can see failing;
- `diff.external`, `diff.<d>.command`, textconv and filter drivers are not run by this plumbing at all, so `--no-ext-diff` and `--no-textconv` are a belt too; the pager is not run because the runner's stdout is a pipe, never a terminal;
- `core.fsmonitor` is asked by `diff-tree` with a pathspec, which reads the index, and the runner's `-c core.fsmonitor=false` (ADR-0066 §2) stops it;
- a replace ref (`refs/replace/<oid>`) makes `log`, `diff-tree` and `cat-file` show another commit's author, message, parents and files under the original oid, `git.read`'s `cat-file` included; `--no-replace-objects` or `GIT_NO_REPLACE_OBJECTS=1` stops it;
- `.git/info/grafts` gives a commit forged parents, and `--no-replace-objects` does not stop it on either git; `GIT_GRAFT_FILE=/dev/null` does;
- `i18n.logOutputEncoding = UTF-16` re-encodes `log`'s output and breaks the NUL-separated records, unless `--encoding=UTF-8` is given;
- `diff-tree -m --first-parent` on a merge still listed every parent's differences, so a merge's files are read with the explicit two-tree form.

On a generated repository of 100,000 commits, 40 merged branches and 142 refs, on an Apple M3: a page of 500 commits by `--date-order` took 65–108 ms with a commit-graph and 327–415 ms without one, the same at skip 0, 50,000 and 99,500 (date order walks the whole history either way); the tip set 72–153 ms; ahead and behind 41–420 ms, the worst against the oldest branch without a commit-graph. On a clone of the owner's project (29 commits, 3 refs, no commit-graph) the tip set and a page each took 16–34 ms.

## Decision

1. **Three queries**, domain `git`, workspace-scoped, audience `human`, never MCP tools, `GIT_NOT_FOUND` when git is missing:
   - **`git.log`**: one page of commits (oid, parents, author name and e-mail, committer time, subject); the refs on each commit come from `git.refs`, joined in the browser.
   - **`git.refs`**: HEAD, with ahead and behind for the current branch only; local branches, remote-tracking branches, tags and worktrees; and the tip set.
   - **`git.show`**: one commit's details (author, committer, both times, the full message, parents) and its files with added and removed line counts.
   - `git.read`'s `base` widens to a full commit oid, as ADR-0066 §1 promised.
   - Each result drops its bulk once delivered (ADR-0020 §Retention).
2. **Addressing by oid.** Every read takes full hex oids, validated before git starts; no ref name reaches argv in a read. The **tip set** is HEAD and every branch, remote-tracking branch and tag, peeled to commits (a tag on a tree or a blob is dropped), deduplicated, capped at 5,000 (HEAD and branches first, then remote-tracking branches, then tags, newest first); past the cap `tipsTruncated` says so. One function builds it for `git.log` and `git.refs`. A later page passes the first page's tips back as `from`, with `skip`, so pages stay one walk while refs move. Tips reach git on stdin (`git log --stdin`). A page holds at most 1,000 commits (500 by default). `git.log` walks the whole repository, even when the workspace is a subdirectory of it; `git.show` lists only the workspace's files.
3. **The history fence**: ADR-0066 §2's invocation, whose environment gains `GIT_NO_REPLACE_OBJECTS=1` and `GIT_GRAFT_FILE=/dev/null` for every git the daemon runs (so `git.read` and `git.watch` cannot be shown a replaced or grafted history either), with `-c advice.graftFileDeprecated=false`, since git otherwise prints a deprecation hint for any graft file it opens, `/dev/null` included, ahead of its own error; plus:
   - on every history command, the global `--literal-pathspecs --no-replace-objects` before the subcommand, so a replace ref cannot show another commit's author, message, parents or files under an oid;
   - on `log`, `--no-show-signature --no-notes --no-color --no-ext-diff --no-textconv --no-use-mailmap --encoding=UTF-8`, the raw author fields (`%an`, `%ae`), and no format that verifies a signature or shows notes;
   - a commit's files from plumbing `diff-tree` with `--no-ext-diff --no-textconv --no-color`, in the two-tree form `diff-tree <first parent> <commit>` (`--root` for a root commit);
   - `for-each-ref`, `rev-parse` and `worktree list` take only the global switches.
   ADR-0066 §2's `core.fsmonitor=false` and §3's filter overrides apply as before.
4. **Limits and codes**: at ADR-0066 §2's 4 MiB output cap `git.log` returns whole records with `more: true` and `git.show` sets `truncated`. ADR-0066 §4's codes; an unknown oid, or one that is not a commit, is `REQUEST_INVALID_PARAMS`. No new code.
5. **A merge commit is shown against its first parent**: what the merge brought into its branch (`git diff <merge>^1 <merge>`). A root commit is shown against the empty tree.
6. **Commit text, names, e-mails and ref names are project content** (ADR-0015 §1, ADR-0066 §6): no untrusted mark, since only the UI reads them. The UI renders them as text, each cell bidi-isolated.

## Consequences

- The guard is a list of what git executes on a history read, kept by `internal/domain/git/history_harden_test.go`, which runs each attack against real git; as with ADR-0066, a git that grows a new executable read path is not covered until a test names it.
- Refs moved by another git between two pages do not reshape the history being paged; the UI reloads when the tip set changes (ADR-0070 §3).
- More than 5,000 tips: the oldest tags drop out of the graph's starting points, and `tipsTruncated` says so. Past the runner's 4 MiB output cap (about 38,000 refs) `for-each-ref`'s list is cut in ref-name order, so what drops first is the alphabetical tail; `tipsTruncated` says so too.
- A crafted `objects/info/commit-graph` can misstate parents and dates. It runs nothing, and turning it off would cost the commit-graph's 3–5x speed-up, so it is a stated limit.
- Ahead and behind for branches other than the current one, a per-parent or combined merge diff, signatures, mailmap and notes are not read.
- `mvp.md`'s git row becomes `watch`, `read`, `log`, `refs`, `show`.

## Alternatives considered

- **Ref names as `from`**: argv injection to guard (a ref named `--output=…`), and a ref that moves between pages reshapes the walk.
- **Tips on argv**: `ARG_MAX` on a repository with tens of thousands of tags.
- **A daemon-held cursor per client**: state per connection, lost on reconnect, for no gain over a stateless `from` and `skip`.
- **`git log -- <prefix>` for a subdirectory workspace**: history simplification rewrites parents, so the graph is no longer the repository's.
- **A combined diff (`--cc`) for a merge**: hides what the branch brought, in a hunk format the browser cannot reuse. A parent selector is the later answer.
- **Ahead and behind for every branch**: its cost grows with the branch count.
- **Marking commit text untrusted** (ADR-0015 §1's mark): no agent receives it, so the mark protects nobody and costs every consumer.
- **libgit2 or go-git** (BeardGit reads through libgit2): a new dependency and a second implementation of filters and config; `mvp.md` says the git CLI.
