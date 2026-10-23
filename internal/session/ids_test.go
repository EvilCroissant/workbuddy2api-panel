package session

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var uuidV4RE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// imagePartSig 计算 part 原文摘要（sha256 前 8 hex），供期望值精确构造——
// 签名算法变更时测试期望值随此 helper 单点同步。
func imagePartSig(t *testing.T, part string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(part))
	return hex.EncodeToString(sum[:4])
}

// TestResolveConversationID 覆盖 conversationId 提取的 snake/camel/缺失三态：
//   - metadata.conversation_id / metadata.conversationId → 取值
//   - 顶层 conversation_id / conversationId → 取值（snake 优先同 ExtractKey）
//   - 缺失 / 只有 user_id → ""（会话头族语义只认对话 ID，绝不回落 user_id）
func TestResolveConversationID(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"metadata snake_case", `{"metadata":{"conversation_id":"conv-1"}}`, "conv-1"},
		{"metadata camelCase", `{"metadata":{"conversationId":"conv-2"}}`, "conv-2"},
		{"top-level snake_case", `{"conversation_id":"conv-3"}`, "conv-3"},
		{"top-level camelCase", `{"conversationId":"conv-4"}`, "conv-4"},
		{"snake wins over camel", `{"conversation_id":"conv-s","conversationId":"conv-c"}`, "conv-s"},
		{"missing", `{"model":"glm-5.2"}`, ""},
		{"empty body", ``, ""},
		{"broken json", `{broken`, ""},
		{"metadata user_id only", `{"metadata":{"user_id":"u1"}}`, ""},
		{"top-level user_id only", `{"user_id":"u1"}`, ""},
		{"empty string value", `{"conversationId":""}`, ""},
		{"non-string value", `{"conversationId":123}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ResolveConversationID([]byte(c.body)); got != c.want {
				t.Errorf("ResolveConversationID(%q) = %q want %q", c.body, got, c.want)
			}
		})
	}
}

// TestNewMessageIDFormat 32 位 hex 且不为空、两次调用大概率不同（随机性冒烟）。
func TestNewMessageIDFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		id := NewMessageID()
		if _, ok := CompactUUIDv7(id); !ok {
			t.Fatalf("NewMessageID() = %q, want UUIDv7", id)
		}
		if len(id) != 32 {
			t.Fatalf("NewMessageID() = %q len=%d want 32", id, len(id))
		}
		for _, ch := range id {
			if !strings.ContainsRune("0123456789abcdef", ch) {
				t.Fatalf("NewMessageID() = %q has non-hex char %q", id, ch)
			}
		}
	}
}

// TestUUIDv4Formats 覆盖国际版 chat 会话头所需的 UUID v4：随机值格式合法，
// 稳定派生值同输入同值、异输入异值，空输入保持随机。
func TestUUIDv4Formats(t *testing.T) {
	for _, id := range []string{NewUUID(), UUIDv4For("conversation:abc"), UUIDv4For("request:abc"), UUIDv4For("")} {
		if !uuidV4RE.MatchString(id) {
			t.Fatalf("UUID value %q is not RFC 4122 v4", id)
		}
	}
	if a, b := UUIDv4For("same"), UUIDv4For("same"); a != b {
		t.Fatalf("UUIDv4For same input drifted: %q vs %q", a, b)
	}
	if a, b := UUIDv4For("a"), UUIDv4For("b"); a == b {
		t.Fatalf("UUIDv4For different inputs collided: %q", a)
	}
	if a, b := UUIDv4For(""), UUIDv4For(""); a == b {
		t.Fatalf("UUIDv4For empty input should be fresh: %q vs %q", a, b)
	}
}

// TestRequestIDForKeyStability 同 key 恒稳定、异 key 各不同、空 key 每次新值。
func TestRequestIDForKeyStability(t *testing.T) {
	// 预热清空包级缓存，避免其他测试污染 key（测试隔离）。
	k1a := RequestIDForKey("conv-a")
	k1b := RequestIDForKey("conv-a")
	if k1a != k1b {
		t.Errorf("same key should be stable: %q vs %q", k1a, k1b)
	}
	k2 := RequestIDForKey("conv-b")
	if k1a == k2 {
		t.Errorf("different keys should differ: %q", k1a)
	}
	// 空 key：每次调用生成新值（无会话则无"会话内稳定"语义）。
	e1 := RequestIDForKey("")
	e2 := RequestIDForKey("")
	if e1 == e2 {
		t.Errorf("empty key should yield fresh values each call: %q", e1)
	}
	// 稳定值自身也须是 32 hex（可作 B3 TraceId 直接使用）。
	for _, id := range []string{k1a, k2, e1} {
		if len(id) != 32 {
			t.Errorf("RequestIDForKey value %q len=%d want 32", id, len(id))
		}
	}
}

// TestTurnKeyExtraction 轮级兜底键的提取：取**最后一条** user 消息的「序号+文本」，
// 轮内追加 assistant/tool 消息不改变键；无 user / 无文本 / 坏 JSON 一律空串。
func TestTurnKeyExtraction(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"single user", `{"messages":[{"role":"user","content":"你好"}]}`, "u0:你好"},
		{"last user wins", `{"messages":[{"role":"user","content":"第一问"},{"role":"assistant","content":"答"},{"role":"user","content":"第二问"}]}`, "u2:第二问"},
		// agent 多步：轮内追加 assistant/tool 消息，末条 user 位置与内容不变 → 同键。
		{"agent step keeps same key", `{"messages":[{"role":"user","content":"任务"},{"role":"assistant","tool_calls":[{"id":"c1"}]},{"role":"tool","content":"结果"}]}`, "u0:任务"},
		{"multimodal parts text joined", `{"messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:x"}}]}]}`, "u0:看图\n[image_url:" + imagePartSig(t, `{"type":"image_url","image_url":{"url":"data:x"}}`) + "]"},
		{"no user message", `{"messages":[{"role":"system","content":"sys"}]}`, ""},
		{"empty messages", `{"messages":[]}`, ""},
		{"messages key absent", `{"model":"glm-5.2"}`, ""},
		{"broken json", `{broken`, ""},
		{"empty body", ``, ""},
		{"empty content", `{"messages":[{"role":"user","content":""}]}`, ""},
		{"null content", `{"messages":[{"role":"user","content":null}]}`, ""},
		// 纯图片 content 按内容签名派生非空轮级键（G1 修复，原为 ""）；键含 image part 摘要。
		{"image only content", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`, "u0:[image_url:" + imagePartSig(t, `{"type":"image_url","image_url":{"url":"x"}}`) + "]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TurnKey([]byte(c.body)); got != c.want {
				t.Errorf("TurnKey(%q) = %q want %q", c.body, got, c.want)
			}
		})
	}
}

// 同一存活轮级映射稳定，异键独立，空键新建，输出为去横线 UUIDv7。
func TestTurnRequestIDDerivation(t *testing.T) {
	a1 := TurnRequestID("u0:同一个问题")
	a2 := TurnRequestID("u0:同一个问题")
	if a1 != a2 {
		t.Errorf("same turn key should derive same id: %q vs %q", a1, a2)
	}
	if b := TurnRequestID("u0:另一个问题"); b == a1 {
		t.Errorf("different turn keys should derive different ids: %q", b)
	}
	// 同文本但序号不同（不同轮里内容相同的提问）也要分开。
	if c := TurnRequestID("u2:同一个问题"); c == a1 {
		t.Errorf("same text at different position should differ: %q", c)
	}
	// 空键：无轮可聚合 → 每次新值（保持原有请求级独立行为）。
	if e1, e2 := TurnRequestID(""), TurnRequestID(""); e1 == e2 {
		t.Errorf("empty turn key should yield fresh values each call: %q", e1)
	}
	for _, id := range []string{a1, TurnRequestID(""), TurnRequestID("x")} {
		if _, ok := CompactUUIDv7(id); !ok {
			t.Errorf("TurnRequestID value %q want UUIDv7", id)
		}
	}
}

func TestUUIDv7TimestampAndValidation(t *testing.T) {
	now := time.UnixMilli(1792710000123)
	id := newUUIDv7(now)
	raw, err := hex.DecodeString(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint64(append([]byte{0, 0}, raw[:6]...)); got != uint64(now.UnixMilli()) {
		t.Fatalf("UUIDv7 timestamp=%d want %d", got, now.UnixMilli())
	}
	hyphenated := id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
	for _, value := range []string{id, hyphenated, strings.ToUpper(hyphenated)} {
		if got, ok := CompactUUIDv7(value); !ok || got != id {
			t.Errorf("CompactUUIDv7(%q)=%q,%v", value, got, ok)
		}
	}
	for _, value := range []string{"", NewUUID(), id[:16] + "0" + id[17:], id[:12] + "4" + id[13:], "-" + id, id[:31] + "z"} {
		if _, ok := CompactUUIDv7(value); ok {
			t.Errorf("invalid UUIDv7 accepted: %q", value)
		}
	}
}

func TestRequestIDCacheEvictionAndIdleExpiry(t *testing.T) {
	c := newRequestIDCache(2, time.Minute)
	now := time.Unix(1700000000, 0)
	a := c.get("turn", "a", now)
	b := c.get("turn", "b", now.Add(time.Second))
	if got := c.get("turn", "a", now.Add(2*time.Second)); got != a {
		t.Fatal("cache hit changed ID")
	}
	c.get("turn", "c", now.Add(3*time.Second))
	if got := c.get("turn", "a", now.Add(4*time.Second)); got != a {
		t.Fatal("recently used entry was evicted")
	}
	if got := c.get("turn", "b", now.Add(5*time.Second)); got == b {
		t.Fatal("least recently used entry was retained")
	}
	if len(c.entries) != 2 || c.lru.Len() != 2 {
		t.Fatal("cache capacity exceeded")
	}
	if got := c.get("turn", "a", now.Add(64*time.Second)); got == a {
		t.Fatal("entry survived its full idle TTL")
	}
	if len(c.entries) != 2 {
		t.Fatal("unexpired entries should survive another entry's expiry")
	}
	c.get("turn", "d", now.Add(3*time.Minute))
	if len(c.entries) != 1 || c.lru.Len() != 1 {
		t.Fatal("expired entries were not collected")
	}
}

func TestRequestIDCacheConcurrentScopes(t *testing.T) {
	c := newRequestIDCache(100, time.Minute)
	now := time.Now()
	ids := make(chan string, 64)
	var wg sync.WaitGroup
	for i := 0; i < cap(ids); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids <- c.get("turn", "same", now)
		}()
	}
	wg.Wait()
	close(ids)
	want := c.get("turn", "same", now)
	for id := range ids {
		if id != want {
			t.Fatal("concurrent creation split one turn into multiple IDs")
		}
	}
	if c.get("session", "same", now) == want {
		t.Fatal("session fallback and turn namespaces collided")
	}
	if c.get("turn", "", now) == c.get("turn", "", now) {
		t.Fatal("empty keys must not be cached")
	}
}
