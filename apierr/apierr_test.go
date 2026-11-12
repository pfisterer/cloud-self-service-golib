package apierr_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/pfisterer/cloud-self-service-golib/apierr"
	"github.com/pfisterer/cloud-self-service-golib/msg"
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
			apierr.Is(errNotFound, http.StatusNotFound, "not_found", msg.T("Nicht gefunden.", "Not found.")),
			apierr.IsWithText(errConflict, http.StatusConflict, "conflict"),
			apierr.As(func(r *refusal) *apierr.Error {
				if r.Reason == "" {
					return nil
				}
				return apierr.New(http.StatusConflict, "refused", msg.Raw(r.Reason))
			}),
		},
		Fallback: apierr.Fallback(http.StatusBadRequest, "invalid"),
	}
	own := apierr.New(http.StatusForbidden, "forbidden", msg.T("Keine Berechtigung.", "Not allowed."))
	plain := errors.New("Name fehlt.")
	twin := msg.Errorf("Name fehlt.", "Name missing.")
	cases := []struct {
		name string
		c    apierr.Classifier
		err  error
		want apierr.Body
		code int
		en   string
	}{
		{"sentinel, wrapped", base, fmt.Errorf("project 7: %w", errNotFound), apierr.Body{Error: "Nicht gefunden.", Code: "not_found"}, 404, "Not found."},
		{"sentinel with its own text", base, fmt.Errorf("Der Knoten wurde inzwischen geändert: %w", errConflict), apierr.Body{Error: "Der Knoten wurde inzwischen geändert: conflict", Code: "conflict"}, 409, "Der Knoten wurde inzwischen geändert: conflict"},
		{"sentinel with its own text in both languages", base, msg.Errorf("Der Knoten wurde inzwischen geändert: %w", "The node has changed in the meantime: %w", errConflict), apierr.Body{Error: "Der Knoten wurde inzwischen geändert: conflict", Code: "conflict"}, 409, "The node has changed in the meantime: conflict"},
		{"sentinel wrapped with a text of its own", base, msg.Wrap(errConflict, msg.T("Schon vergeben.", "Already taken.")), apierr.Body{Error: "Schon vergeben.", Code: "conflict"}, 409, "Already taken."},
		{"sentinel with a text, wrapped without adding to it", base, fmt.Errorf("%w", msg.Wrap(errConflict, msg.T("Schon vergeben.", "Already taken."))), apierr.Body{Error: "Schon vergeben.", Code: "conflict"}, 409, "Already taken."},
		{"sentinel with a text, wrapped with more text", base, fmt.Errorf("Projekt 7: %w", msg.Wrap(errConflict, msg.T("Schon vergeben.", "Already taken."))), apierr.Body{Error: "Projekt 7: Schon vergeben.", Code: "conflict"}, 409, "Projekt 7: Schon vergeben."},
		{"type", base, fmt.Errorf("x: %w", &refusal{"Budget aufgebraucht."}), apierr.Body{Error: "Budget aufgebraucht.", Code: "refused"}, 409, "Budget aufgebraucht."},
		{"type whose rule declines", base, &refusal{}, apierr.Body{Error: "", Code: "invalid"}, 400, ""},
		{"an *Error stays as it is", base, fmt.Errorf("wrapped: %w", own), apierr.Body{Error: "Keine Berechtigung.", Code: "forbidden"}, 403, "Not allowed."},
		{"fallback of the service", base, plain, apierr.Body{Error: "Name fehlt.", Code: "invalid"}, 400, "Name fehlt."},
		{"fallback of the service, both languages", base, twin, apierr.Body{Error: "Name fehlt.", Code: "invalid"}, 400, "Name missing."},
		{"no fallback is internal", apierr.Classifier{}, plain, apierr.Body{Error: "Interner Fehler.", Code: "internal"}, 500, "Internal error."},
		{"an area in front of the base", base.With(apierr.Is(plain, http.StatusGone, "gone", msg.T("Weg.", "Gone."))), plain, apierr.Body{Error: "Weg.", Code: "gone"}, 410, "Gone."},
		{"the area keeps the base's rules", base.With(), errNotFound, apierr.Body{Error: "Nicht gefunden.", Code: "not_found"}, 404, "Not found."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.c.Classify(tc.err)
			if e.Body(msg.DE) != tc.want || e.Status != tc.code {
				t.Fatalf("got %d %+v, want %d %+v", e.Status, e.Body(msg.DE), tc.code, tc.want)
			}
			if en := e.Body(msg.EN); en.Error != tc.en || en.Code != tc.want.Code {
				t.Fatalf("English: got %+v, want %q", en, tc.en)
			}
			if e.Error() != tc.want.Error {
				t.Fatalf("Error() = %q, want the German text", e.Error())
			}
			if e != own && e.Cause() != tc.err {
				t.Fatalf("cause = %v, want the classified error", e.Cause())
			}
			if e.MCPError().Error() != tc.want.Error || errors.Unwrap(e.MCPError()) != nil {
				t.Fatalf("MCP text %q must be the message alone", e.MCPError())
			}
			if got := msg.Localize(e.MCPError(), msg.EN).Error(); got != tc.en {
				t.Fatalf("MCP text in English %q, want %q", got, tc.en)
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
