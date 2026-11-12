package msg_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/pfisterer/cloud-self-service-golib/msg"
)

func TestFromAcceptLanguage(t *testing.T) {
	for header, want := range map[string]msg.Lang{
		"":                             msg.DE,
		"en":                           msg.EN,
		"EN":                           msg.EN,
		"de":                           msg.DE,
		"en-US":                        msg.EN,
		"en_GB":                        msg.EN,
		"de-DE,de;q=0.9,en;q=0.8":      msg.DE,
		"en-US,en;q=0.9,de;q=0.8":      msg.EN,
		"fr-FR,fr;q=0.9,en;q=0.5":      msg.EN,
		"fr-FR,fr;q=0.9":               msg.DE,
		"*":                            msg.DE,
		"de;q=0.2, en;q=0.7":           msg.EN,
		"de;q=0.5, en;q=0.5":           msg.DE,
		"en;q=0.5, de;q=0.5":           msg.EN,
		"en;q=0":                       msg.DE,
		"en;q=0, de;q=0":               msg.DE,
		"en ; q = 0.3 , de ; q = 0.1":  msg.EN,
		"en;q=abc, de;q=0.1":           msg.DE,
		"en;q=2, de;q=0.1":             msg.DE,
		"english":                      msg.DE,
		"de-AT;level=1;q=0.4,en;q=0.3": msg.DE,
		",,;;q=,":                      msg.DE,
	} {
		if got := msg.FromAcceptLanguage(header); got != want {
			t.Errorf("FromAcceptLanguage(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestContext(t *testing.T) {
	ctx := context.Background()
	if _, ok := msg.LangFrom(ctx); ok || msg.LangOf(ctx) != msg.DE {
		t.Fatal("an empty context has no language and reads as the default")
	}
	ctx = msg.WithLang(ctx, msg.EN)
	if l, ok := msg.LangFrom(ctx); !ok || l != msg.EN || msg.LangOf(ctx) != msg.EN {
		t.Fatal("the language put in is the one read")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", "en")
	if msg.FromRequest(r) != msg.EN {
		t.Fatal("FromRequest reads the header")
	}
	if msg.FromRequest(r.WithContext(msg.WithLang(r.Context(), msg.DE))) != msg.DE {
		t.Fatal("FromRequest prefers the context")
	}
}

func TestText(t *testing.T) {
	x := msg.T("Ja.", "Yes.")
	if x.In(msg.DE) != "Ja." || x.In(msg.EN) != "Yes." || x.String() != "Ja." || fmt.Sprint(x) != "Ja." || !x.Complete() || x.IsZero() {
		t.Fatalf("T: %v", x)
	}
	if half := msg.T("Nur deutsch.", ""); half.In(msg.EN) != "Nur deutsch." || half.Complete() {
		t.Fatal("a text without English falls back to German and is not complete")
	}
	if !(msg.Text{}).IsZero() {
		t.Fatal("zero")
	}
	if r := msg.Raw("b_17"); r.In(msg.EN) != "b_17" || r.In(msg.DE) != "b_17" {
		t.Fatal("Raw")
	}
	f := msg.F("%d %s %s", "%d %s %s", 3, msg.T("Tage", "days"), msg.Wrap(errors.New("x"), msg.T("weg", "gone")))
	if f.In(msg.DE) != "3 Tage weg" || f.In(msg.EN) != "3 days gone" {
		t.Fatalf("F: %q / %q", f.In(msg.DE), f.In(msg.EN))
	}
	// %q quotes the text of each language.
	if q := msg.F("%q", "%q", msg.T("a", "b")); q.In(msg.DE) != `"a"` || q.In(msg.EN) != `"b"` {
		t.Fatalf("F %%q: %v", q)
	}
}

var errKind = errors.New("kind")

func TestErrors(t *testing.T) {
	w := msg.Wrap(errKind, msg.T("Abgelehnt.", "Refused."))
	if w.Error() != "Abgelehnt." || !errors.Is(w, errKind) || msg.TextOf(w).In(msg.EN) != "Refused." {
		t.Fatalf("Wrap: %v", w)
	}
	if msg.Wrap(nil, msg.T("a", "b")).Error() != "a" {
		t.Fatal("Wrap(nil)")
	}
	// Errorf wraps with %w like fmt.Errorf, in both languages, with the inner text in each.
	e := msg.Errorf("Projekt %q: %w", "Project %q: %w", "p1", w)
	if e.Error() != `Projekt "p1": Abgelehnt.` || msg.TextOf(e).In(msg.EN) != `Project "p1": Refused.` {
		t.Fatalf("Errorf: %q / %q", e.Error(), msg.TextOf(e).In(msg.EN))
	}
	if !errors.Is(e, errKind) || !errors.Is(e, w) {
		t.Fatal("Errorf must keep errors.Is")
	}
	two := msg.Errorf("%w und %w", "%w and %w", errKind, w)
	if !errors.Is(two, errKind) || msg.TextOf(two).In(msg.EN) != "kind and Refused." {
		t.Fatalf("two %%w: %q", msg.TextOf(two).In(msg.EN))
	}
	if errors.Unwrap(msg.Errorf("ohne", "without")) != nil {
		t.Fatal("nothing wrapped")
	}
	// A wrapper that adds nothing keeps the text, one that adds to it is passed on as it is.
	if msg.TextOf(fmt.Errorf("%w", w)).In(msg.EN) != "Refused." {
		t.Fatal("a bare wrapper keeps the text")
	}
	if got := msg.TextOf(fmt.Errorf("db: %w", w)); got.In(msg.EN) != "db: Abgelehnt." {
		t.Fatalf("a wrapper with more text: %q", got.In(msg.EN))
	}
	if msg.TextOf(errors.New("plain")).In(msg.EN) != "plain" || !msg.TextOf(nil).IsZero() {
		t.Fatal("TextOf plain/nil")
	}
	// Localize renders in a language and keeps the chain.
	en := msg.Localize(e, msg.EN)
	if en.Error() != `Project "p1": Refused.` || !errors.Is(en, errKind) {
		t.Fatalf("Localize: %v", en)
	}
	if msg.Localize(e, msg.DE) != e || msg.Localize(errKind, msg.EN) != errKind || msg.Localize(nil, msg.EN) != nil {
		t.Fatal("Localize leaves German, plain errors and nil alone")
	}
	if l, ok := errors.AsType[msg.Localized](e); !ok || l.Text().In(msg.EN) != `Project "p1": Refused.` {
		t.Fatal("Errorf is Localized")
	}
}

func TestFromRequestNil(t *testing.T) {
	if got := msg.FromRequest(nil); got != msg.Default {
		t.Fatalf("FromRequest(nil) = %q, want %q", got, msg.Default)
	}
}
