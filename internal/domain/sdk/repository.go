package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// DefaultRepositoryBase is where Google serves the SDK repository. A constant: the
// configuration cannot move it (ADR-0129 Q3). Options.RepositoryBase replaces it in tests
// only, with an httptest TLS server.
const DefaultRepositoryBase = "https://dl.google.com/android/repository/"

const (
	repositoryIndex = "repository2-3.xml"
	siteListIndex   = "addons_list-5.xml"

	// The bounds on a fetch (ADR-0129 Q3): each body while it is read, the number of lists
	// and of packages, and the time for the whole of it. The repository is kept in memory
	// for repositoryTTL.
	maxRepositoryBody = 16 << 20
	maxRepoLists      = 32
	maxRepoPackages   = 20000
	repositoryBudget  = 25 * time.Second
	repositoryTTL     = time.Hour
	fetchConcurrency  = 4
	maxRedirects      = 3
)

var (
	errBodyTooLarge  = errors.New("a repository file is larger than the bound")
	errRedirectAway  = errors.New("the repository redirected to another host or to http")
	listPathPattern  = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	errRepositoryOff = errors.New("the repository base is not an https URL")
)

// RepositoryOptions configure a Repository. The zero value reads Google's repository for
// the host the process runs on.
type RepositoryOptions struct {
	// Base replaces DefaultRepositoryBase; it must be https. Tests only.
	Base string
	// Client replaces the HTTP client (an httptest TLS server's, which trusts its own
	// certificate). Its redirects are limited all the same.
	Client *http.Client
	// GOOS and GOARCH replace the host's, to choose archives and the system-image ABI.
	GOOS, GOARCH string
	// Now replaces the clock the cache's age is read from.
	Now func() time.Time
}

// Model is the repository as Droidyard reads it: the stable channel's packages for this
// host, one entry per id, with the licences their lists declare. Immutable once built.
type Model struct {
	// Packages in the order the lists gave them.
	Packages []*RemotePackage
	// Licences by (id, sha1): one id can carry several texts across lists.
	Licences map[LicenceRef]Licence
	// Warnings are what the fetch ignored or dropped, without the offending text.
	Warnings []string

	byID map[string]*RemotePackage
}

// Package is the stable entry for id.
func (m *Model) Package(id string) (*RemotePackage, bool) {
	p, ok := m.byID[id]
	return p, ok
}

// Snapshot is what Load answers: the model, and how it was obtained.
type Snapshot struct {
	// Model is nil when State is "failed".
	Model *Model
	// State is "read" (fetched by this call), "cached" (the copy in memory, possibly with
	// the cause a refetch failed) or "failed" (nothing to show).
	State     string
	FetchedAt time.Time
	// Cause says why a fetch failed, when one did.
	Cause string
}

// Repository reads Google's SDK repository and keeps what it read in memory for an hour.
// One value serves every capability that needs the repository, so a listing, a consent
// and an install see the same text.
type Repository struct {
	base    *url.URL
	baseErr error
	client  *http.Client
	hostOS  string
	hostArc string
	abi     string
	now     func() time.Time

	// The bounds, lowered by tests.
	maxBody     int64
	maxLists    int
	maxPackages int
	budget      time.Duration
	ttl         time.Duration

	sem       chan struct{}
	model     *Model
	fetchedAt time.Time
}

// NewRepository builds a Repository. A base that is not a usable https URL does not
// fail here: every Load answers failed, with the reason.
func NewRepository(o RepositoryOptions) *Repository {
	r := &Repository{
		now:         o.Now,
		maxBody:     maxRepositoryBody,
		maxLists:    maxRepoLists,
		maxPackages: maxRepoPackages,
		budget:      repositoryBudget,
		ttl:         repositoryTTL,
		sem:         make(chan struct{}, 1),
	}
	if r.now == nil {
		r.now = time.Now
	}
	baseStr := o.Base
	if baseStr == "" {
		baseStr = DefaultRepositoryBase
	}
	u, err := url.Parse(baseStr)
	switch {
	case err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "":
		r.baseErr = errRepositoryOff
	default:
		u.Path = strings.TrimRight(u.Path, "/") + "/"
		u.RawPath = ""
		r.base = u
	}
	goos, goarch := o.GOOS, o.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	r.hostOS = map[string]string{"darwin": "macosx", "linux": "linux", "windows": "windows"}[goos]
	r.hostArc = map[string]string{"amd64": "x64", "arm64": "aarch64", "386": "x86"}[goarch]
	r.abi = map[string]string{"amd64": "x86_64", "arm64": "arm64-v8a"}[goarch]

	client := o.Client
	if client == nil {
		client = &http.Client{Transport: http.DefaultTransport}
	}
	c := *client
	// No redirect off the repository's host, and none to http (ADR-0129 Q3).
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return errors.New("too many redirects")
		}
		if r.base == nil || req.URL.Scheme != "https" || req.URL.Host != r.base.Host {
			return errRedirectAway
		}
		return nil
	}
	r.client = &c
	return r
}

// Load returns the repository: from memory while it is under an hour old, else fetched.
// refresh refetches. A failed fetch keeps the copy in memory and says why; with no copy
// the snapshot is "failed". It never returns an error: the caller decides what a failed
// repository means.
func (r *Repository) Load(ctx context.Context, refresh bool) Snapshot {
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return Snapshot{State: "failed", Cause: describe(ctx.Err())}
	}
	now := r.now()
	if r.model != nil && !refresh && now.Sub(r.fetchedAt) < r.ttl {
		return Snapshot{Model: r.model, State: "cached", FetchedAt: r.fetchedAt}
	}
	m, err := r.fetch(ctx)
	if err == nil {
		r.model, r.fetchedAt = m, now
		return Snapshot{Model: m, State: "read", FetchedAt: now}
	}
	if r.model != nil {
		return Snapshot{Model: r.model, State: "cached", FetchedAt: r.fetchedAt, Cause: describe(err)}
	}
	return Snapshot{State: "failed", Cause: describe(err)}
}

// get reads one repository file: HTTPS on the base's host, status 200, the body cut at the
// bound.
func (r *Repository) get(ctx context.Context, rel string) ([]byte, error) {
	u := *r.base
	u.Path = path.Join(r.base.Path, rel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, r.maxBody+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > r.maxBody {
		return nil, errBodyTooLarge
	}
	return body, nil
}

// fetch reads repository2-3.xml and the system-image lists addons_list-5.xml names, within
// the budget, and builds the model. The two index files are essential; one system-image
// list that cannot be read is skipped with a warning.
func (r *Repository) fetch(ctx context.Context) (*Model, error) {
	if r.baseErr != nil {
		return nil, r.baseErr
	}
	ctx, cancel := context.WithTimeout(ctx, r.budget)
	defer cancel()

	env := parseEnv{base: r.base, hostOS: r.hostOS, hostArch: r.hostArc, abi: r.abi, maxPkgs: r.maxPackages}

	data, err := r.get(ctx, repositoryIndex)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", repositoryIndex, err)
	}
	env.listPath = repositoryIndex
	main, err := parseList(data, env)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", repositoryIndex, err)
	}

	var warnings []string
	data, err = r.get(ctx, siteListIndex)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", siteListIndex, err)
	}
	urls, err := siteListURLs(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", siteListIndex, err)
	}
	paths, ignored := r.listPaths(urls)
	if ignored > 0 {
		warnings = append(warnings, fmt.Sprintf("ignored %d system-image list URLs that are not paths inside the repository", ignored))
	}
	if len(paths) > r.maxLists {
		warnings = append(warnings, fmt.Sprintf("the repository names %d system-image lists; only the first %d were read", len(paths), r.maxLists))
		paths = paths[:r.maxLists]
	}

	type listResult struct {
		list *parsedList
		err  error
	}
	results := make([]listResult, len(paths))
	var wg sync.WaitGroup
	gate := make(chan struct{}, fetchConcurrency)
	for i, p := range paths {
		wg.Add(1)
		gate <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-gate }()
			body, err := r.get(ctx, p)
			if err != nil {
				results[i].err = err
				return
			}
			e := env
			e.listPath = p
			results[i].list, results[i].err = parseList(body, e)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		// The budget ended (or the caller cancelled) before every list was read: a partial
		// repository is not presented as the repository.
		return nil, err
	}

	m := &Model{Licences: map[LicenceRef]Licence{}, byID: map[string]*RemotePackage{}}
	total := drops{}
	seen := main.seen
	merge := func(l *parsedList) {
		for k, n := range l.drops {
			total[k] += n
		}
		for _, lic := range l.licences {
			m.Licences[LicenceRef{ID: lic.ID, SHA1: lic.SHA1}] = lic
		}
		for _, p := range l.packages {
			if _, dup := m.byID[p.ID]; dup {
				total["id listed in two lists"]++
				continue
			}
			m.byID[p.ID] = p
			m.Packages = append(m.Packages, p)
		}
	}
	merge(main)
	failedLists := 0
	for _, res := range results {
		if res.err != nil {
			failedLists++
			continue
		}
		seen += res.list.seen
		merge(res.list)
	}
	if seen > r.maxPackages {
		return nil, fmt.Errorf("more than %d packages", r.maxPackages)
	}
	if failedLists > 0 {
		warnings = append(warnings, fmt.Sprintf("%d of %d system-image lists could not be read and are missing", failedLists, len(paths)))
	}
	if len(total) > 0 {
		var parts []string
		n := 0
		for k, c := range total {
			parts = append(parts, fmt.Sprintf("%s ×%d", k, c))
			n += c
		}
		sort.Strings(parts)
		warnings = append(warnings, fmt.Sprintf("dropped %d repository entries: %s", n, strings.Join(parts, ", ")))
	}
	m.Warnings = warnings
	return m, nil
}

// listPaths turns the site list's urls into clean relative paths inside the base; any
// other url (absolute, another host, http, a leading slash, a climb) is counted in
// ignored and never fetched. A repeated path is read once.
func (r *Repository) listPaths(urls []string) (paths []string, ignored int) {
	seen := map[string]bool{}
	for _, raw := range urls {
		rel, ok := relativeToBase(raw)
		if !ok || !listPathPattern.MatchString(rel) || path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") {
			ignored++
			continue
		}
		if !seen[rel] {
			seen[rel] = true
			paths = append(paths, rel)
		}
	}
	return paths, ignored
}

// describe is an error as a sentence a person can read: a fixed text for the known
// causes, else the error with control characters removed and cut short, since part of it
// can be text the repository sent.
func describe(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the repository did not answer in time"
	case errors.Is(err, context.Canceled):
		return "the call was cancelled"
	case errors.Is(err, errBodyTooLarge):
		return fmt.Sprintf("a repository file is larger than %d MiB", maxRepositoryBody>>20)
	case errors.Is(err, errRedirectAway):
		return "the repository redirected to another host, which is not followed"
	case errors.Is(err, errRepositoryOff):
		return "the repository base is not an https URL"
	case errors.Is(err, errDirective):
		return "a repository file has a DOCTYPE, which is not accepted"
	}
	var b strings.Builder
	for _, r := range err.Error() {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			r = ' '
		}
		b.WriteRune(r)
		if b.Len() >= 200 {
			b.WriteString("…")
			break
		}
	}
	return b.String()
}
