package upstream

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/session"
)

func TestChatStreamHTTPReplayPreservesCorrelation(t *testing.T) {
	var captured []http.Header
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = append(captured, r.Header.Clone())
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		if r.URL.Path != "/replay" {
			w.Header().Set("Location", "/replay")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	rc, status, _, err := c.ChatStream(testAuth(), []byte(`{"model":"glm-5.2","messages":[]}`), "", ChatMeta{})
	if err != nil || status != http.StatusOK {
		t.Fatalf("status=%d err=%v", status, err)
	}
	rc.Close()
	if len(captured) != 2 || bodies[0] != bodies[1] {
		t.Fatalf("expected one HTTP replay of identical body, got %d requests", len(captured))
	}
	for _, key := range []string{"X-Conversation-ID", "X-Conversation-Request-ID", "X-Conversation-Message-ID", "X-Request-ID", "X-B3-SpanId"} {
		if captured[0].Get(key) == "" || captured[0].Get(key) != captured[1].Get(key) {
			t.Errorf("%s changed during HTTP replay", key)
		}
	}
}

func TestChatStreamModelCallsRefreshMessageAndSpan(t *testing.T) {
	var captured []http.Header
	c := testClient(func(r *http.Request) (*http.Response, error) {
		captured = append(captured, r.Header.Clone())
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n"))}, nil
	})
	meta := ChatMeta{ConversationID: "dialog", ConversationRequestID: session.TurnRequestID("dialog:turn")}
	for i := 0; i < 2; i++ {
		rc, _, _, err := c.ChatStream(testAuth(), []byte(`{"model":"glm-5.2","messages":[]}`), "", meta)
		if err != nil {
			t.Fatal(err)
		}
		rc.Close()
	}
	for _, key := range []string{"X-Conversation-ID", "X-Conversation-Request-ID"} {
		if captured[0].Get(key) != captured[1].Get(key) {
			t.Errorf("same-turn model calls changed %s", key)
		}
	}
	for _, key := range []string{"X-Conversation-Message-ID", "X-Request-ID", "X-B3-SpanId"} {
		if captured[0].Get(key) == captured[1].Get(key) {
			t.Errorf("new model call reused %s", key)
		}
	}
}
