package mcpserve_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pfisterer/cloud-self-service-golib/mcpserve"
	"github.com/pfisterer/cloud-self-service-golib/msg"
)

var errGone = errors.New("gone")

type langOut struct {
	Lang string `json:"lang"`
}

// buildLang registers a tool that reports the language it sees and one that fails with a text in both languages.
func buildLang(caller testCaller) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	mcpserve.AddTool(s, caller, false, &mcp.Tool{Name: "lang"},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ echoIn) (*mcp.CallToolResult, langOut, error) {
			return nil, langOut{Lang: string(msg.LangOf(ctx))}, nil
		})
	mcpserve.AddTool(s, caller, false, &mcp.Tool{Name: "fail"},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
			return nil, echoOut{}, msg.Errorf("%q ist weg: %w", "%q is gone: %w", in.Text, msg.Wrap(errGone, msg.T("gelöscht", "deleted")))
		})
	return s
}

type withHeader struct {
	lang string
	next http.RoundTripper
}

func (h withHeader) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if h.lang != "" {
		r.Header.Set("Accept-Language", h.lang)
	}
	return h.next.RoundTrip(r)
}

func connectLang(t *testing.T, srv *httptest.Server, lang string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	transport := &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: withHeader{lang, http.DefaultTransport}}}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil).Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// A tool sees the language of the HTTP request, and its errors are rendered in it; German without a header.
func TestHandler_ToolsSpeakTheCallersLanguage(t *testing.T) {
	h := mcpserve.Handler(buildLang)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(mcpserve.WithCaller(r.Context(), testCaller{name: "alice"})))
	}))
	t.Cleanup(srv.Close)

	for _, tc := range []struct{ header, lang, text string }{
		{"", "de", `"x" ist weg: gelöscht`},
		{"en-US,en;q=0.9", "en", `"x" is gone: deleted`},
		{"fr", "de", `"x" ist weg: gelöscht`},
	} {
		session := connectLang(t, srv, tc.header)
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "lang", Arguments: map[string]any{"text": "x"}})
		if err != nil {
			t.Fatalf("lang: %v", err)
		}
		if out, _ := res.StructuredContent.(map[string]any); out["lang"] != tc.lang {
			t.Errorf("Accept-Language %q: the tool saw %v, want %s", tc.header, out["lang"], tc.lang)
		}
		res, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "fail", Arguments: map[string]any{"text": "x"}})
		if err != nil || !res.IsError || len(res.Content) != 1 {
			t.Fatalf("fail: %v %+v", err, res)
		}
		if got := res.Content[0].(*mcp.TextContent).Text; got != tc.text {
			t.Errorf("Accept-Language %q: error %q, want %q", tc.header, got, tc.text)
		}
	}
}

// The 401 without a caller is JSON in the caller's language.
func TestHandler_UnauthenticatedInEnglish(t *testing.T) {
	srv := serve(t, nil)
	for header, want := range map[string]string{"": "Anmeldung erforderlich.", "en": "Sign-in required."} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")
		if header != "" {
			req.Header.Set("Accept-Language", header)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body struct{ Error, Code string }
		_ = json.NewDecoder(res.Body).Decode(&body)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized || body.Error != want || body.Code != "unauthenticated" {
			t.Errorf("Accept-Language %q: got %d %+v, want 401 %q", header, res.StatusCode, body, want)
		}
	}
}

// ToolLang falls back to the header of the call when the context carries no language.
func TestToolLang(t *testing.T) {
	req := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: http.Header{"Accept-Language": {"en"}}}}
	if got := mcpserve.ToolLang(context.Background(), req); got != msg.EN {
		t.Errorf("header: %q", got)
	}
	if got := mcpserve.ToolLang(msg.WithLang(context.Background(), msg.DE), req); got != msg.DE {
		t.Errorf("context first: %q", got)
	}
	if got := mcpserve.ToolLang(context.Background(), nil); got != msg.DE {
		t.Errorf("nothing: %q", got)
	}
}
