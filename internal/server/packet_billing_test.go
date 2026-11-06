package server

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestQuotaEstimateCountsReasoningAndToolsAcrossChunks(t *testing.T) {
	read := func(chunks []string) int64 {
		t.Helper()
		var frames strings.Builder
		for _, delta := range chunks {
			fmt.Fprintf(&frames, "data: {\"choices\":[{\"delta\":%s}]}\n\n", delta)
		}
		stats := newChatStatsReaderSince(strings.NewReader(frames.String()), time.Now())
		stats.quotaOutput = &quotaTextEstimate{}
		if _, err := io.Copy(io.Discard, stats); err != nil {
			t.Fatal(err)
		}
		if !stats.sawOutput {
			t.Fatal("generated output not detected")
		}
		return stats.quotaOutput.tokens()
	}
	whole := read([]string{`{"content":"abcd你好","reasoning_content":"abcdefgh","tool_calls":[{"function":{"name":"test","arguments":"abcd"}}]}`})
	split := read([]string{`{"content":"ab"}`, `{"content":"cd你"}`, `{"content":"好","reasoning_content":"a"}`, `{"reasoning_content":"bcdefgh"}`, `{"tool_calls":[{"function":{"name":"test","arguments":"a"}}]}`, `{"tool_calls":[{"function":{"arguments":"bcd"}}]}`})
	if whole != 7 || split != whole {
		t.Fatalf("output estimate depends on stream boundaries or missed fields: whole=%d split=%d", whole, split)
	}
}

func TestQuotaEstimateDoesNotCountEncodedMediaAsText(t *testing.T) {
	plain := estimateQuotaPrompt([]byte(`{"messages":[{"role":"user","content":"你好test"}]}`))
	withMedia := func(size int) int64 {
		return estimateQuotaPrompt([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"你好test"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + strings.Repeat("A", size) + `"}}]}]}`))
	}
	if plain <= 0 || withMedia(4) <= plain || withMedia(4) != withMedia(100000) {
		t.Fatal("encoded image length inflated token quota estimate")
	}
	withTools := estimateQuotaPrompt([]byte(`{"messages":[{"role":"user","content":"你好test"}],"tools":[{"type":"function","function":{"name":"search","description":"find documents","parameters":{"type":"object"}}}]}`))
	if withTools <= plain {
		t.Fatal("tool definitions missing from estimated input")
	}
}

func TestQuotaOptionalOutputShapeDoesNotLoseMeasuredUsage(t *testing.T) {
	stats := newChatStatsReaderSince(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}],\"usage\":{\"total_tokens\":100,\"credit\":0.25}}\n\n"), time.Now())
	stats.quotaOutput = &quotaTextEstimate{}
	if _, err := io.Copy(io.Discard, stats); err != nil {
		t.Fatal(err)
	}
	if credit, ok := stats.Credit(); !ok || credit != 0.25 || !stats.Usage().HasTotalTokens || stats.Usage().TotalTokens != 100 {
		t.Fatal("optional output shape prevented measured billing")
	}
}
