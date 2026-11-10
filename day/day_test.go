package day

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseForms(t *testing.T) {
	for in, want := range map[string]string{
		"2026-12-31":                     "2026-12-31",
		" 2026-12-31 ":                   "2026-12-31",
		"2026-12-30T23:00:00.000Z":       "2026-12-31", // the portal's picker: 31 December at local midnight, winter
		"2026-06-29T22:00:00.000Z":       "2026-06-30", // the same in summer time
		"2026-12-31T00:00:00.000Z":       "2026-12-31", // a stored bare day sent back by the form
		"2026-12-31T00:00:00Z":           "2026-12-31",
		"2026-12-31T23:59:59Z":           "2027-01-01", // 00:59:59 in Berlin (a former term end needs ParseUTCDate)
		"2026-12-31T22:59:59Z":           "2026-12-31",
		"2026-06-30T23:59:59+02:00":      "2026-06-30",
		"2026-06-30T22:30:00Z":           "2026-07-01", // 00:30 in Berlin
		"2026-06-30T21:59:59.999999999Z": "2026-06-30",
		"2026-07-01T00:30:00-05:00":      "2026-07-01", // 07:30 in Berlin
	} {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestParseUTCDate(t *testing.T) {
	for in, want := range map[string]string{"2026-12-31T23:59:59Z": "2026-12-31", "2026-12-31": "2026-12-31", "2026-06-30T23:30:00+02:00": "2026-06-30"} {
		if d, err := ParseUTCDate(in); err != nil || d.String() != want {
			t.Errorf("ParseUTCDate(%q) = %v, %v; want %s", in, d, err, want)
		}
	}
	if _, err := ParseUTCDate("31.12.2026"); err == nil {
		t.Error("ParseUTCDate accepted a bad day")
	}
}

func TestParseRejects(t *testing.T) {
	for _, in := range []string{"", "31.12.2026", "2026-13-01", "2026-12-31T25:00:00Z", "2026-12-31 10:00", "morgen"} {
		if d, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %v, want an error", in, d)
		}
	}
}

func TestSameDayIsEqual(t *testing.T) {
	// The bug of 10.11.2026: the stored date-only end and the form's timestamp of the same day must be the same end.
	a, _ := Parse("2026-12-31")
	b, _ := Parse("2026-12-30T23:00:00.000Z")
	if a.Compare(b) != 0 || a.Before(b) || a.After(b) || a != b {
		t.Fatalf("%v and %v must be the same day", a, b)
	}
}

func TestOrderAndArithmetic(t *testing.T) {
	a, b := New(2026, 12, 31), New(2027, 1, 1)
	if !a.Before(b) || !b.After(a) || a.Compare(b) != -1 || b.Compare(a) != 1 {
		t.Fatal("order")
	}
	if a.AddDays(1) != b || b.AddDays(-1) != a || New(2027, 2, 29) != New(2027, 3, 1) {
		t.Fatal("arithmetic")
	}
}

func TestStartEndInBerlin(t *testing.T) {
	d := New(2026, 3, 29) // summer time starts: 23 hours
	if got := d.Start().UTC().Format(time.RFC3339); got != "2026-03-28T23:00:00Z" {
		t.Errorf("Start = %s", got)
	}
	if got := d.End().UTC().Format(time.RFC3339); got != "2026-03-29T22:00:00Z" {
		t.Errorf("End = %s", got)
	}
	if Of(d.End().Add(-time.Nanosecond)) != d || Of(d.End()) != d.AddDays(1) || Of(d.Start()) != d {
		t.Error("Of at the edges")
	}
}

func TestZeroAndText(t *testing.T) {
	var z Date
	if !z.IsZero() || z.String() != "" || New(2026, 1, 1).IsZero() {
		t.Fatal("zero value")
	}
	var v struct {
		End  Date  `json:"end"`
		Opt  *Date `json:"opt,omitempty"`
		Null Date  `json:"null"`
	}
	if err := json.Unmarshal([]byte(`{"end":"2026-12-31T00:00:00.000Z","null":""}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.End != New(2026, 12, 31) || !v.Null.IsZero() || v.Opt != nil {
		t.Fatalf("unmarshal: %+v", v)
	}
	out, _ := json.Marshal(v)
	if string(out) != `{"end":"2026-12-31","null":""}` {
		t.Fatalf("marshal: %s", out)
	}
	if err := json.Unmarshal([]byte(`{"end":"31.12.2026"}`), &v); err == nil {
		t.Fatal("bad day accepted")
	}
}

func TestToday(t *testing.T) {
	if Today() != Of(time.Now()) {
		t.Skip("crossed midnight in Berlin")
	}
}
