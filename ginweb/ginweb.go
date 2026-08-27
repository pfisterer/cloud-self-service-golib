// Package ginweb holds the HTTP wiring that every gin-based service on the
// platform had written the same way: the CORS policy, the "don't cache me"
// middleware, and the read-only-token rule for REST.
//
// It is the one place this module takes gin, and the reasoning is the same that
// let mcpserve take the MCP SDK: a consumer that imports no ginweb still does not
// link gin because of this package, but every consumer that DOES serve a gin
// router shares one implementation instead of three copies that drift. The CORS
// policy in particular is security-relevant — a reflected-origin mistake here is
// a credential leak — and a rule that has to be right must not live in three
// files at once.
package ginweb

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ── Caching ──────────────────────────────────────────────────────────────────

// DisableCaching tells the browser to hold onto nothing. Used in development for
// the whole router, and in production for the routes that embed a version or
// serve mutable API state, so a deploy is visible on the next request rather
// than whenever a cache happens to expire.
func DisableCaching() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		c.Next()
	}
}

// ── CORS ─────────────────────────────────────────────────────────────────────

// CORSOptions configures EnableCORS. AllowMethods and AllowHeaders fall back to
// a sensible default when nil, so a service that wants the common set says
// nothing; one that needs extra request headers (a TSIG key, a dev-auth header)
// lists them.
type CORSOptions struct {
	// AllowedOrigins are the exact browser origins permitted cross-origin
	// (e.g. "https://selfservice.dhbw.cloud"). Empty means none — the right
	// default when the SPA reaches the API same-origin through a BFF.
	AllowedOrigins []string
	// DevMode additionally allows any loopback origin, so the local Vite dev
	// server can call the API without anyone configuring an allowlist.
	DevMode bool
	// AllowMethods defaults to GET, POST, PUT, DELETE, OPTIONS.
	AllowMethods []string
	// AllowHeaders defaults to Origin, Content-Type, Authorization.
	AllowHeaders []string
	Log          *zap.SugaredLogger
}

var (
	defaultCORSMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	defaultCORSHeaders = []string{"Origin", "Content-Type", "Authorization"}
)

// EnableCORS restricts cross-origin access to opts.AllowedOrigins (exact origin
// matches), mounted on the given router group.
//
// The important part is what it is NOT: it does not reflect the request origin.
// Reflecting any origin AND allowing credentials is the combination that lets an
// arbitrary page issue credentialed requests to the API and read the answers —
// behind a BFF that turns a session cookie into a Bearer, that is a full account
// takeover from a foreign tab. An unlisted origin is simply refused.
func EnableCORS(rg *gin.RouterGroup, opts CORSOptions) {
	allowed := make(map[string]bool, len(opts.AllowedOrigins))
	for _, origin := range opts.AllowedOrigins {
		if trimmed := strings.TrimRight(strings.TrimSpace(origin), "/"); trimmed != "" {
			allowed[trimmed] = true
		}
	}

	if opts.Log != nil {
		switch {
		case opts.DevMode:
			opts.Log.Infof("CORS: development mode — allowing loopback origins plus %v", opts.AllowedOrigins)
		case len(allowed) == 0:
			opts.Log.Info("CORS: no allowed origins configured — cross-origin API access is disabled")
		default:
			opts.Log.Infof("CORS: allowing cross-origin API access from %v", opts.AllowedOrigins)
		}
	}

	methods := opts.AllowMethods
	if methods == nil {
		methods = defaultCORSMethods
	}
	headers := opts.AllowHeaders
	if headers == nil {
		headers = defaultCORSHeaders
	}

	rg.Use(cors.New(cors.Config{
		AllowOriginFunc: func(origin string) bool {
			origin = strings.TrimRight(origin, "/")
			return allowed[origin] || (opts.DevMode && isLoopbackOrigin(origin))
		},
		AllowCredentials: true,
		AllowMethods:     methods,
		AllowHeaders:     headers,
		MaxAge:           1 * time.Hour,
	}))

	// Gin runs group middleware only for requests that MATCH a route, and no
	// handler is registered for OPTIONS — without this catch-all a preflight
	// would 404 before the CORS middleware ever ran, and every allowed origin
	// would break too. The handler itself sets nothing: for an allowed origin
	// the middleware has already answered 204 with the headers, and any other
	// origin was aborted with 403 before reaching here.
	rg.OPTIONS("/*path", func(c *gin.Context) { c.Status(http.StatusNoContent) })
}

// isLoopbackOrigin reports whether an origin points at this machine — the dev
// server and any local tooling. Only consulted in development mode.
func isLoopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// ── Read-only tokens ─────────────────────────────────────────────────────────

// readOnlyKey carries whether the caller authenticated with a read-only API
// token. Private to this package — services go through SetReadOnly / IsReadOnly
// rather than the gin context key, so the literal is nobody else's business.
const readOnlyKey = "csg_readOnly"

// SetReadOnly records, on the request, whether its credential is read-only.
// A service's auth middleware calls this once it has resolved the token — with
// false for an interactive/OIDC login, which carries no such flag, so a later
// reader never has to tell "not read-only" from "nobody set it".
func SetReadOnly(c *gin.Context, readOnly bool) {
	c.Set(readOnlyKey, readOnly)
}

// IsReadOnly reports whether this request authenticated with a read-only token.
// For routes where the HTTP method does not say what the operation does — MCP,
// where every tool call is a POST — this is the question to ask, once per
// operation, against that operation's own answer.
func IsReadOnly(c *gin.Context) bool {
	v, ok := c.Get(readOnlyKey)
	if !ok {
		return false
	}
	readOnly, ok := v.(bool)
	return ok && readOnly
}

// RejectWritesForReadOnlyTokens refuses anything but GET for a read-only token.
//
// This is the REST rule, and it belongs on the REST route group rather than in
// the auth middleware because it is an approximation: the method stands in for
// "does this change anything", which holds for these routes and nowhere else.
// In the auth middleware it looked like a property of the credential — and would
// refuse every MCP tool call, reads included, since those are all POSTs.
func RejectWritesForReadOnlyTokens(log *zap.SugaredLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && IsReadOnly(c) {
			if log != nil {
				log.Warnf("Attempt to use read-only token for non-GET operation: %s %s",
					c.Request.Method, c.Request.URL.Path)
			}
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "token is read-only"})
			return
		}
		c.Next()
	}
}
