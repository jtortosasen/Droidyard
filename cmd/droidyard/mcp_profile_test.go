package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	mcpadapter "github.com/jtortosasen/droidyard/adapters/mcp"
)

// goldenPath is plan m1o D3: the golden path's tools and the reopen ADR-0020 allows.
// It is written out here, apart from mcpadapter.CoreTools, so a change to the profile
// is a change to two places.
var goldenPath = []string{
	"workspace_open", "workspace_describe", "project_modules", "build_assemble", "build_errors",
	"build_lastResult", "file_list", "file_read", "file_write", "emulator_list", "emulator_start",
	"device_list", "device_install", "device_launch", "device_uiTree", "device_tapElement",
	"device_inputText", "device_logs", "device_screenshot", "operation_wait", "operation_cancel",
	"artifact_read",
}

// nextStepJoins are the tools a core tool's nextStep names that D3 did not list, so
// the agent is never sent to a tool its session does not list (plan m1o step 6).
// Each is reachable from a core tool: project_variants from build_assemble's variant
// check, build_run from build_errors and build_lastResult's BUILD_NOT_FOUND,
// emulator_stop from emulator_start's EMULATOR_BOOT_TIMEOUT.
var nextStepJoins = []string{"project_variants", "build_run", "emulator_stop"}

// toolNames lists the tools a shim offers, without the test daemon's fixture tools,
// and the compact bytes of the list with outputSchema dropped.
func toolNames(t *testing.T, c *shim, id int) (names []string, modelBytes int) {
	t.Helper()
	c.send(`{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"tools/list"}`)
	var list struct {
		Result struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(c.recv(), &list); err != nil {
		t.Fatal(err)
	}
	var shipped []map[string]json.RawMessage
	for _, tool := range list.Result.Tools {
		var name string
		json.Unmarshal(tool["name"], &name)
		if strings.HasPrefix(name, "fixture_") {
			continue
		}
		names = append(names, name)
		delete(tool, "outputSchema")
		shipped = append(shipped, tool)
	}
	raw, err := json.Marshal(shipped)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names, len(raw)
}

func sorted(in ...[]string) []string {
	var out []string
	for _, l := range in {
		out = append(out, l...)
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

// TestCoreProfileListsTheGoldenPath: `--tools core` lists D3's tools plus the nextStep
// joins and nothing else, not even the daemon's other tools; a call to a tool outside
// the profile is answered exactly as a call to a name that does not exist (MCP's tool
// not found); the instructions say the list is cut.
func TestCoreProfileListsTheGoldenPath(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	c := startShim(t, p.state, "core", "--tools", "core")
	init := c.initialize("core")
	if want := sorted(goldenPath, nextStepJoins); !equalStrings(mcpadapterSorted(), want) {
		t.Errorf("mcpadapter.CoreTools = %v; want D3 plus the nextStep joins %v", mcpadapterSorted(), want)
	}

	names, size := toolNames(t, c, 1)
	if want := sorted(goldenPath, nextStepJoins); !equalStrings(names, want) {
		t.Errorf("the core profile lists %v; want %v", names, want)
	}
	t.Logf("core profile tools/list without outputSchema: %d bytes, %d tools", size, len(names))

	instr, _ := init["instructions"].(string)
	if instr != mcpadapter.Instructions+mcpadapter.CoreNote {
		t.Errorf("the core instructions are %q; want the instructions plus the core note", instr)
	}

	// shell.exec exists and is not core; so does a name nothing registers.
	c.callTool(2, "device_uninstall", `{}`)
	outside := c.recv()
	c.callTool(3, "no_suchTool", `{}`)
	missing := c.recv()
	var o, m struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(outside, &o)
	json.Unmarshal(missing, &m)
	if o.Error == nil || m.Error == nil {
		t.Fatalf("a call outside the profile must be a protocol error; got %s and %s", outside, missing)
	}
	if o.Error.Code != m.Error.Code || strings.ReplaceAll(o.Error.Message, "device_uninstall", "X") != strings.ReplaceAll(m.Error.Message, "no_suchTool", "X") {
		t.Errorf("outside the profile: %s; a missing name: %s; want the same answer", outside, missing)
	}
	c.exit()
}

func mcpadapterSorted() []string { return sorted(mcpadapter.CoreTools) }

// TestFileOperationsAreNotCore: the tree's operations (ADR-0053 §1) are tools under
// `--tools all`, the default, and under no other profile: the core profile stays the
// golden path (ADR-0036 Decision 4), so a session that asked for it pays nothing for them.
// Reversing the decision (a file tool in mcpadapter.CoreTools) fails the core half.
func TestFileOperationsAreNotCore(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	ops := []string{"file_create", "file_move", "file_copy"}
	for _, profile := range []struct {
		args []string
		want bool
	}{{[]string{"--tools", "core"}, false}, {[]string{"--tools", "all"}, true}, {nil, true}} {
		c := startShim(t, p.state, "ops", profile.args...)
		c.initialize("ops")
		names, _ := toolNames(t, c, 1)
		c.exit()
		for _, op := range ops {
			listed := false
			for _, n := range names {
				listed = listed || n == op
			}
			if listed != profile.want {
				t.Errorf("%v: %s listed = %v; want %v", profile.args, op, listed, profile.want)
			}
		}
	}
}

// TestDefaultListsEverything: without the flag, and with --tools all, every tool is
// listed (the surface ADR-0036 Decision 4 promises stays unchanged), and the
// instructions are the plain ones.
func TestDefaultListsEverything(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	var lists [][]string
	for _, args := range [][]string{nil, {"--tools", "all"}} {
		c := startShim(t, p.state, "all", args...)
		init := c.initialize("all")
		names, size := toolNames(t, c, 1)
		if len(names) != 37 {
			t.Errorf("%v lists %d tools; want 37", args, len(names))
		}
		if size > maxToolListBytes {
			t.Errorf("%v: %d bytes", args, size)
		}
		if instr, _ := init["instructions"].(string); instr != mcpadapter.Instructions {
			t.Errorf("%v: the instructions are not the plain ones: %q", args, instr)
		}
		lists = append(lists, names)
		c.exit()
	}
	if !equalStrings(lists[0], lists[1]) {
		t.Errorf("--tools all lists %v; the default lists %v", lists[1], lists[0])
	}
	have := map[string]bool{}
	for _, n := range lists[0] {
		have[n] = true
	}
	for _, n := range mcpadapter.CoreTools {
		if !have[n] {
			t.Errorf("the default surface lacks the core tool %s", n)
		}
	}
}

// TestUnknownProfileIsRefusedBeforeConnecting: a typo must not open a session. The
// state has no daemon, so a shim that tried to connect would exit 1 with a dial error.
func TestUnknownProfileIsRefusedBeforeConnecting(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--tools", "everything"}, {"--tools="}, {"--tools"}} {
		c := startShim(t, t.TempDir(), "bad", args...)
		if code := c.wait(); code != 2 {
			t.Errorf("%v: exit %d; want the usage error 2", args, code)
		}
		if !strings.Contains(c.stderr.String(), "--tools needs core or all") {
			t.Errorf("%v: stderr %q does not say what --tools takes", args, c.stderr)
		}
	}
}

// TestCoreNextStepsNameCoreTools: a core tool's error must not send the agent to a tool
// its session does not list. Every tool named in a nextStep text of the core domains
// (the strings handed to WithNextStep, and the next/fix values built beside them) is in
// the profile, unless it is on the list below with the reason it cannot be reached from
// a core tool. A tool found here that is neither joins the profile.
func TestCoreNextStepsNameCoreTools(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	c := startShim(t, p.state, "all", "--tools", "all")
	c.initialize("all")
	names, _ := toolNames(t, c, 1)
	c.exit()
	exposed := map[string]string{} // canonical name -> MCP name
	for _, n := range names {
		exposed[strings.Replace(n, "_", ".", 1)] = n
	}
	inCore := map[string]bool{}
	for _, n := range mcpadapter.CoreTools {
		inCore[n] = true
	}
	// Named by nextSteps that no core tool returns.
	unreachable := map[string]string{
		"device.uninstall":    "only device.uninstall's own DEVICE_UNINSTALL_FAILED",
		"emulator.wipe":       "only emulator.wipe's own boot timeout",
		"build.tasks":         "only build.run's task check (checkTasks)",
		"system.capabilities": "the unknown-capability error, which an MCP session cannot send: an unknown tool name is refused by MCP first",
	}

	var found = map[string][]string{}
	token := regexp.MustCompile(`\b[a-z]+\.[A-Za-z]+\b`)
	fset := token0()
	for _, dir := range []string{"build", "build/gradle", "device", "emulator", "file", "project"} {
		scanNextSteps(t, fset, filepath.Join("..", "..", "internal", "domain", dir), token, exposed, found)
	}
	for _, dir := range []string{"capability", "workspace", "artifact"} {
		scanNextSteps(t, fset, filepath.Join("..", "..", "internal", dir), token, exposed, found)
	}
	if len(found) < 10 {
		t.Fatalf("found only %d tools named by nextSteps (%v); the scan is not reading them", len(found), found)
	}
	for canon, where := range found {
		mcp := exposed[canon]
		if inCore[mcp] {
			continue
		}
		if _, ok := unreachable[canon]; ok {
			continue
		}
		t.Errorf("a nextStep names %s (%s), which the core profile does not list: add it to mcpadapter.CoreTools and nextStepJoins, or list it as unreachable with the reason", canon, strings.Join(where, ", "))
	}
	// The exceptions must still be exceptions: one that has since joined the profile,
	// or is no longer named, is stale.
	for canon := range unreachable {
		if inCore[exposed[canon]] {
			t.Errorf("%s is in the core profile; drop it from the unreachable list", canon)
		}
		if _, ok := found[canon]; !ok {
			t.Errorf("%s is no longer named by a nextStep; drop it from the unreachable list", canon)
		}
	}
}

func token0() *token.FileSet { return token.NewFileSet() }

// scanNextSteps records, per exposed tool, the files whose nextStep texts name it: the
// string literals inside a WithNextStep call, or assigned to a variable named next or
// fix.
func scanNextSteps(t *testing.T, fset *token.FileSet, dir string, re *regexp.Regexp, exposed map[string]string, found map[string][]string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files in %s: %v", dir, err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		note := func(n ast.Node) {
			ast.Inspect(n, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				for _, tok := range re.FindAllString(text, -1) {
					if _, ok := exposed[tok]; ok {
						found[tok] = appendOnce(found[tok], filepath.Base(path))
					}
				}
				return true
			})
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "WithNextStep" {
					for _, a := range n.Args {
						note(a)
					}
				}
			case *ast.AssignStmt:
				for i, l := range n.Lhs {
					if id, ok := l.(*ast.Ident); ok && (id.Name == "next" || id.Name == "fix") && i < len(n.Rhs) {
						note(n.Rhs[i])
					}
				}
			}
			return true
		})
	}
}

func appendOnce(l []string, s string) []string {
	for _, e := range l {
		if e == s {
			return l
		}
	}
	return append(l, s)
}
