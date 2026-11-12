package ginweb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pfisterer/cloud-self-service-golib/apierr"
	"github.com/pfisterer/cloud-self-service-golib/msg"
)

func probe(r http.Handler, path string) (int, ReadyBody) {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	var b ReadyBody
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	return w.Code, b
}

func TestHealth(t *testing.T) {
	var dbDown, idpDown bool
	h := &Health{Timeout: 50 * time.Millisecond}
	h.AddCheck("database", func(context.Context) error {
		if dbDown {
			return errors.New("down")
		}
		return nil
	})
	h.AddCheck("slow", func(ctx context.Context) error { return nil })
	h.AddDegraded("oidc_keys", func(context.Context) error {
		if idpDown {
			return errors.New("away")
		}
		return nil
	})
	r := gin.New()
	h.Mount(r)

	if code, b := probe(r, "/readyz"); code != 200 || b.Status != "ok" || len(b.Failed)+len(b.Degraded) != 0 {
		t.Fatalf("all up: %d %+v", code, b)
	}
	idpDown = true
	if code, b := probe(r, "/readyz"); code != 200 || len(b.Degraded) != 1 || b.Degraded[0] != "oidc_keys" || len(b.Failed) != 0 {
		t.Fatalf("a degraded check must not fail readiness: %d %+v", code, b)
	}
	dbDown = true
	if code, b := probe(r, "/readyz"); code != 503 || b.Status != "unavailable" || len(b.Failed) != 1 || b.Failed[0] != "database" {
		t.Fatalf("database down: %d %+v", code, b)
	}
	if code, _ := probe(r, "/healthz"); code != 200 {
		t.Fatalf("liveness does not depend on checks: %d", code)
	}

	// A check that does not answer in time counts as failed.
	hang := &Health{Timeout: 20 * time.Millisecond}
	hang.AddCheck("hangs", func(ctx context.Context) error { <-ctx.Done(); time.Sleep(10 * time.Millisecond); return nil })
	if b, ok := hang.Ready(context.Background()); ok || len(b.Failed) != 1 {
		t.Fatalf("hanging check: %v %+v", ok, b)
	}
	// No checks: ready.
	if _, ok := (&Health{}).Ready(context.Background()); !ok {
		t.Fatal("no checks must be ready")
	}
}

func TestAbortError(t *testing.T) {
	for _, withMiddleware := range []bool{true, false} {
		r := gin.New()
		if withMiddleware {
			r.Use(Language())
		}
		r.GET("/x", func(c *gin.Context) { AbortError(c, apierr.New(http.StatusConflict, "refused", msg.T("Nein.", "No."))) })
		for header, want := range map[string]string{
			"":                   `{"error":"Nein.","code":"refused"}`,
			"en":                 `{"error":"No.","code":"refused"}`,
			"en-US,en;q=0.9":     `{"error":"No.","code":"refused"}`,
			"fr-FR, de;q=0.5":    `{"error":"Nein.","code":"refused"}`,
			"fr":                 `{"error":"Nein.","code":"refused"}`,
			"de-DE, en;q=0.8":    `{"error":"Nein.","code":"refused"}`,
			"de;q=0.1, en;q=0.2": `{"error":"No.","code":"refused"}`,
		} {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			if header != "" {
				req.Header.Set("Accept-Language", header)
			}
			r.ServeHTTP(w, req)
			if w.Code != 409 || w.Body.String() != want {
				t.Errorf("middleware %v, Accept-Language %q: got %d %s, want %s", withMiddleware, header, w.Code, w.Body, want)
			}
		}
	}
}

// Language stores the language in the request's context, where the handler and everything it calls find it, and says that answers vary by it.
func TestLanguage(t *testing.T) {
	r := gin.New()
	r.Use(Language())
	var got msg.Lang
	r.GET("/x", func(c *gin.Context) { got = msg.LangOf(c.Request.Context()) })
	for header, want := range map[string]msg.Lang{"": msg.DE, "en-GB": msg.EN, "de": msg.DE, "*": msg.DE} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Accept-Language", header)
		r.ServeHTTP(w, req)
		if got != want || w.Header().Get("Vary") != "Accept-Language" {
			t.Errorf("Accept-Language %q: lang %q, Vary %q; want %q", header, got, w.Header().Get("Vary"), want)
		}
	}
}

// The refusals of this package answer in the caller's language too.
func TestOwnRefusalsInEnglish(t *testing.T) {
	r := gin.New()
	r.Use(Language(), LimitRequestBody(4))
	r.POST("/x", func(c *gin.Context) {})
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("too long"))
	req.Header.Set("Accept-Language", "en")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge || w.Body.String() != `{"error":"The request is too large.","code":"too_large"}` {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
}
