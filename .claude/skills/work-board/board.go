// Command board is the work-board skill's tool: what is in flight in other sessions,
// what is open, and the board page. Run it from any worktree of this repository:
//
//	go run .claude/skills/work-board/board.go status
//	go run .claude/skills/work-board/board.go claim <id> <touches> <what>
//	go run .claude/skills/work-board/board.go adr <id>
//	go run .claude/skills/work-board/board.go closing <id>
//	go run .claude/skills/work-board/board.go release <id>
//	go run .claude/skills/work-board/board.go page <out.html>
//
// The register and the plans index are read from main, so every session sees the
// same record. Lanes (what each parallel session works on) live in the repository's
// common git directory, shared by every worktree and never committed: they are
// coordination between sessions on this machine, not a documented fact.
package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed board.html
var pageTemplate string

const (
	registerPath = "docs/roadmap/known-limits.md"
	plansIndex   = "docs/plans/README.md"
	worktreeDir  = ".claude/worktrees"
)

// touchesKnown are the shared resources a lane declares. None is exclusive: they say
// where two lanes will meet, at run time (devices) or at the close (the others).
var touchesKnown = []string{"devices", "contract", "web", "docs", "git", "build", "release", "process"}

var (
	milestoneRE = regexp.MustCompile(`^\d+[a-z]+$`)
	slugRE      = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	adrFileRE   = regexp.MustCompile(`^docs/adr/(\d{4})-`)
)

type row struct {
	Item   string `json:"item"`
	Kind   string `json:"kind"`
	Area   string `json:"area"`
	Work   string `json:"work"`
	Rec    string `json:"rec"`
	Closed string `json:"-"`
}

type plan struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type lane struct {
	ID       string `json:"id"`
	Branch   string `json:"branch"`
	Worktree string `json:"worktree"`
	Touches  string `json:"touches"`
	ADR      string `json:"adr"`
	Since    string `json:"since"`
	State    string `json:"state"`
	What     string `json:"what"`
	// Filled by inspect, not stored.
	Ahead      int    `json:"ahead"`
	LastCommit string `json:"lastCommit"`
	PlanStatus string `json:"planStatus"`
	Problem    string `json:"problem,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		fail("usage: board status | claim <id> <touches> <what> | touches <id> <touches> | adr <id> | closing <id> | release <id> | page <out.html>")
	}
	var err error
	switch args := os.Args[2:]; os.Args[1] {
	case "status":
		err = status()
	case "claim":
		if len(args) != 3 {
			fail("usage: board claim <id> <touches,comma-separated> <what>")
		}
		err = claim(args[0], args[1], args[2])
	case "touches":
		if len(args) != 2 {
			fail("usage: board touches <id> <touches,comma-separated>")
		}
		err = setTouches(args[0], args[1])
	case "adr":
		err = needOne(args, reserveADR)
	case "closing":
		err = needOne(args, markClosing)
	case "release":
		err = needOne(args, release)
	case "page":
		err = needOne(args, page)
	default:
		fail("unknown command " + os.Args[1])
	}
	if err != nil {
		fail(err.Error())
	}
}

func needOne(args []string, f func(string) error) error {
	if len(args) != 1 {
		return errors.New("this command takes one argument")
	}
	return f(args[0])
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "board: "+msg)
	os.Exit(1)
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// mainTree is the repository's main working tree, the first one git lists.
func mainTree() (string, error) {
	out, err := git("worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(out, "\n")
	return strings.TrimPrefix(first, "worktree "), nil
}

// --- the record on main

// recordRef is where the register and the plans index are read: main, or BOARD_REF
// to preview a branch's record before it is merged.
func recordRef() string { return or(os.Getenv("BOARD_REF"), "main") }

func splitRow(l string) []string {
	l = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(l), "|"), "|")
	var cells []string
	var cur strings.Builder
	inCode := false
	for _, r := range l {
		switch {
		case r == '`':
			inCode = !inCode
			cur.WriteRune(r)
		case r == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

var linkRE = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)

func readRegister() ([]row, error) {
	text, err := git("show", recordRef()+":"+registerPath)
	if err != nil {
		return nil, err
	}
	var rows []row
	in := false
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "| Item | Kind | Area | Work |") {
			in = true
			continue
		}
		if !in || strings.HasPrefix(l, "| ---") {
			continue
		}
		if !strings.HasPrefix(l, "|") {
			break
		}
		c := splitRow(l)
		if len(c) != 7 {
			return nil, fmt.Errorf("%s: a row with %d cells: %.60s", registerPath, len(c), l)
		}
		rows = append(rows, row{Item: linkRE.ReplaceAllString(c[0], "$1"), Kind: c[1], Area: c[2], Work: c[3], Rec: c[5], Closed: c[6]})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s on %s: no register table with a Work column", registerPath, recordRef())
	}
	return rows, nil
}

func readPlans() ([]plan, error) {
	text, err := git("show", recordRef()+":"+plansIndex)
	if err != nil {
		return nil, err
	}
	var plans []plan
	for _, l := range strings.Split(text, "\n") {
		if !strings.HasPrefix(l, "| [") {
			continue
		}
		c := splitRow(l)
		if len(c) != 3 {
			continue
		}
		id, title, _ := strings.Cut(c[1], " — ")
		plans = append(plans, plan{ID: id, Title: title, Status: c[2]})
	}
	return plans, nil
}

// nextMilestone is the id after the highest one in the plans index and the lanes.
func nextMilestone(plans []plan, lanes []lane) string {
	best := ""
	consider := func(id string) {
		if milestoneRE.MatchString(id) && (len(id) > len(best) || (len(id) == len(best) && id > best)) {
			best = id
		}
	}
	for _, p := range plans {
		consider(p.ID)
	}
	for _, l := range lanes {
		consider(l.ID)
	}
	if best == "" {
		return "?"
	}
	b := []byte(best)
	if b[len(b)-1] < 'z' {
		b[len(b)-1]++
		return string(b)
	}
	return string(b) + "a"
}

// --- lanes

func lanesPath() (string, error) {
	dir, err := git("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "droidyard-lanes"), nil
}

const lanesHeader = "# id\tbranch\tworktree\ttouches\tadr\tsince\tstate\twhat"

func readLanes() ([]lane, error) {
	p, err := lanesPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lanes []lane
	for _, l := range strings.Split(string(raw), "\n") {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		f := strings.Split(l, "\t")
		if len(f) != 8 {
			return nil, fmt.Errorf("%s: a lane with %d fields: %q", p, len(f), l)
		}
		lanes = append(lanes, lane{ID: f[0], Branch: f[1], Worktree: f[2], Touches: f[3], ADR: f[4], Since: f[5], State: f[6], What: f[7]})
	}
	return lanes, nil
}

// withLanes runs f on the lanes under a lock and writes back what it returns.
func withLanes(f func([]lane) ([]lane, error)) error {
	p, err := lanesPath()
	if err != nil {
		return err
	}
	lock := p + ".lock"
	for i := 0; ; i++ {
		fd, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fd.Close()
			break
		}
		if i == 50 {
			return fmt.Errorf("%s is held; if no board command is running, remove it", lock)
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer os.Remove(lock)
	lanes, err := readLanes()
	if err != nil {
		return err
	}
	if lanes, err = f(lanes); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(lanesHeader + "\n")
	for _, l := range lanes {
		for _, s := range []string{l.ID, l.Branch, l.Worktree, l.Touches, l.ADR, l.Since, l.State, l.What} {
			if strings.ContainsAny(s, "\t\n") {
				return fmt.Errorf("a lane field holds a tab or newline: %q", s)
			}
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", l.ID, l.Branch, l.Worktree, l.Touches, l.ADR, l.Since, l.State, l.What)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func find(lanes []lane, id string) int {
	for i, l := range lanes {
		if l.ID == id {
			return i
		}
	}
	return -1
}

func has(list string, item string) bool {
	for _, s := range strings.Split(list, ",") {
		if s == item {
			return true
		}
	}
	return false
}

func claim(id, touches, what string) error {
	branch := ""
	switch {
	case milestoneRE.MatchString(id):
		branch = "m" + id
	case slugRE.MatchString(id):
		branch = id
	default:
		return fmt.Errorf("id %q: a milestone such as 2p, or a lowercase slug such as fix-init", id)
	}
	if err := checkTouches(touches); err != nil {
		return err
	}
	root, err := mainTree()
	if err != nil {
		return err
	}
	wt := filepath.Join(root, worktreeDir, branch)
	err = withLanes(func(lanes []lane) ([]lane, error) {
		if find(lanes, id) >= 0 {
			return nil, fmt.Errorf("%s is already claimed", id)
		}
		if plans, err := readPlans(); err == nil {
			for _, p := range plans {
				if p.ID == id {
					return nil, fmt.Errorf("%s is in the plans index already (%s)", id, p.Status)
				}
			}
		}
		if _, err := git("rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			return nil, fmt.Errorf("branch %s exists already; resume it, or remove it first", branch)
		}
		if _, err := git("worktree", "add", wt, "-b", branch, "main"); err != nil {
			return nil, err
		}
		return append(lanes, lane{ID: id, Branch: branch, Worktree: wt, Touches: touches, ADR: "-", Since: time.Now().Format("2006-01-02"), State: "working", What: what}), nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("claimed %s: branch %s, worktree %s\n", id, branch, wt)
	return nil
}

func reserveADR(id string) error {
	listing, err := git("ls-tree", "-r", "--name-only", "main", "docs/adr/")
	if err != nil {
		return err
	}
	high := 0
	for _, f := range strings.Split(listing, "\n") {
		if m := adrFileRE.FindStringSubmatch(f); m != nil {
			n, _ := strconv.Atoi(m[1])
			high = max(high, n)
		}
	}
	var got int
	err = withLanes(func(lanes []lane) ([]lane, error) {
		i := find(lanes, id)
		if i < 0 {
			return nil, fmt.Errorf("%s has no lane; claim it first", id)
		}
		for _, l := range lanes {
			for _, s := range strings.Split(l.ADR, ",") {
				if n, err := strconv.Atoi(s); err == nil {
					high = max(high, n)
				}
			}
		}
		got = high + 1
		if lanes[i].ADR == "-" {
			lanes[i].ADR = ""
		} else {
			lanes[i].ADR += ","
		}
		lanes[i].ADR += fmt.Sprintf("%04d", got)
		return lanes, nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("ADR-%04d reserved for %s\n", got, id)
	return nil
}

func checkTouches(touches string) error {
	for _, t := range strings.Split(touches, ",") {
		if !has(strings.Join(touchesKnown, ","), t) {
			return fmt.Errorf("touches %q: each one of %s", t, strings.Join(touchesKnown, ", "))
		}
	}
	return nil
}

// setTouches replaces a lane's touches, when its work turns out to reach further.
func setTouches(id, touches string) error {
	if err := checkTouches(touches); err != nil {
		return err
	}
	return withLanes(func(lanes []lane) ([]lane, error) {
		i := find(lanes, id)
		if i < 0 {
			return nil, fmt.Errorf("%s has no lane", id)
		}
		lanes[i].Touches = touches
		return lanes, nil
	})
}

func markClosing(id string) error {
	return withLanes(func(lanes []lane) ([]lane, error) {
		i := find(lanes, id)
		if i < 0 {
			return nil, fmt.Errorf("%s has no lane", id)
		}
		for _, l := range lanes {
			if l.ID != id && l.State == "closing" {
				return nil, fmt.Errorf("%s is closing; wait until it is released, then merge main into %s", l.ID, lanes[i].Branch)
			}
		}
		lanes[i].State = "closing"
		return lanes, nil
	})
}

// release ends a lane after its branch is merged: the worktree, the branch and the lane go.
func release(id string) error {
	return withLanes(func(lanes []lane) ([]lane, error) {
		i := find(lanes, id)
		if i < 0 {
			return nil, fmt.Errorf("%s has no lane", id)
		}
		l := lanes[i]
		if _, err := git("rev-parse", "--verify", "--quiet", "refs/heads/"+l.Branch); err == nil {
			if _, err := git("merge-base", "--is-ancestor", l.Branch, "main"); err != nil {
				return nil, fmt.Errorf("%s is not merged into main; merge it, or remove the worktree and branch by hand to abandon it", l.Branch)
			}
			if _, err := os.Stat(l.Worktree); err == nil {
				if _, err := git("worktree", "remove", l.Worktree); err != nil {
					return nil, err
				}
			}
			if _, err := git("branch", "-d", l.Branch); err != nil {
				return nil, err
			}
		}
		return append(lanes[:i], lanes[i+1:]...), nil
	})
}

// inspect fills what git says about each lane, and finds milestone branches and
// worktrees no lane names.
func inspect(lanes []lane) (orphans []string) {
	orphans = []string{}
	named := map[string]bool{}
	for i := range lanes {
		l := &lanes[i]
		named[l.Branch] = true
		if _, err := os.Stat(l.Worktree); err != nil {
			l.Problem = "its worktree is missing"
		}
		if n, err := git("rev-list", "--count", "main.."+l.Branch); err == nil {
			l.Ahead, _ = strconv.Atoi(n)
		} else {
			l.Problem = "its branch is missing"
			continue
		}
		l.LastCommit, _ = git("log", "-1", "--format=%cs %s", l.Branch)
		if files, err := git("ls-tree", "--name-only", l.Branch, "docs/plans/"); err == nil {
			for _, f := range strings.Split(files, "\n") {
				if strings.HasPrefix(filepath.Base(f), l.Branch+"-") {
					text, _ := git("show", l.Branch+":"+f)
					for _, line := range strings.Split(text, "\n") {
						if s, ok := strings.CutPrefix(line, "- Status: "); ok {
							l.PlanStatus = s
							break
						}
					}
				}
			}
		}
	}
	if out, err := git("worktree", "list", "--porcelain"); err == nil {
		for _, block := range strings.Split(out, "\n\n")[1:] {
			path, branch := "", ""
			for _, line := range strings.Split(block, "\n") {
				if p, ok := strings.CutPrefix(line, "worktree "); ok {
					path = p
				}
				if b, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
					branch = b
				}
			}
			if branch != "" && !named[branch] && !strings.Contains(path, "/agent-") {
				orphans = append(orphans, fmt.Sprintf("worktree %s on %s, with no lane", path, branch))
				named[branch] = true
			}
		}
	}
	if out, err := git("branch", "--format=%(refname:short)", "--list", "m[0-9]*"); err == nil && out != "" {
		for _, b := range strings.Split(out, "\n") {
			if !named[b] {
				orphans = append(orphans, fmt.Sprintf("branch %s, with no lane", b))
			}
		}
	}
	return orphans
}

// --- reports

func status() error {
	rows, err := readRegister()
	if err != nil {
		return err
	}
	plans, err := readPlans()
	if err != nil {
		return err
	}
	lanes, err := readLanes()
	if err != nil {
		return err
	}
	orphans := inspect(lanes)
	head, _ := git("log", "-1", "--format=%h %cs %s", "main")
	fmt.Printf("main: %s\n\n", head)

	fmt.Println("In flight:")
	if len(lanes) == 0 {
		fmt.Println("  nothing")
	}
	for _, l := range lanes {
		fmt.Printf("  %s  %s [%s] touches %s, ADR %s, since %s\n", l.ID, l.What, l.State, l.Touches, l.ADR, l.Since)
		fmt.Printf("      %s at %s: %d commits ahead of main; plan %s; last %s\n", l.Branch, l.Worktree, l.Ahead, or(l.PlanStatus, "not written"), or(l.LastCommit, "-"))
		if l.Problem != "" {
			fmt.Printf("      PROBLEM: %s\n", l.Problem)
		}
	}
	for _, o := range orphans {
		fmt.Printf("  UNTRACKED: %s\n", o)
	}

	fmt.Println("\nNot done in the plans index:")
	none := true
	for _, p := range plans {
		if p.Status != "Done" {
			fmt.Printf("  %s  %s (%s)\n", p.ID, p.Title, p.Status)
			none = false
		}
	}
	if none {
		fmt.Println("  nothing")
	}
	fmt.Printf("  next free milestone id: %s\n", nextMilestone(plans, lanes))

	byWork := map[string]map[string]int{}
	open, closed := 0, 0
	for _, r := range rows {
		if r.Closed != "" {
			closed++
			continue
		}
		open++
		w := or(r.Work, "(none)")
		if byWork[w] == nil {
			byWork[w] = map[string]int{}
		}
		byWork[w][r.Kind]++
	}
	keys := make([]string, 0, len(byWork))
	for k := range byWork {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("\nOpen register rows by Work (%d open, %d closed):\n", open, closed)
	for _, k := range keys {
		total, parts := 0, []string{}
		for _, kind := range []string{"limit", "unverified", "deferred", "open question"} {
			if n := byWork[k][kind]; n > 0 {
				total += n
				parts = append(parts, fmt.Sprintf("%d %s", n, kind))
			}
		}
		fmt.Printf("  %-13s %3d  (%s)\n", k, total, strings.Join(parts, ", "))
	}
	fmt.Println("\nA group's rows: grep the register for its Work cell, e.g. grep '| C3 |' " + registerPath)
	return nil
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func page(out string) error {
	rows, err := readRegister()
	if err != nil {
		return err
	}
	plans, err := readPlans()
	if err != nil {
		return err
	}
	lanes, err := readLanes()
	if err != nil {
		return err
	}
	if lanes == nil {
		lanes = []lane{} // the page iterates it; nil would marshal as null
	}
	orphans := inspect(lanes)
	open := []row{}
	closed := 0
	for _, r := range rows {
		if r.Closed == "" {
			open = append(open, r)
		} else {
			closed++
		}
	}
	head, _ := git("log", "-1", "--format=%h · %cs · %s", "main")
	data, err := json.Marshal(map[string]any{
		"generated": time.Now().Format("2006-01-02 15:04"),
		"main":      head,
		"rows":      open,
		"closed":    closed,
		"plans":     plans,
		"lanes":     lanes,
		"orphans":   orphans,
		"next":      nextMilestone(plans, lanes),
	})
	if err != nil {
		return err
	}
	// </script> cannot appear inside the JSON: json.Marshal escapes < as <.
	html := strings.Replace(pageTemplate, "/*DATA*/null", string(data), 1)
	if html == pageTemplate {
		return errors.New("board.html has no /*DATA*/null placeholder")
	}
	return os.WriteFile(out, []byte(html), 0o644)
}
