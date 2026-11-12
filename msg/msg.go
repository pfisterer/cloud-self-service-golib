// Package msg is how a service speaks to people in German and English: a text for people is a pair (Text), written once with both languages side by side, and the caller picks the language by Accept-Language; German is the default when the header is missing or names neither.
//
// The pair is fixed where the text is written, not looked up later by a key: a message is read where it is made, a constructor that takes both languages cannot be called with one, and nothing needs a catalogue that drifts from the code. Errors carry their Text (Errorf, Wrap, Err) and keep wrapping as fmt.Errorf does, so errors.Is and errors.As work unchanged; the language is chosen only when the answer is rendered (apierr, ginweb, mcpserve).
//
// No dependencies beyond the standard library.
package msg

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Lang is a language a text for people is rendered in.
type Lang string

// The languages there are.
const (
	DE Lang = "de"
	EN Lang = "en"
)

// Default is the language when the caller names none of ours: German, the language of the platform.
const Default = DE

// Langs are all languages, Default first.
func Langs() []Lang { return []Lang{DE, EN} }

// ParseLang reads a language tag as Accept-Language and Content-Language write it: de, de-DE, en, en_US, EN-gb. false for anything else (and for *).
func ParseLang(tag string) (Lang, bool) {
	primary, _, _ := strings.Cut(strings.TrimSpace(tag), "-")
	primary, _, _ = strings.Cut(primary, "_")
	switch strings.ToLower(primary) {
	case "de":
		return DE, true
	case "en":
		return EN, true
	}
	return "", false
}

// FromAcceptLanguage is the language an Accept-Language header asks for: of the tags that name one of ours, the one with the highest q (the first of equals); tags with q=0 are refused. Default when the header is empty, malformed or names neither (also for "*").
func FromAcceptLanguage(header string) Lang {
	best, bestQ := Default, -1.0
	for part := range strings.SplitSeq(header, ",") {
		tag, params, _ := strings.Cut(part, ";")
		l, ok := ParseLang(tag)
		if !ok {
			continue
		}
		q := 1.0
		for p := range strings.SplitSeq(params, ";") {
			name, value, found := strings.Cut(strings.TrimSpace(p), "=")
			if !found || !strings.EqualFold(strings.TrimSpace(name), "q") {
				continue
			}
			v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil || v < 0 || v > 1 {
				v = 0
			}
			q = v
		}
		if q > 0 && q > bestQ {
			best, bestQ = l, q
		}
	}
	return best
}

// FromRequest is the language r asks for: one already in its context (WithLang, put there by a middleware), else its Accept-Language header.
func FromRequest(r *http.Request) Lang {
	// No request (a gin context built without one, e.g. in a test) is the default, not a panic.
	if r == nil {
		return Default
	}
	if l, ok := LangFrom(r.Context()); ok {
		return l
	}
	return FromAcceptLanguage(r.Header.Get("Accept-Language"))
}

type langKey struct{}

// WithLang is ctx carrying the language of the request.
func WithLang(ctx context.Context, l Lang) context.Context {
	return context.WithValue(ctx, langKey{}, l)
}

// LangFrom is the language WithLang put in ctx; false if none was put there.
func LangFrom(ctx context.Context) (Lang, bool) {
	l, ok := ctx.Value(langKey{}).(Lang)
	return l, ok
}

// LangOf is the language of ctx, Default if none was put there.
func LangOf(ctx context.Context) Lang {
	if l, ok := LangFrom(ctx); ok {
		return l
	}
	return Default
}

// Text is a text for people in German and English. The zero Text is empty in both.
type Text struct {
	de, en string
}

// T is the text de in German and en in English.
func T(de, en string) Text { return Text{de: de, en: en} }

// F is T with both formatted as fmt.Sprintf does, with the same args. An arg that is a Text, or an error that carries one (TextOf), is written in the language of each side.
func F(de, en string, args ...any) Text {
	return Text{de: fmt.Sprintf(de, in(args, DE)...), en: fmt.Sprintf(en, in(args, EN)...)}
}

// Raw is a text that is the same in every language: a name, an id, a message of another system that we pass on as it is.
func Raw(s string) Text { return Text{de: s, en: s} }

// In is t in l; the German text where t has no English one.
func (t Text) In(l Lang) string {
	if l == EN && t.en != "" {
		return t.en
	}
	return t.de
}

// String is the German text, so a Text prints in German with %s and %v.
func (t Text) String() string { return t.de }

// IsZero reports a Text without any text.
func (t Text) IsZero() bool { return t.de == "" && t.en == "" }

// Complete reports a Text with both languages, the check for "nothing reaches people in one language only".
func (t Text) Complete() bool { return t.de != "" && t.en != "" }

// in is args with every Text, and every error that carries one, written in l.
func in(args []any, l Lang) []any {
	out, copied := args, false
	for i, a := range args {
		var s string
		switch v := a.(type) {
		case Text:
			s = v.In(l)
		case error:
			t, ok := lookup(v)
			if !ok {
				continue
			}
			s = t.In(l)
		default:
			continue
		}
		if !copied {
			out, copied = append([]any(nil), args...), true
		}
		out[i] = s
	}
	return out
}

// Localized is an error that carries its text for people. apierr.Error is one, and every error of this package.
type Localized interface {
	error
	Text() Text
}

// textError is an error with a Text, wrapping what its constructor wrapped.
type textError struct {
	text    Text
	wrapped []error
}

func (e *textError) Error() string   { return e.text.de }
func (e *textError) Text() Text      { return e.text }
func (e *textError) Unwrap() []error { return e.wrapped }

// Err is an error whose text is t.
func Err(t Text) error { return &textError{text: t} }

// Errorf is fmt.Errorf for both languages: an error whose text is F(de, en, args...) and which wraps the args de wraps with %w (errors.Is and errors.As see them). en should use %w (or %v) for the same args.
func Errorf(de, en string, args ...any) error {
	e := &textError{text: F(strings.ReplaceAll(de, "%w", "%v"), strings.ReplaceAll(en, "%w", "%v"), args...)}
	switch w := fmt.Errorf(de, args...).(type) {
	case interface{ Unwrap() error }:
		e.wrapped = []error{w.Unwrap()}
	case interface{ Unwrap() []error }:
		e.wrapped = w.Unwrap()
	}
	return e
}

// Wrap is an error whose text is t and which is kind for errors.Is and errors.As, without kind's own text in it: kind decides the status of the answer, t is what the caller reads.
func Wrap(kind error, t Text) error {
	if kind == nil {
		return Err(t)
	}
	return &textError{text: t, wrapped: []error{kind}}
}

// lookup is the Text of err when err says nothing beyond it: err is Localized, or wraps one without adding to its text (fmt.Errorf("%w", e)).
func lookup(err error) (Text, bool) {
	l, ok := errors.AsType[Localized](err)
	if !ok || l.Error() != err.Error() {
		return Text{}, false
	}
	return l.Text(), true
}

// TextOf is err's text for people: its Text when err carries one and says nothing beyond it, otherwise its Error() in every language (Raw). Zero for nil.
func TextOf(err error) Text {
	if err == nil {
		return Text{}
	}
	if t, ok := lookup(err); ok {
		return t
	}
	return Raw(err.Error())
}

// Localize is err with its text in l, for an answer that can only carry a string (an MCP tool's error). It wraps err, so errors.Is and errors.As still see it. err itself when it carries no Text or l is German.
func Localize(err error, l Lang) error {
	if err == nil || l == DE {
		return err
	}
	t, ok := lookup(err)
	if !ok || t.In(l) == err.Error() {
		return err
	}
	return &localized{text: t.In(l), err: err}
}

type localized struct {
	text string
	err  error
}

func (e *localized) Error() string { return e.text }
func (e *localized) Unwrap() error { return e.err }
