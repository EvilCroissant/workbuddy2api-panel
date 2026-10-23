package session

import (
	"container/list"
	"crypto/sha256"
	"sync"
	"time"
)

const (
	requestIDCapacity = 16384
	requestIDIdleTTL  = 30 * time.Minute
)

var requestIDs = newRequestIDCache(requestIDCapacity, requestIDIdleTTL)

type requestIDEntry struct {
	key        [32]byte
	id         string
	lastActive time.Time
}

type requestIDCache struct {
	mu       sync.Mutex
	entries  map[[32]byte]*list.Element
	lru      list.List
	capacity int
	ttl      time.Duration
}

func newRequestIDCache(capacity int, ttl time.Duration) *requestIDCache {
	return &requestIDCache{entries: make(map[[32]byte]*list.Element), capacity: capacity, ttl: ttl}
}

func (c *requestIDCache) get(scope, key string, now time.Time) string {
	if key == "" {
		return newUUIDv7(now)
	}
	// 只保留摘要，避免缓存请求正文或随输入长度放大内存。
	sum := sha256.Sum256([]byte(scope + "\x00" + key))
	c.mu.Lock()
	defer c.mu.Unlock()
	// 并发调用可能在获取锁前暂停，保持 LRU 时间顺序供空闲过期扫描使用。
	if e := c.lru.Front(); e != nil && now.Before(e.Value.(*requestIDEntry).lastActive) {
		now = e.Value.(*requestIDEntry).lastActive
	}
	for e := c.lru.Back(); e != nil; e = c.lru.Back() {
		if now.Sub(e.Value.(*requestIDEntry).lastActive) < c.ttl {
			break
		}
		c.remove(e)
	}
	if e := c.entries[sum]; e != nil {
		entry := e.Value.(*requestIDEntry)
		entry.lastActive = now
		c.lru.MoveToFront(e)
		return entry.id
	}
	id := newUUIDv7(now)
	if c.capacity <= 0 {
		return id
	}
	for c.lru.Len() >= c.capacity {
		c.remove(c.lru.Back())
	}
	c.entries[sum] = c.lru.PushFront(&requestIDEntry{key: sum, id: id, lastActive: now})
	return id
}

func (c *requestIDCache) remove(e *list.Element) {
	delete(c.entries, e.Value.(*requestIDEntry).key)
	c.lru.Remove(e)
}
