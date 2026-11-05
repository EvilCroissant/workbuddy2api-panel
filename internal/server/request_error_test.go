package server

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
)

func TestRequestErrorExtractsOnlyDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, body, message, code, id string }{
		{"business error", `{"code":11101,"msg":"Unmarshal chat params failed","requestId":"up-1","accessToken":"secret","messages":[{"content":"private prompt"}]}`, "Unmarshal chat params failed", "11101", "up-1"},
		{"nested error", `{"error":{"message":"The model provider is rate-limiting requests.","data":{"code":"11140","request_id":"up-2"}}}`, "The model provider is rate-limiting requests.", "11140", "up-2"},
		{"plain text", "upstream timed out", "upstream timed out", "", ""},
		{"missing message", `{"code":12153,"accessToken":"secret"}`, "上游未提供错误说明", "12153", ""},
		{"nested unknown fields", `{"message":"{\"accessToken\":\"secret\"}"}`, "上游未提供错误说明", "", ""},
		{"credential in message", `{"msg":"invalid Bearer secret"}`, "invalid [REDACTED]", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := requestErrorDetail("gateway_code", tc.body, "处理提示")
			if detail.Message != tc.message || detail.UpstreamCode != tc.code || detail.UpstreamRequestID != tc.id || detail.Code != "gateway_code" || detail.Hint != "处理提示" {
				t.Fatalf("detail=%+v", detail)
			}
			raw, _ := json.Marshal(detail)
			if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "private prompt") {
				t.Fatalf("unexpected response fields archived: %s", raw)
			}
		})
	}
	long := requestErrorDetail("error", strings.Repeat("中", 3000), "")
	if !long.Truncated || len(long.Message) > maxRequestErrorMessage || !utf8.ValidString(long.Message) {
		t.Fatalf("unbounded or invalid UTF-8 error: %d bytes", len(long.Message))
	}
}

func TestRequestFailureDetailsReachMemoryAndArchive(t *testing.T) {
	for _, tc := range []struct {
		name, upstreamBody, wantCode, wantMessage string
		status                                    int
		stream                                    bool
	}{
		{"HTTP parameter error", `{"code":11101,"msg":"Unmarshal chat params failed: invalid tools","requestId":"upstream-400"}`, "11101", "Unmarshal chat params failed: invalid tools", 400, false},
		{"stream error", "data: {\"error\":{\"code\":6004,\"message\":\"rate limit reached\",\"requestId\":\"upstream-stream\"}}\n\ndata: [DONE]\n\n", "6004", "rate limit reached", 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := reqlog.New(reqlog.Config{Enabled: true, Dir: t.TempDir()})
			defer logs.Close()
			up := newFakeUpstream(t, func(string) (int, string, bool) { return tc.status, tc.upstreamBody, tc.stream })
			h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}), Upstream: up, RequestLog: logs})
			body := `{"model":"glm-5.2","messages":[],"stream":` + strconv.FormatBool(tc.stream) + `}`
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
			rows := logs.Snapshot().Recent
			if len(rows) != 1 || rows[0].OK || rows[0].Error == nil {
				t.Fatalf("missing failure detail: %+v body=%s", rows, w.Body.String())
			}
			detail := rows[0].Error
			if detail.UpstreamCode != tc.wantCode || detail.Message != tc.wantMessage || detail.UpstreamRequestID == "" {
				t.Fatalf("detail=%+v", detail)
			}
			if rows[0].RequestID != w.Header().Get("X-Request-Id") || rows[0].Attempts != 1 {
				t.Fatalf("trace identity lost: %+v", rows[0])
			}
			logs.Close()
			archived, err := logs.ReadArchive(10, reqlog.Filter{})
			if err != nil || len(archived) != 1 || archived[0].Error == nil || *archived[0].Error != *detail {
				t.Fatalf("archive lost error details: %+v err=%v", archived, err)
			}
		})
	}
}

func TestRequestSuccessDoesNotArchivePreviousAttemptError(t *testing.T) {
	logs := reqlog.New(reqlog.Config{})
	defer logs.Close()
	calls := 0
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		if calls == 1 {
			return 402, `{"code":14018,"msg":"余额不足"}`, false
		}
		return 200, sseOK, true
	})
	h := NewHandler(Config{RequestLog: logs, Upstream: up, Pool: testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
	)})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	rows := logs.Snapshot().Recent
	if w.Code != 200 || len(rows) != 1 || !rows[0].OK || rows[0].Error != nil || rows[0].Attempts != 2 {
		t.Fatalf("successful retry misreported: rows=%+v body=%s", rows, w.Body.String())
	}
}
