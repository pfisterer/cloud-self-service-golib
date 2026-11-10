// Package day is a calendar day in Europe/Berlin: the unit in which projects and budgets end.
//
// End dates had been stored as "2026-12-31", "2026-12-31T00:00:00.000Z" and "2026-12-31T23:59:59Z" and compared as instants or as strings, so the same day written twice could look like an extension (os-mgt 0.8.75). Decided on 10.11.2026: an end is a day, compared as a day in Europe/Berlin, stored as "2006-01-02"; a timestamp in the input is accepted and turned into its day in Berlin. That is what the portal means: its date picker reports a day as local midnight, so 31 December arrives as "2026-12-30T23:00:00.000Z". A writer that encoded a day as its UTC date at another time of day (os-mgt's former term end "…T23:59:59Z") has to say so itself, with ParseUTCDate.
package day

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // Europe/Berlin also in images without a zone database (distroless, scratch).
)

// Layout is how a day is written and stored.
const Layout = "2006-01-02"

// Berlin is the zone a day is counted in.
var Berlin = mustLoad("Europe/Berlin")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// Date is one calendar day. The zero value is no day (IsZero).
type Date struct {
	t time.Time // midnight UTC of the day; only year, month and day matter
}

// New is the day year-month-day; out-of-range values normalise as time.Date does.
func New(year int, month time.Month, d int) Date {
	return Date{time.Date(year, month, d, 0, 0, 0, 0, time.UTC)}
}

// Of is the day an instant falls on in Berlin.
func Of(t time.Time) Date {
	y, m, d := t.In(Berlin).Date()
	return New(y, m, d)
}

// Today is the current day in Berlin.
func Today() Date { return Of(time.Now()) }

// Parse reads a day: "2006-01-02", or an RFC 3339 timestamp with or without fractional seconds ("…Z" or an offset like "+02:00"), which counts as the day it falls on in Berlin. Surrounding spaces are ignored; anything else is an error.
func Parse(s string) (Date, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(Layout, s); err == nil {
		return New(t.Date()), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return Of(t), nil
	}
	return Date{}, fmt.Errorf("not a day: %q (want %s or RFC 3339)", s, Layout)
}

// ParseUTCDate reads s like Parse but takes a timestamp's date in UTC instead of its day in Berlin: for values a writer stored as "this UTC date" at a time of day that falls on the next day in Berlin, such as a former term end "2026-12-31T23:59:59Z".
func ParseUTCDate(s string) (Date, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return New(t.UTC().Date()), nil
	}
	return Parse(s)
}

// Normalize is s as it is stored: Parse, then String.
func Normalize(s string) (string, error) {
	d, err := Parse(s)
	if err != nil {
		return "", err
	}
	return d.String(), nil
}

// IsZero reports whether d is no day.
func (d Date) IsZero() bool { return d.t.IsZero() }

// String is d as "2006-01-02"; "" for the zero value.
func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return d.t.Format(Layout)
}

// Compare is -1, 0 or +1 as d is before, the same as or after e.
func (d Date) Compare(e Date) int { return d.t.Compare(e.t) }

// Before reports whether d is an earlier day than e.
func (d Date) Before(e Date) bool { return d.t.Before(e.t) }

// After reports whether d is a later day than e.
func (d Date) After(e Date) bool { return d.t.After(e.t) }

// AddDays is the day n days after d (before, for negative n).
func (d Date) AddDays(n int) Date { return Date{d.t.AddDate(0, 0, n)} }

// Start is the first instant of d in Berlin.
func (d Date) Start() time.Time {
	y, m, dd := d.t.Date()
	return time.Date(y, m, dd, 0, 0, 0, 0, Berlin)
}

// End is the first instant after d in Berlin, i.e. the start of the next day: something that ends on d runs while now.Before(d.End()).
func (d Date) End() time.Time { return d.AddDays(1).Start() }

// MarshalText writes d as "2006-01-02" (JSON, YAML).
func (d Date) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText reads what Parse reads; "" is the zero value.
func (d *Date) UnmarshalText(b []byte) error {
	if strings.TrimSpace(string(b)) == "" {
		*d = Date{}
		return nil
	}
	p, err := Parse(string(b))
	if err != nil {
		return err
	}
	*d = p
	return nil
}
