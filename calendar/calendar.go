// Package calendar is how usage is counted over days, the contract between the services that book usage and the budget tree that grants amounts per window: the windows a budget may have (24h, 7d, 30d, total) with their calendar rules, and the period of a report (from, to).
//
// Both count in calendar days in Europe/Berlin (package day), decided on 11.11.2026: before, the AI service booked its usage rows per UTC day, so "24h" reset at 01:00 or 02:00 local time, and the two services each had their own list of window names and their own parsing of from and to, which differed when only to was given and in the longest period allowed. A window or a rule that is added here exists for every service at once; what a service offers of it is its own subset (Only).
package calendar

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/pfisterer/cloud-self-service-golib/apierr"
	"github.com/pfisterer/cloud-self-service-golib/day"
)

// The names of the windows.
const (
	Window24h   = "24h"
	Window7d    = "7d"
	Window30d   = "30d"
	WindowTotal = "total"
)

// Window is a span of calendar days a budget counts its usage in.
type Window struct {
	// Name is how it is written in the APIs and stored: 24h, 7d, 30d, total.
	Name string
	// Days is how many calendar days it counts, today included; 0 for total, which counts since the start of whatever the budget belongs to.
	Days int
	// Label is its name for people in German, to follow "… verbraucht": heute, in 7 Tagen, insgesamt.
	Label string
}

// windows are all windows there are, shortest first.
var windows = []Window{
	{Window24h, 1, "heute"},
	{Window7d, 7, "in 7 Tagen"},
	{Window30d, 30, "in 30 Tagen"},
	{WindowTotal, 0, "insgesamt"},
}

// Windows are all windows, shortest first, total last. The slice is a copy.
func Windows() []Window { return slices.Clone(windows) }

// Names are the names of all windows, in the order of Windows.
func Names() []string { return names(windows) }

// Lookup is the window called name; false if there is none.
func Lookup(name string) (Window, bool) {
	i := slices.IndexFunc(windows, func(w Window) bool { return w.Name == name })
	if i < 0 {
		return Window{}, false
	}
	return windows[i], true
}

// Only is the subset of the windows a service offers somewhere, in the order of Windows, e.g. the budget tree without total. It panics on a name that is no window, at startup, so a service cannot offer a window the other side does not know.
func Only(names ...string) []Window {
	for _, n := range names {
		if _, ok := Lookup(n); !ok {
			panic(fmt.Sprintf("calendar: no window %q", n))
		}
	}
	var out []Window
	for _, w := range windows {
		if slices.Contains(names, w.Name) {
			out = append(out, w)
		}
	}
	return out
}

// NamesOf are the names of ws.
func NamesOf(ws []Window) []string { return names(ws) }

func names(ws []Window) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Name
	}
	return out
}

// Start is the first day w counts at now: today in Berlin and the Days-1 days before it (24h = today; 7d = today and the 6 days before), so what is used today counts at once. For total it is the zero day: the caller knows where its whole term starts.
func (w Window) Start(now time.Time) day.Date {
	if w.Days <= 0 {
		return day.Date{}
	}
	return day.Of(now).AddDays(-(w.Days - 1))
}

// Period is a span of days, both included.
type Period struct {
	From day.Date
	To   day.Date
}

// Days is how many days p covers, both ends included.
func (p Period) Days() int { return p.To.Sub(p.From) + 1 }

// Start is the first instant of p in Berlin; End the first instant after it. Instants in [Start, End) belong to p.
func (p Period) Start() time.Time { return p.From.Start() }

// End is the first instant after p in Berlin (the start of the day after To).
func (p Period) End() time.Time { return p.To.End() }

// Rule is how a report reads its period from the query: its default and the longest period it allows.
//
// The rule is one for every report: without to, to is the last day of the default (yesterday, or today for a report of live numbers); without from, the period is Days long and ends at to, whether to was given or not; a period longer than MaxDays or one that ends before it starts is refused.
type Rule struct {
	// Days is the length of the default period and of one that names only to; 0 = 30.
	Days int
	// IncludeToday makes the default end today rather than yesterday: for numbers that are live (an hourly report), not for a report of finished days, whose numbers should not change while one reads them.
	IncludeToday bool
	// MaxDays is the longest period allowed, both ends included; 0 = no limit.
	MaxDays int
}

// DefaultDays is the length of a report's default period.
const DefaultDays = 30

// Report is the rule of a report over finished days: by default the last 30 finished days, at most maxDays.
func Report(maxDays int) Rule { return Rule{Days: DefaultDays, MaxDays: maxDays} }

func (r Rule) days() int {
	if r.Days <= 0 {
		return DefaultDays
	}
	return r.Days
}

// Default is the period r gives at now when the caller names none.
func (r Rule) Default(now time.Time) Period {
	to := day.Of(now)
	if !r.IncludeToday {
		to = to.AddDays(-1)
	}
	return Period{From: to.AddDays(-(r.days() - 1)), To: to}
}

func errPeriod(format string, args ...any) *apierr.Error {
	return apierr.New(http.StatusBadRequest, "invalid", fmt.Sprintf(format, args...))
}

// Parse reads from and to (YYYY-MM-DD, empty = as the rule says) at now. Its errors are a 400 (*apierr.Error, code invalid) with a German text.
func (r Rule) Parse(from, to string, now time.Time) (Period, error) {
	p := r.Default(now)
	if s := strings.TrimSpace(to); s != "" {
		t, err := time.Parse(day.Layout, s)
		if err != nil {
			return Period{}, errPeriod("to ist kein Tag im Format JJJJ-MM-TT: %q.", s)
		}
		p.To = day.New(t.Date())
	}
	p.From = p.To.AddDays(-(r.days() - 1))
	if s := strings.TrimSpace(from); s != "" {
		t, err := time.Parse(day.Layout, s)
		if err != nil {
			return Period{}, errPeriod("from ist kein Tag im Format JJJJ-MM-TT: %q.", s)
		}
		p.From = day.New(t.Date())
	}
	if p.From.After(p.To) {
		return Period{}, errPeriod("from (%s) liegt nach to (%s).", p.From, p.To)
	}
	if r.MaxDays > 0 && p.Days() > r.MaxDays {
		return Period{}, errPeriod("Ein Zeitraum umfasst höchstens %d Tage, dieser %d.", r.MaxDays, p.Days())
	}
	return p, nil
}
