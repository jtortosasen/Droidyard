// Command stress is the prove-change skill's tool for flaky tests: it runs packages
// repeatedly under a chosen load and says which tests failed, at what rate, what they
// printed, and what the run cost. Run it from the module whose packages are measured:
//
//	go run .claude/skills/prove-change/stress.go [flags] <packages>
//
//	-count N     go test -count=N (default 1)
//	-cpu L       go test -cpu L, a comma list; each value is its own run, so a rate is
//	             reported per value (default: go test's own)
//	-copies K    K concurrent copies of the whole run (default 1)
//	-load B      B background loops of `go test -count=1 ./...` in the module root,
//	             their results discarded, for as long as the copies run (default 0)
//	-race        go test -race
//	-run RE      go test -run RE
//	-timeout D   go test -timeout D
//	-warm        build the packages once first, uncounted, so a run's CPU time carries
//	             no compile time
//	-all         list every test with its failures/runs, not only the failing ones
//
// Output: each failing test with failures/runs per -cpu value and its first five
// output lines; the 20 slowest tests (max and median elapsed); each package's elapsed;
// each copy's CPU time, the user plus system time of the waited `go test`, which
// includes the test binary and every process it waited for (a detached child that
// outlives its waiter is not counted). Exit 0 when nothing failed, 1 when a measured
// test, a package or a copy failed or a process of the run was left behind, 2 on a
// usage error, 130 when interrupted.
//
// Every `go test` the tool starts, copy or load, runs in its own process group
// (Setpgid), and the tool signals those groups by pid: at exit, on SIGINT and on SIGTERM
// it sends SIGTERM and, after 10 s, SIGKILL. It never signals anything by name. It sets
// GOTMPDIR to a directory of its own, so every test binary, and every process
// re-executed from one, runs from a path under it. After the cleanup the tool lists any
// process whose command line holds that path (a directory this run created, so nothing
// else can hold it): a process the run left behind. It stops those by pid, and the run
// exits 1 for them unless it was interrupted (a killed test binary cannot clean up).
//
// Interrupting `go run` with SIGINT does not reach this tool (the go command ignores the
// signal and waits); build it (`go build -o <path> stress.go`) to interrupt it by pid.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type config struct {
	count   int
	cpus    []string // "" is go test's default
	copies  int
	load    int
	race    bool
	run     string
	timeout string
	warm    bool
	all     bool
	pkgs    []string
}

func main() {
	os.Exit(realMain())
}

func realMain() int {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "stress:", err)
		return 2
	}
	root, err := moduleRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "stress:", err)
		return 2
	}
	tmp, err := os.MkdirTemp("", "stress-gotmp-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "stress:", err)
		return 2
	}
	g := &groups{set: map[int]bool{}, env: append(os.Environ(), "GOTMPDIR="+tmp)}

	var interrupted atomic.Bool
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		n := 0
		for {
			select {
			case <-sigs:
				n++
				interrupted.Store(true)
				if n == 1 {
					go g.reap(10 * time.Second)
				} else {
					g.signalAll(syscall.SIGKILL)
				}
			case <-done:
				return
			}
		}
	}()

	code := execute(cfg, root, tmp, g, &interrupted)

	// The cleanup is the same on every path: groups first, then the leak check.
	g.reap(10 * time.Second)
	leaked := leftBehind(tmp)
	if len(leaked) > 0 {
		fmt.Printf("\nLEFT BEHIND: %d process(es) outlived their test binary and hold %s in their command line; stopped by pid:\n", len(leaked), tmp)
		for _, l := range leaked {
			fmt.Println("  " + l.line)
		}
		stopLeaked(tmp, leaked)
		if !interrupted.Load() {
			code = 1 // a run that was not interrupted must leave nothing
		}
	}
	if len(leftBehind(tmp)) == 0 {
		os.RemoveAll(tmp)
	}
	close(done)
	if interrupted.Load() {
		fmt.Println("\nstress: interrupted")
		return 130
	}
	return code
}

func parseFlags(args []string) (*config, error) {
	fs := flag.NewFlagSet("stress", flag.ContinueOnError)
	cfg := &config{}
	var cpu string
	fs.IntVar(&cfg.count, "count", 1, "go test -count")
	fs.StringVar(&cpu, "cpu", "", "go test -cpu, a comma list; one run per value")
	fs.IntVar(&cfg.copies, "copies", 1, "concurrent copies of the run")
	fs.IntVar(&cfg.load, "load", 0, "background loops of `go test -count=1 ./...` in the module root")
	fs.BoolVar(&cfg.race, "race", false, "go test -race")
	fs.StringVar(&cfg.run, "run", "", "go test -run")
	fs.StringVar(&cfg.timeout, "timeout", "", "go test -timeout")
	fs.BoolVar(&cfg.warm, "warm", false, "build the packages once first, uncounted")
	fs.BoolVar(&cfg.all, "all", false, "list every test, not only the failing ones")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	cfg.pkgs = fs.Args()
	if len(cfg.pkgs) == 0 {
		return nil, fmt.Errorf("no packages given")
	}
	if cfg.count < 1 || cfg.copies < 1 || cfg.load < 0 {
		return nil, fmt.Errorf("-count and -copies must be at least 1 and -load at least 0")
	}
	if cpu == "" {
		cfg.cpus = []string{""}
	} else {
		for _, c := range strings.Split(cpu, ",") {
			if n, err := strconv.Atoi(c); err != nil || n < 1 {
				return nil, fmt.Errorf("-cpu: %q is not a positive integer", c)
			}
			cfg.cpus = append(cfg.cpus, c)
		}
	}
	return cfg, nil
}

// moduleRoot is the directory of the module the tool is run in.
func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		return "", fmt.Errorf("not inside a Go module")
	}
	return filepath.Dir(mod), nil
}

func execute(cfg *config, root, tmp string, g *groups, interrupted *atomic.Bool) int {
	if cfg.warm {
		args := []string{"test", "-count=1", "-run", "^$"}
		if cfg.race {
			args = append(args, "-race")
		}
		args = append(args, cfg.pkgs...)
		if out, err := g.output("", args); err != nil && !interrupted.Load() {
			fmt.Printf("stress: the warm-up build failed: %v\n%s", err, tail(notOK(out), 40))
			return 2
		}
	}

	// Background load: B loops, each restarting its `go test` until the copies end.
	stopLoad := make(chan struct{})
	var loads sync.WaitGroup
	for i := 0; i < cfg.load; i++ {
		loads.Add(1)
		go func() {
			defer loads.Done()
			for {
				select {
				case <-stopLoad:
					return
				default:
				}
				g.output(root, []string{"test", "-count=1", "./..."})
			}
		}()
	}

	agg := newAggregate()
	copies := make([]copyStat, cfg.copies)
	var wg sync.WaitGroup
	start := time.Now()
	for k := 0; k < cfg.copies; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			copies[k] = runCopy(cfg, g, agg, interrupted)
		}(k)
	}
	wg.Wait()
	close(stopLoad)
	g.reap(10 * time.Second) // ends the load loops' current `go test`
	loads.Wait()
	wall := time.Since(start)
	if interrupted.Load() {
		return 130
	}
	return agg.report(cfg, copies, wall)
}

type copyStat struct {
	cpu, user, sys, wall time.Duration
	invocations          int
	failed               []string // invocations that exited non-zero
}

// runCopy is one copy: for each -cpu value, one `go test -json` over the packages.
// Separate invocations, not one with -cpu L, because the JSON stream does not say which
// value a test ran under.
func runCopy(cfg *config, g *groups, agg *aggregate, interrupted *atomic.Bool) copyStat {
	var st copyStat
	begin := time.Now()
	for _, cpu := range cfg.cpus {
		if interrupted.Load() {
			break
		}
		args := []string{"test", "-json", "-count=" + strconv.Itoa(cfg.count)}
		if cfg.race {
			args = append(args, "-race")
		}
		if cpu != "" {
			args = append(args, "-cpu", cpu)
		}
		if cfg.run != "" {
			args = append(args, "-run", cfg.run)
		}
		if cfg.timeout != "" {
			args = append(args, "-timeout", cfg.timeout)
		}
		args = append(args, cfg.pkgs...)
		cmd := exec.Command("go", args...)
		cmd.Env = g.env
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.StdoutPipe()
		if err == nil {
			err = g.start(cmd)
		}
		if err != nil {
			st.failed = append(st.failed, fmt.Sprintf("cpu=%s: %v", label(cpu), err))
			continue
		}
		p := agg.parse(label(cpu), stdout)
		werr := cmd.Wait()
		g.done(cmd)
		st.invocations++
		if ps := cmd.ProcessState; ps != nil {
			st.user += ps.UserTime()
			st.sys += ps.SystemTime()
		}
		if werr != nil && !interrupted.Load() {
			// A failing test makes go test exit 1; that is already reported by test.
			// Anything else (a build failure, a signal, no output) is reported here.
			if p.events == 0 || p.failedPackages == 0 && p.failedTests == 0 {
				msg := fmt.Sprintf("cpu=%s: %v", label(cpu), werr)
				if s := strings.TrimSpace(tail(stderr.Bytes(), 10)); s != "" {
					msg += "\n" + indent(s, "      ")
				}
				if len(p.raw) > 0 {
					msg += "\n" + indent(strings.Join(lastN(p.raw, 10), "\n"), "      ")
				}
				st.failed = append(st.failed, msg)
			}
		}
	}
	st.cpu = st.user + st.sys
	st.wall = time.Since(begin)
	return st
}

func label(cpu string) string {
	if cpu == "" {
		return "default"
	}
	return cpu
}

// event is one line of `go test -json` (test2json, plus the go command's build events).
type event struct {
	Action     string
	Package    string
	ImportPath string
	Test       string
	Elapsed    float64
	Output     string
}

type testKey struct{ pkg, test string }

type testStat struct {
	runs, fails map[string]int // by -cpu label
	elapsed     []float64
	first       []string // the first five output lines of the first failure
}

type pkgFail struct {
	pkg, cpu string
	lines    []string
}

type aggregate struct {
	mu       sync.Mutex
	tests    map[testKey]*testStat
	pkgTimes map[string][]float64
	pkgFails []pkgFail
}

func newAggregate() *aggregate {
	return &aggregate{tests: map[testKey]*testStat{}, pkgTimes: map[string][]float64{}}
}

type parsed struct {
	events, failedTests, failedPackages int
	testFails                           map[string]int // failed tests by package
	raw                                 []string       // stdout lines that were not JSON
}

// parse reads one invocation's JSON stream into the aggregate.
func (a *aggregate) parse(cpu string, r io.Reader) *parsed {
	p := &parsed{testFails: map[string]int{}}
	out := map[testKey][]string{} // output of tests still running
	pkgOut := map[string][]string{}
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var ev event
			if json.Unmarshal(line, &ev) != nil || ev.Action == "" {
				p.raw = append(p.raw, strings.TrimRight(string(line), "\r\n"))
			} else {
				p.events++
				a.handle(cpu, ev, out, pkgOut, p)
			}
		}
		if err != nil {
			break
		}
	}
	return p
}

func (a *aggregate) handle(cpu string, ev event, out map[testKey][]string, pkgOut map[string][]string, p *parsed) {
	pkg := ev.Package
	if pkg == "" {
		pkg = ev.ImportPath
	}
	key := testKey{pkg, ev.Test}
	switch ev.Action {
	case "output", "build-output":
		if ev.Test == "" {
			pkgOut[pkg] = append(pkgOut[pkg], ev.Output)
		} else {
			out[key] = append(out[key], ev.Output)
		}
	case "pass", "fail", "build-fail":
		a.mu.Lock()
		defer a.mu.Unlock()
		if ev.Test == "" {
			// A package that failed because its tests did is already reported by them.
			if ev.Action != "pass" && p.testFails[pkg] == 0 {
				p.failedPackages++
				a.pkgFails = append(a.pkgFails, pkgFail{pkg, cpu, lastN(cleanLines(pkgOut[pkg]), 12)})
			}
			if ev.Action != "build-fail" {
				a.pkgTimes[pkg] = append(a.pkgTimes[pkg], ev.Elapsed)
			}
			return
		}
		st := a.tests[key]
		if st == nil {
			st = &testStat{runs: map[string]int{}, fails: map[string]int{}}
			a.tests[key] = st
		}
		st.runs[cpu]++
		st.elapsed = append(st.elapsed, ev.Elapsed)
		if ev.Action == "fail" {
			st.fails[cpu]++
			p.failedTests++
			p.testFails[pkg]++
			if st.first == nil {
				st.first = firstN(cleanLines(out[key]), 5)
			}
		}
		delete(out, key)
	}
}

// cleanLines splits output into lines and drops the framework's own markers, keeping
// what the test printed.
func cleanLines(chunks []string) []string {
	var lines []string
	for _, c := range chunks {
		for _, l := range strings.Split(strings.TrimRight(c, "\n"), "\n") {
			t := strings.TrimSpace(l)
			if t == "" || strings.HasPrefix(t, "=== ") || strings.HasPrefix(t, "--- FAIL") || strings.HasPrefix(t, "--- PASS") {
				continue
			}
			lines = append(lines, strings.TrimRight(l, "\r"))
		}
	}
	return lines
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func lastN(s []string, n int) []string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func maxOf(v []float64) float64 {
	m := 0.0
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}

func (a *aggregate) report(cfg *config, copies []copyStat, wall time.Duration) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	failed := false
	fmt.Printf("stress: %s count=%d cpu=%s copies=%d load=%d race=%v run=%q, wall %s\n",
		strings.Join(cfg.pkgs, " "), cfg.count, strings.Join(labels(cfg.cpus), ","), cfg.copies, cfg.load, cfg.race, cfg.run, wall.Round(100*time.Millisecond))

	keys := make([]testKey, 0, len(a.tests))
	for k := range a.tests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].pkg != keys[j].pkg {
			return keys[i].pkg < keys[j].pkg
		}
		return keys[i].test < keys[j].test
	})
	rates := func(st *testStat) string {
		var parts []string
		for _, c := range labels(cfg.cpus) {
			parts = append(parts, fmt.Sprintf("cpu=%s %d/%d", c, st.fails[c], st.runs[c]))
		}
		return strings.Join(parts, "  ")
	}
	// A parent whose subtest failed fails with it; the subtest is the finding.
	hasFailedChild := func(k testKey) bool {
		for _, o := range keys {
			if o.pkg == k.pkg && strings.HasPrefix(o.test, k.test+"/") && sum(a.tests[o].fails) > 0 {
				return true
			}
		}
		return false
	}

	var failing []testKey
	for _, k := range keys {
		if sum(a.tests[k].fails) > 0 && !hasFailedChild(k) {
			failing = append(failing, k)
		}
	}
	fmt.Printf("\nFAILING TESTS: %d\n", len(failing))
	for _, k := range failing {
		st := a.tests[k]
		fmt.Printf("  %s %s  %s\n", k.pkg, k.test, rates(st))
		for _, l := range st.first {
			fmt.Println("      " + l)
		}
		failed = true
	}
	if len(a.pkgFails) > 0 {
		fmt.Printf("\nFAILING PACKAGES (build failure, panic, timeout): %d\n", len(a.pkgFails))
		for _, f := range a.pkgFails {
			fmt.Printf("  %s  cpu=%s\n", f.pkg, f.cpu)
			for _, l := range f.lines {
				fmt.Println("      " + l)
			}
		}
		failed = true
	}
	if cfg.all {
		fmt.Printf("\nALL TESTS (failures/runs per -cpu value):\n")
		for _, k := range keys {
			fmt.Printf("  %s %s  %s\n", k.pkg, k.test, rates(a.tests[k]))
		}
	}

	byMax := append([]testKey(nil), keys...)
	sort.SliceStable(byMax, func(i, j int) bool {
		return maxOf(a.tests[byMax[i]].elapsed) > maxOf(a.tests[byMax[j]].elapsed)
	})
	fmt.Printf("\nSLOWEST TESTS (max and median elapsed over all runs; fails/runs):\n")
	for i, k := range byMax {
		if i == 20 {
			break
		}
		st := a.tests[k]
		runs := 0
		for _, n := range st.runs {
			runs += n
		}
		fmt.Printf("  %8.2fs %8.2fs  %d/%d  %s %s\n", maxOf(st.elapsed), median(st.elapsed), sum(st.fails), runs, k.pkg, k.test)
	}

	pkgs := make([]string, 0, len(a.pkgTimes))
	for p := range a.pkgTimes {
		pkgs = append(pkgs, p)
	}
	sort.Slice(pkgs, func(i, j int) bool { return median(a.pkgTimes[pkgs[i]]) > median(a.pkgTimes[pkgs[j]]) })
	fmt.Printf("\nPACKAGES (elapsed per invocation, max and median over %d):\n", len(copies)*len(cfg.cpus))
	for _, p := range pkgs {
		fmt.Printf("  %8.2fs %8.2fs  %s\n", maxOf(a.pkgTimes[p]), median(a.pkgTimes[p]), p)
	}

	fmt.Printf("\nCOPIES (CPU = user + system of the waited go test, test binary and processes it waited for included):\n")
	for i, c := range copies {
		fmt.Printf("  copy %d: cpu %.2fs (user %.2fs, system %.2fs), wall %.2fs, %d invocation(s)\n",
			i+1, c.cpu.Seconds(), c.user.Seconds(), c.sys.Seconds(), c.wall.Seconds(), c.invocations)
		for _, f := range c.failed {
			fmt.Println("    FAILED: " + f)
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

func labels(cpus []string) []string {
	out := make([]string, len(cpus))
	for i, c := range cpus {
		out[i] = label(c)
	}
	return out
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func tail(b []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	return strings.Join(lastN(lines, n), "\n") + "\n"
}

// notOK drops the lines of go test's output for packages that passed.
func notOK(b []byte) []byte {
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(l, "ok ") && !strings.HasPrefix(l, "? ") {
			keep = append(keep, l)
		}
	}
	return []byte(strings.Join(keep, "\n"))
}

func indent(s, pre string) string {
	return pre + strings.ReplaceAll(s, "\n", "\n"+pre)
}

// groups is the registry of process groups the tool started. Each is signalled by its
// pgid (the pid of its leader), so a signal never reaches anything the tool did not
// start. A group stays registered while it has members, which also keeps its number
// from being reused.
type groups struct {
	mu  sync.Mutex
	set map[int]bool
	env []string
}

func (g *groups) start(cmd *exec.Cmd) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := cmd.Start(); err != nil {
		return err
	}
	g.set[cmd.Process.Pid] = true
	return nil
}

// done is called after cmd was waited: the group is dropped if it is empty.
func (g *groups) done(cmd *exec.Cmd) {
	g.mu.Lock()
	defer g.mu.Unlock()
	pg := cmd.Process.Pid
	if syscall.Kill(-pg, 0) == syscall.ESRCH {
		delete(g.set, pg)
	}
}

// output runs `go args` in dir in its own group, discarding stdout and returning the
// combined output tail; it is the warm-up and the background load.
func (g *groups) output(dir string, args []string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = g.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var buf tailBuffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := g.start(cmd); err != nil {
		return nil, err
	}
	err := cmd.Wait()
	g.done(cmd)
	return buf.b, err
}

func (g *groups) signalAll(sig syscall.Signal) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for pg := range g.set {
		syscall.Kill(-pg, sig)
	}
}

// alive reports whether a registered group still has members, dropping the empty ones.
func (g *groups) alive() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for pg := range g.set {
		if syscall.Kill(-pg, 0) == syscall.ESRCH {
			delete(g.set, pg)
		} else {
			n++
		}
	}
	return n
}

// reap sends SIGTERM to every registered group and, if members remain after grace,
// SIGKILL.
func (g *groups) reap(grace time.Duration) {
	if g.alive() == 0 {
		return
	}
	g.signalAll(syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for g.alive() > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if g.alive() > 0 {
		g.signalAll(syscall.SIGKILL)
		time.Sleep(200 * time.Millisecond)
		g.alive()
	}
}

// tailBuffer keeps the last 64 KiB written to it.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 64<<10 {
		t.b = t.b[len(t.b)-64<<10:]
	}
	return len(p), nil
}

type straggler struct {
	pid  int
	line string
}

// leftBehind lists the processes whose command line holds dir, the directory this run
// created: nothing the tool did not start can hold it. It waits up to five seconds, since
// a process tied to a test binary (dieWithTheTest) needs a moment to notice it is gone.
func leftBehind(dir string) []straggler {
	var found []straggler
	for i := 0; i < 25; i++ {
		found = found[:0]
		out, err := exec.Command("ps", "-axo", "pid=,pgid=,command=").Output()
		if err != nil {
			return []straggler{{0, "ps failed: " + err.Error()}}
		}
		for _, l := range strings.Split(string(out), "\n") {
			l = strings.TrimSpace(l)
			if !strings.Contains(l, strings.TrimPrefix(dir, "/private")) || strings.Contains(l, "ps -axo") {
				continue
			}
			pid, _ := strconv.Atoi(strings.Fields(l)[0])
			if pid != os.Getpid() {
				found = append(found, straggler{pid, l})
			}
		}
		if len(found) == 0 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return found
}

// stopLeaked sends SIGTERM and then SIGKILL to the stragglers' pids, never to a group.
func stopLeaked(dir string, ss []straggler) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		for _, s := range ss {
			if s.pid > 0 {
				syscall.Kill(s.pid, sig)
			}
		}
		time.Sleep(time.Second)
		if len(leftBehind(dir)) == 0 {
			return
		}
	}
}
