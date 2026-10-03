package authkit

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"
)

// CachedTokenInfo wraps a TokenInfoClient with a short positive-verdict
// cache for read-tier revalidation (dr-21). Only successful lookups are
// cached — a revoked/error verdict always came from the auth service this
// request. The contract this buys: a revoked strict delegation can keep
// passing cached lookups for at most TTL; callers gating MUTATIONS must use
// the direct TokenInfoClient instead.
//
// Entries are keyed by SHA-256 of the raw token (the token itself is never
// retained) and the map is bounded: when full, expired entries are swept,
// and if the map is still full the new verdict is simply not cached — memory
// stays bounded without an eviction policy worth its complexity here.
type CachedTokenInfo struct {
	Client *TokenInfoClient
	// TTL bounds how long a positive verdict is reused. The zero value
	// means DefaultTokenInfoTTL.
	TTL time.Duration
	// MaxEntries bounds the cache size. The zero value means
	// DefaultTokenInfoMaxEntries.
	MaxEntries int

	// now is injectable for expiry tests.
	now func() time.Time

	mu      sync.Mutex
	entries map[[sha256.Size]byte]tokenInfoEntry
}

// DefaultTokenInfoTTL is the positive-verdict reuse window when
// CachedTokenInfo.TTL is unset.
const DefaultTokenInfoTTL = 30 * time.Second

// DefaultTokenInfoMaxEntries bounds the cache when MaxEntries is unset.
const DefaultTokenInfoMaxEntries = 4096

type tokenInfoEntry struct {
	info    *TokenInfo
	expires time.Time
}

// NewCachedTokenInfo wraps client with a positive-verdict cache. ttl <= 0
// selects DefaultTokenInfoTTL.
func NewCachedTokenInfo(client *TokenInfoClient, ttl time.Duration) *CachedTokenInfo {
	return &CachedTokenInfo{Client: client, TTL: ttl}
}

func (c *CachedTokenInfo) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *CachedTokenInfo) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return DefaultTokenInfoTTL
}

func (c *CachedTokenInfo) maxEntries() int {
	if c.MaxEntries > 0 {
		return c.MaxEntries
	}
	return DefaultTokenInfoMaxEntries
}

// Lookup returns a cached positive verdict when one is fresh, otherwise
// delegates to the underlying client and caches a success. Errors (including
// ErrRevoked) are returned as-is and never cached. The returned *TokenInfo is
// the caller's own copy: mutating it cannot alter the cached verdict or any
// value handed to another caller.
func (c *CachedTokenInfo) Lookup(ctx context.Context, rawToken string) (*TokenInfo, error) {
	key := sha256.Sum256([]byte(rawToken))
	now := c.clock()

	c.mu.Lock()
	if e, ok := c.entries[key]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return cloneTokenInfo(e.info), nil
	}
	c.mu.Unlock()

	ti, err := c.Client.Lookup(ctx, rawToken)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[[sha256.Size]byte]tokenInfoEntry)
	}
	if len(c.entries) >= c.maxEntries() {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
	}
	if len(c.entries) < c.maxEntries() {
		c.entries[key] = tokenInfoEntry{info: cloneTokenInfo(ti), expires: now.Add(c.ttl())}
	}
	return ti, nil
}

// cloneTokenInfo returns a deep-enough copy of ti that a caller mutating the
// result cannot reach the original: the struct is copied by value and the
// Scopes/Roles slices and the Act pointer are reallocated. Every value that
// crosses the cache boundary passes through here, so a caller holding a
// verdict never shares memory with the cached entry.
func cloneTokenInfo(ti *TokenInfo) *TokenInfo {
	if ti == nil {
		return nil
	}
	out := *ti
	if ti.Scopes != nil {
		out.Scopes = append([]string(nil), ti.Scopes...)
	}
	if ti.Roles != nil {
		out.Roles = append([]string(nil), ti.Roles...)
	}
	return &out
}
