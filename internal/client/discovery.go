package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"time"
)

type discoveryCache struct {
	ETag      string          `json:"etag,omitempty"`
	ExpiresAt time.Time       `json:"expires_at"`
	Body      json.RawMessage `json:"body"`
}

// Discovery caches only non-sensitive capability metadata, scoped to the
// exact cloud URL and access identity. Expired cache entries are revalidated
// with ETag when supported; failures never serve stale capability claims.
func (c *Client) Discovery(ctx context.Context, path string, out any, fresh bool) error {
	dir, err := StateDir()
	if err != nil {
		return err
	}
	key := sha256.Sum256([]byte(c.BaseURL + "\n" + Identity(c.Token) + "\n" + path))
	cachePath := filepath.Join(dir, "discovery", hex.EncodeToString(key[:])+".json")
	var cached discoveryCache
	if b, err := ReadPrivate(cachePath); err == nil && !fresh {
		_ = json.Unmarshal(b, &cached)
	}
	if len(cached.Body) > 0 && time.Now().Before(cached.ExpiresAt) {
		return json.Unmarshal(cached.Body, out)
	}
	headers := map[string]string{}
	if cached.ETag != "" && len(cached.Body) > 0 {
		headers["If-None-Match"] = cached.ETag
	}
	resp, err := c.request(ctx, "GET", path, nil, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		if len(cached.Body) == 0 {
			return errors.New("cloud returned 304 without a cached discovery response")
		}
	} else {
		b, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if err != nil {
			return err
		}
		if len(b) > 1<<20 {
			return errors.New("discovery response exceeds 1 MiB limit")
		}
		if !json.Valid(b) {
			return errors.New("invalid cloud discovery JSON")
		}
		cached.Body = b
		cached.ETag = resp.Header.Get("ETag")
	}
	if err = json.Unmarshal(cached.Body, out); err != nil {
		return err
	}
	cached.ExpiresAt = time.Now().UTC().Add(5 * time.Minute)
	// Cache writes are optional; a read-only client can still discover and run.
	_ = WritePrivate(cachePath, cached)
	return nil
}
