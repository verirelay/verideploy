// Package network provides bounded, context-aware HTTP GETs.
package network

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

// Client returns an HTTP client that follows redirects only to HTTPS (HTTP for
// loopback hosts), or none at all when follow is false. Redirect targets may
// carry a query: GitHub release downloads redirect to signed URLs. Callers
// bound each request with a context deadline.
func Client(follow bool) *http.Client {
	return &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if !follow {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if r.URL.Scheme == "https" || r.URL.Scheme == "http" && loopback(r.URL.Hostname()) {
			return nil
		}
		return fmt.Errorf("refusing redirect to %s://%s", r.URL.Scheme, r.URL.Host)
	}}
}

// CheckURL requires an absolute HTTPS URL without credentials, query or
// fragment. HTTP is allowed only for loopback hosts.
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%q is not an absolute URL without credentials, query or fragment", raw)
	}
	if u.Scheme == "https" || u.Scheme == "http" && loopback(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%q must use HTTPS", raw)
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Copy writes the body of a 200 response to w and fails once it exceeds limit
// bytes.
func Copy(ctx context.Context, c *http.Client, address string, w io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return &StatusError{Status: res.StatusCode}
	}
	n, err := io.Copy(w, io.LimitReader(res.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("response exceeds %d bytes", limit)
	}
	return nil
}

// Get returns the body of a 200 response of at most limit bytes.
func Get(ctx context.Context, c *http.Client, address string, limit int64) ([]byte, error) {
	var b bytes.Buffer
	if err := Copy(ctx, c, address, &b, limit); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// StatusError reports a response status other than 200.
type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP %d", e.Status) }

// IsNotFound reports whether err is a 404 response.
func IsNotFound(err error) bool {
	var s *StatusError
	return errors.As(err, &s) && s.Status == http.StatusNotFound
}
