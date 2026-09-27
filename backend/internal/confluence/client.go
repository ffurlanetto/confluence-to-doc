// Package confluence is a minimal client for the Confluence REST API
// (Server / Data Center), authenticated with a Personal Access Token.
package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

var (
	ErrUnauthorized = errors.New("confluence: invalid or expired personal access token")
	ErrForbidden    = errors.New("confluence: access denied")
	ErrNotFound     = errors.New("confluence: page not found")
	ErrTooLarge     = errors.New("confluence: resource exceeds size limit")
	ErrForeignHost  = errors.New("confluence: refusing to send credentials to a foreign host")
)

// Retryable reports whether err is a transient failure worth retrying later.
func Retryable(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code == http.StatusTooManyRequests || se.Code >= 500
	}
	return !errors.Is(err, ErrUnauthorized) && !errors.Is(err, ErrForbidden) &&
		!errors.Is(err, ErrNotFound) && !errors.Is(err, ErrTooLarge) && !errors.Is(err, ErrForeignHost) &&
		!errors.Is(err, context.Canceled)
}

// StatusError is returned for unexpected HTTP statuses.
type StatusError struct {
	Code int
	Path string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("confluence: unexpected status %d for %s", e.Code, e.Path)
}

type PageSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	SpaceKey  string `json:"spaceKey"`
	SpaceName string `json:"spaceName"`
	WebURL    string `json:"webUrl"`
}

type Page struct {
	PageSummary
	Version  int
	BodyHTML string
}

type User struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

type Client struct {
	base       *url.URL
	token      string
	http       *http.Client
	maxRetries int
	sleep      func(context.Context, time.Duration) error
}

type Option func(*Client)

// WithMaxRetries overrides the number of retries on 429/5xx responses.
func WithMaxRetries(n int) Option { return func(c *Client) { c.maxRetries = n } }

// NewClient returns a client for the Confluence instance at base. The token
// is only ever sent to that exact scheme+host.
func NewClient(base *url.URL, token string, httpClient *http.Client, opts ...Option) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	b := *base
	b.Path = strings.TrimRight(b.Path, "/")
	c := &Client{base: &b, token: token, http: httpClient, maxRetries: 3, sleep: sleepCtx}
	// Never follow redirects to another host with our Authorization header.
	hc := *c.http
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if !c.sameOrigin(req.URL) {
			return ErrForeignHost
		}
		return nil
	}
	c.http = &hc
	for _, o := range opts {
		o(c)
	}
	return c
}

// BaseURL returns the configured Confluence base URL.
func (c *Client) BaseURL() *url.URL { u := *c.base; return &u }

// CurrentUser validates the token and returns the authenticated user.
func (c *Client) CurrentUser(ctx context.Context) (*User, error) {
	var u User
	if err := c.getJSON(ctx, "/rest/api/user/current", nil, &u); err != nil {
		return nil, err
	}
	if u.Username == "" && u.DisplayName == "" {
		// Anonymous access returns 200 with an empty/anonymous user.
		return nil, ErrUnauthorized
	}
	return &u, nil
}

type apiContent struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Space *struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"space"`
	Version *struct {
		Number int `json:"number"`
	} `json:"version"`
	Body *struct {
		ExportView *struct {
			Value string `json:"value"`
		} `json:"export_view"`
	} `json:"body"`
	Links struct {
		WebUI string `json:"webui"`
	} `json:"_links"`
}

type apiList struct {
	Results []apiContent `json:"results"`
	Size    int          `json:"size"`
	Limit   int          `json:"limit"`
	Start   int          `json:"start"`
	Links   struct {
		Next string `json:"next"`
	} `json:"_links"`
}

func (c *Client) toPage(a apiContent) Page {
	p := Page{PageSummary: PageSummary{ID: a.ID, Title: a.Title}}
	if a.Space != nil {
		p.SpaceKey, p.SpaceName = a.Space.Key, a.Space.Name
	}
	if a.Version != nil {
		p.Version = a.Version.Number
	}
	if a.Body != nil && a.Body.ExportView != nil {
		p.BodyHTML = a.Body.ExportView.Value
	}
	if a.Links.WebUI != "" {
		p.WebURL = c.base.String() + a.Links.WebUI
	}
	return p
}

// GetPage fetches a page, optionally with its rendered (export view) body.
func (c *Client) GetPage(ctx context.Context, id string, withBody bool) (*Page, error) {
	if !isNumeric(id) {
		return nil, ErrNotFound
	}
	expand := "space,version"
	if withBody {
		expand += ",body.export_view"
	}
	var a apiContent
	if err := c.getJSON(ctx, "/rest/api/content/"+id, url.Values{"expand": {expand}}, &a); err != nil {
		return nil, err
	}
	if a.Type != "" && a.Type != "page" {
		return nil, ErrNotFound
	}
	p := c.toPage(a)
	return &p, nil
}

// ListChildren returns all direct child pages of id, in Confluence order.
func (c *Client) ListChildren(ctx context.Context, id string, withBody bool) ([]Page, error) {
	if !isNumeric(id) {
		return nil, ErrNotFound
	}
	expand := "space,version"
	if withBody {
		expand += ",body.export_view"
	}
	var out []Page
	const limit = 50
	for start := 0; ; {
		q := url.Values{"expand": {expand}, "limit": {strconv.Itoa(limit)}, "start": {strconv.Itoa(start)}}
		var l apiList
		if err := c.getJSON(ctx, "/rest/api/content/"+id+"/child/page", q, &l); err != nil {
			return nil, err
		}
		for _, r := range l.Results {
			out = append(out, c.toPage(r))
		}
		if l.Links.Next == "" || len(l.Results) == 0 {
			return out, nil
		}
		start += len(l.Results)
	}
}

// SearchPages finds pages by title (CQL), optionally restricted to a space.
func (c *Client) SearchPages(ctx context.Context, query, spaceKey string, limit int) ([]PageSummary, error) {
	cql := fmt.Sprintf(`type=page AND title ~ %s`, cqlQuote(query))
	if spaceKey != "" {
		cql += " AND space=" + cqlQuote(spaceKey)
	}
	q := url.Values{"cql": {cql}, "limit": {strconv.Itoa(limit)}, "expand": {"space"}}
	var l apiList
	if err := c.getJSON(ctx, "/rest/api/content/search", q, &l); err != nil {
		return nil, err
	}
	out := make([]PageSummary, 0, len(l.Results))
	for _, r := range l.Results {
		out = append(out, c.toPage(r).PageSummary)
	}
	return out, nil
}

// FindPageByTitle resolves a "/display/SPACE/Title" style reference.
func (c *Client) FindPageByTitle(ctx context.Context, spaceKey, title string) (*Page, error) {
	q := url.Values{"spaceKey": {spaceKey}, "title": {title}, "type": {"page"}, "expand": {"space,version"}}
	var l apiList
	if err := c.getJSON(ctx, "/rest/api/content", q, &l); err != nil {
		return nil, err
	}
	if len(l.Results) == 0 {
		return nil, ErrNotFound
	}
	p := c.toPage(l.Results[0])
	return &p, nil
}

// ResolveURL turns a possibly relative URL found in page content into an
// absolute URL on the Confluence instance.
func (c *Client) ResolveURL(raw string) (*url.URL, error) {
	ref, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	return c.base.ResolveReference(ref), nil
}

// Download fetches a resource hosted on the Confluence instance (typically an
// attachment or an image) with a hard size limit.
func (c *Client) Download(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, error) {
	u, err := c.ResolveURL(rawURL)
	if err != nil {
		return nil, "", err
	}
	if !c.sameOrigin(u) {
		return nil, "", ErrForeignHost
	}
	resp, err := c.do(ctx, u)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxBytes {
		return nil, "", ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > maxBytes {
		return nil, "", ErrTooLarge
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func (c *Client) getJSON(ctx context.Context, p string, q url.Values, dst any) error {
	u := *c.base
	u.Path = path.Join(c.base.Path, p)
	u.RawQuery = q.Encode()
	resp, err := c.do(ctx, &u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(dst); err != nil {
		return fmt.Errorf("confluence: decoding %s: %w", p, err)
	}
	return nil
}

// do performs a GET with retries on 429/5xx, honouring Retry-After.
func (c *Client) do(ctx context.Context, u *url.URL) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			if errors.Is(err, ErrForeignHost) {
				return nil, ErrForeignHost
			}
			if ctx.Err() != nil || attempt >= c.maxRetries {
				return nil, fmt.Errorf("confluence: request %s: %w", u.Path, err)
			}
			if err := c.sleep(ctx, backoff(attempt, "")); err != nil {
				return nil, err
			}
			continue
		}
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return resp, nil
		case resp.StatusCode == http.StatusUnauthorized:
			drain(resp)
			return nil, ErrUnauthorized
		case resp.StatusCode == http.StatusForbidden:
			drain(resp)
			return nil, ErrForbidden
		case resp.StatusCode == http.StatusNotFound:
			drain(resp)
			return nil, ErrNotFound
		case (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt < c.maxRetries:
			wait := backoff(attempt, resp.Header.Get("Retry-After"))
			drain(resp)
			if err := c.sleep(ctx, wait); err != nil {
				return nil, err
			}
		default:
			drain(resp)
			return nil, &StatusError{Code: resp.StatusCode, Path: u.Path}
		}
	}
}

func (c *Client) sameOrigin(u *url.URL) bool {
	return strings.EqualFold(u.Scheme, c.base.Scheme) && strings.EqualFold(u.Host, c.base.Host)
}

func backoff(attempt int, retryAfter string) time.Duration {
	if s, err := strconv.Atoi(retryAfter); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, time.Minute)
	}
	return time.Duration(1<<attempt) * 500 * time.Millisecond
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

func cqlQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

func isNumeric(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
