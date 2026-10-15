package ginweb

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// crossOriginRequest builds a request to host api.example with the given browser headers; an empty value leaves the header out.
func crossOriginRequest(method, origin, secFetchSite string) *http.Request {
	req := httptest.NewRequest(method, "http://api.example/v1/thing", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "text/plain")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if secFetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", secFetchSite)
	}
	return req
}

func crossOriginRouter(opts CrossOriginOptions) *gin.Engine {
	r := gin.New()
	r.Use(RejectCrossOriginWrites(opts))
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		r.Handle(m, "/v1/thing", func(c *gin.Context) { c.Status(http.StatusOK) })
	}
	return r
}

// TestRejectCrossOriginWrites covers the decision table: Sec-Fetch-Site first, Origin vs Host as fallback, non-browser requests pass, safe methods always pass, trusted origins pass.
func TestRejectCrossOriginWrites(t *testing.T) {
	opts := CrossOriginOptions{TrustedOrigins: []string{" https://trusted.example/ "}}
	cases := []struct {
		name, method, origin, site string
		want                       int
	}{
		// The attack: a page on a sibling host of the same site, no preflight.
		{"same-site POST refused", http.MethodPost, "https://evil.example", "same-site", http.StatusForbidden},
		{"cross-site PUT refused", http.MethodPut, "https://evil.example", "cross-site", http.StatusForbidden},
		{"cross-site PATCH refused", http.MethodPatch, "https://evil.example", "cross-site", http.StatusForbidden},
		{"cross-site DELETE refused", http.MethodDelete, "https://evil.example", "cross-site", http.StatusForbidden},
		{"cross-site POST with Origin null refused", http.MethodPost, "null", "cross-site", http.StatusForbidden},
		// Legitimate browser traffic. Sec-Fetch-Site wins over a mismatching Host, which is what a Host-rewriting proxy produces.
		{"same-origin POST allowed", http.MethodPost, "https://ui.example", "same-origin", http.StatusOK},
		{"user-initiated POST allowed", http.MethodPost, "", "none", http.StatusOK},
		{"trusted origin cross-site POST allowed", http.MethodPost, "https://trusted.example", "cross-site", http.StatusOK},
		// Safe methods are never judged.
		{"cross-site GET allowed", http.MethodGet, "https://evil.example", "cross-site", http.StatusOK},
		{"cross-site HEAD allowed", http.MethodHead, "https://evil.example", "cross-site", http.StatusOK},
		// Fallback for browsers without Sec-Fetch-Site.
		{"old browser Origin matches Host allowed", http.MethodPost, "https://api.example", "", http.StatusOK},
		{"old browser Origin mismatch refused", http.MethodPost, "https://evil.example", "", http.StatusForbidden},
		{"old browser trusted origin allowed", http.MethodPost, "https://trusted.example", "", http.StatusOK},
		// Non-browser clients (CLI, PAT, service-to-service) send neither header.
		{"no browser headers POST allowed", http.MethodPost, "", "", http.StatusOK},
		{"no browser headers DELETE allowed", http.MethodDelete, "", "", http.StatusOK},
	}
	r := crossOriginRouter(opts)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, crossOriginRequest(tc.method, tc.origin, tc.site))
			if w.Code != tc.want {
				t.Fatalf("got %d, want %d (body %q)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// TestRejectCrossOriginWritesDevMode: loopback origins (the Vite dev server) are trusted only in development mode.
func TestRejectCrossOriginWritesDevMode(t *testing.T) {
	for _, tc := range []struct {
		dev  bool
		want int
	}{{true, http.StatusOK}, {false, http.StatusForbidden}} {
		w := httptest.NewRecorder()
		crossOriginRouter(CrossOriginOptions{DevMode: tc.dev}).ServeHTTP(w, crossOriginRequest(http.MethodPost, "http://localhost:5173", "same-site"))
		if w.Code != tc.want {
			t.Fatalf("devMode=%v: got %d, want %d", tc.dev, w.Code, tc.want)
		}
	}
	// Dev mode does not open the door to non-loopback origins.
	w := httptest.NewRecorder()
	crossOriginRouter(CrossOriginOptions{DevMode: true}).ServeHTTP(w, crossOriginRequest(http.MethodPost, "https://evil.example", "cross-site"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("devMode with foreign origin: got %d, want 403", w.Code)
	}
}

// TestRejectCrossOriginWritesInvalidTrustedOrigin: a malformed entry is skipped, not fatal, and trusts nothing.
func TestRejectCrossOriginWritesInvalidTrustedOrigin(t *testing.T) {
	r := crossOriginRouter(CrossOriginOptions{TrustedOrigins: []string{"evil.example", "https://ok.example/path", ""}})
	for _, origin := range []string{"evil.example", "https://ok.example"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, crossOriginRequest(http.MethodPost, origin, "cross-site"))
		if w.Code != http.StatusForbidden {
			t.Fatalf("origin %q: got %d, want 403", origin, w.Code)
		}
	}
}

// TestEnableCORSRejectsCrossOriginWrites: a group set up with EnableCORS gets the CSRF guard without a second call, and the CORS allowlist is what it trusts.
func TestEnableCORSRejectsCrossOriginWrites(t *testing.T) {
	r := gin.New()
	v1 := r.Group("/v1")
	EnableCORS(v1, CORSOptions{AllowedOrigins: []string{"https://app.example"}})
	v1.POST("/thing", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, tc := range []struct {
		name, origin, site string
		want               int
	}{
		{"same-site foreign page refused", "https://evil.example", "same-site", http.StatusForbidden},
		// A Host-rewriting proxy: the gin cors middleware lets an Origin through that matches Host, the CSRF guard still sees same-site and refuses.
		{"same-site page matching Host refused", "http://api.example", "same-site", http.StatusForbidden},
		{"allowlisted origin allowed", "https://app.example", "cross-site", http.StatusOK},
		{"same-origin allowed", "http://api.example", "same-origin", http.StatusOK},
		{"non-browser allowed", "", "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, crossOriginRequest(http.MethodPost, tc.origin, tc.site))
			if w.Code != tc.want {
				t.Fatalf("got %d, want %d", w.Code, tc.want)
			}
		})
	}

	// The preflight for an allowed origin is still answered by CORS, untouched by the guard.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "http://api.example/v1/thing", nil)
	req.Header.Set("Origin", "https://app.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatalf("preflight: got %d, ACAO %q", w.Code, w.Header().Get("Access-Control-Allow-Origin"))
	}
}

// TestLimitRequestBody: a body within the limit is read whole, a larger one fails to read (chunked, no Content-Length) or is refused up front (Content-Length).
func TestLimitRequestBody(t *testing.T) {
	r := gin.New()
	r.Use(LimitRequestBody(8))
	r.POST("/x", func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		c.Status(http.StatusOK)
	})

	for _, tc := range []struct {
		name          string
		body          string
		unknownLength bool
		want          int
	}{
		{"within limit", "12345678", false, http.StatusOK},
		{"announced too large", "123456789", false, http.StatusRequestEntityTooLarge},
		{"streamed too large", "123456789", true, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(tc.body))
			if tc.unknownLength {
				req.ContentLength = -1
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("got %d, want %d", w.Code, tc.want)
			}
		})
	}
}

// TestAPISecurityHeaders: the headers are present on a normal answer and on an aborted one.
func TestAPISecurityHeaders(t *testing.T) {
	r := gin.New()
	r.Use(APISecurityHeaders())
	r.GET("/ok", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
	r.GET("/denied", func(c *gin.Context) { c.AbortWithStatusJSON(http.StatusForbidden, gin.H{}) })
	for _, path := range []string{"/ok", "/denied"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		for header, want := range map[string]string{
			"X-Content-Type-Options":  "nosniff",
			"X-Frame-Options":         "DENY",
			"Referrer-Policy":         "no-referrer",
			"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
		} {
			if got := w.Header().Get(header); got != want {
				t.Fatalf("%s: %s = %q, want %q", path, header, got, want)
			}
		}
	}
}
