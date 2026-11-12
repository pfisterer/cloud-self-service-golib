// Package page is how a list endpoint pages: the bounds a caller asks for (limit, offset), the answer {items, total, limit, offset}, and the same for an MCP tool.
//
// Every list of the services is paged, because lists such as a budget's projects or the workspace projects grow with the people using them. The shape comes from openstack-management-api's node lists (tree.NodePage), which the self-service UI already pages through: items is the page, total the number of matches without paging, limit and offset the bounds that were applied, so a client can tell "that is all" (offset + len(items) >= total) from "there is more".
//
// Gin-free on purpose, like mcpserve: Parse and FromQuery read plain strings and url.Values; ginweb.PageRequest is the gin helper.
package page

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/pfisterer/cloud-self-service-golib/apierr"
	"github.com/pfisterer/cloud-self-service-golib/msg"
)

// DefaultLimit is the page size of a REST list when the caller names none; MaxLimit is the largest a caller gets, a larger limit is cut to it.
const (
	DefaultLimit = 100
	MaxLimit     = 500
)

// MCPDefaultLimit and MCPMaxLimit are the page sizes of an MCP tool: smaller than REST's, since a page lands in a model's context, where 50 projects with their limits are already a lot to read, and a model that wants more pages through.
const (
	MCPDefaultLimit = 50
	MCPMaxLimit     = 200
)

// Page is one page of a list as the API answers it. Items is never null in JSON.
type Page[T any] struct {
	// The entries of this page.
	Items []T `json:"items"`
	// How many entries match, without paging.
	Total int `json:"total" example:"240"`
	// The page size that was applied.
	Limit int `json:"limit" example:"100"`
	// How many entries were skipped.
	Offset int `json:"offset" example:"0"`
}

// More reports whether entries follow after this page.
func (p Page[T]) More() bool { return p.Offset+len(p.Items) < p.Total }

// Request is the page a caller asks for. The zero value is the first page of DefaultLimit.
type Request struct {
	Limit  int
	Offset int
}

// normalized is r with the defaults applied: a limit of 0 or less is DefaultLimit, one above MaxLimit is MaxLimit, a negative offset is 0.
func (r Request) normalized() Request {
	if r.Limit <= 0 {
		r.Limit = DefaultLimit
	}
	r.Limit = min(r.Limit, MaxLimit)
	r.Offset = max(r.Offset, 0)
	return r
}

// errInvalid is the refusal of bounds that do not parse.
func errInvalid(detail msg.Text) *apierr.Error {
	return apierr.New(http.StatusBadRequest, "invalid", msg.F("Ungültige Angaben zum Blättern (limit, offset): %s", "Invalid paging (limit, offset): %s", detail))
}

// Parse reads limit and offset as a query gives them: empty (or blank) is the default (DefaultLimit, 0), a limit above MaxLimit is cut to it. A value that is no number, a limit of 0 or less and a negative offset are a 400 (*apierr.Error, code invalid) with a text in German and English.
func Parse(limit, offset string) (Request, error) {
	r := Request{Limit: DefaultLimit}
	if s := strings.TrimSpace(limit); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return Request{}, errInvalid(msg.F("limit ist keine ganze Zahl (%q).", "limit is not a whole number (%q).", s))
		}
		if n <= 0 {
			return Request{}, errInvalid(msg.T("limit muss größer als 0 sein.", "limit must be greater than 0."))
		}
		r.Limit = min(n, MaxLimit)
	}
	if s := strings.TrimSpace(offset); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return Request{}, errInvalid(msg.F("offset ist keine ganze Zahl (%q).", "offset is not a whole number (%q).", s))
		}
		if n < 0 {
			return Request{}, errInvalid(msg.T("offset darf nicht negativ sein.", "offset must not be negative."))
		}
		r.Offset = n
	}
	return r, nil
}

// FromQuery is Parse on the query parameters limit and offset.
func FromQuery(q url.Values) (Request, error) { return Parse(q.Get("limit"), q.Get("offset")) }

// New is the page of items for r, where the store already applied r and counted total: for a list read page by page from a database.
func New[T any](items []T, total int, r Request) Page[T] {
	r = r.normalized()
	if items == nil {
		items = []T{}
	}
	return Page[T]{Items: items, Total: total, Limit: r.Limit, Offset: r.Offset}
}

// Of is the page r of a list held in memory whole. An offset past the end is an empty page with the total.
func Of[T any](all []T, r Request) Page[T] {
	r = r.normalized()
	start := min(r.Offset, len(all))
	end := min(start+r.Limit, len(all))
	items := make([]T, end-start)
	copy(items, all[start:end])
	return Page[T]{Items: items, Total: len(all), Limit: r.Limit, Offset: r.Offset}
}

// Input is the paging an MCP tool takes, to embed in its input struct. Zero means the default, so a model that leaves it out still gets a sensible answer.
type Input struct {
	Limit  int `json:"limit,omitempty" jsonschema:"how many to return, default 50, at most 200"`
	Offset int `json:"offset,omitempty" jsonschema:"how many to skip, for paging; total in the answer says how many there are"`
}

// Request is the page in asks for: a limit of 0 or less is MCPDefaultLimit, one above MCPMaxLimit is MCPMaxLimit, a negative offset is 0. A model's input is not refused over its bounds.
func (in Input) Request() Request {
	limit := in.Limit
	if limit <= 0 {
		limit = MCPDefaultLimit
	}
	return Request{Limit: min(limit, MCPMaxLimit), Offset: max(in.Offset, 0)}
}
