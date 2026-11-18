// Package oidcauth verifies OIDC ID tokens without making the identity
// provider a condition for starting up.
//
// Both services used to build their verifier with oidc.NewProvider, which
// fetches the issuer's discovery document over the network, and they treated a
// failure there as fatal. While the provider was unreachable, every pod that
// restarted died on the spot and kept crashing — nothing could start while
// somebody else's login service was away, even once its own cluster was
// healthy again.
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
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/pfisterer/cloud-self-service-golib/authn"
	"github.com/pfisterer/cloud-self-service-golib/retry"
	"go.uber.org/zap"
)

// ErrKeysUnavailable is returned by Verify when the token could not be judged
// because the provider's keys could not be fetched. Callers answer 503 for it,
// not 401.
var ErrKeysUnavailable = errors.New("oidc: signing keys are currently unavailable")

// ErrNoIdentity is returned by Verify for a token that verifies but names nobody the platform can key on: no e-mail address, one that is not verified, or one that is not plain ASCII (authn.Claims.CheckIdentity). The error wraps the reason. Callers answer 401, like any other rejected token.
var ErrNoIdentity = errors.New("oidcauth: the token names no verified e-mail address")

var (
	errNoIssuer   = errors.New("oidcauth: issuer URL is required")
	errNoClientID = errors.New("oidcauth: client ID is required")
)

// Config is what a deployment states about its provider.
type Config struct {
	// IssuerURL is verified against the token's iss claim. Required, with or
	// without discovery.
	IssuerURL string
	// ClientID is the audience a token must carry. Required: without it there is no audience to check, and a token issued to any other client of the realm would do.
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
	// MinRefetchInterval is the shortest time between two fetches of the key set. Zero means the default below. go-oidc fetches the key set again for every token whose key it does not know, so without a floor each forged token with an unknown kid — which anybody can send — is a request to the provider. Within the interval the last answer is used again: a token signed with a key that appeared since then is rejected until the interval has passed, which is the price of the floor.
	MinRefetchInterval time.Duration
}

const (
	defaultKeyFailureWindow   = 2 * time.Minute
	defaultMinRefetchInterval = 10 * time.Second
	// maxKeySetBytes caps what is read of a key set response. A realm's key set is a few kilobytes.
	maxKeySetBytes = 1 << 20
)

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
	if strings.TrimSpace(cfg.IssuerURL) == "" {
		return nil, errNoIssuer
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		return nil, errNoClientID
	}
	window := cfg.KeyFailureWindow
	if window <= 0 {
		window = defaultKeyFailureWindow
	}

	minRefetch := cfg.MinRefetchInterval
	if minRefetch <= 0 {
		minRefetch = defaultMinRefetchInterval
	}

	health := &keyFetchHealth{window: window}
	// Every key fetch goes through this client, which is the only place that
	// learns whether the provider answered. go-oidc reports "could not verify"
	// either way, so without it a dead provider and a forged token are the same
	// error string. The throttle sits outside the health recording, so an
	// answer it repeats is not counted as a second exchange with the provider.
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &throttledTransport{
			inner:    &healthRecordingTransport{inner: http.DefaultTransport, health: health},
			interval: minRefetch,
			now:      time.Now,
		},
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

// NewWithRetry is New, tried again by p while discovery fails: with an empty JWKSURL, New asks the provider at once, and right after a pod starts that can fail although nothing is wrong (the NetworkPolicies let a new pod through only a moment later). With JWKSURL set New makes no network call, so it is called once. A missing issuer or client ID is not tried again. p.OnRetry, when nil, logs each failed attempt at info level; the zero Policy tries for 60 s.
func NewWithRetry(ctx context.Context, cfg Config, log *zap.SugaredLogger, p retry.Policy) (*Verifier, error) {
	if p.OnRetry == nil {
		p.OnRetry = func(attempt int, err error, wait time.Duration) {
			log.Infow("OIDC provider not reachable yet, trying again", "attempt", attempt, "in", wait.String(), "error", err)
		}
	}
	var v *Verifier
	err := retry.Do(ctx, p, func(context.Context) error {
		var err error
		v, err = New(cfg, log)
		if errors.Is(err, errNoIssuer) || errors.Is(err, errNoClientID) {
			return retry.Permanent(err)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

// Verify checks signature, issuer, audience and expiry, and that the token names a person by a verified, plain ASCII e-mail address (ErrNoIdentity otherwise), and returns the claims.
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
	// A token that names nobody is refused here, at the one place tokens enter, so a service that reads claims.Email itself instead of Identity() gets only verified, plain addresses too.
	if _, err := claims.CheckIdentity(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoIdentity, err)
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

// throttledTransport keeps at least interval between two requests for the same URL: within it, the last answer (or error) is handed out again without asking the provider. go-oidc's RemoteKeySet fetches the key set anew for every token whose kid it does not know; this is what keeps a stream of forged tokens from becoming a stream of requests to the identity provider. Only GET is throttled — that is all go-oidc sends.
type throttledTransport struct {
	inner    http.RoundTripper
	interval time.Duration
	now      func() time.Time

	mu   sync.Mutex
	last map[string]*recordedAnswer
}

// recordedAnswer is a response read to the end, so it can be handed out more than once, or the error in its place.
type recordedAnswer struct {
	at     time.Time
	status int
	text   string
	header http.Header
	body   []byte
	err    error
}

func (t *throttledTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return t.inner.RoundTrip(req)
	}
	key := req.URL.String()

	// Held across the fetch: a second caller for the same URL waits for the answer and gets it, instead of sending its own request.
	t.mu.Lock()
	defer t.mu.Unlock()
	if a, ok := t.last[key]; ok && t.now().Sub(a.at) < t.interval {
		return a.replay(req)
	}

	a := &recordedAnswer{}
	res, err := t.inner.RoundTrip(req)
	if err != nil {
		a.err = err
	} else {
		body, readErr := io.ReadAll(io.LimitReader(res.Body, maxKeySetBytes))
		_ = res.Body.Close()
		if readErr != nil {
			a.err = readErr
		} else {
			a.status, a.text, a.header, a.body = res.StatusCode, res.Status, res.Header.Clone(), body
		}
	}
	a.at = t.now()
	if t.last == nil {
		t.last = make(map[string]*recordedAnswer)
	}
	t.last[key] = a
	return a.replay(req)
}

func (a *recordedAnswer) replay(req *http.Request) (*http.Response, error) {
	if a.err != nil {
		return nil, a.err
	}
	return &http.Response{
		Status:        a.text,
		StatusCode:    a.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        a.header.Clone(),
		Body:          io.NopCloser(strings.NewReader(string(a.body))),
		ContentLength: int64(len(a.body)),
		Request:       req,
	}, nil
}
