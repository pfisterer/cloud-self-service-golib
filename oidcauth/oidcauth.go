// Package oidcauth verifies OIDC ID tokens without making the identity
// provider a condition for starting up.
//
// Both services used to build their verifier with oidc.NewProvider, which
// fetches the issuer's discovery document over the network, and they treated a
// failure there as fatal. On 2026-09-25 the university's Keycloak went down
// during a power cut; every pod that happened to restart in that window died on
// the spot and stayed in CrashLoopBackOff — the self-service was unreachable
// for hours after its own cluster was healthy again, because nothing in it
// could start while somebody else's login service was away.
//
// So the endpoint is configuration, not a discovery result: given JWKSURL the
// verifier is built from static values and touches the network for the first
// time when it has a token to check. The keys are then cached, and an outage
// while a pod is running costs only the tokens whose signing key is not yet
// known. Discovery remains available for the local and mock setups that have no
// JWKS address to hand, and as the answer for a provider whose endpoints are
// not the ones we configured.
//
// What the caller has to do is distinguish two failures that look alike from
// inside Verify: a token this service rejects (401), and a token it cannot
// judge right now because the provider is away (503, ErrKeysUnavailable). The
// difference matters at the far end: a 401 tells the browser to throw its
// session away and sign in again — which is exactly what cannot be done while
// the provider is down, so the user is sent into a loop that looks like our bug.
package oidcauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coreos/go-oidc"
	"github.com/pfisterer/cloud-self-service-golib/authn"
	"go.uber.org/zap"
)

// ErrKeysUnavailable is returned by Verify when the token could not be judged
// because the provider's keys could not be fetched. Callers answer 503 for it,
// not 401.
var ErrKeysUnavailable = errors.New("oidc: signing keys are currently unavailable")

// Config is what a deployment states about its provider.
type Config struct {
	// IssuerURL is verified against the token's iss claim. Required, with or
	// without discovery.
	IssuerURL string
	// ClientID is the audience a token must carry.
	ClientID string
	// JWKSURL is the provider's key set. Set it, and nothing is fetched at
	// startup; leave it empty to discover it from IssuerURL, which requires the
	// provider to be reachable at that moment.
	JWKSURL string
	// KeyFailureWindow is how long after a failed key fetch the verifier keeps
	// reporting itself as degraded. Zero means the default below. It exists
	// because a fetch failure is a moment, while "the provider is away" is a
	// state, and the status shown to a user has to be the latter.
	KeyFailureWindow time.Duration
}

const defaultKeyFailureWindow = 2 * time.Minute

// Verifier checks ID tokens and remembers whether the last attempt to reach the
// provider's key set worked.
type Verifier struct {
	cfg      Config
	verifier *oidc.IDTokenVerifier
	client   *http.Client
	health   *keyFetchHealth
	log      *zap.SugaredLogger
}

// New builds a verifier. It performs network I/O only when cfg.JWKSURL is
// empty, in which case it discovers the key set from the issuer and fails if
// the provider is unreachable.
func New(cfg Config, log *zap.SugaredLogger) (*Verifier, error) {
	if cfg.IssuerURL == "" {
		return nil, fmt.Errorf("oidcauth: issuer URL is required")
	}
	window := cfg.KeyFailureWindow
	if window <= 0 {
		window = defaultKeyFailureWindow
	}

	health := &keyFetchHealth{window: window}
	// Every key fetch goes through this client, which is the only place that
	// learns whether the provider answered. go-oidc reports "could not verify"
	// either way, so without it a dead provider and a forged token are the same
	// error string.
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &healthRecordingTransport{inner: http.DefaultTransport, health: health},
	}

	oidcConfig := &oidc.Config{ClientID: cfg.ClientID}

	if cfg.JWKSURL != "" {
		ctx := oidc.ClientContext(context.Background(), client)
		keys := oidc.NewRemoteKeySet(ctx, cfg.JWKSURL)
		return &Verifier{
			cfg:      cfg,
			verifier: oidc.NewVerifier(cfg.IssuerURL, keys, oidcConfig),
			client:   client,
			health:   health,
			log:      log,
		}, nil
	}

	log.Infow("no JWKS URL configured, discovering it from the issuer — this needs the provider to be up right now",
		"issuer", cfg.IssuerURL)
	ctx := oidc.ClientContext(context.Background(), client)
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidcauth: discover provider %q: %w", cfg.IssuerURL, err)
	}
	return &Verifier{
		cfg:      cfg,
		verifier: provider.Verifier(oidcConfig),
		client:   client,
		health:   health,
		log:      log,
	}, nil
}

// Verify checks signature, issuer, audience and expiry and returns the claims.
//
// A failure while the key set is unreachable is reported as ErrKeysUnavailable,
// wrapped around the original error so the log still says what happened.
func (v *Verifier) Verify(ctx context.Context, rawIDToken string) (*authn.Claims, error) {
	if v == nil || v.verifier == nil {
		return nil, fmt.Errorf("oidcauth: verifier is not configured")
	}

	idToken, err := v.verifier.Verify(oidc.ClientContext(ctx, v.client), rawIDToken)
	if err != nil {
		if v.KeysUnavailable() {
			return nil, fmt.Errorf("%w: %v", ErrKeysUnavailable, err)
		}
		return nil, err
	}

	if idToken.Expiry.Before(time.Now()) {
		return nil, fmt.Errorf("oidcauth: token expired at %s", idToken.Expiry.Format(time.RFC3339))
	}

	var claims authn.Claims
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidcauth: parse claims: %w", err)
	}
	return &claims, nil
}

// KeysUnavailable reports whether the last attempt to fetch the provider's keys
// failed and the failure is recent enough to still describe the present. False
// while nothing has been fetched yet: a provider nobody has asked anything of
// is not known to be broken.
func (v *Verifier) KeysUnavailable() bool {
	if v == nil {
		return false
	}
	return v.health.unavailable(time.Now())
}

// LastKeyFetchError is the error text of the last failed fetch, for a status
// endpoint. Empty while healthy.
func (v *Verifier) LastKeyFetchError() string {
	if v == nil || !v.KeysUnavailable() {
		return ""
	}
	return v.health.lastError()
}

// IssuerURL is what the deployment configured, for status output.
func (v *Verifier) IssuerURL() string {
	if v == nil {
		return ""
	}
	return v.cfg.IssuerURL
}

// keyFetchHealth records the outcome of key fetches. Times are unix nanoseconds
// in atomics rather than a mutex-guarded struct: this is read on every request
// that carries an OIDC token and written rarely.
type keyFetchHealth struct {
	window       time.Duration
	lastFailure  atomic.Int64
	lastSuccess  atomic.Int64
	failureText  atomic.Pointer[string]
	failureCount atomic.Int64
}

func (h *keyFetchHealth) recordSuccess(now time.Time) {
	h.lastSuccess.Store(now.UnixNano())
}

func (h *keyFetchHealth) recordFailure(now time.Time, reason string) {
	h.lastFailure.Store(now.UnixNano())
	h.failureCount.Add(1)
	h.failureText.Store(&reason)
}

func (h *keyFetchHealth) unavailable(now time.Time) bool {
	failure := h.lastFailure.Load()
	if failure == 0 || failure <= h.lastSuccess.Load() {
		return false
	}
	return now.Sub(time.Unix(0, failure)) <= h.window
}

func (h *keyFetchHealth) lastError() string {
	if text := h.failureText.Load(); text != nil {
		return *text
	}
	return ""
}

// healthRecordingTransport is what turns "go-oidc could not verify this token"
// into an answerable question: it sees the HTTP exchange the key fetch makes.
type healthRecordingTransport struct {
	inner  http.RoundTripper
	health *keyFetchHealth
}

func (t *healthRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.inner.RoundTrip(req)
	now := time.Now()
	switch {
	case err != nil:
		t.health.recordFailure(now, err.Error())
	case res.StatusCode >= 500:
		// A proxy in front of a stopped provider answers 502 with an HTML page;
		// that is the shape this whole package exists for.
		t.health.recordFailure(now, fmt.Sprintf("%s answered %s", req.URL.Host, res.Status))
	default:
		t.health.recordSuccess(now)
	}
	return res, err
}
