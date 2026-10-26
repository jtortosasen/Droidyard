package sdk

import (
	"context"
	"net/http"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

// Authorize is the authorization hook the scheduler runs before sdk.requirements'
// handler: trust on the project root, then the Gradle root's resolution (internal/policy;
// ADR-0015 §2, ADR-0075 §3). It returns a gradle.Roots as Call.Authorized.
type Authorize func(ctx context.Context, call *capability.Call) (any, error)

// Client is what sdk.requirements needs of the sidecar client: the cached project model,
// which carries the probe's per-module Android facts and its status. *gradle.Client
// implements it.
type Client interface {
	Model(ctx context.Context, root string) (gradle.Model, bool, error)
}

// Options configure the sdk capabilities. The zero value registers every descriptor and
// answers sdk.status from an empty environment; only sdk.requirements' handler needs a
// Client, and only it carries the trust hook.
type Options struct {
	// Env is the daemon's environment ("KEY=value" entries, as os.Environ gives them):
	// Host and the project SDK's env branch read ANDROID_HOME and ANDROID_SDK_ROOT from
	// it, never from the process's own environment.
	Env []string
	// Canonical canonicalises a path for sameAsHost, resolving symlinks as a workspace
	// root is (platform.Env.Canonical). Nil resolves the filesystem's own.
	Canonical func(string) string
	// Client fetches the project model sdk.requirements compares the project's needs
	// against. Nil still registers the capability; no handler runs in a schema-only
	// registry.
	Client Client
	// Authorize is the trust re-check for sdk.requirements: it runs a Gradle
	// configuration pass, so only a trusted root is answered.
	Authorize Authorize

	// RepositoryBase replaces DefaultRepositoryBase, and HTTPClient and GOOS/GOARCH the
	// HTTP client and the host the repository is read for. For tests only (an httptest
	// TLS server): the repository's location is not configuration (ADR-0129 Q3).
	RepositoryBase string
	HTTPClient     *http.Client
	GOOS, GOARCH   string
}

// Register adds sdk.status, sdk.requirements and sdk.packages to the registry. sdk.status
// needs no external tool and answers found: false when there is no host SDK (D11);
// sdk.requirements runs a configuration pass and answers only for a trusted root (D12);
// sdk.packages reads Google's repository, one Repository shared by every capability that
// needs it.
func Register(reg *capability.Registry, o Options) {
	repo := NewRepository(RepositoryOptions{Base: o.RepositoryBase, Client: o.HTTPClient, GOOS: o.GOOS, GOARCH: o.GOARCH})
	registerStatus(reg, o.Env)
	registerRequirements(reg, o)
	registerPackages(reg, o.Env, repo)
}
