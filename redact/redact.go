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
//
// The key is matched case-insensitively in both forms. In the key-value form a value may be quoted ('pa ss word', with \' and \\ inside) and may have blanks around its "="; the whole value goes, however many words it spans. A URL that does not parse is replaced as a whole, since there is no telling where its password is.
func ConnString(dsn string) string {
	if strings.Contains(dsn, "://") {
		return connURL(dsn)
	}
	return connKeyValue(dsn)
}

func connURL(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "(unparsable DSN)"
	}
	if _, ok := u.User.Password(); ok {
		u.User = url.UserPassword(u.User.Username(), Secret("x"))
	}
	q := u.Query()
	changed := false
	for k, vs := range q {
		if strings.EqualFold(k, "password") {
			for i, v := range vs {
				vs[i] = Secret(v)
			}
			changed = true
		}
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// connKeyValue walks a libpq key-value string the way libpq reads it: a key, optional blanks, "=", optional blanks, and a value that is either quoted (up to the next unescaped ') or runs to the next blank. Every pair comes out as key=value, joined by single blanks; a password's value is replaced by Secret of it. Text that is no pair (a stray word) is kept as it is.
func connKeyValue(dsn string) string {
	var out []string
	s := dsn
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		if s == "" {
			break
		}
		// The key runs to "=" or a blank.
		end := strings.IndexAny(s, "= \t\r\n")
		if end < 0 {
			end = len(s)
		}
		key := s[:end]
		rest := strings.TrimLeft(s[end:], " \t\r\n")
		if !strings.HasPrefix(rest, "=") {
			// No "=" follows: not a pair, keep the word.
			out = append(out, key)
			s = s[end:]
			continue
		}
		rest = strings.TrimLeft(rest[1:], " \t\r\n")
		raw, value, n := connValue(rest)
		s = rest[n:]
		if strings.EqualFold(key, "password") {
			out = append(out, key+"="+Secret(value))
		} else {
			out = append(out, key+"="+raw)
		}
	}
	return strings.Join(out, " ")
}

// connValue reads one value at the start of s and returns it as written, as libpq understands it (quotes and escapes removed), and how many bytes of s it took. An unterminated quote runs to the end of s.
func connValue(s string) (raw, value string, n int) {
	var b strings.Builder
	if strings.HasPrefix(s, "'") {
		i := 1
		for i < len(s) {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				b.WriteByte(s[i+1])
				i += 2
				continue
			}
			i++
			if c == '\'' {
				return s[:i], b.String(), i
			}
			b.WriteByte(c)
		}
		return s, b.String(), len(s)
	}
	i := 0
	for i < len(s) && !strings.ContainsRune(" \t\r\n", rune(s[i])) {
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return s[:i], b.String(), i
}
