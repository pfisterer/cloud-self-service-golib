package ginweb

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ── Cross-origin writes (CSRF) ───────────────────────────────────────────────

// CrossOriginOptions configures RejectCrossOriginWrites.
type CrossOriginOptions struct {
	// TrustedOrigins are browser origins ("https://host[:port]") whose writes are accepted although the browser calls them cross-origin or same-site. Pass the CORS allowlist here: an origin allowed to read the API cross-origin is the same origin allowed to write to it. Also the place for the BFF's own origin when a proxy in front rewrites the Host header (see RejectCrossOriginWrites).
	TrustedOrigins []string
	// DevMode additionally trusts any loopback origin, as EnableCORS does, so the local Vite dev server keeps working.
	DevMode bool
	Log     *zap.SugaredLogger
}

// RejectCrossOriginWrites refuses state-changing requests (anything but GET, HEAD and OPTIONS) that a browser sent on behalf of a page from another origin, with 403. It is Go's net/http CrossOriginProtection mounted as gin middleware.
//
// Why a BFF needs it: the session cookie is SameSite=Lax, and every host under the platform's registrable domain is the same site, so a page on any of them can make the victim's browser send a credentialed POST that needs no preflight (Content-Type text/plain — gin's ShouldBindJSON does not look at the Content-Type). The proxy turns the cookie into a Bearer and the backend runs the request as the victim. CORS does not help there: it only stops the page from READING the answer.
//
// How a request is judged, in order:
//   - Sec-Fetch-Site (sent by every browser since 2023): "same-origin" or "none" passes, anything else ("same-site", "cross-site") is refused unless Origin is trusted.
//   - Without Sec-Fetch-Site, the Origin header is compared with the Host header; a mismatch is refused unless Origin is trusted.
//   - With neither header the request is not from a browser (CLI, PAT client, service-to-service) and passes — those carry their credential explicitly, so there is nothing to forge.
//
// Proxies: the browser computes Sec-Fetch-Site against the origin it talked to, so a proxy rewriting Host (as one must to reach an upstream behind a name-based ingress) does not affect modern browsers. Only the Origin-vs-Host fallback sees the rewritten Host; for the rare browser without Sec-Fetch-Site behind such a proxy, list the BFF's public origin in TrustedOrigins. Caddy and Traefik pass Host through unchanged by default, so the other routes need nothing.
func RejectCrossOriginWrites(opts CrossOriginOptions) gin.HandlerFunc {
	protection := http.NewCrossOriginProtection()
	for _, origin := range opts.TrustedOrigins {
		origin = normalizeOrigin(origin)
		if origin == "" {
			continue
		}
		if err := protection.AddTrustedOrigin(origin); err != nil && opts.Log != nil {
			opts.Log.Warnf("Cross-origin protection: ignoring invalid trusted origin: %v", err)
		}
	}

	return func(c *gin.Context) {
		err := protection.Check(c.Request)
		if err == nil {
			c.Next()
			return
		}
		origin := c.GetHeader("Origin")
		if opts.DevMode && isLoopbackOrigin(normalizeOrigin(origin)) {
			c.Next()
			return
		}
		if opts.Log != nil {
			opts.Log.Warnf("Refused cross-origin %s %s (Origin %q, Sec-Fetch-Site %q): %v", c.Request.Method, c.Request.URL.Path, origin, c.GetHeader("Sec-Fetch-Site"), err)
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "cross-origin request refused"})
	}
}

// normalizeOrigin trims whitespace and a trailing slash, the two ways a configured origin differs from what a browser sends in the Origin header.
func normalizeOrigin(origin string) string {
	return strings.TrimRight(strings.TrimSpace(origin), "/")
}

// ── Request body limit ───────────────────────────────────────────────────────

// LimitRequestBody caps the request body at maxBytes. A handler reading past it gets an error (ShouldBindJSON fails, the handler answers 400 as for any malformed body) and the connection is closed, so a client cannot keep the service busy reading an unbounded body. A body announced larger than the limit by Content-Length is refused up front with 413.
//
// Not mounted by default: a service that accepts uploads (CSV, LDIF) needs a larger limit on those routes than on the rest, so where to put which limit is its decision.
func LimitRequestBody(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > maxBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
			return
		}
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

// ── Security headers ─────────────────────────────────────────────────────────

// APISecurityHeaders sets the response headers an API answering JSON should carry: no MIME sniffing, no framing, no referrer, and a Content-Security-Policy that lets a response rendered as a document load nothing at all — so a JSON body that happens to contain markup cannot become a page.
//
// For API groups only: the CSP would break an HTML landing page that uses inline styles. Caching is DisableCaching's business and not set here.
func APISecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		c.Next()
	}
}
