package inventory

import (
	"os/user"
	"strconv"
	"sync"
)

// UserCache resolves UIDs to names via os/user and caches the results,
// including failures (rendered as the numeric UID).
type UserCache struct {
	mu    sync.Mutex
	names map[int]string
}

// NewUserCache returns an empty cache.
func NewUserCache() *UserCache {
	return &UserCache{names: make(map[int]string)}
}

// Name implements UserResolver.
func (c *UserCache) Name(uid int) string {
	if uid < 0 {
		return "?"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if n, ok := c.names[uid]; ok {
		return n
	}
	n := strconv.Itoa(uid)
	if u, err := user.LookupId(n); err == nil {
		n = u.Username
	}
	c.names[uid] = n
	return n
}
