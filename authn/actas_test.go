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
		{name: "two addresses with a blank", header: "a@x.de b@x.de", actor: "admin@x.de", may: true, err: ErrActAsInvalid},
		{name: "display name form", header: "Bob <bob@x.de>", actor: "admin@x.de", may: true, err: ErrActAsInvalid},
		{name: "non-ASCII", header: "jürgen@x.de", actor: "admin@x.de", may: true, err: ErrActAsInvalid},
		{name: "Kelvin sign is not k", header: "\u212Aarl@x.de", actor: "admin@x.de", may: true, err: ErrActAsInvalid},
		{name: "no local part", header: "@x.de", actor: "admin@x.de", may: true, err: ErrActAsInvalid},
		{name: "control character", header: "bob\x00@x.de", actor: "admin@x.de", may: true, err: ErrActAsInvalid},
		{name: "invalid is invalid even without the right", header: "not-an-address", actor: "someone@x.de", err: ErrActAsInvalid},
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
