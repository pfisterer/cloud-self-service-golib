package ginweb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pfisterer/cloud-self-service-golib/apierr"
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
	r := gin.New()
	r.GET("/x", func(c *gin.Context) { AbortError(c, apierr.New(http.StatusConflict, "refused", "Nein.")) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != 409 || w.Body.String() != `{"error":"Nein.","code":"refused"}` {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
}
