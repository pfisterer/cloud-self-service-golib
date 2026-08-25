package mcpserve_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pfisterer/cloud-self-service-golib/mcpserve"
)

// testCaller is what a service's own caller type looks like from here: anything
// at all, as long as it can say whether its credential may write.
type testCaller struct {
	name     string
	readOnly bool
}

func (c testCaller) ReadOnly() bool { return c.readOnly }

type echoIn struct {
	Text string `json:"text"`
}

type echoOut struct {
	Text   string `json:"text"`
	Caller string `json:"caller"`
}

// build registers one read tool and one mutating tool, so a test can see which
// of them a given caller is shown.
func build(caller testCaller) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)

	mcpserve.AddTool(s, caller, false, &mcp.Tool{Name: "read_thing", Description: "reads"},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
			return nil, echoOut{Text: in.Text, Caller: caller.name}, nil
		})
	mcpserve.AddTool(s, caller, true, &mcp.Tool{Name: "write_thing", Description: "writes"},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
			return nil, echoOut{Text: in.Text, Caller: caller.name}, nil
		})
	return s
}

// serve mounts the handler behind a stand-in for the service's authentication
// middleware: it puts a caller in the request context, or does not.
func serve(t *testing.T, caller *testCaller) *httptest.Server {
	t.Helper()

	h := mcpserve.Handler(build)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if caller != nil {
			r = r.WithContext(mcpserve.WithCaller(r.Context(), *caller))
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func connect(t *testing.T, srv *httptest.Server) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func TestAddTool_ReadOnlyCallerIsNotShownMutatingTools(t *testing.T) {
	session := connect(t, serve(t, &testCaller{name: "alice", readOnly: true}))

	names := toolNames(t, session)
	if !slices.Contains(names, "read_thing") {
		t.Errorf("a read tool must survive a read-only credential, got %v", names)
	}
	if slices.Contains(names, "write_thing") {
		t.Errorf("a mutating tool must not be offered to a read-only credential, got %v", names)
	}
}

func TestAddTool_WritingCallerIsShownEverything(t *testing.T) {
	session := connect(t, serve(t, &testCaller{name: "alice"}))

	names := toolNames(t, session)
	for _, want := range []string{"read_thing", "write_thing"} {
		if !slices.Contains(names, want) {
			t.Errorf("%q missing for a writing credential, got %v", want, names)
		}
	}
}

// Every MCP call is a POST, reads included. The whole reason the read-only rule
// lives at the tool rather than at the HTTP method is that a read-only caller
// must still be able to call a read.
func TestHandler_ReadOnlyCallerCanStillCallAReadTool(t *testing.T) {
	session := connect(t, serve(t, &testCaller{name: "alice", readOnly: true}))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "read_thing", Arguments: map[string]any{"text": "hello"},
	})
	if err != nil {
		t.Fatalf("call read_thing: %v", err)
	}
	if res.IsError {
		t.Fatalf("read_thing reported an error: %+v", res.Content)
	}
}

// A tool runs as the caller the middleware resolved, not as anything the
// arguments name.
func TestHandler_ToolsRunAsTheCallerInTheContext(t *testing.T) {
	session := connect(t, serve(t, &testCaller{name: "alice"}))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "write_thing", Arguments: map[string]any{"text": "hello"},
	})
	if err != nil {
		t.Fatalf("call write_thing: %v", err)
	}
	out, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result shape: %T", res.StructuredContent)
	}
	if out["caller"] != "alice" {
		t.Errorf("tool ran as %v, want alice", out["caller"])
	}
}

// The one place this package is stricter than the two hand-written versions it
// replaces. Both took the caller with a discarded ok, which hands the tools a
// ZERO caller when authentication did not run: no identity, and ReadOnly()
// false — the most permissive caller there is, produced by the situation that
// should produce the least.
func TestHandler_RefusesARequestWithNoCaller(t *testing.T) {
	srv := serve(t, nil)

	res, err := http.Post(srv.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("got %d, want 401 for a request that carries no caller", res.StatusCode)
	}
}

// A caller type is addressed by its own type, so one kind of caller cannot be
// read out of the context as another.
func TestCallerFrom_IsTypedPerCallerType(t *testing.T) {
	type otherCaller struct{ testCaller }

	ctx := mcpserve.WithCaller(context.Background(), testCaller{name: "alice"})
	if _, ok := mcpserve.CallerFrom[otherCaller](ctx); ok {
		t.Error("a caller of one type must not be readable as another")
	}
	got, ok := mcpserve.CallerFrom[testCaller](ctx)
	if !ok || got.name != "alice" {
		t.Errorf("got %+v, %v — want the caller that was put in", got, ok)
	}
}

func TestConfirmEcho(t *testing.T) {
	if err := mcpserve.ConfirmEcho("confirm_name", "prod", "prod", "budget b_17"); err != nil {
		t.Errorf("a matching echo must pass, got %v", err)
	}

	err := mcpserve.ConfirmEcho("confirm_name", "staging", "prod", "budget b_17")
	if err == nil {
		t.Fatal("a mismatched echo must be refused")
	}
	// The message has to carry all three, or the person approving the call
	// cannot see what went wrong: what they said, what it really is, and which
	// argument to fix.
	for _, want := range []string{"confirm_name", "staging", "prod", "budget b_17"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q is missing %q", err.Error(), want)
		}
	}
}
