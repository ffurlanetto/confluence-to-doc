//go:build live

// Compatibility check against a real Confluence Data Center, read-only:
//
//	CONFLUENCE_LIVE_URL=https://confluence.example.com CONFLUENCE_LIVE_PAT=... \
//	CONFLUENCE_LIVE_PAGE=123456 make confluence-check
//
// It calls every endpoint the application uses, with the same client, and
// reports what the instance answered. See docs/operations/confluence-compatibility.md.
package confluence_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
)

func liveClient(t *testing.T) (*confluence.Client, string) {
	t.Helper()
	raw, pat, page := os.Getenv("CONFLUENCE_LIVE_URL"), os.Getenv("CONFLUENCE_LIVE_PAT"), os.Getenv("CONFLUENCE_LIVE_PAGE")
	if raw == "" || pat == "" || page == "" {
		t.Skip("set CONFLUENCE_LIVE_URL, CONFLUENCE_LIVE_PAT and CONFLUENCE_LIVE_PAGE")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("CONFLUENCE_LIVE_URL: %v", err)
	}
	return confluence.NewClient(u, pat, nil), page
}

func TestLiveConfluence(t *testing.T) {
	c, pageID := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	user, err := c.CurrentUser(ctx)
	if err != nil {
		t.Fatalf("GET /rest/api/user/current (token check): %v", err)
	}
	t.Logf("signed in as %q", user.DisplayName)

	if expiry, err := c.TokenExpiry(ctx); err != nil {
		t.Logf("GET /rest/pat/latest/tokens/{id} (token expiry, optional): %v", err)
	} else {
		t.Logf("token expiry: %v", expiry)
	}

	page, err := c.GetPage(ctx, pageID, true)
	if err != nil {
		t.Fatalf("GET /rest/api/content/{id}?expand=body.export_view: %v", err)
	}
	if strings.TrimSpace(page.BodyHTML) == "" {
		t.Errorf("page %s came back without its export_view body", pageID)
	}
	t.Logf("page %q in space %s, %d bytes of HTML", page.Title, page.SpaceKey, len(page.BodyHTML))

	children, err := c.ListChildren(ctx, pageID, true)
	if err != nil {
		t.Fatalf("GET /rest/api/content/{id}/child/page: %v", err)
	}
	t.Logf("%d child pages", len(children))

	words := strings.Fields(page.Title)
	if len(words) > 0 {
		results, err := c.SearchPages(ctx, words[0], "", 5)
		if err != nil {
			t.Fatalf("GET /rest/api/content/search (CQL): %v", err)
		}
		t.Logf("search %q: %d results", words[0], len(results))
	}

	if found, err := c.FindPageByTitle(ctx, page.SpaceKey, page.Title); err != nil || found.ID != pageID {
		t.Errorf("GET /rest/api/content?spaceKey&title: %v (found %+v)", err, found)
	}

	if i := strings.Index(page.BodyHTML, "/download/attachments/"); i >= 0 {
		end := strings.IndexAny(page.BodyHTML[i:], `"' `)
		if end > 0 {
			src := page.BodyHTML[i : i+end]
			data, ctype, err := c.Download(ctx, src, 20<<20)
			if err != nil {
				t.Errorf("downloading the attachment %s: %v", src, err)
			} else {
				t.Logf("attachment %s: %d bytes of %s", src, len(data), ctype)
			}
		}
	}
}
