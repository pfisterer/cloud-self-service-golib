package ginweb

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pfisterer/cloud-self-service-golib/apierr"
	"github.com/pfisterer/cloud-self-service-golib/msg"
)

// AbortError answers e as JSON ({"error": …, "code": …}) with its status, its text in the request's language (Lang), and aborts the chain.
func AbortError(c *gin.Context, e *apierr.Error) {
	c.AbortWithStatusJSON(e.Status, e.Body(Lang(c)))
}

// Language stores the language the request asks for (msg.FromAcceptLanguage: de or en, German by default) in its context, where msg.LangOf finds it — in the handlers, in the services they call and in the tools of an MCP endpoint behind it. It also adds Vary: Accept-Language, since the text of an answer depends on it. Mount it on the engine, before everything that can answer with a text.
func Language() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Vary", "Accept-Language")
		if _, ok := msg.LangFrom(c.Request.Context()); !ok {
			c.Request = c.Request.WithContext(msg.WithLang(c.Request.Context(), msg.FromAcceptLanguage(c.GetHeader("Accept-Language"))))
		}
		c.Next()
	}
}

// Lang is the language of the request: the one Language stored, or, where it is not mounted, its Accept-Language header.
func Lang(c *gin.Context) msg.Lang { return msg.FromRequest(c.Request) }

// Check is one readiness check: nil when the dependency it names can be used.
type Check func(ctx context.Context) error

type namedCheck struct {
	name     string
	check    Check
	degrades bool
}

// Health serves the probes: GET /healthz for liveness and GET /readyz for readiness.
//
// /healthz answers 200 as long as the process serves requests at all; it checks nothing, since a liveness probe that fails over a dependency restarts a pod that a restart cannot fix. /readyz runs the checks added with AddCheck and answers 503 with the names of the failing ones, so the pod gets no traffic until they pass; checks added with AddDegraded are reported but never make it 503 — for a dependency without which some requests still work (an identity provider that is away does not stop API tokens), where taking every pod out of the Service at once would turn a partial outage into a full one.
type Health struct {
	// Timeout is how long all checks of one /readyz together may take; 0 = 3 s. A check still running then counts as failed.
	Timeout time.Duration
	mu      sync.RWMutex
	checks  []namedCheck
}

// AddCheck adds a check /readyz must pass.
func (h *Health) AddCheck(name string, check Check) { h.add(namedCheck{name: name, check: check}) }

// AddDegraded adds a check /readyz reports under degraded but that does not make it fail.
func (h *Health) AddDegraded(name string, check Check) {
	h.add(namedCheck{name: name, check: check, degrades: true})
}

func (h *Health) add(c namedCheck) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks = append(h.checks, c)
}

// ReadyBody is the answer of /readyz: status ok or unavailable, and the names of the checks that failed (sorted).
type ReadyBody struct {
	Status   string   `json:"status" example:"ok"`
	Failed   []string `json:"failed,omitempty"`
	Degraded []string `json:"degraded,omitempty"`
}

// Ready runs every check concurrently and reports the result; ok is false when a check added with AddCheck failed.
func (h *Health) Ready(ctx context.Context) (body ReadyBody, ok bool) {
	h.mu.RLock()
	checks := slices.Clone(h.checks)
	h.mu.RUnlock()
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	results := make(chan int, len(checks))
	failed := make([]bool, len(checks))
	for i, c := range checks {
		failed[i] = true
		go func() {
			if c.check(ctx) == nil {
				results <- i
				return
			}
			results <- -1
		}()
	}
	for range checks {
		select {
		case i := <-results:
			if i >= 0 {
				failed[i] = false
			}
			continue
		case <-ctx.Done():
		}
		break
	}
	body.Status = "ok"
	ok = true
	for i, c := range checks {
		switch {
		case !failed[i]:
		case c.degrades:
			body.Degraded = append(body.Degraded, c.name)
		default:
			body.Failed = append(body.Failed, c.name)
			body.Status, ok = "unavailable", false
		}
	}
	slices.Sort(body.Failed)
	slices.Sort(body.Degraded)
	return body, ok
}

// Mount registers GET /healthz and GET /readyz on r. They need no authentication; mount them before the auth middleware or on the engine.
func (h *Health) Mount(r gin.IRoutes) {
	live := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) }
	ready := func(c *gin.Context) {
		body, ok := h.Ready(c.Request.Context())
		status := http.StatusOK
		if !ok {
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, body)
	}
	r.GET("/healthz", live)
	r.GET("/readyz", ready)
}
