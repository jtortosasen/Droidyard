# ADR-0073: A Git write never asks: no askpass program either, and the user's own credential helpers on a trusted root

- Status: Accepted
- Date: 2026-10-13
- Supersedes: ADR-0071 §Decision (in part: item 3's means of "never a prompt")
- Relates to: ADR-0015 §5; ADR-0066 §2; ADR-0071 §2, §3; plan m2n P1
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated.

## Context

ADR-0071 §3 decides that a write never prompts, by `GIT_TERMINAL_PROMPT=0` and no terminal. Plan m2n's P1 spike showed both hold for a terminal prompt: under `Setsid` a `core.sshCommand` cannot open `/dev/tty`, and an HTTP 401 fails in under a second with "terminal prompts disabled". Its verifier then found that git runs an askpass program before it checks `GIT_TERMINAL_PROMPT`: with `SSH_ASKPASS` in the daemon's environment or `core.askPass` in the repository's configuration, a fetch against the 401 server ran the program, waited for its answer and failed only after it (6.3–6.5 s with a script sleeping 3 s, on git 2.51.2 and 2.50.1); with `GIT_ASKPASS=` (empty) added, it failed in 85–112 ms and the program never ran. `scrubEnv` drops `GIT_ASKPASS` but not `SSH_ASKPASS`, which some Linux desktops set for every session. Credential helpers are a separate path: git runs the configured `credential.helper`, which may itself open a window (macOS Keychain's access dialog, Git Credential Manager's sign-in).

## Decision

1. **Every write's environment** also drops `SSH_ASKPASS` and sets `GIT_ASKPASS=` (empty, which overrides `core.askPass` and `SSH_ASKPASS` for git's own prompts) and `SSH_ASKPASS_REQUIRE=never` (for ssh's). `GIT_SSH_COMMAND` and ssh's `BatchMode` are not set, so the repository's or the user's `core.sshCommand` stays theirs; under `Setsid`, ssh has no terminal to prompt on once askpass is gone.
2. **Credential helpers stay the user's own** on a trusted root (ADR-0071 §2, ADR-0015 §5): a helper that answers from a store works; one that opens its own window is the user's choice, bounded by the write's 5-minute limit and its cancel.

## Consequences

- git's own credential prompt fails at once from the UI, with ADR-0071 §3's nextStep to run the command once in a terminal (observed on macOS, both gits); ssh's own askpass is turned off, unverified against a real ssh endpoint; git-lfs reads `core.askPass` itself, so the write prefix also carries `-c core.askPass=` (plan m2n P2), unverified without git-lfs installed.
- A Keychain or Git Credential Manager window may appear for a fetch, as it does for the user's own git; whether it does on the owner's machine is unverified until the owner's check (plan m2n P10).
- The current rule lives in `internal/domain/git/write.go`'s `writeEnv`.

## Alternatives considered

- **`GIT_TERMINAL_PROMPT=0` alone** (ADR-0071 §3 as written): an askpass program configured by the environment or the repository still waits.
- **`GIT_SSH_COMMAND="ssh -o BatchMode=yes"`**: overrides a repository's or user's `core.sshCommand` (a jump host, another key).
- **Emptying `credential.helper`**: breaks the user's stored credentials on their own trusted repository, the case ADR-0071 §2 keeps working.
