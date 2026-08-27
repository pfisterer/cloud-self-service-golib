// Package redact makes a secret safe to put in a log line.
//
// Every service on the platform logs its resolved configuration at startup, and
// that configuration holds API keys, TSIG keys and database passwords. Each had
// grown its own masking helper, and they had drifted: one showed the first few
// characters, another truncated a whole DSN to four bytes and lost the host with
// it. One package, one answer to "how much of a secret is safe to show".
package redact

import (
	"fmt"
	"strings"
)

// shown is how many leading characters of a secret are kept as a hint — enough
// to tell two keys apart in a log without revealing anything brute-forceable.
const shown = 4

// Secret returns a short, non-reversible preview of a secret: the first few
// characters plus its length, or just a marker when the value is too short to
// reveal even that safely. Empty stays empty, so an unset option still reads as
// unset rather than as "****".
//
// The length guard is also why this never panics on a short or empty string —
// the slice below cannot go out of range.
func Secret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= shown {
		return "****"
	}
	return fmt.Sprintf("%s…(len %d)", s[:shown], len(s))
}

// ConnString redacts only the password=… field of a space-separated DSN
// (the libpq / GORM key-value form, e.g. "host=db user=x password=secret …"),
// keeping host, user and dbname visible so a broken connection is still
// diagnosable from the log.
func ConnString(dsn string) string {
	fields := strings.Fields(dsn)
	for i, f := range fields {
		if k, v, ok := strings.Cut(f, "="); ok && strings.EqualFold(k, "password") {
			fields[i] = k + "=" + Secret(v)
		}
	}
	return strings.Join(fields, " ")
}
