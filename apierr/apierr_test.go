package apierr_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/pfisterer/cloud-self-service-golib/apierr"
)

var (
	errNotFound = errors.New("not found")
	errConflict = errors.New("conflict")
)

type refusal struct{ Reason string }

func (r *refusal) Error() string { return r.Reason }

func TestClassify(t *testing.T) {
	base := apierr.Classifier{
		Rules: []apierr.Rule{
			apierr.Is(errNotFound, http.StatusNotFound, "not_found", "Nicht gefunden."),
			apierr.IsWithText(errConflict, http.StatusConflict, "conflict"),
			apierr.As(func(r *refusal) *apierr.Error {
				if r.Reason == "" {
					return nil
				}
				return apierr.New(http.StatusConflict, "refused", r.Reason)
			}),
		},
		Fallback: func(err error) *apierr.Error { return apierr.New(http.StatusBadRequest, "invalid", err.Error()) },
	}
	own := apierr.New(http.StatusForbidden, "forbidden", "Keine Berechtigung.")
	plain := errors.New("Name fehlt.")
	cases := []struct {
		name string
		c    apierr.Classifier
		err  error
		want apierr.Body
		code int
	}{
		{"sentinel, wrapped", base, fmt.Errorf("project 7: %w", errNotFound), apierr.Body{Error: "Nicht gefunden.", Code: "not_found"}, 404},
		{"sentinel with its own text", base, fmt.Errorf("Der Knoten wurde inzwischen geändert: %w", errConflict), apierr.Body{Error: "Der Knoten wurde inzwischen geändert: conflict", Code: "conflict"}, 409},
		{"type", base, fmt.Errorf("x: %w", &refusal{"Budget aufgebraucht."}), apierr.Body{Error: "Budget aufgebraucht.", Code: "refused"}, 409},
		{"type whose rule declines", base, &refusal{}, apierr.Body{Error: "", Code: "invalid"}, 400},
		{"an *Error stays as it is", base, fmt.Errorf("wrapped: %w", own), apierr.Body{Error: "Keine Berechtigung.", Code: "forbidden"}, 403},
		{"fallback of the service", base, plain, apierr.Body{Error: "Name fehlt.", Code: "invalid"}, 400},
		{"no fallback is internal", apierr.Classifier{}, plain, apierr.Body{Error: "Interner Fehler.", Code: "internal"}, 500},
		{"an area in front of the base", base.With(apierr.Is(plain, http.StatusGone, "gone", "Weg.")), plain, apierr.Body{Error: "Weg.", Code: "gone"}, 410},
		{"the area keeps the base's rules", base.With(), errNotFound, apierr.Body{Error: "Nicht gefunden.", Code: "not_found"}, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.c.Classify(tc.err)
			if e.Body() != tc.want || e.Status != tc.code {
				t.Fatalf("got %d %+v, want %d %+v", e.Status, e.Body(), tc.code, tc.want)
			}
			if e != own && e.Cause() != tc.err {
				t.Fatalf("cause = %v, want the classified error", e.Cause())
			}
			if e.MCPError().Error() != tc.want.Error || errors.Unwrap(e.MCPError()) != nil {
				t.Fatalf("MCP text %q must be the message alone", e.MCPError())
			}
		})
	}
	if base.Classify(nil) != nil {
		t.Fatal("nil is no error")
	}
	// The package-level answers are not changed by a classification.
	_ = apierr.Classifier{}.Classify(plain)
	if apierr.Internal.Cause() != nil {
		t.Fatal("Internal got a cause")
	}
}
