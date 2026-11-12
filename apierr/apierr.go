// Package apierr is how a service turns the errors of its operations into the answer of its API: a status, a stable code and a text for people, the same for REST and MCP.
//
// Texts are pairs (msg.Text, German and English), rendered in the caller's language (msg.Lang) only when the answer is written: Body for REST, MCPError for MCP.
//
// The mechanism is shared, the tables are not. Which errors a service tells apart and what it answers otherwise are decisions of that service (one falls back to 400 with the operation's message, another to 500 or 502 with a fixed text), so each service writes its own Classifier: its rules, in order, and its fallback. What is shared is the shape of the answer, the order in which a classification is made, and the rendering, so that the body a browser reads and the text a model reads come from the same place.
package apierr

import (
	"errors"
	"net/http"

	"github.com/pfisterer/cloud-self-service-golib/msg"
)

// Error is an error as the API answers it. cause is what led to it, for the service's log; it never reaches the caller.
type Error struct {
	// Status is the HTTP status.
	Status int
	// Code is the stable, machine-readable reason: what a client matches on instead of the text.
	Code string
	// Msg is the text for people, in German and English.
	Msg   msg.Text
	cause error
}

// New is an answer without a cause.
func New(status int, code string, text msg.Text) *Error {
	return &Error{Status: status, Code: code, Msg: text}
}

// Error is the text for people in German (the default language), for logs and for code that has no caller's language at hand.
func (e *Error) Error() string { return e.Msg.String() }

// Text is the text for people in both languages (msg.Localized).
func (e *Error) Text() msg.Text { return e.Msg }

// Cause is the error the answer was made from, for the log; nil if there is none.
func (e *Error) Cause() error { return e.cause }

// WithCause is a copy of e with this cause.
func (e Error) WithCause(cause error) *Error {
	e.cause = cause
	return &e
}

// Body is the JSON body of an error answer: the text in error (the field the self-service UI reads) and the stable reason in code.
type Body struct {
	// Readable text for people.
	Error string `json:"error" example:"Keine Berechtigung."`
	// Stable machine-readable reason.
	Code string `json:"code" example:"forbidden"`
}

// Body is e as a response body, its text in l.
func (e *Error) Body(l msg.Lang) Body { return Body{Error: e.Msg.In(l), Code: e.Code} }

// MCPError is what an MCP tool reports of e: an error whose text is Msg, without the cause — the model reads what a person would read in the portal, not the database's message. mcpserve.AddTool renders it in the caller's language (msg.Localize); a tool registered otherwise does that itself.
func (e *Error) MCPError() error { return msg.Err(e.Msg) }

// Rule is one row of a service's table: the answer for err, or nil when the row does not apply.
type Rule func(err error) *Error

// Is answers errors that are target (errors.Is) with a fixed answer.
func Is(target error, status int, code string, text msg.Text) Rule {
	return func(err error) *Error {
		if errors.Is(err, target) {
			return &Error{Status: status, Code: code, Msg: text}
		}
		return nil
	}
}

// IsWithText answers errors that are target with status and code, and the error's own text as Msg (msg.TextOf: both languages when the error carries them, else its Error() in both): for an operation whose messages are written for the caller to read.
func IsWithText(target error, status int, code string) Rule {
	return func(err error) *Error {
		if errors.Is(err, target) {
			return &Error{Status: status, Code: code, Msg: msg.TextOf(err)}
		}
		return nil
	}
}

// As answers errors of type T (errors.As) by answer, which may return nil to let the next row decide.
func As[T error](answer func(T) *Error) Rule {
	return func(err error) *Error {
		if t, ok := errors.AsType[T](err); ok {
			return answer(t)
		}
		return nil
	}
}

// Internal is the fallback when a Classifier has none: 500, code internal, "Interner Fehler." / "Internal error.".
var Internal = Error{Status: http.StatusInternalServerError, Code: "internal", Msg: msg.T("Interner Fehler.", "Internal error.")}

// Fallback is a Classifier's Fallback that answers every error with status and code and its own text (msg.TextOf), e.g. a service whose operations' messages without a sentinel are validation messages for the caller (400, invalid).
func Fallback(status int, code string) func(error) *Error {
	return func(err error) *Error { return &Error{Status: status, Code: code, Msg: msg.TextOf(err)} }
}

// Classifier is one service's (or one area's) table: Rules in order, then Fallback.
type Classifier struct {
	Rules []Rule
	// Fallback answers what no rule matched; nil = Internal.
	Fallback func(err error) *Error
}

// Classify is the answer for err, nil for nil. An *Error anywhere in err's chain is answered as it is; otherwise the first rule that matches decides, and then the fallback. The answer carries err as its cause unless the rule set one.
func (c Classifier) Classify(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e
	}
	for _, rule := range c.Rules {
		if e := rule(err); e != nil {
			return withCause(e, err)
		}
	}
	if c.Fallback != nil {
		if e := c.Fallback(err); e != nil {
			return withCause(e, err)
		}
	}
	return Internal.WithCause(err)
}

// With is a copy of c with more rules in front of its own: an area of the service that tells more errors apart and otherwise answers like c.
func (c Classifier) With(rules ...Rule) Classifier {
	return Classifier{Rules: append(append([]Rule{}, rules...), c.Rules...), Fallback: c.Fallback}
}

func withCause(e *Error, err error) *Error {
	if e.cause == nil {
		e.cause = err
	}
	return e
}
