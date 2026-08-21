package envconf

import (
	"reflect"
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		val  string
		want string
	}{
		{name: "unset falls back", set: false, want: "fallback"},
		{name: "empty falls back", set: true, val: "", want: "fallback"},
		// The case that made this package necessary: the same call trimmed in
		// two services and not in the third.
		{name: "surrounding whitespace is not part of the value", set: true, val: "  value  ", want: "value"},
		{name: "whitespace only falls back", set: true, val: "   ", want: "fallback"},
		{name: "inner whitespace is kept", set: true, val: "two words", want: "two words"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("TEST_STRING", tc.val)
			}
			if got := String("TEST_STRING", "fallback"); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInt(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		val  string
		want int
	}{
		{name: "unset falls back", set: false, want: 8080},
		{name: "parsed", set: true, val: "9090", want: 9090},
		{name: "trimmed then parsed", set: true, val: " 9090 ", want: 9090},
		{name: "negative", set: true, val: "-1", want: -1},
		// A typo must not take the process down, but it must not be silent
		// either — the warning goes to stderr, which this cannot assert.
		{name: "letter O instead of zero falls back", set: true, val: "8O80", want: 8080},
		{name: "float falls back", set: true, val: "80.5", want: 8080},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("TEST_INT", tc.val)
			}
			if got := Int("TEST_INT", 8080); got != tc.want {
				t.Errorf("Int() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestBool(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		val  string
		want bool
	}{
		{name: "unset keeps the default", set: false, want: true},
		{name: "false overrides a true default", set: true, val: "false", want: false},
		{name: "uppercase", set: true, val: "TRUE", want: true},
		{name: "numeric", set: true, val: "0", want: false},
		{name: "trimmed", set: true, val: " false ", want: false},
		// "yes" is not a boolean to strconv.ParseBool. Falling back to true
		// here is the dangerous direction, which is why it is worth a test:
		// the warning on stderr is the only thing that reveals it.
		{name: "yes is not a boolean", set: true, val: "yes", want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("TEST_BOOL", tc.val)
			}
			if got := Bool("TEST_BOOL", true); got != tc.want {
				t.Errorf("Bool() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStringSlice(t *testing.T) {
	fallback := []string{"default"}

	cases := []struct {
		name      string
		set       bool
		val       string
		transform []func(string) string
		want      []string
	}{
		{name: "unset falls back", set: false, want: fallback},
		{name: "empty falls back", set: true, val: "", want: fallback},
		{name: "split and trimmed", set: true, val: "a, b ,c", want: []string{"a", "b", "c"}},
		{name: "single value", set: true, val: "only", want: []string{"only"}},
		// The old dynamic-zones implementation kept these as empty strings,
		// which meant an empty CORS origin reached the router.
		{name: "trailing separator yields no empty entry", set: true, val: "a,b,", want: []string{"a", "b"}},
		{name: "doubled separator yields no empty entry", set: true, val: "a,,b", want: []string{"a", "b"}},
		{name: "separators only", set: true, val: ",,", want: []string{}},
		{
			name:      "transform applies after trimming",
			set:       true,
			val:       " Alice@Example.COM , BOB@example.com ",
			transform: []func(string) string{strings.ToLower},
			want:      []string{"alice@example.com", "bob@example.com"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("TEST_SLICE", tc.val)
			}
			got := StringSlice("TEST_SLICE", fallback, tc.transform...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("StringSlice() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestStringSet(t *testing.T) {
	t.Run("unset falls back to the given set", func(t *testing.T) {
		fallback := map[string]struct{}{"default": {}}
		got := StringSet("TEST_SET", fallback)
		if !reflect.DeepEqual(got, fallback) {
			t.Errorf("StringSet() = %#v, want %#v", got, fallback)
		}
	})

	t.Run("lowercased membership, duplicates collapsed", func(t *testing.T) {
		t.Setenv("TEST_SET", "Admin@example.com, admin@EXAMPLE.com ,other@example.com")
		got := StringSet("TEST_SET", nil, strings.ToLower)

		want := map[string]struct{}{
			"admin@example.com": {},
			"other@example.com": {},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("StringSet() = %#v, want %#v", got, want)
		}
	})
}
