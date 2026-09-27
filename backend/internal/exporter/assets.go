package exporter

import (
	"context"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
)

// ConfluenceAssets adapts a Confluence client to the Assets interface.
type ConfluenceAssets struct {
	Client        *confluence.Client
	MaxImageBytes int64
}

func (a ConfluenceAssets) ResolveURL(raw string) (string, error) {
	u, err := a.Client.ResolveURL(raw)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (a ConfluenceAssets) FetchImage(ctx context.Context, src string) ([]byte, string, error) {
	return a.Client.Download(ctx, src, a.MaxImageBytes)
}
