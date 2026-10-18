package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/skills"
)

// updateSkill rewrites the skill's generated tool map instead of comparing against it:
// go test ./cmd/droidyard -update.
var updateSkill = flag.Bool("update", false, "rewrite skills/android/droidyard-android/references/tools.md")

const (
	skillDir  = "../../skills/android/droidyard-android/"
	toolsFile = skillDir + "references/tools.md"
)

// capabilityName finds the dotted capability names a description mentions, which the
// map writes as the MCP tools they are.
var capabilityName = regexp.MustCompile(`\b[a-z]+\.[a-z][A-Za-z0-9]*\b`)

// oneLine is the first sentence of a tool's description, on one line, with the
// capability names it mentions spelled as MCP tool names, short enough for a table.
func oneLine(desc string, tools map[string]bool) string {
	d := strings.Join(strings.Fields(desc), " ")
	if i := strings.Index(d, ". "); i >= 0 {
		d = d[:i+1]
	}
	d = capabilityName.ReplaceAllStringFunc(d, func(m string) string {
		if as := strings.Replace(m, ".", "_", 1); tools[as] {
			return as
		}
		return m
	})
	const limit = 150
	if len(d) > limit {
		cut := strings.LastIndex(d[:limit], " ")
		d = d[:cut] + " ..."
	}
	return strings.ReplaceAll(d, "|", `\|`)
}

// toolMap is references/tools.md: every tool a session can list, with whether it lists
// in the core profile, whether it only reads, and a line from its description. It is
// generated so the skill cannot name, or miss, a tool the server does not have
// (ADR-0037 Decision 4).
func toolMap(tools []mcpTool) string {
	known := map[string]bool{}
	for _, t := range tools {
		known[t.Name] = true
	}
	var b strings.Builder
	b.WriteString("# Tools\n\n")
	b.WriteString("Generated from droidyard's registry by `go test ./cmd/droidyard -update`; do not edit.\n")
	b.WriteString("A session started with `--tools core` lists only the tools marked core.\n\n")
	b.WriteString("| Tool | Core | Effect | What it does |\n|---|---|---|---|\n")
	for _, t := range tools {
		core, effect := "", "changes state"
		if t.Core {
			core = "core"
		}
		if t.ReadOnly {
			effect = "read-only"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", t.Name, core, effect, oneLine(t.Description, known))
	}
	return b.String()
}

// TestSkillToolMapMatchesTheRegistry: references/tools.md is the registry's tools as
// the MCP adapter lists them. A tool added, renamed, removed or re-annotated fails here
// until the map is regenerated and the diff reviewed (go test ./cmd/droidyard -update).
func TestSkillToolMapMatchesTheRegistry(t *testing.T) {
	reg, _, _, _, _, _, _ := registry(isolatedEnv(t), nil, nil, nil)
	got := toolMap(mcpTools(reg))
	if *updateSkill {
		if err := os.WriteFile(toolsFile, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s", toolsFile)
		return
	}
	want, err := os.ReadFile(toolsFile)
	if err != nil {
		t.Fatalf("reading the tool map: %v (run: go test ./cmd/droidyard -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s is out of date with the registry. If the change is intended, run: go test ./cmd/droidyard -update\n\n--- generated ---\n%s", toolsFile, got)
	}
	// The binary embeds the same file the repository holds.
	embedded, err := fsReadFile(t, "references/tools.md")
	if err != nil || embedded != string(want) {
		t.Errorf("the embedded tool map differs from %s (%v)", toolsFile, err)
	}
}

func fsReadFile(t *testing.T, name string) (string, error) {
	t.Helper()
	b, err := fs.ReadFile(skills.DroidyardAndroid(), name)
	return string(b), err
}

// toolLike matches the words that look like an MCP tool name: lower-case, an
// underscore, then a camel-case verb.
var toolLike = regexp.MustCompile(`\b[a-z]+_[a-zA-Z]+\b`)

// notTools are tool-like words in the skill's prose that are not tools, each with what
// it is. TestSkillNamesOnlyToolsThatExist fails if one stops appearing, so the list
// cannot outlive the prose.
var notTools = map[string]string{
	"likes_alpha": "a view id in the selectors example (expect: appears: {id: ...})",
}

// unknownTools lists the tool-like words in the skill's prose that name no tool.
func unknownTools(prose string, known map[string]bool) []string {
	seen := map[string]bool{}
	var bad []string
	for _, w := range toolLike.FindAllString(prose, -1) {
		if _, not := notTools[w]; !known[w] && !not && !seen[w] {
			seen[w] = true
			bad = append(bad, w)
		}
	}
	sort.Strings(bad)
	return bad
}

func knownTools(t *testing.T) map[string]bool {
	t.Helper()
	reg, _, _, _, _, _, _ := registry(isolatedEnv(t), nil, nil, nil)
	known := map[string]bool{}
	for _, tool := range mcpTools(reg) {
		known[tool.Name] = true
	}
	return known
}

// TestSkillNamesOnlyToolsThatExist: every tool the skill's prose names is a tool a
// session lists. A tool renamed in the registry leaves the skill pointing at nothing,
// and an agent following it calls a tool that is not there.
func TestSkillNamesOnlyToolsThatExist(t *testing.T) {
	prose, err := os.ReadFile(skillDir + "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if bad := unknownTools(string(prose), knownTools(t)); len(bad) > 0 {
		t.Errorf("SKILL.md names tools that do not exist: %v", bad)
	}
	for w := range notTools {
		if !strings.Contains(string(prose), w) {
			t.Errorf("notTools lists %q, which the skill no longer contains; remove it", w)
		}
	}
	if !toolLike.MatchString(string(prose)) {
		t.Error("SKILL.md names no tool at all; the check above would pass on any prose")
	}
}

// TestSkillCheckFailsOnARenamedTool: the check is not vacuous. With build_assemble
// renamed in the registry, the skill that names it is reported.
func TestSkillCheckFailsOnARenamedTool(t *testing.T) {
	prose, err := os.ReadFile(skillDir + "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	known := knownTools(t)
	if !known["build_assemble"] {
		t.Fatal("build_assemble is not a tool; pick another one for this test")
	}
	delete(known, "build_assemble")
	known["build_assembleVariant"] = true
	bad := unknownTools(string(prose), known)
	if len(bad) != 1 || bad[0] != "build_assemble" {
		t.Errorf("a renamed tool reported %v; want [build_assemble]", bad)
	}
}

// TestToolMapMarksTheCoreProfileAndReadOnlyTools: the columns follow the adapter's
// profile and the descriptors' annotations, not a list kept here.
func TestToolMapMarksTheCoreProfileAndReadOnlyTools(t *testing.T) {
	reg, _, _, _, _, _, _ := registry(isolatedEnv(t), nil, nil, nil)
	m := toolMap(mcpTools(reg))
	for _, tc := range []struct{ tool, core, effect string }{
		{"device_uiTree", "core", "read-only"},
		{"build_assemble", "core", "changes state"},
		{"device_uninstall", "", "changes state"},
		{"build_tasks", "", "read-only"},
	} {
		want := fmt.Sprintf("| `%s` | %s | %s |", tc.tool, tc.core, tc.effect)
		if !strings.Contains(m, want) {
			t.Errorf("the tool map has no row starting %q", want)
		}
	}
	if strings.Contains(m, "fixture_") {
		t.Error("the tool map lists a test fixture")
	}
}
