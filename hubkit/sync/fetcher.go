package sync

import (
	"context"

	"git.stit.tech/stit-core/golibinfra/hubkit/client"
)

// HTTPFetcher — Fetcher поверх hubkit/client: маппит SyncFeed на Page.
type HTTPFetcher struct {
	c *client.Client
}

// NewHTTPFetcher строит Fetcher поверх клиента hub API.
func NewHTTPFetcher(c *client.Client) *HTTPFetcher {
	return &HTTPFetcher{c: c}
}

// Fetch — страница фида ресурса после курсора after (см. client.SyncFeed).
func (f *HTTPFetcher) Fetch(ctx context.Context, resource string, after int64, limit int) (Page, error) {
	page, err := f.c.SyncFeed(ctx, resource, after, limit)
	if err != nil {
		return Page{}, err
	}
	return Page{Items: page.Data, NextAfter: page.NextAfter}, nil
}
