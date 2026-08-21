// Package envconf reads configuration from environment variables.
//
// It exists because the platform's services each grew the same helper and then
// drifted apart: the same function name trimmed the value in two of them and
// not in the third, so a Helm value with a trailing blank worked in two
// services and broke in the one. What this package buys is that "what does
// FOO=<something> mean" has one answer for the whole platform.
//
// The rules, which hold for every function here:
//
//   - Unset and empty are the same thing, and so is whitespace-only: the
//     default wins. Configuration arrives from Helm values, where "not set" and
//     "set to nothing" are barely distinguishable, so treating them alike is
//     what a caller expects.
//   - Values are trimmed. Whitespace around a value in a YAML file was not
//     meant to be part of it.
//   - A value that cannot be parsed falls back to the default and writes one
//     line to stderr. It does not abort. This code runs before the logger
//     exists, at a point where dying over a typo in an optional setting would
//     be worse than continuing — but a silent fallback is how a typo survives
//     into production, so the line on stderr is the whole point.
package envconf

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// String returns the trimmed value of key, or defaultVal if it is unset, empty
// or nothing but whitespace.
func String(key, defaultVal string) string {
	if val, ok := lookup(key); ok {
		return val
	}
	return defaultVal
}

// Int returns the value of key parsed as an integer.
//
// A value that is not an integer yields defaultVal and a warning on stderr.
func Int(key string, defaultVal int) int {
	val, ok := lookup(key)
	if !ok {
		return defaultVal
	}

	parsed, err := strconv.Atoi(val)
	if err != nil {
		warn(key, val, "an integer", defaultVal)
		return defaultVal
	}
	return parsed
}

// Bool returns the value of key parsed as a boolean, accepting everything
// strconv.ParseBool does ("1", "t", "true", "TRUE", "0", "f", "false", ...).
//
// A value that is not a boolean yields defaultVal and a warning on stderr.
func Bool(key string, defaultVal bool) bool {
	val, ok := lookup(key)
	if !ok {
		return defaultVal
	}

	parsed, err := strconv.ParseBool(val)
	if err != nil {
		warn(key, val, "a boolean", defaultVal)
		return defaultVal
	}
	return parsed
}

// StringSlice splits the value of key on commas and returns the trimmed,
// non-empty parts in order. Comma is not configurable on purpose: every list
// this platform reads from the environment is comma-separated, and a second
// separator would only be one more thing to get wrong at the call site.
//
// Each transform is applied in order, after trimming. A part that a transform
// empties is dropped, same as one that was empty to begin with. This is where
// strings.ToLower goes when the list holds identifiers that must compare
// case-insensitively, such as e-mail addresses.
//
// Trailing and doubled separators ("a,b," or "a,,b") therefore do not produce
// empty entries — the caller gets the list that was meant.
func StringSlice(key string, defaultVal []string, transform ...func(string) string) []string {
	val, ok := lookup(key)
	if !ok {
		return defaultVal
	}

	parts := strings.Split(val, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part, ok := clean(part, transform); ok {
			out = append(out, part)
		}
	}
	return out
}

// StringSet is StringSlice as a set, for the membership tests that are the only
// reason those lists are read at all. Duplicates collapse; order is lost.
func StringSet(key string, defaultVal map[string]struct{}, transform ...func(string) string) map[string]struct{} {
	val, ok := lookup(key)
	if !ok {
		return defaultVal
	}

	parts := strings.Split(val, ",")
	out := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		if part, ok := clean(part, transform); ok {
			out[part] = struct{}{}
		}
	}
	return out
}

// lookup is the single place that decides what "the variable is set" means.
func lookup(key string) (string, bool) {
	val := strings.TrimSpace(os.Getenv(key))
	return val, val != ""
}

func clean(part string, transform []func(string) string) (string, bool) {
	part = strings.TrimSpace(part)
	for _, t := range transform {
		part = t(part)
	}
	return part, part != ""
}

// warn reports a value that could not be parsed. The value is included because
// these are ports, timeouts and feature flags — never credentials, which are
// read as strings and never land here.
func warn(key, val, want string, defaultVal any) {
	fmt.Fprintf(os.Stderr, "envconf: %s=%q is not %s, using default %v\n", key, val, want, defaultVal)
}
