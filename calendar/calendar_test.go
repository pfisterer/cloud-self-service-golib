package calendar

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pfisterer/cloud-self-service-golib/apierr"
	"github.com/pfisterer/cloud-self-service-golib/day"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestWindows(t *testing.T) {
	if got := Names(); !slices.Equal(got, []string{"24h", "7d", "30d", "total"}) {
		t.Errorf("Names() = %v", got)
	}
	if got := NamesOf(Only(Window30d, Window24h, Window7d)); !slices.Equal(got, []string{"24h", "7d", "30d"}) {
		t.Errorf("Only = %v", got)
	}
	if w, ok := Lookup("7d"); !ok || w.Days != 7 || w.Label != "in 7 Tagen" {
		t.Errorf("Lookup(7d) = %+v, %v", w, ok)
	}
	if _, ok := Lookup("1y"); ok {
		t.Error("Lookup(1y) found a window")
	}
	defer func() {
		if recover() == nil {
			t.Error("Only(1y) did not panic")
		}
	}()
	Only("1y")
}

func TestWindowStartBerlinMidnight(t *testing.T) {
	w24, _ := Lookup(Window24h)
	w7, _ := Lookup(Window7d)
	wt, _ := Lookup(WindowTotal)
	for _, tc := range []struct {
		now     string
		w       Window
		want    string
		comment string
	}{
		// 23:30 UTC on 11 November is 00:30 on 12 November in Berlin (CET): the new day has begun, in UTC it had not.
		{"2026-11-11T23:30:00Z", w24, "2026-11-12", "after Berlin midnight, before UTC midnight"},
		{"2026-11-11T22:59:59Z", w24, "2026-11-11", "a second before Berlin midnight"},
		{"2026-11-11T23:00:00Z", w7, "2026-11-06", "7d at Berlin midnight: the new day and the 6 before"},
		// Summer time (CEST, UTC+2): Berlin midnight is 22:00 UTC.
		{"2026-07-01T22:00:00Z", w24, "2026-07-02", "summer: Berlin midnight at 22:00 UTC"},
		{"2026-07-01T21:59:59Z", w24, "2026-07-01", "summer: before it"},
	} {
		if got := tc.w.Start(at(tc.now)).String(); got != tc.want {
			t.Errorf("%s: %s.Start(%s) = %s; want %s", tc.comment, tc.w.Name, tc.now, got, tc.want)
		}
	}
	if !wt.Start(at("2026-11-11T12:00:00Z")).IsZero() {
		t.Error("total has a start")
	}
}

func TestWindowStartAcrossDST(t *testing.T) {
	w7, _ := Lookup(Window7d)
	// Clocks went back on 25 October 2026 (03:00 CEST -> 02:00 CET): that day has 25 hours, and 7d still counts seven calendar days.
	if got := w7.Start(at("2026-10-27T10:00:00Z")).String(); got != "2026-10-21" {
		t.Errorf("7d after the autumn change = %s", got)
	}
	// Clocks go forward on 28 March 2027 (02:00 CET -> 03:00 CEST): a 23-hour day.
	if got := w7.Start(at("2027-03-29T22:30:00Z")).String(); got != "2027-03-24" {
		t.Errorf("7d after the spring change = %s", got)
	}
	// 00:30 on 29 March 2027 in Berlin is 22:30 UTC on 28 March (CEST, UTC+2).
	w24, _ := Lookup(Window24h)
	if got := w24.Start(at("2027-03-28T22:30:00Z")).String(); got != "2027-03-29" {
		t.Errorf("24h just after midnight following the spring change = %s", got)
	}
}

func TestPeriodInstants(t *testing.T) {
	// The autumn change: 25 October 2026 runs from 22:00 UTC the day before to 23:00 UTC, 25 hours.
	p := Period{From: day.New(2026, 10, 25), To: day.New(2026, 10, 25)}
	if p.Days() != 1 || p.End().Sub(p.Start()) != 25*time.Hour {
		t.Errorf("25.10.2026: %d days, %v", p.Days(), p.End().Sub(p.Start()))
	}
	if got := p.Start().UTC().Format(time.RFC3339); got != "2026-10-24T22:00:00Z" {
		t.Errorf("Start = %s", got)
	}
	// The spring change: 28 March 2027 has 23 hours.
	p = Period{From: day.New(2027, 3, 28), To: day.New(2027, 3, 28)}
	if p.End().Sub(p.Start()) != 23*time.Hour {
		t.Errorf("28.3.2027: %v", p.End().Sub(p.Start()))
	}
	p = Period{From: day.New(2026, 10, 1), To: day.New(2026, 10, 31)}
	if p.Days() != 31 {
		t.Errorf("October: %d days", p.Days())
	}
}

func TestRuleParse(t *testing.T) {
	now := at("2026-11-11T12:00:00Z")
	r := Report(1098)
	for _, tc := range []struct {
		name, from, to string
		rule           Rule
		now            time.Time
		want           string
	}{
		{"default: the last 30 finished days", "", "", r, now, "2026-10-12..2026-11-10"},
		{"only to: 30 days ending there", "", "2026-06-30", r, now, "2026-06-01..2026-06-30"},
		{"only from: up to yesterday", "2026-11-01", "", r, now, "2026-11-01..2026-11-10"},
		{"both", "2026-01-01", "2026-01-31", r, now, "2026-01-01..2026-01-31"},
		{"one day", "2026-01-01", "2026-01-01", r, now, "2026-01-01..2026-01-01"},
		{"live numbers: 7 days to today", "", "", Rule{Days: 7, IncludeToday: true, MaxDays: 365}, now, "2026-11-05..2026-11-11"},
		// 00:30 on 12 November in Berlin: yesterday is the 11th, although in UTC it is still the 11th.
		{"after Berlin midnight", "", "", r, at("2026-11-11T23:30:00Z"), "2026-10-13..2026-11-11"},
	} {
		p, err := tc.rule.Parse(tc.from, tc.to, tc.now)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := p.From.String() + ".." + p.To.String(); got != tc.want {
			t.Errorf("%s: %s; want %s", tc.name, got, tc.want)
		}
	}
}

func TestRuleParseRefuses(t *testing.T) {
	now := at("2026-11-11T12:00:00Z")
	for _, tc := range []struct {
		from, to, text string
		max            int
	}{
		{"2026-13-01", "", "from ist kein Tag", 0},
		{"", "gestern", "to ist kein Tag", 0},
		{"2026-11-02", "2026-11-01", "liegt nach", 0},
		{"2026-01-01", "2026-01-31", "höchstens 30 Tage", 30},
		{"2026-11-01T00:00:00Z", "", "from ist kein Tag", 0},
	} {
		_, err := Report(tc.max).Parse(tc.from, tc.to, now)
		e, ok := errors.AsType[*apierr.Error](err)
		if !ok || e.Status != http.StatusBadRequest || e.Code != "invalid" || !strings.Contains(e.Msg, tc.text) {
			t.Errorf("Parse(%q, %q) = %v; want a 400 with %q", tc.from, tc.to, err, tc.text)
		}
	}
	// The longest period allowed is MaxDays days, both ends included.
	if _, err := Report(30).Parse("2026-01-01", "2026-01-30", now); err != nil {
		t.Errorf("30 days refused with MaxDays 30: %v", err)
	}
}
