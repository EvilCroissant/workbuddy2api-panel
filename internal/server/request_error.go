package server

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/logfmt"
	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
)

const maxRequestErrorMessage = 4096

var requestErrorCredential = regexp.MustCompile(`(?i)\bBearer\s+[a-z0-9._~+/=-]+|\beyJ[a-z0-9_-]+\.[a-z0-9_-]+\.[a-z0-9_-]+`)

// captureRequestError 旁路记录最终返回给调用方的错误，不缓存正常响应或 SSE 正文。
func captureRequestError(w http.ResponseWriter, code, message, hint string) {
	for depth := 0; depth < 8 && w != nil; depth++ {
		if observer, ok := w.(*responseObserver); ok {
			observer.failure = requestErrorDetail(code, message, hint)
			return
		}
		wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = wrapper.Unwrap()
	}
}

// requestErrorDetail 解开 error.message 内嵌的上游 JSON，只提取已知错误字段。
// 兼容 code/msg、error.message、error.data.code 和数值/字符串业务码。
func requestErrorDetail(code, message, hint string) *reqlog.ErrorDetail {
	detail := &reqlog.ErrorDetail{Code: code, Message: message, Hint: hint}
	var parse func(string, int)
	parse = func(raw string, depth int) {
		if depth >= 6 {
			return
		}
		raw = strings.TrimSpace(raw)
		if len(raw) > 64<<10 {
			detail.Truncated = true
			if strings.HasPrefix(raw, "{") {
				detail.Message = "上游错误响应过长，未保存正文"
			}
			return
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &fields) != nil || fields == nil {
			return
		}
		if depth == 0 || strings.TrimSpace(detail.Message) == raw {
			detail.Message = "上游未提供错误说明"
		}
		if value := errorField(fields, "code"); value != "" {
			detail.UpstreamCode = value
		}
		if value := errorField(fields, "requestId", "request_id", "requestID"); value != "" {
			detail.UpstreamRequestID = value
		}
		if detail.Hint == "" {
			detail.Hint = errorField(fields, "gateway_hint")
		}
		if value := errorField(fields, "message", "msg", "error_description"); value != "" {
			detail.Message = value
			parse(value, depth+1)
		}
		for _, key := range []string{"error", "data"} {
			if nested := fields[key]; len(nested) > 0 {
				parse(string(nested), depth+1)
			}
		}
	}
	parse(message, 0)
	for _, field := range []struct {
		value *string
		limit int
	}{
		{&detail.Code, 128}, {&detail.Message, maxRequestErrorMessage},
		{&detail.Hint, 2048}, {&detail.UpstreamCode, 128}, {&detail.UpstreamRequestID, 256},
	} {
		trimmed := requestErrorCredential.ReplaceAllString(strings.TrimSpace(*field.value), "[REDACTED]")
		*field.value = logfmt.Truncate(trimmed, field.limit)
		if len(*field.value) < len(trimmed) {
			detail.Truncated = true
		}
	}
	return detail
}

func errorField(fields map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		raw := fields[key]
		var text string
		if json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
		var number json.Number
		if json.Unmarshal(raw, &number) == nil && number.String() != "" {
			return number.String()
		}
	}
	return ""
}
