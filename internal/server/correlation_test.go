package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/session"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func TestChatCorrelationAcrossToolStepsAndTurns(t *testing.T) {
	var headers []http.Header
	up := &upstream.Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		headers = append(headers, r.Header.Clone())
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sseOK))}, nil
	})}}
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, MaxRotate: 1})
	for _, tc := range []struct{ conversation, messages string }{
		{"dialog-a", `[{"role":"user","content":"hello"}]`},
		{"dialog-a", `[{"role":"user","content":"hello"},{"role":"assistant","content":"tool call"},{"role":"tool","content":"tool output"}]`},
		{"dialog-a", `[{"role":"user","content":"hello"},{"role":"assistant","content":"answer"},{"role":"user","content":"next"}]`},
		{"dialog-b", `[{"role":"user","content":"hello"}]`},
	} {
		body := fmt.Sprintf(`{"model":"glm-5.2","conversation_id":%q,"messages":%s}`, tc.conversation, tc.messages)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	if len(headers) != 4 {
		t.Fatalf("upstream requests=%d want 4", len(headers))
	}
	for i, header := range headers {
		for _, key := range []string{"X-Conversation-Request-ID", "X-Conversation-Message-ID"} {
			if _, ok := session.CompactUUIDv7(header.Get(key)); !ok {
				t.Errorf("request %d: %s is not UUIDv7", i, key)
			}
		}
		if header.Get("X-Request-ID") != header.Get("X-Conversation-Message-ID") {
			t.Errorf("request %d: message and request IDs differ", i)
		}
	}
	conv, turn, message := "X-Conversation-ID", "X-Conversation-Request-ID", "X-Conversation-Message-ID"
	if headers[0].Get(conv) != headers[1].Get(conv) || headers[0].Get(turn) != headers[1].Get(turn) {
		t.Fatal("tool continuation split the conversation or turn")
	}
	if headers[1].Get(message) == headers[0].Get(message) {
		t.Fatal("tool continuation reused the prior model call's message ID")
	}
	if headers[2].Get(conv) != headers[0].Get(conv) || headers[2].Get(turn) == headers[0].Get(turn) {
		t.Fatal("new user input must keep conversation and change turn")
	}
	if headers[3].Get(conv) == headers[0].Get(conv) || headers[3].Get(turn) == headers[0].Get(turn) {
		t.Fatal("identical input in different conversations shared correlation IDs")
	}
}
