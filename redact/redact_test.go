package redact

import (
	"strings"
	"testing"
)

func TestSecret(t *testing.T) {
	cases := map[string]string{
		"":            "",    // unset stays unset
		"abcd":        "set", // nothing of the value, not even its length
		"supersecret": "set",
	}
	for in, want := range cases {
		if got := Secret(in); got != want {
			t.Errorf("Secret(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConnString(t *testing.T) {
	in := "host=db user=app password=hunter2secret dbname=core sslmode=disable"
	got := ConnString(in)
	if want := "host=db user=app password=set dbname=core sslmode=disable"; got != want {
		t.Fatalf("ConnString redaction:\n got  %q\n want %q", got, want)
	}
	// Everything but the password is preserved verbatim; the password does not leak.
	for _, keep := range []string{"host=db", "user=app", "dbname=core", "sslmode=disable"} {
		if !strings.Contains(got, keep) {
			t.Errorf("ConnString dropped %q from %q", keep, got)
		}
	}
	if strings.Contains(got, "hunter2secret") {
		t.Errorf("ConnString leaked the password: %q", got)
	}
}

func TestConnStringURL(t *testing.T) {
	for in, want := range map[string]string{
		"postgres://app:hunter2secret@db:5432/core?sslmode=disable": "postgres://app:set@db:5432/core?sslmode=disable",
		"postgres://db/core?password=hunter2secret&user=app":        "postgres://db/core?password=set&user=app",
		"postgres://app@db/core":                                    "postgres://app@db/core",
	} {
		got := ConnString(in)
		if got != want || strings.Contains(got, "hunter2secret") {
			t.Errorf("ConnString(%q) = %q, want %q", in, got, want)
		}
	}
}
