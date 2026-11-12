// Package mcptest holds the test helpers every service with an MCP endpoint had written for itself: a client session in memory or over HTTP with a bearer token, the tool names, a call and its text, and the two drift checks that keep the MCP tools in step with the REST API.
//
// The drift checks are the reason this package exists rather than four more copies of a session helper. A service offers its operations twice, as REST routes and as MCP tools, and the tools take hand-written input structs rather than the request types they feed. Both are deliberate, and both drift silently: a route added on one side is never offered on the other, a field added to a request type never reaches the tool. CheckRoutes and CheckInputs turn that silence into a failing test that asks for a decision, not for a tool or a field: every route and every field is answered for, by what offers it or by a reason written down why an agent does not get it.
//
// Depends on the MCP SDK and testing only, no web framework: a service lists its routes as "METHOD /path" strings, whatever router it uses.
package mcptest

import (
	"context"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectTimeout bounds connecting, not the session: a test that hangs on a dead endpoint fails instead.
const connectTimeout = 20 * time.Second

func newClient() *mcp.Client {
	return mcp.NewClient(&mcp.Implementation{Name: "mcptest", Version: "1"}, nil)
}

// InMemory connects a client to s over an in-memory transport and returns the client's session, closed when the test ends. For tests of what a server offers, without HTTP and authentication in front of it.
func InMemory(t testing.TB, s *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	t.Cleanup(cancel)
	clientSide, serverSide := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatalf("mcptest: connect server: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := newClient().Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatalf("mcptest: connect client: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// headerTransport puts the headers on every request, the way an MCP client configured with a token (or a language) does.
type headerTransport struct {
	header http.Header
	base   http.RoundTripper
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	for k, v := range h.header {
		clone.Header[k] = v
	}
	return h.base.RoundTrip(clone)
}

// Dial connects to the streamable HTTP endpoint (the full URL, e.g. srv.URL + "/mcp") with "Authorization: Bearer <bearer>" on every request, or with no Authorization header when bearer is empty. The session is closed when the test ends; a failure to connect fails the test.
func Dial(t testing.TB, endpoint, bearer string) *mcp.ClientSession {
	t.Helper()
	return DialHeader(t, endpoint, bearer, nil)
}

// DialLang is Dial with "Accept-Language: <lang>" on every request (none when lang is empty), for a test of the language of the answers.
func DialLang(t testing.TB, endpoint, bearer, lang string) *mcp.ClientSession {
	t.Helper()
	header := http.Header{}
	if lang != "" {
		header.Set("Accept-Language", lang)
	}
	return DialHeader(t, endpoint, bearer, header)
}

// DialHeader is Dial with header added to every request.
func DialHeader(t testing.TB, endpoint, bearer string, header http.Header) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	t.Cleanup(cancel)
	transport := &mcp.StreamableClientTransport{Endpoint: endpoint}
	header = header.Clone()
	if bearer != "" {
		if header == nil {
			header = http.Header{}
		}
		header.Set("Authorization", "Bearer "+bearer)
	}
	if len(header) > 0 {
		transport.HTTPClient = &http.Client{Transport: headerTransport{header: header, base: http.DefaultTransport}}
	}
	cs, err := newClient().Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("mcptest: connect to %s: %v", endpoint, err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// ToolNames returns the names of the tools the session is offered, sorted, across every page of the listing.
func ToolNames(t testing.TB, cs *mcp.ClientSession) []string {
	t.Helper()
	var names []string
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("mcptest: list tools: %v", err)
		}
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// Call calls the tool and returns its result. Only a failure of the call itself (transport, protocol, an unknown tool) fails the test; a tool that reports an error returns a result with IsError set, for the test to look at.
func Call(t testing.TB, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("mcptest: call %s: %v", name, err)
	}
	return res
}

// Text returns the text content of a result, the parts joined; for an error result that is the message.
func Text(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// Route is the decision for one REST route: the tools that do what it does, or the reason an agent does not get it. Exactly one of the two.
type Route struct {
	// Tools do what the route does; one tool may stand behind several routes, and one route may need several tools.
	Tools []string
	// Omitted says why an agent is not given the route, in words its user could read.
	Omitted string
}

// CheckRoutes holds the routes of the API against the MCP tools: routes as "METHOD /path" (as the router registers them, without the MCP endpoint itself and the CORS preflight), tools as the names a caller who may do everything is offered, and decisions keyed by route.
//
// It fails on a route without a decision, a decision for a route that does not exist, a decision with both tools and a reason or neither, a decision naming a tool that does not exist, and a tool no route stands behind: a tool that does something the API cannot is not what the tools are for.
func CheckRoutes(t testing.TB, routes, tools []string, decisions map[string]Route) {
	t.Helper()
	exists := map[string]bool{}
	for _, r := range routes {
		exists[r] = true
	}
	behindARoute := map[string]bool{}
	for _, route := range slices.Sorted(maps.Keys(decisions)) {
		d := decisions[route]
		if !exists[route] {
			t.Errorf("%s has a decision, but no such route is registered: remove the entry", route)
		}
		if (len(d.Tools) == 0) == (d.Omitted == "") {
			t.Errorf("%s: name the tools that do what it does, or the reason an agent does not get it; one of the two", route)
		}
		for _, tool := range d.Tools {
			if !slices.Contains(tools, tool) {
				t.Errorf("%s is mapped to %q, but no such tool is offered", route, tool)
			}
			behindARoute[tool] = true
		}
	}
	for _, route := range slices.Sorted(slices.Values(routes)) {
		if _, ok := decisions[route]; !ok {
			t.Errorf("%s reaches no tool: map it to the tools that do what it does, or write down under Omitted why an agent does not get it", route)
		}
	}
	for _, tool := range slices.Sorted(slices.Values(tools)) {
		if !behindARoute[tool] {
			t.Errorf("tool %q stands behind no REST route; every tool mirrors a route, so both stay in step", tool)
		}
	}
}

// Input holds a tool's input struct against the request type it feeds. Fields are compared by their JSON names: a field's json tag name, or for an untagged exported field its Go name in snake_case (GPUClass is gpu_class), as a tool would call it; unexported fields and fields tagged "-" do not count, and an embedded struct without a tag name contributes its own fields, as encoding/json flattens it.
type Input struct {
	// Tool names the contract in a failure message.
	Tool string
	// Domain is the request type the tool feeds, Input the tool's input struct; both as values, e.g. tree.CreateNodeRequest{}.
	Domain, Input any
	// Renamed maps a field of Domain to the tool's name for it, where the tool can be clearer than the storage layer.
	Renamed map[string]string
	// Omitted lists the fields of Domain the tool deliberately does not offer, each with the reason.
	Omitted map[string]string
}

// CheckInputs holds every tool input against its request type: each field of Domain must be offered by Input under the same name, offered under the name Renamed gives, or left out with a reason in Omitted. A new field matches none of the three and fails the test, which forces a decision, not the field. A Renamed entry whose target Input lacks fails, and so does a Renamed or Omitted entry for a field Domain no longer has.
func CheckInputs(t testing.TB, inputs []Input) {
	t.Helper()
	for _, c := range inputs {
		domainName, inputName := typeName(c.Domain), typeName(c.Input)
		where := c.Tool + " (" + domainName + ")"
		domain := jsonFields(c.Domain)
		input := jsonFields(c.Input)
		for _, field := range slices.Sorted(maps.Keys(domain)) {
			switch {
			case c.Omitted[field] != "":
				// Deliberately not offered, the reason is recorded.
			case c.Renamed[field] != "":
				if !input[c.Renamed[field]] {
					t.Errorf("%s: %q is mapped to %q, but %s has no such field", where, field, c.Renamed[field], inputName)
				}
			case !input[field]:
				t.Errorf("%s: %s.%s reaches no tool input: add it to %s, map it under Renamed, or write down under Omitted why an agent must not set it", where, domainName, field, inputName)
			}
		}
		// A stale entry claims a decision about a field that no longer exists.
		for _, field := range slices.Sorted(maps.Keys(c.Omitted)) {
			if !domain[field] {
				t.Errorf("%s: Omitted lists %q, which %s does not have", where, field, domainName)
			}
		}
		for _, field := range slices.Sorted(maps.Keys(c.Renamed)) {
			if !domain[field] {
				t.Errorf("%s: Renamed lists %q, which %s does not have", where, field, domainName)
			}
		}
	}
}

func typeName(v any) string {
	if v == nil {
		return "<nil>"
	}
	return reflect.TypeOf(v).String()
}

// jsonFields returns the JSON names of a struct's fields as CheckInputs compares them: a field's json tag name, or for an untagged field its Go name in snake_case (GPUClass is gpu_class, MaxHours is max_hours, GPUs is gpus), as a tool would call it; fields tagged "-" and unexported fields are skipped, and an embedded struct without a tag name contributes its own fields. v may be a struct or a pointer to one; anything else has no fields.
func jsonFields(v any) map[string]bool {
	out := map[string]bool{}
	if v == nil {
		return out
	}
	collectFields(reflect.TypeOf(v), out)
	return out
}

func collectFields(t reflect.Type, out map[string]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := range t.NumField() {
		f := t.Field(i)
		tag, hasTag := f.Tag.Lookup("json")
		name, _, _ := strings.Cut(tag, ",")
		if hasTag && tag == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				// encoding/json promotes the fields of an embedded struct, exported or not.
				collectFields(ft, out)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = snakeCase(f.Name)
		}
		out[name] = true
	}
}

// snakeCase is a Go field name as a json tag would spell it: GPUClass is gpu_class, MaxHours is max_hours, GPUs is gpus, WorkingDir is working_dir.
func snakeCase(name string) string {
	var b strings.Builder
	rs := []rune(name)
	for i, r := range rs {
		// A word starts at an upper-case letter after a lower-case one, or where an acronym ends (GPUClass); a trailing lower-case letter alone is the acronym's plural (GPUs).
		if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(rs[i-1]) || (i+2 < len(rs) && unicode.IsLower(rs[i+1]) && unicode.IsUpper(rs[i-1]))) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
