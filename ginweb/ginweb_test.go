package ginweb

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// TestRejectWritesForReadOnlyTokens covers the whole read-only rule: a GET is
// always allowed, a write is refused only when the token is read-only, and a
// request nobody flagged is treated as writable (an OIDC login is not read-only).
func TestRejectWritesForReadOnlyTokens(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		setRO    *bool
		wantCode int
	}{
		{"GET read-only allowed", http.MethodGet, boolp(true), http.StatusOK},
		{"POST read-only refused", http.MethodPost, boolp(true), http.StatusForbidden},
		{"POST writable allowed", http.MethodPost, boolp(false), http.StatusOK},
		{"POST unflagged allowed", http.MethodPost, nil, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(func(c *gin.Context) {
				if tc.setRO != nil {
					SetReadOnly(c, *tc.setRO)
				}
			})
			r.Use(RejectWritesForReadOnlyTokens(nil))
			r.Handle(tc.method, "/x", func(c *gin.Context) { c.Status(http.StatusOK) })

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, "/x", nil))
			if w.Code != tc.wantCode {
				t.Fatalf("%s: got %d, want %d", tc.name, w.Code, tc.wantCode)
			}
		})
	}
}

// TestEnableCORSDoesNotReflectUnlistedOrigin is the security-critical property:
// an origin that is not on the allowlist gets no Access-Control-Allow-Origin
// echoing it back, so a foreign page cannot read credentialed responses.
func TestEnableCORSDoesNotReflectUnlistedOrigin(t *testing.T) {
	r := gin.New()
	v1 := r.Group("/v1")
	EnableCORS(v1, CORSOptions{AllowedOrigins: []string{"https://app.example"}})
	v1.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	// Allowed origin is echoed.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	req.Header.Set("Origin", "https://app.example")
	r.ServeHTTP(w, req)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Errorf("allowed origin: ACAO = %q, want the origin", got)
	}

	// Unlisted origin is NOT reflected.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	req.Header.Set("Origin", "https://evil.example")
	r.ServeHTTP(w, req)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got == "https://evil.example" {
		t.Errorf("unlisted origin was reflected: ACAO = %q", got)
	}
}

// TestEnableCORSLoopbackOnlyInDev shows the loopback exception is gated on DevMode.
func TestEnableCORSLoopbackOnlyInDev(t *testing.T) {
	check := func(devMode bool) string {
		r := gin.New()
		v1 := r.Group("/v1")
		EnableCORS(v1, CORSOptions{DevMode: devMode})
		v1.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
		req.Header.Set("Origin", "http://localhost:5173")
		r.ServeHTTP(w, req)
		return w.Header().Get("Access-Control-Allow-Origin")
	}
	if got := check(true); got != "http://localhost:5173" {
		t.Errorf("dev mode: loopback should be allowed, ACAO = %q", got)
	}
	if got := check(false); got == "http://localhost:5173" {
		t.Errorf("prod mode: loopback must not be allowed, ACAO = %q", got)
	}
}

func boolp(b bool) *bool { return &b }
