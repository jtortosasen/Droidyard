# ADR-0071: The UI's Git writes: fetch, checkout, new branch and submodule update, on a trusted root

- Status: Accepted
- Date: 2026-10-13
- Supersedes: ADR-0066 §Decision (in part: item 2's "fetching is off on every root, because the daemon never fetches" and its 10 s limit, and item 4's `GIT_FAILED` cause, for the write capabilities here only)
- Superseded in part by: ADR-0073 (§Decision)
- Superseded in part by: ADR-0074 (§Decision)
- Relates to: ADR-0015 §2, §5; ADR-0020; ADR-0053 §2; ADR-0058; ADR-0060 §2; ADR-0069; ADR-0070; plan m2n Q3
- Decided by the owner, 2026-10-12, for the scope (reading plus the safe writes: fetch, checkout of a branch, a new branch, submodule update; pull, push, merge, rebase, stash, rename and delete later), the audience (the UI only: "the AI already uses git on its own"), submodules as roots in one log, and synchronised branches as IntelliJ has them. The rest is Delegated (the main session under the owner's standing instruction to apply the recommended option), including the six questions plan m2n Q3 put to the owner, each at its draft.

## Context

ADR-0066 made every git the daemon runs read-only: no fetch, no network, 10 s, the repository's executable configuration neutralised on an untrusted root. The owner asked for a Git client in the UI in the shape of SourceGit's toolbar and IntelliJ's branch and submodule workflow, with the safe writes now and the rest later (plan m2n items 6–8). A write runs more of the repository's configuration than a read can: a fetch runs its transport and credential configuration (`core.sshCommand`, `remote.<n>.uploadpack`, a credential helper), a submodule update may run `submodule.<n>.update = !cmd`, and a checkout runs smudge filters. Neutralising all of that on an untrusted root is a list too long to keep, and an LFS checkout with empty smudge filters writes pointer files into the work tree.

## Decision

1. **Four commands and one query**, domain `git`, workspace-scoped, audience `human`, never MCP tools. Every write is long-running and cancellable, so none is bounded by ADR-0020's queue wait behind another write:
   - **`git.fetch`**: all remotes, or one; no prune, no submodule recursion.
   - **`git.checkout`**: a local branch; a remote-tracking branch creates its local tracking branch.
   - **`git.createBranch`**: `name`, `from` (a commit oid of the repository) and `checkout`; with `roots` (§6), `fromIn` gives one start oid per submodule root, `{<path>: <oid>}`, since an oid names a commit of one repository only, and a submodule root without an entry refuses the whole call.
   - **`git.updateSubmodules`**: `submodule update --init --recursive`, for all submodules or the given paths.
   - **`git.submodules`**, a query: each submodule's path, recorded oid, checked-out oid and state `uninitialised`, `clean`, `outdated` or `dirty`.
2. **A trusted root only** (ADR-0015 §2); on an untrusted one each write answers `WORKSPACE_UNTRUSTED` with a nextStep to trust the project. On a trusted root the repository's transport, credential, update and filter configuration is the user's own git, as `build.*` is the user's own build (ADR-0015 §5). The UI shows the write buttons on an untrusted root and lets the refusal say why.
3. **Invocation**: ADR-0066 §2's prefix and environment, except that network commands use git's own protocol policy (no `GIT_ALLOW_PROTOCOL=none`, no `protocol.allow=never`). Never a prompt: `GIT_TERMINAL_PROMPT=0` and no terminal, so a credential that needs one fails with `GIT_FAILED` and a nextStep to run the command once in a terminal. 5 minutes for a network command, 60 s for the rest; the whole process group killed on cancel; `--no-recurse-submodules` on checkout. **Hooks stay off** (`core.hooksPath=/dev/null`), as on every git the daemon runs.
4. **One write at a time per repository**, queued in order (`overview.md` §Concurrency); reads never wait for a write. A lock held by another git (the agent's own) is `GIT_FAILED` with git's line.
5. **Names**: checked in Go (no leading `-`, no control character) and by `git check-ref-format --branch`, and passed after `--end-of-options`. A checkout git refuses over local changes is `GIT_FAILED` with git's cause: no force, no automatic stash. A write's `GIT_FAILED` cause is git's stderr from its first `fatal:` or `error:` line, at most 20 lines and 4 KiB, so a refused checkout names the files in the way; the reads keep ADR-0066 §4's first line. **The UI refuses a checkout while an editor buffer is dirty**, naming the files, before calling the daemon. No new error code.
6. **Synchronised branches**: `git.checkout` and `git.createBranch` take `roots`, a list of paths (`.` and submodule paths), applied in order, the repository first and then submodules depth-first, stopping at the first failure; the result is `completed` with one outcome per root (ADR-0053 §2's pattern), and nothing applied is rolled back. The UI's synchronise toggle is on by default when every root has the branch.
7. **Submodules as roots**: the reads (`git.log`, `git.refs`, `git.show`, `git.read`) take an optional `root`, a submodule path inside the workspace, contained lexically and checked against the parent's gitlinks; git runs there under the workspace's trust. The browser merges each root's pages by commit time into one log, with a colour stripe per root and a root filter.

## Consequences

- The fetch is the first daemon git that touches the network, and only on a trusted root.
- A credential that needs a prompt never works from the UI; the nextStep sends the user to a terminal once.
- A synchronised checkout can leave the repository on the new branch and a submodule on the old one when the submodule refuses; the per-root result says which.
- The current rules live in `internal/domain/git` and its package comment; `docs/interfaces/mcp.md`'s not-exposed row and `mvp.md`'s git row gain the new names, in the plan steps that build them.

## Alternatives considered

- **Writes on an untrusted root with the configuration neutralised**: the transport, credential, update and filter commands form a list that git grows; and an LFS checkout with empty smudge filters writes pointer files into the work tree.
- **Shelling out to the user's terminal**: no process ownership, no cancel, no result to show.
- **Rolling back a synchronised checkout**: another checkout, which can fail as the first one did.
- **Running the repository's hooks on a trusted root, as the git CLI does**: a `post-checkout` hook is the repository's command run by the daemon unasked; the owner's agents and terminal still run them.
- **Leaving dirty buffers to the editor's revision check (ADR-0058)**: the save is refused only after the checkout has rewritten the file under the buffer; refusing first loses nothing.
- **Hiding the write buttons on an untrusted root**: the user would not learn why they are missing; a refusal with "trust this project" does.
- **`roots: [{path, from}]` with no top-level `from`**: one shape, but `roots` would then differ between `git.checkout` and `git.createBranch`; and a submodule branching from its own HEAD is not what "New branch from <branch>" means when every root has that branch.
- **A write's `GIT_FAILED` cause as git's first line only, as for the reads**: for a refused checkout that line does not name the files in the way.
