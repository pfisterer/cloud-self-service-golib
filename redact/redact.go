// Package redact makes a secret safe to put in a log line.
//
// Every service on the platform logs its resolved configuration at startup, and
// that configuration holds API keys, TSIG keys and database passwords. Each had
// grown its own masking helper, and they had drifted: one showed the first few
// characters, another truncated a whole DSN to four bytes and lost the host with
// it. One package, one answer to "how much of a secret is safe to show".
package redact

import (
	"net/url"
	"strings"
)

// Secret returns what a log line may say about a secret: "set" when it has a value, "" when it is empty, so an unset option still reads as unset. Nothing of the value itself, not even a prefix or its length: logs end up in Loki and in alert messages, readable far beyond the people who may know the secret.
func Secret(s string) string {
	if s == "" {
		return ""
	}
	return "set"
}

// ConnString redacts the password of a DSN and keeps the rest (host, user, dbname), so a broken connection is still diagnosable from the log: the key-value form ("host=db user=x password=secret …", libpq / GORM) and the URL form ("postgres://user:secret@host/db?password=…").
func ConnString(dsn string) string {
	if strings.Contains(dsn, "://") {
		if u, err := url.Parse(dsn); err == nil {
			if _, ok := u.User.Password(); ok {
				u.User = url.UserPassword(u.User.Username(), Secret("x"))
			}
			if q := u.Query(); q.Has("password") {
				q.Set("password", Secret("x"))
				u.RawQuery = q.Encode()
			}
			return u.String()
		}
		return "(unparsable DSN)"
	}
	fields := strings.Fields(dsn)
	for i, f := range fields {
		if k, v, ok := strings.Cut(f, "="); ok && strings.EqualFold(k, "password") {
			fields[i] = k + "=" + Secret(v)
		}
	}
	return strings.Join(fields, " ")
}
