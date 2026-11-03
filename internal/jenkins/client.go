// Package jenkins is a minimal Jenkins REST API client.
package jenkins

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Options struct {
	URL      string
	User     string
	Token    string
	Insecure bool
	Timeout  time.Duration
}

type Client struct {
	base  *url.URL
	user  string
	token string
	http  *http.Client

	crumbOnce sync.Once
	crumb     *crumb
}

type crumb struct {
	Field string `json:"crumbRequestField"`
	Value string `json:"crumb"`
}

// APIError carries the HTTP status so callers can map it to exit codes.
type APIError struct {
	Status  int
	Method  string
	URL     string
	Message string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("%s %s: HTTP %d", e.Method, e.URL, e.Status)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

func IsStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

func New(o Options) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(o.URL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid Jenkins URL %q", o.URL)
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: o.Insecure} //nolint:gosec // opt-in per server
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsCfg
	// Crumbs are bound to the session cookie, so a jar is required.
	jar, _ := cookiejar.New(nil)
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &Client{
		base:  u,
		user:  o.User,
		token: o.Token,
		http: &http.Client{Transport: tr, Jar: jar, Timeout: timeout,
			// Jenkins answers actions like POST .../stop with a redirect to an
			// HTML page; following it would turn the result into that page.
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if via[0].Method != http.MethodGet && via[0].Method != http.MethodHead {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
	}, nil
}

func (c *Client) BaseURL() string { return c.base.String() }

// resolve accepts either a server-relative path or an absolute URL on the same host.
func (c *Client) resolve(p string) (*url.URL, error) {
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		u, err := url.Parse(p)
		if err != nil {
			return nil, err
		}
		if u.Host != c.base.Host {
			return nil, fmt.Errorf("%w: URL %s is not on server %s", ErrInvalidInput, p, c.base.Host)
		}
		return u, nil
	}
	ref, err := url.Parse(p)
	if err != nil {
		return nil, err
	}
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + "/" + strings.TrimLeft(ref.Path, "/")
	u.RawPath = ""
	if ref.RawPath != "" {
		u.RawPath = strings.TrimRight(c.base.EscapedPath(), "/") + "/" + strings.TrimLeft(ref.RawPath, "/")
	}
	u.RawQuery = ref.RawQuery
	return &u, nil
}

func (c *Client) newRequest(ctx context.Context, method, p string, body io.Reader) (*http.Request, error) {
	u, err := c.resolve(p)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if c.user != "" || c.token != "" {
		req.SetBasicAuth(c.user, c.token)
	}
	req.Header.Set("User-Agent", "jenkinsctl")
	return req, nil
}

// Do sends a request and converts non-2xx responses into *APIError.
// The caller must close the returned body.
func (c *Client) Do(ctx context.Context, method, p string, contentType string, body io.Reader) (*http.Response, error) {
	req, err := c.newRequest(ctx, method, p, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if method != http.MethodGet && method != http.MethodHead {
		if cr := c.getCrumb(ctx); cr != nil {
			req.Header.Set(cr.Field, cr.Value)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// The query (usually a long tree= filter) only adds noise to the message.
		shown := *req.URL
		shown.RawQuery = ""
		return nil, &APIError{
			Status:  resp.StatusCode,
			Method:  method,
			URL:     shown.String(),
			Message: summarizeBody(resp.Header.Get("Content-Type"), b),
		}
	}
	return resp, nil
}

// getCrumb is best-effort: API-token auth is crumb-exempt on modern Jenkins,
// and the issuer may be disabled entirely.
func (c *Client) getCrumb(ctx context.Context) *crumb {
	c.crumbOnce.Do(func() {
		var cr crumb
		if err := c.GetJSON(ctx, "/crumbIssuer/api/json", &cr); err == nil && cr.Field != "" {
			c.crumb = &cr
		}
	})
	return c.crumb
}

func (c *Client) GetJSON(ctx context.Context, p string, out any) error {
	resp, err := c.Do(ctx, http.MethodGet, p, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", p, err)
	}
	return nil
}

func (c *Client) PostForm(ctx context.Context, p string, form url.Values) (*http.Response, error) {
	var body io.Reader
	ct := ""
	if len(form) > 0 {
		body = strings.NewReader(form.Encode())
		ct = "application/x-www-form-urlencoded"
	}
	return c.Do(ctx, http.MethodPost, p, ct, body)
}

// summarizeBody strips Jenkins' HTML error pages down to something readable.
func summarizeBody(ct string, b []byte) string {
	s := string(b)
	if strings.Contains(ct, "html") {
		if i := strings.Index(s, "<title>"); i >= 0 {
			if j := strings.Index(s[i:], "</title>"); j > 0 {
				return strings.TrimSpace(s[i+7 : i+j])
			}
		}
		return ""
	}
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
