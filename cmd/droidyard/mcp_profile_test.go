package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/jtortosasen/droidyard/internal/capability"
)

// goldenPath is plan m1o D3 without the file tools (ADR-0060): the golden path's tools, the
// reopen ADR-0020 allows and the worktree entry of ADR-0082 §5. It is written out here, apart
// from mcpadapter.CoreTools, so a change to the profile is a change to two places.
var goldenPath = []string{
	"workspace_open", "workspace_enter", "workspace_describe", "project_modules", "build_assemble", "build_errors",
	"build_lastResult", "emulator_list", "emulator_start",
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

// TestFileCapabilitiesAreNotTools: ADR-0060. No file.* is an MCP tool under any profile
// (no flag, all, core), and mcp-go answers a call to one "tool not found"; the core also
// refuses file.read from an MCP client (REQUEST_NOT_ALLOWED_HERE, with no operation), while
// the CLI and the UI's HTTP side still run it. Reversing the decision (a file.* with
// audience all) lists its tool and lets the MCP client in, so the first and third
// assertions fail.
func TestFileCapabilitiesAreNotTools(t *testing.T) {
	t.Parallel()
	p, _ := startHTTP(t, "127.0.0.1:0")
	for _, args := range [][]string{nil, {"--tools", "all"}, {"--tools", "core"}} {
		c := startShim(t, p.state, "files", args...)
		c.initialize("files")
		names, _ := toolNames(t, c, 1)
		if len(names) == 0 {
			t.Errorf("%v lists no tools; the check below would pass vacuously", args)
		}
		for _, n := range names {
			if strings.HasPrefix(n, "file_") {
				t.Errorf("%v lists %s; no file.* is an MCP tool (ADR-0060)", args, n)
			}
		}
		if len(args) == 0 {
			c.callTool(2, "file_read", `{"path":"a.kt"}`)
			var msg struct {
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(c.recv(), &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Error == nil || msg.Error.Code != -32602 || !strings.Contains(msg.Error.Message, "tool 'file_read' not found") {
				t.Errorf("tools/call file_read answered %+v; want -32602 tool 'file_read' not found", msg.Error)
			}
		}
		c.exit()
	}

	root, _ := projectWith(t, "settings.gradle")
	if err := os.WriteFile(filepath.Join(root, "a.kt"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	openEnv, code := callIn(t, p.state, root, "workspace.open")
	if code != 0 {
		t.Fatalf("workspace.open exited %d: %+v", code, openEnv.Error)
	}
	ws := workspaceOf(t, openEnv).ID

	id, err := p.ops.Start(context.Background(), capability.Request{Capability: "file.read", Workspace: ws,
		Params: json.RawMessage(`{"path":"a.kt"}`), Client: capability.ClientMCP})
	var e *capability.Error
	if !errors.As(err, &e) || e.Code != capability.ErrNotAllowedHere || id != "" {
		t.Errorf("file.read as client mcp: (%q, %v); want REQUEST_NOT_ALLOWED_HERE and no operation", id, err)
	}

	env, code := callIn(t, p.state, root, "file.read", "--params", `{"path":"a.kt"}`)
	if code != 0 || env.Error != nil || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Errorf("file.read from the CLI: exit %d, error %+v, operation %+v; want it completed", code, env.Error, env.Operation)
	}

	resp := callRequest(portOf(t, p.d.HTTPAddr()), p.token,
		`{"id":"r","capability":"file.read","workspace":"`+ws+`","params":{"path":"a.kt"}}`).send(t, p.d.HTTPAddr())
	henv := envelopeOf(t, resp)
	if resp.StatusCode != 200 || henv.Error != nil || henv.Operation == nil || henv.Operation.State != capability.StateCompleted {
		t.Errorf("POST /v1/call file.read: %d, error %+v, operation %+v; want it completed", resp.StatusCode, henv.Error, henv.Operation)
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
		if len(names) != 36 {
			t.Errorf("%v lists %d tools; want 36", args, len(names))
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
		"device.uninstall":       "only device.uninstall's own DEVICE_UNINSTALL_FAILED",
		"emulator.wipe":          "only emulator.wipe's own boot timeout",
		"build.tasks":            "only build.run's task check (checkTasks)",
		"device.recordUi":        "only device.recordUi's own DEVICE_UI_DUMP_FAILED retry (uirecord.go)",
		"device.stopUiRecording": "only device.recordUi's own DEVICE_UI_RECORDING_ACTIVE (uirecord.go)",
		"system.capabilities":    "the unknown-capability error, which an MCP session cannot send: an unknown tool name is refused by MCP first",
	}

	// Every published capability, exposed or not: a token naming one is a nextStep
	// reference. The core-profile check below reads only the core domains' packages
	// (shell_exec is not a core tool); the reachability check reads those plus
	// internal/policy, whose POLICY_SHELL_DISABLED reaches agents through shell_exec.
	all := map[string]string{}
	for name := range published(t) {
		all[name] = name
	}
	token := regexp.MustCompile(`\b[a-z]+\.[A-Za-z]+\b`)
	fset := token0()
	found := map[string][]string{}    // core packages
	foundAll := map[string][]string{} // core packages and internal/policy
	root := filepath.Join("..", "..", "internal")
	for _, dir := range []string{"domain/build", "domain/build/gradle", "domain/device", "domain/emulator", "domain/project", "capability", "workspace", "artifact"} {
		scanNextSteps(t, fset, filepath.Join(root, dir), token, all, found)
		scanNextSteps(t, fset, filepath.Join(root, dir), token, all, foundAll)
	}
	scanNextSteps(t, fset, filepath.Join(root, "policy"), token, all, foundAll)
	for canon := range found {
		if _, ok := exposed[canon]; !ok {
			delete(found, canon)
		}
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

	// A nextStep must not send an agent to a published capability that is no MCP tool
	// (audience human, or not stateless), unless it is on this list with the reason.
	notExposed := map[string]string{
		"policy.update": "named in POLICY_SHELL_DISABLED as the user's channel: the agent asks the user",
		"policy.get":    "only policy.update's own POLICY_REVISION_CONFLICT (update.go:296); an MCP session cannot call policy.update",
	}
	for canon, where := range foundAll {
		if _, ok := exposed[canon]; ok {
			continue
		}
		if _, ok := notExposed[canon]; ok {
			continue
		}
		t.Errorf("a nextStep names %s (%s), a published capability that is not an MCP tool: name another route, or list it in notExposed with the reason", canon, strings.Join(where, ", "))
	}
	for canon := range notExposed {
		if _, ok := exposed[canon]; ok {
			t.Errorf("%s is an MCP tool; drop it from the notExposed list", canon)
		}
		if _, ok := foundAll[canon]; !ok {
			t.Errorf("%s is no longer named by a nextStep; drop it from the notExposed list", canon)
		}
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
