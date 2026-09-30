package gtfs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

type staticFeedArtifact struct {
	url  string
	path string
	hash string
	size int64
}

// staticArtifactCache keeps compressed feed artifacts off the Go heap while a
// campaign retries other feeds. Its lifetime is exactly one campaign.
type staticArtifactCache struct {
	dir       string
	artifacts map[string]staticFeedArtifact
	totalSize int64
	maxSize   int64
}

func newStaticArtifactCache(maxSize int64) (*staticArtifactCache, error) {
	dir, err := os.MkdirTemp("", "watchdog-static-refresh-")
	if err != nil {
		return nil, err
	}
	return &staticArtifactCache{dir: dir, artifacts: make(map[string]staticFeedArtifact), maxSize: maxSize}, nil
}

func (c *staticArtifactCache) Put(feedURL string, data []byte) (staticFeedArtifact, error) {
	previous := c.artifacts[feedURL]
	newTotal := c.totalSize - previous.size + int64(len(data))
	if c.maxSize > 0 && newTotal > c.maxSize {
		return staticFeedArtifact{}, fmt.Errorf("static refresh cache limit exceeded")
	}
	sum := sha256.Sum256(data)
	urlSum := sha256.Sum256([]byte(feedURL))
	name := hex.EncodeToString(urlSum[:8]) + "-" + hex.EncodeToString(sum[:]) + ".zip"
	path := filepath.Join(c.dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return staticFeedArtifact{}, err
	}
	if previous.path != "" && previous.path != path {
		_ = os.Remove(previous.path)
	}
	artifact := staticFeedArtifact{url: feedURL, path: path, hash: hex.EncodeToString(sum[:]), size: int64(len(data))}
	c.artifacts[feedURL] = artifact
	c.totalSize = newTotal
	return artifact, nil
}

func (c *staticArtifactCache) Get(feedURL string) (staticFeedArtifact, bool) {
	artifact, ok := c.artifacts[feedURL]
	return artifact, ok
}

func (c *staticArtifactCache) Delete(feedURL string) {
	artifact, ok := c.artifacts[feedURL]
	if !ok {
		return
	}
	delete(c.artifacts, feedURL)
	c.totalSize -= artifact.size
	_ = os.Remove(artifact.path)
}

func (c *staticArtifactCache) Read(feedURL string) ([]byte, error) {
	artifact, ok := c.artifacts[feedURL]
	if !ok {
		return nil, fmt.Errorf("static feed %q is not cached", feedURL)
	}
	return os.ReadFile(artifact.path)
}

func (c *staticArtifactCache) Close() error {
	return os.RemoveAll(c.dir)
}
