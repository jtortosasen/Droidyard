package git

// The Git writes' commands (plan m2n part 2, step P1; ADR-0071 §3, §5). Builders only:
// the capabilities that run them, and the runner that starts a write git (its process
// group, its signals, its timeouts), come in later steps. What each flag below is for,
// and what git does with each, is recorded fact by fact in write_spike_test.go.
//
// A write is never run on an untrusted root (ADR-0071 §2), so none of the read
// invocation's neutralising of the repository's configuration is repeated here beyond
// the fixed prefix: the repository's transport, credential, update and filter
// configuration is the user's own git. What stays off on every git the daemon starts:
// hooks (core.hooksPath), fsmonitor, a prompt, and, since a write leaves objects
// behind, a detached gc or maintenance that would outlive the call.

import (
	"slices"
	"strings"
)

// writeConfig is what every write is given before its subcommand, in the order of git's
// own global options: the read builders' two global switches (hist), then
//
//   - gc.auto=0 and maintenance.auto=false: a fetch and a checkout end with
//     `maintenance run --auto`, which can detach a `gc` that outlives the call and the
//     process group the daemon kills. Which of the two stops it, in which git, is
//     recorded in the spike.
var writeConfig = []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false"}

// writeCmd is the command line of a write: the global switches, the write config, then
// the subcommand and its arguments. A name never comes before --end-of-options and a path
// never before `--`, with the global --literal-pathspecs so a gitlink path holding `*`
// or `:(` is a path and not a pattern.
func writeCmd(args ...string) []string {
	return hist(slices.Concat(writeConfig, args)...)
}

// writeFixedPrefix is the fixed prefix run.go gives every git, except that a network
// command (fetch, a submodule update that clones) leaves out protocol.allow=never: git's
// own protocol policy then applies (ADR-0071 §3). Part 2's runner takes this in place of
// the literal list in run.go's exec; a test (TestWritePrefixIsTheReadPrefix) holds the
// two equal for a command that is not a network one.
func writeFixedPrefix(network bool) []string {
	p := []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}
	if !network {
		p = append(p, "-c", "protocol.allow=never")
	}
	return append(p, "-c", "advice.graftFileDeprecated=false")
}

// writeEnv is the environment a write git runs in: scrubEnv's, and for a network command
// without GIT_ALLOW_PROTOCOL=none; never a prompt, whoever would ask (ADR-0071 §3).
//
//   - GIT_TERMINAL_PROMPT=0 stops git's own terminal prompt, but git asks an askpass
//     program first and waits for it: GIT_ASKPASS, then core.askPass, then SSH_ASKPASS.
//     So SSH_ASKPASS is dropped from the inherited environment and GIT_ASKPASS is set
//     empty, which an empty value makes git skip every askpass, core.askPass included
//     (TestWriteSpike5AskpassNeverRuns).
//   - ssh's own prompt (a passphrase, a host key) goes to SSH_ASKPASS when ssh has no
//     terminal; SSH_ASKPASS_REQUIRE=never makes it fail instead. GIT_SSH_COMMAND and
//     BatchMode are not set: they would override the user's core.sshCommand.
func writeEnv(environ []string, network bool) []string {
	environ = slices.DeleteFunc(slices.Clone(environ), func(kv string) bool {
		return strings.HasPrefix(kv, "SSH_ASKPASS=") || strings.HasPrefix(kv, "SSH_ASKPASS_REQUIRE=")
	})
	env := append(scrubEnv(environ), "GIT_ASKPASS=", "SSH_ASKPASS_REQUIRE=never")
	if !network {
		return env
	}
	return slices.DeleteFunc(env, func(kv string) bool { return kv == "GIT_ALLOW_PROTOCOL=none" })
}

// fetchArgs fetches one remote, or every remote when remote is empty. No prune of
// remote-tracking refs or tags, no recursion into submodules (their own fetch is
// updateArgs's business). The remote is a configured remote's name (validName).
func fetchArgs(remote string) []string {
	args := writeCmd("fetch", "--no-recurse-submodules", "--no-prune", "--no-prune-tags")
	if remote == "" {
		return append(args, "--all")
	}
	return append(args, "--end-of-options", remote)
}

// switchArgs switches to an existing local branch. --no-guess: a name that is only a
// remote-tracking branch's is not made into a local branch by git's own guess; that is
// trackArgs, asked for explicitly. --no-recurse-submodules: a submodule is a root of its
// own (ADR-0071 §6).
func switchArgs(branch string) []string {
	return writeCmd("switch", "--no-recurse-submodules", "--no-guess", "--end-of-options", branch)
}

// trackArgs creates the local branch name at the remote-tracking branch start (for
// example origin/feature/x) with start as its upstream, and switches to it.
func trackArgs(name, start string) []string {
	return writeCmd("switch", "--no-recurse-submodules", "--track", "--create", name, "--end-of-options", start)
}

// branchArgs creates the branch name at the commit from (a full object id the caller
// validated), with no upstream, and switches to it when checkout is set.
func branchArgs(name, from string, checkout bool) []string {
	if checkout {
		return writeCmd("switch", "--no-recurse-submodules", "--no-track", "--create", name, "--end-of-options", from)
	}
	return writeCmd("branch", "--no-track", "--end-of-options", name, from)
}

// updateArgs brings the submodules to the commits the superproject records, initialising
// them first: all of them, or the given paths (after `--`, literal).
func updateArgs(paths []string) []string {
	args := writeCmd("submodule", "update", "--init", "--recursive")
	if len(paths) == 0 {
		return args
	}
	return append(append(args, "--"), paths...)
}

// refFormatArgs asks git whether name is a branch name it would accept as it is.
func refFormatArgs(name string) []string {
	return hist("check-ref-format", "--branch", name)
}

// maxNameBytes is the longest name validName allows.
const maxNameBytes = 255

// validName is the check in Go that comes first (ADR-0071 §5): a name that no later step
// has to defend against. No empty name, no leading '-' (it would be read as an option
// wherever it lands), no control character, no "@{" (git expands "@{-1}" to the previous
// branch's name), not HEAD or "@" (its synonym), no prefix refs/ heads/ remotes/ tags/ and
// no pseudo-ref's name (only capitals and '_', ending in HEAD: FETCH_HEAD, ORIG_HEAD,
// MERGE_HEAD...): such a name is ambiguous with another ref, and git resolves it to that
// ref or warns (TestWriteSpike9EndOfOptionsAndNames records what each builder does), at
// most 255 bytes. It does not say that git accepts the name; refFormatArgs asks that. A
// name that starts with an existing remote's name and a '/' is ambiguous with that
// remote's branches; that needs the repository's remotes and is the caller's check.
func validName(name string) bool {
	if name == "" || name == "HEAD" || name == "@" || len(name) > maxNameBytes || name[0] == '-' || strings.Contains(name, "@{") {
		return false
	}
	for _, p := range []string{"refs/", "heads/", "remotes/", "tags/"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	if strings.HasSuffix(name, "HEAD") && !strings.ContainsFunc(name, func(c rune) bool { return c != '_' && (c < 'A' || c > 'Z') }) {
		return false
	}
	return !strings.ContainsFunc(name, func(c rune) bool { return c < ' ' || c == 0x7f })
}
