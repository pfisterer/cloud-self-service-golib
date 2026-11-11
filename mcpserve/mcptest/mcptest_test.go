package mcptest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recorder is a testing.TB that collects what a check reports instead of failing the test running it.
type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

// expect fails unless the check reported exactly one error per want, each containing its want.
func (r *recorder) expect(t *testing.T, wants ...string) {
	t.Helper()
	if len(r.errs) != len(wants) {
		t.Fatalf("got %d errors, want %d:\n%s", len(r.errs), len(wants), strings.Join(r.errs, "\n"))
	}
	for _, w := range wants {
		if !slices.ContainsFunc(r.errs, func(e string) bool { return strings.Contains(e, w) }) {
			t.Errorf("no error mentions %q:\n%s", w, strings.Join(r.errs, "\n"))
		}
	}
}

type echoIn struct {
	Text string `json:"text"`
}

func testServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
		if in.Text == "" {
			return nil, nil, fmt.Errorf("text is empty")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}, &mcp.TextContent{Text: "!"}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "another"}, func(context.Context, *mcp.CallToolRequest, echoIn) (*mcp.CallToolResult, any, error) {
		return nil, nil, nil
	})
	return s
}

func TestInMemoryToolNamesCallText(t *testing.T) {
	cs := InMemory(t, testServer())
	if got := ToolNames(t, cs); !slices.Equal(got, []string{"another", "echo"}) {
		t.Fatalf("ToolNames = %v", got)
	}
	if res := Call(t, cs, "echo", map[string]any{"text": "hi"}); res.IsError || Text(res) != "hi!" {
		t.Fatalf("echo: %v %q", res.IsError, Text(res))
	}
	if res := Call(t, cs, "echo", map[string]any{"text": ""}); !res.IsError || !strings.Contains(Text(res), "text is empty") {
		t.Fatalf("a tool error must come back as a result: %v %q", res.IsError, Text(res))
	}
	if res := Call(t, cs, "echo", nil); !res.IsError || Text(res) == "" {
		t.Fatalf("nil arguments are sent as an empty object: %v %q", res.IsError, Text(res))
	}
	if Text(nil) != "" {
		t.Fatal("Text(nil)")
	}
}

func TestDialSendsTheBearer(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return testServer() }, &mcp.StreamableHTTPOptions{Stateless: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cs := Dial(t, srv.URL, "s3cret")
	if got := ToolNames(t, cs); len(got) != 2 {
		t.Fatalf("ToolNames over HTTP = %v", got)
	}
	_ = Dial(t, srv.URL, "")
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(seen, "Bearer s3cret") || !slices.Contains(seen, "") {
		t.Fatalf("Authorization headers seen: %q", seen)
	}
	for _, a := range seen {
		if a != "Bearer s3cret" && a != "" {
			t.Fatalf("unexpected Authorization %q", a)
		}
	}
}

func TestCheckRoutes(t *testing.T) {
	routes := []string{"GET /v1/things", "POST /v1/things", "GET /v1/admin"}
	tools := []string{"list_things", "create_thing"}
	good := map[string]Route{
		"GET /v1/things":  {Tools: []string{"list_things"}},
		"POST /v1/things": {Tools: []string{"create_thing"}},
		"GET /v1/admin":   {Omitted: "admin only"},
	}
	r := &recorder{TB: t}
	CheckRoutes(r, routes, tools, good)
	r.expect(t)

	for name, c := range map[string]struct {
		routes, tools []string
		decisions     map[string]Route
		want          string
	}{
		"unmapped route": {append(slices.Clone(routes), "DELETE /v1/things/:id"), tools, good, "DELETE /v1/things/:id reaches no tool"},
		"stale decision": {routes, tools, with(good, "PUT /v1/gone", Route{Omitted: "x"}), "PUT /v1/gone has a decision, but no such route"},
		"both":           {routes, tools, with(good, "GET /v1/admin", Route{Tools: []string{"list_things"}, Omitted: "x"}), "GET /v1/admin: name the tools"},
		"neither":        {routes, tools, with(good, "GET /v1/admin", Route{}), "GET /v1/admin: name the tools"},
		"unknown tool":   {routes, tools, with(good, "GET /v1/admin", Route{Tools: []string{"get_admin"}}), `mapped to "get_admin"`},
		"uncovered tool": {routes, append(slices.Clone(tools), "orphan_tool"), good, `tool "orphan_tool" stands behind no REST route`},
	} {
		t.Run(name, func(t *testing.T) {
			r := &recorder{TB: t}
			CheckRoutes(r, c.routes, c.tools, c.decisions)
			r.expect(t, c.want)
		})
	}
}

func with(m map[string]Route, k string, v Route) map[string]Route {
	out := map[string]Route{}
	for key, val := range m {
		out[key] = val
	}
	out[k] = v
	return out
}

type paging struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type hidden struct {
	Secret string `json:"secret"`
}

type domainReq struct {
	paging
	*hidden
	ParentID   string `json:"parent_id"`
	Name       string `json:"name,omitempty"`
	GPUClass   string
	MaxHours   int
	Ignored    string `json:"-"`
	unexported string
	Meta       paging `json:"meta"`
}

func TestJSONFields(t *testing.T) {
	got := jsonFields(domainReq{})
	want := []string{"gpu_class", "limit", "max_hours", "meta", "name", "offset", "parent_id", "secret"}
	var names []string
	for n := range got {
		names = append(names, n)
	}
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Fatalf("jsonFields = %v, want %v", names, want)
	}
	if len(jsonFields(&domainReq{})) != len(want) || len(jsonFields(nil)) != 0 || len(jsonFields(3)) != 0 {
		t.Fatal("pointer, nil and non-struct")
	}
	_ = domainReq{}.unexported
}

func TestSnakeCase(t *testing.T) {
	for in, want := range map[string]string{"GPUClass": "gpu_class", "MaxHours": "max_hours", "GPUs": "gpus", "Person": "person", "Live": "live", "WorkingDir": "working_dir", "ID": "id", "ParentID": "parent_id"} {
		if got := snakeCase(in); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}

type toolIn struct {
	BudgetID string `json:"budget_id"`
	Name     string `json:"name"`
	paging
}

func TestCheckInputs(t *testing.T) {
	type plain struct {
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
		Limit    int    `json:"limit"`
	}
	good := Input{Tool: "create", Domain: plain{}, Input: toolIn{}, Renamed: map[string]string{"parent_id": "budget_id"}}
	r := &recorder{TB: t}
	CheckInputs(r, []Input{good})
	r.expect(t)

	type grown struct {
		plain
		Kind string `json:"kind"`
	}
	for name, c := range map[string]struct {
		in   Input
		want string
	}{
		"uncovered field":    {Input{Tool: "create", Domain: grown{}, Input: toolIn{}, Renamed: good.Renamed}, "kind reaches no tool input"},
		"renamed to nothing": {Input{Tool: "create", Domain: plain{}, Input: toolIn{}, Renamed: map[string]string{"parent_id": "parent"}}, `"parent_id" is mapped to "parent"`},
		"stale omitted":      {Input{Tool: "create", Domain: plain{}, Input: toolIn{}, Renamed: good.Renamed, Omitted: map[string]string{"gone": "x"}}, `Omitted lists "gone"`},
		"stale renamed":      {Input{Tool: "create", Domain: plain{}, Input: toolIn{}, Renamed: map[string]string{"parent_id": "budget_id", "gone": "name"}}, `Renamed lists "gone"`},
	} {
		t.Run(name, func(t *testing.T) {
			r := &recorder{TB: t}
			CheckInputs(r, []Input{c.in})
			r.expect(t, c.want)
		})
	}
	// An omitted field with a reason is answered for.
	r = &recorder{TB: t}
	CheckInputs(r, []Input{{Tool: "create", Domain: grown{}, Input: toolIn{}, Renamed: good.Renamed, Omitted: map[string]string{"kind": "the tool sets it"}}})
	r.expect(t)
}
