package upstream

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/session"
)

// TestChatHeadersAlwaysSendConversationTrio 回归 issue #190：国际端点收紧通道
// 校验后，缺 X-Conversation-ID 会报 conversation id missing（11-128）。
// 会话 ID 使用稳定 UUIDv4 格式，轮次与消息 ID 使用去横线 UUIDv7。
func TestChatHeadersAlwaysSendConversationTrio(t *testing.T) {
	c := &Client{}
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.ChatHeaders(req, testAuth(), "", ChatMeta{})

	uuidV4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for _, h := range []string{
		"X-Conversation-ID",
		"X-Conversation-Request-ID",
		"X-Conversation-Message-ID",
	} {
		v := req.Header.Get(h)
		if v == "" {
			t.Fatalf("%s missing (通道校验会拒绝缺少会话三元组的请求)", h)
		}
		if h == "X-Conversation-ID" {
			if !uuidV4.MatchString(v) {
				t.Errorf("%s = %q, want UUIDv4 format", h, v)
			}
		} else if _, ok := session.CompactUUIDv7(v); !ok {
			t.Errorf("%s = %q, want compact UUIDv7", h, v)
		}
	}

	// 兜底会话 ID 每次请求新生成（不是常量），且与 request/message ID 不冲突。
	req2, _ := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", nil)
	c.ChatHeaders(req2, testAuth(), "", ChatMeta{})
	if req.Header.Get("X-Conversation-ID") == req2.Header.Get("X-Conversation-ID") {
		t.Error("fallback X-Conversation-ID should be fresh per request")
	}
}

// 本地会话标识稳定映射到同一上游会话，覆盖零值兜底和显式传入的边界。
func TestChatHeadersMapsConversationID(t *testing.T) {
	c := &Client{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", nil)
	const conv = "0123456789abcdef0123456789abcdef"
	c.ChatHeaders(req, testAuth(), "", ChatMeta{ConversationID: conv})
	want := session.UUIDv4For("conversation:" + conv)
	if got := req.Header.Get("X-Conversation-ID"); got != want {
		t.Errorf("X-Conversation-ID = %q, want stable mapped %q", got, want)
	}
}

func TestChatHeadersReusesCapturedModelCall(t *testing.T) {
	meta := (ChatMeta{ConversationID: "dialog", ConversationRequestID: "turn"}).withMessageIDs()
	var first http.Header
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", nil)
		(&Client{}).ChatHeaders(req, testAuth(), "", meta)
		if i == 0 {
			first = req.Header.Clone()
			continue
		}
		for _, key := range []string{"X-Conversation-ID", "X-Conversation-Request-ID", "X-Conversation-Message-ID", "X-Request-ID", "X-B3-SpanId"} {
			if req.Header.Get(key) != first.Get(key) {
				t.Errorf("captured %s changed across HTTP attempts", key)
			}
		}
	}
}

func TestChatHeadersNormalizesTurnIDWithinConversation(t *testing.T) {
	valid := session.NewMessageID()
	hyphenated := valid[:8] + "-" + valid[8:12] + "-" + valid[12:16] + "-" + valid[16:20] + "-" + valid[20:]
	for _, candidate := range []string{valid, hyphenated} {
		if got := normalizeConversationRequestID(candidate, "conv"); got != valid {
			t.Errorf("valid UUIDv7 changed: %q", got)
		}
	}
	a := normalizeConversationRequestID("legacy-turn", "conv-a")
	b := normalizeConversationRequestID("legacy-turn", "conv-b")
	if a == b || a != normalizeConversationRequestID("legacy-turn", "conv-a") {
		t.Fatal("legacy turn mapping must be stable and scoped to its conversation")
	}
	for _, value := range []string{a, b, normalizeConversationRequestID("0123456789abcdef0123456789abcdef", "conv")} {
		if _, ok := session.CompactUUIDv7(value); !ok {
			t.Errorf("normalized turn ID is not UUIDv7: %q", value)
		}
	}
}

// testAuth 返回最小的 CN 账号（ChatHeaders 会读 UID/凭证，nil 会解引用）。
func testAuth() *auth.Auth {
	return &auth.Auth{UID: "u1", AccessToken: "at"}
}
