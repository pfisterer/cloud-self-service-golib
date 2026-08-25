// Package mcpserve holds the parts of serving an MCP endpoint that are the same
// in every service: how a caller reaches a tool, which tools a read-only
// credential is shown, and how a destructive tool asks to be sure.
//
// The tools themselves stay where their domain is. What is here is the wiring
// around them, and it was already written twice — identically — before this
// package existed.
//
// Deliberately no gin, though both consumers use it. A router is four lines of
// glue that belong to the service; taking a web framework into this module for
// them would make every consumer depend on that choice, including the one that
// serves no MCP endpoint at all. The same reasoning kept a gin middleware out of
// `logging`.
package mcpserve

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Caller is what this package needs to know about whoever is on the other end
// of an MCP request. Exactly one thing: whether their credential may change
// anything. Everything else about an identity — who they are, what they may
// reach — is the service's business and never travels through here.
type Caller interface {
	// ReadOnly reports that the credential this request arrived with is
	// limited to reads.
	ReadOnly() bool
}

// callerKey is generic so that each caller type gets a key of its own. Two
// services in one process (or one service growing a second kind of caller)
// cannot then read each other's value out of a shared context.
type callerKey[C Caller] struct{}

// WithCaller returns a context carrying the caller, which is how the identity
// resolved by HTTP middleware reaches a tool: the MCP SDK hands a tool nothing
// but a context.
func WithCaller[C Caller](ctx context.Context, caller C) context.Context {
	return context.WithValue(ctx, callerKey[C]{}, caller)
}

// CallerFrom returns the caller WithCaller put in the context.
func CallerFrom[C Caller](ctx context.Context) (C, bool) {
	caller, ok := ctx.Value(callerKey[C]{}).(C)
	return caller, ok
}

// Handler serves MCP over HTTP, building a fresh server per request around the
// caller in that request's context.
//
// A server per request rather than one for the process: a tool closes over the
// identity that called it, so there is no way for one request's tools to run
// with another's rights.
//
// A request that arrives with no caller is REFUSED, not served. Both hand-written
// versions of this took the caller with a discarded ok and carried on — which
// hands the tools a zero-valued caller: no identity, and ReadOnly() false. That
// is the most permissive caller there is, produced by the one situation that
// should produce the least: authentication did not run.
func Handler[C Caller](build func(C) *mcp.Server) http.Handler {
	inner := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		caller, _ := CallerFrom[C](r.Context())
		return build(caller)
	}, nil)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := CallerFrom[C](r.Context()); !ok {
			http.Error(w, `{"error":"unable to resolve user context"}`, http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// AddTool registers a tool, and leaves out a mutating one when the caller's
// credential is read-only.
//
// Left out rather than registered-and-refused on purpose: a model picks from the
// tools it is shown. Offering one that always fails invites it to try, read the
// error and try again differently — noise for the user and requests for us. A
// read-only credential simply has a smaller toolbox.
//
// This is the whole read-only rule for MCP, and it has to be here rather than in
// the HTTP layer: every MCP call is a POST, reads included, so the method cannot
// stand in for "does this change anything" the way it can for REST. The tool
// says what it does, and the check runs against that.
func AddTool[In, Out any](s *mcp.Server, caller Caller, mutates bool, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	if mutates && caller.ReadOnly() {
		return
	}
	mcp.AddTool(s, t, h)
}

// ConfirmEcho refuses unless the caller echoed back the thing's own name.
//
// It is NOT a defence against prompt injection — injected text can quote a name
// as easily as invent one — and must not be sold as one. It catches the likelier
// failure: a model that resolved "delete the old one" to the wrong thing.
// Echoing the name means it had to read the thing first, and it puts the name in
// front of the person whose client is asking them to approve the call.
//
// field is the argument's name, so the message names what to fix; subject
// describes the thing in the service's own words ("budget b_17", "rule 4",
// "the zone"). "is" rather than "is named", because for some things — a DNS
// zone — the name IS the identity, and "the zone is named x" would say it twice.
func ConfirmEcho(field, echoed, actual, subject string) error {
	if echoed == actual {
		return nil
	}
	return fmt.Errorf("%s %q does not match: %s is %q. Read it back before destroying it",
		field, echoed, subject, actual)
}
