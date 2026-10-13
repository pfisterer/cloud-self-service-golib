package authn

import (
	"errors"
	"testing"
)

func TestActAs(t *testing.T) {
	cases := []struct {
		name, header, actor string
		may                 bool
		want                string
		err                 error
	}{
		{name: "no header", header: "", actor: "admin@x.de", want: "admin@x.de"},
		{name: "blank header", header: "  ", actor: "admin@x.de", want: "admin@x.de"},
		{name: "oneself needs no right", header: "Admin@X.de", actor: "admin@x.de", want: "admin@x.de"},
		{name: "allowed", header: " Student@X.de ", actor: "admin@x.de", may: true, want: "student@x.de"},
		{name: "forbidden", header: "student@x.de", actor: "someone@x.de", err: ErrActAsForbidden},
		{name: "not an address", header: "group:root", actor: "admin@x.de", may: true, err: ErrActAsInvalid},
		{name: "two addresses", header: "a@x.de,b@x.de", actor: "admin@x.de", may: true, err: ErrActAsInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ActAs(tc.header, tc.actor, tc.may)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if got != tc.want {
				t.Errorf("ActAs = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsWrite(t *testing.T) {
	for m, want := range map[string]bool{"GET": false, "head": false, "OPTIONS": false, "POST": true, "PUT": true, "PATCH": true, "DELETE": true} {
		if got := IsWrite(m); got != want {
			t.Errorf("IsWrite(%q) = %v, want %v", m, got, want)
		}
	}
}
