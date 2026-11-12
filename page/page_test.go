package page

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/pfisterer/cloud-self-service-golib/apierr"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		limit, offset string
		want          Request
	}{
		{"", "", Request{DefaultLimit, 0}},
		{" ", " ", Request{DefaultLimit, 0}},
		{"25", "50", Request{25, 50}},
		{"10000", "0", Request{MaxLimit, 0}},
		{" 7 ", "3", Request{7, 3}},
	} {
		got, err := Parse(tc.limit, tc.offset)
		if err != nil || got != tc.want {
			t.Errorf("Parse(%q, %q) = %+v, %v; want %+v", tc.limit, tc.offset, got, err, tc.want)
		}
	}
	for _, tc := range [][2]string{{"x", ""}, {"0", ""}, {"-1", ""}, {"", "-1"}, {"", "y"}, {"1.5", ""}} {
		_, err := Parse(tc[0], tc[1])
		e, ok := errors.AsType[*apierr.Error](err)
		if !ok || e.Status != http.StatusBadRequest || e.Code != "invalid" || !e.Msg.Complete() {
			t.Errorf("Parse(%q, %q) = %v; want a 400 invalid", tc[0], tc[1], err)
		}
	}
}

func TestFromQuery(t *testing.T) {
	got, err := FromQuery(url.Values{"limit": {"5"}, "offset": {"10"}})
	if err != nil || got != (Request{5, 10}) {
		t.Fatalf("FromQuery = %+v, %v", got, err)
	}
}

func TestOf(t *testing.T) {
	all := []int{1, 2, 3, 4, 5}
	for _, tc := range []struct {
		r     Request
		items []int
		more  bool
	}{
		{Request{}, []int{1, 2, 3, 4, 5}, false},
		{Request{Limit: 2}, []int{1, 2}, true},
		{Request{Limit: 2, Offset: 2}, []int{3, 4}, true},
		{Request{Limit: 2, Offset: 4}, []int{5}, false},
		{Request{Limit: 2, Offset: 9}, []int{}, false},
		{Request{Limit: -1, Offset: -3}, []int{1, 2, 3, 4, 5}, false},
	} {
		p := Of(all, tc.r)
		if p.Total != 5 || len(p.Items) != len(tc.items) || p.More() != tc.more {
			t.Errorf("Of(%+v) = %+v, more %v", tc.r, p, p.More())
			continue
		}
		for i := range tc.items {
			if p.Items[i] != tc.items[i] {
				t.Errorf("Of(%+v) = %+v", tc.r, p)
			}
		}
	}
	// The page is a copy: changing it leaves the list alone.
	p := Of(all, Request{Limit: 1})
	p.Items[0] = 99
	if all[0] != 1 {
		t.Error("Of shares its items with the list")
	}
}

func TestJSONShape(t *testing.T) {
	b, err := json.Marshal(New[string](nil, 0, Request{}))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"items":[],"total":0,"limit":100,"offset":0}`; got != want {
		t.Errorf("JSON = %s; want %s", got, want)
	}
	b, _ = json.Marshal(Of([]string{"a", "b"}, Request{Limit: 1, Offset: 1}))
	if got, want := string(b), `{"items":["b"],"total":2,"limit":1,"offset":1}`; got != want {
		t.Errorf("JSON = %s; want %s", got, want)
	}
}

func TestInput(t *testing.T) {
	for _, tc := range []struct {
		in   Input
		want Request
	}{
		{Input{}, Request{MCPDefaultLimit, 0}},
		{Input{Limit: 10, Offset: 20}, Request{10, 20}},
		{Input{Limit: 1000, Offset: -5}, Request{MCPMaxLimit, 0}},
	} {
		if got := tc.in.Request(); got != tc.want {
			t.Errorf("%+v.Request() = %+v; want %+v", tc.in, got, tc.want)
		}
	}
}
