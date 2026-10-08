package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// Teams posts notifications to a Microsoft Teams workflow ("When a Teams
// webhook request is received" / "Post card in a chat or channel"), whose URL
// each user provides.
//
// The URL comes from the user, and the server makes a request to it: that is
// an SSRF waiting to happen unless the destination is pinned. Only HTTPS URLs
// on the allowed host suffixes are accepted — checked when the URL is saved
// and again before every request — and redirects are not followed.
type Teams struct {
	allowed []string
	client  *http.Client
}

// DefaultTeamsHosts are the domains Teams workflows and incoming webhooks are
// served from.
var DefaultTeamsHosts = []string{"logic.azure.com", "powerplatform.com", "webhook.office.com"}

// NewTeams returns a sender accepting URLs on the allowed host suffixes.
func NewTeams(allowed []string, transport http.RoundTripper) *Teams {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Teams{allowed: allowed, client: &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // a workflow URL never redirects
		},
	}}
}

// Validate checks that raw is an HTTPS URL on an allowed host.
func (t *Teams) Validate(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || (u.Port() != "" && u.Port() != "443") {
		return ErrInvalidTeamsURL
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range t.allowed {
		suffix = strings.ToLower(strings.TrimPrefix(suffix, "."))
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return nil
		}
	}
	return ErrInvalidTeamsURL
}

// Send posts n as an Adaptive Card. A refusal by the workflow (4xx other than
// 429) is permanent; anything else may succeed later.
func (t *Teams) Send(ctx context.Context, rawURL string, n domain.Notification) error {
	if err := t.Validate(rawURL); err != nil {
		return Permanent(err)
	}
	return t.sendTo(ctx, rawURL, n)
}

// sendTo posts without validating the URL; Send validates first.
func (t *Teams) sendTo(ctx context.Context, rawURL string, n domain.Notification) error {
	payload, err := json.Marshal(adaptiveCard(n))
	if err != nil {
		return Permanent(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(payload))
	if err != nil {
		return Permanent(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("teams: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("teams: workflow answered %d", resp.StatusCode)
	default:
		return Permanent(fmt.Errorf("teams: workflow refused the message (%d)", resp.StatusCode))
	}
}

// adaptiveCard is the message format Teams workflows post as is.
func adaptiveCard(n domain.Notification) map[string]any {
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body": []map[string]any{
			{"type": "TextBlock", "text": n.Title, "weight": "Bolder", "size": "Medium", "wrap": true},
			{"type": "TextBlock", "text": n.Body, "wrap": true},
		},
	}
	if n.Link != "" {
		card["actions"] = []map[string]any{{"type": "Action.OpenUrl", "title": "Open Confluence Export", "url": n.Link}}
	}
	return map[string]any{
		"type": "message",
		"attachments": []map[string]any{{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"content":     card,
		}},
	}
}
