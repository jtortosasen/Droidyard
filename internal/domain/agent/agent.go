// Package agent holds the capability through which the hook records what an agent ran
// outside droidyard (ADR-0063): agent.recordCommand. It stores nothing: the record is
// the operation's result, which Activity reads through operation.wait. The package also
// owns the facts about a record (the programs, the word pattern, the bounds, the client
// names), so that the hook's reader (internal/assistant) and the capability's
// validation can never disagree about what a record may hold.
package agent

import (
	"context"
	"regexp"
	"strconv"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// version is the API version that introduces agent.recordCommand (ADR-0063 §5).
const version = "0.22.0"

// The bounds of a record (ADR-0063 §1, §4).
const (
	// MaxCommands is the most commands one record holds.
	MaxCommands = 4
	// MaxWords is the most positional words one command holds.
	MaxWords = 3
)

// Programs are the base names of the Android commands a record may name (ADR-0063 §1).
// The enum tag on Command.Program repeats them; TestTagsMatchTheExportedFacts holds the
// two together.
var Programs = []string{"gradlew", "gradle", "adb", "emulator", "sdkmanager", "avdmanager"}

// Clients are the agent clients that may report a command.
var Clients = []string{"claude", "cursor"}

// WordPattern is the pattern of a recorded positional word: ADR-0063 §4's, with one
// optional leading ':' for a Gradle task path (ADR-0065).
const WordPattern = `^:?[A-Za-z0-9][A-Za-z0-9:._-]{0,63}$`

// ToolPattern is the shape of the MCP tool name a record may carry as the suggestion,
// bounded like every other field of a record (step 6 verifier).
const ToolPattern = `^[a-z]{1,16}_[A-Za-z]{1,32}$`

var (
	// Word matches a recordable positional word.
	Word = regexp.MustCompile(WordPattern)
	tool = regexp.MustCompile(ToolPattern)
)

// Command is one Android command found in an agent's shell command.
type Command struct {
	Program   string   `json:"program" jsonschema:"required,enum=gradlew,enum=gradle,enum=adb,enum=emulator,enum=sdkmanager,enum=avdmanager" jsonschema_description:"The program's base name."`
	Words     []string `json:"words" jsonschema:"required,maxItems=3,pattern=^:?[A-Za-z0-9][A-Za-z0-9:._-]{0\\,63}$" jsonschema_description:"At most three positional words (a Gradle task, an adb subcommand); never flags, assignments or known flag values."`
	Suggested string   `json:"suggested,omitempty" jsonschema:"pattern=^[a-z]{1\\,16}_[A-Za-z]{1\\,32}$" jsonschema_description:"The MCP tool that does the same, when there is one."`
}

// Record is agent.recordCommand's parameters and its result: the record as accepted.
type Record struct {
	Client   string    `json:"client" jsonschema:"required,enum=claude,enum=cursor" jsonschema_description:"The agent client that ran the commands."`
	Commands []Command `json:"commands" jsonschema:"required,minItems=1,maxItems=4" jsonschema_description:"The Android commands found in one shell command, in order; one to four."`
}

func invalid(format string, args ...any) *capability.Error {
	return capability.Errorf(capability.ErrInvalidParams, format, args...)
}

func oneOf(s string, set []string) bool {
	for _, v := range set {
		if s == v {
			return true
		}
	}
	return false
}

// validate checks a record against the bounds above and returns it normalised: every
// command has a (possibly empty) words list.
func validate(r Record) (Record, error) {
	if !oneOf(r.Client, Clients) {
		return Record{}, invalid("client %q is not one of claude, cursor", r.Client)
	}
	if n := len(r.Commands); n < 1 || n > MaxCommands {
		return Record{}, invalid("commands has %d entries; want 1 to %d", n, MaxCommands)
	}
	out := Record{Client: r.Client, Commands: make([]Command, len(r.Commands))}
	for i, c := range r.Commands {
		at := "commands[" + strconv.Itoa(i) + "]"
		if !oneOf(c.Program, Programs) {
			return Record{}, invalid("%s.program %q is not one of the observed programs", at, c.Program)
		}
		if len(c.Words) > MaxWords {
			return Record{}, invalid("%s.words has %d entries; want at most %d", at, len(c.Words), MaxWords)
		}
		for j, w := range c.Words {
			if !Word.MatchString(w) {
				return Record{}, invalid("%s.words[%d] does not match %s", at, j, WordPattern)
			}
		}
		if c.Suggested != "" && !tool.MatchString(c.Suggested) {
			return Record{}, invalid("%s.suggested does not match %s", at, ToolPattern)
		}
		out.Commands[i] = Command{Program: c.Program, Words: append([]string{}, c.Words...), Suggested: c.Suggested}
	}
	return out, nil
}

// Register adds agent.recordCommand to the registry.
func Register(reg *capability.Registry) {
	notDestructive := false
	reg.MustRegister(capability.Descriptor{
		Name:         "agent.recordCommand",
		Kind:         capability.Command,
		Scope:        capability.Workspace,
		Statefulness: capability.Stateless,
		Audience:     capability.AudienceHuman,
		Since:        version,
		Description:  "Records that an agent ran Android commands outside droidyard: the client, and per command its program, at most three plain positional words and the droidyard tool that does the same. It is the hook's channel, never an MCP tool; it stores nothing, the record is the operation's result, shown in Activity.",
		Params:       Record{},
		Result:       Record{},
		Errors:       []capability.ErrorCode{capability.ErrInvalidParams},
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "changes nothing in the project or on a device; its only effect is the record in its own result",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return validate(call.Params.(Record))
		},
	})
}
