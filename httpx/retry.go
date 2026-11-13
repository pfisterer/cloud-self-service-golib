// Package httpx holds the HTTP clients the services use to call other hosts, and one call of a JSON API: every client gets a time limit, and the ones that talk to other hosts can repeat a request that failed on the way out.
//
// It came from the AI service, where every client to another host had grown its own loop around DNS hiccups and broken connections; a second service calling a JSON API needs the same Do, StatusError and retry rule, not a copy of it.
package httpx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/pfisterer/cloud-self-service-golib/promtext"
	"github.com/pfisterer/cloud-self-service-golib/retry"
)

// Retries counts the requests a retrying client repeated, by why (dns: a name that did not resolve; connection: a connection that broke before the answer) and how it ended (recovered: a later attempt got an answer; failed: every attempt failed). It answers whether the repeating is still needed: a service writes it into its /metrics (Retries.WriteTo).
var Retries = func() *promtext.CounterVec {
	c := promtext.NewCounterVec("httpx_retries_total", "Requests to other hosts that were repeated after failing on the way out, by reason (dns, connection) and result (recovered, failed). Counted since the service started.", "reason", "result")
	for _, reason := range []string{"dns", "connection"} {
		for _, result := range []string{"recovered", "failed"} {
			c.Init(reason, result)
		}
	}
	return c
}()

// policy tries a request at most three times, 1 s and 2 s apart (retry.Do doubles from 1 s, at most 2 s); no time limit of its own, the request's context and the client's timeout are.
var policy = retry.Policy{Attempts: 3, Total: -1, Initial: time.Second, Max: 2 * time.Second}

// retrying wraps a transport and repeats a request that failed on the way out (up to three attempts, 1 s and 2 s apart). The DNS servers in front of both clusters have shown short outages ("no such host", "server misbehaving") for names that resolve again a second later; any request is repeated for those. GET and HEAD requests are also repeated after a connection that broke before an answer (TLS handshake timeout, connection reset, EOF): calls from the workspace pods to GitHub and quay.io have failed that way in short bursts (16.10.2026), while the same calls a moment later went through. Only requests without a body or with a replayable body (GetBody) are repeated.
type retrying struct{ next http.RoundTripper }

func (r retrying) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var last error
	attempt, sent := 0, 0
	reason := ""
	err := retry.Do(req.Context(), policy, func(context.Context) error {
		attempt++
		if attempt > 1 && req.Body != nil {
			if req.GetBody == nil {
				return retry.Permanent(last)
			}
			body, err := req.GetBody()
			if err != nil {
				return retry.Permanent(last)
			}
			req.Body = body
		}
		sent++
		resp, last = r.next.RoundTrip(req)
		if last != nil {
			r := retryReason(req, last)
			if r == "" {
				return retry.Permanent(last)
			}
			if reason == "" {
				reason = r
			}
		}
		return last
	})
	if attempt == 0 { // the context ended before the first attempt
		return nil, err
	}
	if sent > 1 { // repeated for real, not only given up before sending again
		result := "recovered"
		if last != nil {
			result = "failed"
		}
		Retries.Inc(reason, result)
	}
	return resp, last
}

// retryReason tells whether a failed request is worth another attempt and why, as Retries counts it: "dns" for a name that did not resolve, "connection" for a request without side effects whose connection broke before the answer, "" for no further attempt.
func retryReason(req *http.Request, err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return ""
	}
	if req.Context().Err() != nil {
		return ""
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "TLS handshake timeout") {
		return "connection"
	}
	return ""
}

// transport is the default transport with a shorter TLS handshake timeout, so a stuck handshake leaves room for another attempt within a client's timeout.
func transport() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSHandshakeTimeout = 5 * time.Second
	return t
}

// WithRetry makes c repeat requests that failed on the way out (see retrying); a client without a transport of its own gets the default one with a 5 s TLS handshake limit.
func WithRetry(c *http.Client) *http.Client {
	next := c.Transport
	if next == nil {
		next = transport()
	}
	c.Transport = retrying{next: next}
	return c
}

// NewClient is a client with the time limit timeout for a whole request, answer included, on the default transport.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// RetryingClient is a client with the time limit timeout that repeats requests that failed on the way out (WithRetry).
func RetryingClient(timeout time.Duration) *http.Client {
	return WithRetry(NewClient(timeout))
}
