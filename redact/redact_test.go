package redact

import (
	"strings"
	"testing"
)

func TestSecret(t *testing.T) {
	cases := map[string]string{
		"":            "",     // unset stays unset
		"abcd":        "****", // too short to hint safely
		"abcde":       "abcd…(len 5)",
		"supersecret": "supe…(len 11)",
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
	if want := "host=db user=app password=hunt…(len 13) dbname=core sslmode=disable"; got != want {
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
