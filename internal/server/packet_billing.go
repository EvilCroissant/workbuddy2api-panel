package server

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
)

// quotaTextEstimate is a bounded-memory fallback, not a model tokenizer. ASCII
// text uses four bytes per token and other characters one token per rune. Only
// quota accounting uses it; measured request logs and account costs stay intact.
type quotaTextEstimate struct {
	ascii, other int64
}

func (e *quotaTextEstimate) add(text string) {
	for _, r := range text {
		if r < 128 {
			e.ascii++
		} else {
			e.other++
		}
	}
}

func (e *quotaTextEstimate) tokens() int64 {
	if e == nil {
		return 0
	}
	return (e.ascii+3)/4 + e.other
}

// Estimate the submitted messages and tool definitions rather than the whole
// JSON body. Encoded image/audio bytes must never turn into millions of tokens.
func estimateQuotaPrompt(body []byte) int64 {
	var request struct {
		Messages []struct {
			Content   json.RawMessage `json:"content"`
			Name      string          `json:"name"`
			ToolCalls json.RawMessage `json:"tool_calls"`
		} `json:"messages"`
		Tools     json.RawMessage `json:"tools"`
		Functions json.RawMessage `json:"functions"`
	}
	if json.Unmarshal(body, &request) != nil {
		return 0
	}
	var text quotaTextEstimate
	overhead := int64(3)
	for _, message := range request.Messages {
		overhead += 4
		text.add(message.Name)
		var plain string
		if json.Unmarshal(message.Content, &plain) == nil {
			text.add(plain)
		} else {
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(message.Content, &parts) == nil {
				for _, part := range parts {
					switch part.Type {
					case "text", "input_text":
						text.add(part.Text)
					case "image_url", "input_image", "input_audio":
						overhead += 1024 // explicit rough media allowance
					}
				}
			}
		}
		if len(message.ToolCalls) > 0 && string(message.ToolCalls) != "null" {
			text.add(string(message.ToolCalls))
		}
	}
	if len(request.Tools) > 0 && string(request.Tools) != "null" {
		text.add(string(request.Tools))
	}
	if len(request.Functions) > 0 && string(request.Functions) != "null" {
		text.add(string(request.Functions))
	}
	return overhead + text.tokens()
}

func quotaStatsFor(r *http.Request, stats *chatStatsReader) {
	if tr := requestTraceFrom(r); tr != nil && tr.quota != nil {
		stats.quotaOutput = &quotaTextEstimate{}
	}
}

// settleQuotaAttempt always finishes this attempt. Missing usage is local to
// the request: tokens get a labeled estimate; unknown credits stay uncharged
// and visible for optional adjustment without stopping future requests.
func settleQuotaAttempt(r *http.Request, body []byte, stats *chatStatsReader) {
	tr := requestTraceFrom(r)
	if tr == nil || tr.quota == nil || stats == nil {
		return
	}
	credit, hasCredit := stats.Credit()
	hasCredit = hasCredit && !math.IsNaN(credit) && !math.IsInf(credit, 0) && credit >= 0 && credit <= 1e9
	usage := stats.Usage()
	tokens, known := usage.TotalTokens, usage.HasTotalTokens && usage.TotalTokens >= 0
	if !known && usage.HasPromptTokens && usage.PromptTokens >= 0 && usage.HasCompletionTokens && usage.CompletionTokens >= 0 {
		tokens, known = usage.PromptTokens+usage.CompletionTokens, true
	}
	// A pure error/empty stream with no usage and no generated text is a failed
	// request, not a billable missing-usage completion.
	if !known && !hasCredit && !stats.sawOutput && (stats.SawErrorFrame() || !stats.validFrame) {
		tr.quota.RejectAttempt()
		return
	}
	if tr.quota.IsTokenQuota() && !known {
		prompt := usage.PromptTokens
		if !usage.HasPromptTokens || prompt < 0 {
			prompt = estimateQuotaPrompt(body)
		}
		completion := usage.CompletionTokens
		if !usage.HasCompletionTokens || completion < 0 {
			completion = stats.quotaOutput.tokens()
		}
		tokens = prompt + completion
		tr.quota.ObserveEstimatedTokens(tokens)
		log.Printf("WARN: [quota] key=%s request=%s tokens=%d estimated (upstream usage incomplete)", tr.keyID, tr.id, tokens)
		return
	}
	tr.quota.Observe(tokens, known, credit, hasCredit)
	if !tr.quota.IsTokenQuota() && !hasCredit {
		log.Printf("WARN: [quota] key=%s request=%s credit missing; request uncharged, key remains usable", tr.keyID, tr.id)
	}
}
