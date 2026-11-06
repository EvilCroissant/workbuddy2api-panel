package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func TestPacketGatewayAllowsConcurrentRequests(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(strconv.FormatBool(stream), func(t *testing.T) {
			keys, err := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
			if err != nil {
				t.Fatal(err)
			}
			packet, err := keys.CreatePacket(httpauth.PacketInput{Kind: "tokens", Mode: "equal", Total: 3, Shares: 1, Days: 1, AllowedModels: []string{"glm-5.2"}})
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := keys.ClaimPacket(packet.ClaimToken, "ip")
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{}, 2)
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseRequests := func() { releaseOnce.Do(func() { close(release) }) }
			var wg sync.WaitGroup
			defer func() {
				releaseRequests()
				wg.Wait()
			}()
			up := newFakeUpstream(t, func(string) (int, string, bool) {
				started <- struct{}{}
				<-release
				return 200, sseOK, true
			})
			h := NewHandler(Config{APIKey: "admin", Keys: keys, Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}), Upstream: up})
			call := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[],"stream":`+strconv.FormatBool(stream)+`}`))
				r.Header.Set("Authorization", "Bearer "+claimed.Secret)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			responses := make(chan *httptest.ResponseRecorder, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					responses <- call()
				}()
			}
			for i := 0; i < 2; i++ {
				select {
				case <-started:
				case w := <-responses:
					t.Fatalf("concurrent request rejected before forwarding: %d %s", w.Code, w.Body.String())
				case <-time.After(5 * time.Second):
					t.Fatal("requests did not reach upstream concurrently")
				}
			}
			releaseRequests()
			for i := 0; i < 2; i++ {
				select {
				case w := <-responses:
					if w.Code != 200 {
						t.Fatalf("concurrent request failed: %d %s", w.Code, w.Body.String())
					}
				case <-time.After(5 * time.Second):
					t.Fatal("concurrent request did not finish")
				}
			}
			after, _ := keys.Packet(packet.ID)
			if after.Used != 4 || after.Keys[0].Quota.Pending || after.Keys[0].Quota.Uncertain {
				t.Fatalf("concurrent billing failed: %+v", after)
			}
			if w := call(); w.Code != 402 || !strings.Contains(w.Body.String(), "quota_exceeded") {
				t.Fatalf("exhausted key accepted: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPacketGatewayBillingWithoutLogging(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, kind := range []string{"tokens", "credit"} {
			t.Run(kind+strconv.FormatBool(stream), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "keys.json")
				keys, _ := httpauth.OpenKeys(path)
				total := 3.0
				if kind == "credit" {
					total = 1
				}
				packet, err := keys.CreatePacket(httpauth.PacketInput{Kind: kind, Mode: "equal", Total: total, Shares: 1, Days: 1, AllowedModels: []string{"glm-5.2"}})
				if err != nil {
					t.Fatal(err)
				}
				claimed, err := keys.ClaimPacket(packet.ClaimToken, "203.0.113.1")
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				up := newFakeUpstream(t, func(string) (int, string, bool) {
					calls++
					return 200, strings.ReplaceAll(sseOK, `"total_tokens":2`, `"total_tokens":2,"credit":0.6`), true
				})
				h := NewHandler(Config{APIKey: "admin", Keys: keys, Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}), Upstream: up})
				call := func(model, path string) *httptest.ResponseRecorder {
					r := httptest.NewRequest("POST", path, strings.NewReader(`{"model":"`+model+`","messages":[],"stream":`+strconv.FormatBool(stream)+`}`))
					r.Header.Set("Authorization", "Bearer "+claimed.Secret)
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					return w
				}
				if w := call("other", "/v1/chat/completions"); w.Code != 403 || calls != 0 {
					t.Fatalf("model restriction bypass %d calls=%d", w.Code, calls)
				}
				if w := call("glm-5.2", "/v1/search/messages"); w.Code != 403 || calls != 0 {
					t.Fatal("unmetered search allowed")
				}
				for i := 0; i < 2; i++ {
					if w := call("glm-5.2", "/v1/chat/completions"); w.Code != 200 {
						t.Fatalf("request %d: %d %s", i, w.Code, w.Body.String())
					}
				}
				if w := call("glm-5.2", "/v1/chat/completions"); w.Code != 402 || !strings.Contains(w.Body.String(), "quota_exceeded") || calls != 2 {
					t.Fatalf("exhausted quota accepted %d %s calls=%d", w.Code, w.Body.String(), calls)
				}
				keys, err = httpauth.OpenKeys(path)
				if err != nil {
					t.Fatal(err)
				}
				after, err := keys.Packet(packet.ID)
				if err != nil {
					t.Fatal(err)
				}
				want := 4.0
				if kind == "credit" {
					want = 1.2
				}
				if after.Used != want || after.Keys[0].Quota.Pending {
					t.Fatalf("billing lost %+v", after)
				}
			})
		}
	}
}
func TestPacketUnknownCreditRecordsWarningWithoutLockingKey(t *testing.T) {
	for _, status := range []int{200, 400} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			keys, _ := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
			p, err := keys.CreatePacket(httpauth.PacketInput{Kind: "credit", Mode: "equal", Total: 10, Shares: 1, Days: 1})
			if err != nil {
				t.Fatal(err)
			}
			c, _ := keys.ClaimPacket(p.ClaimToken, "ip")
			up := newFakeUpstream(t, func(string) (int, string, bool) {
				if status == 400 {
					return 400, `{"code":11101,"msg":"invalid tools"}`, false
				}
				return 200, sseOK, true
			})
			h := NewHandler(Config{APIKey: "admin", Keys: keys, Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}), Upstream: up})
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`))
			r.Header.Set("Authorization", "Bearer "+c.Secret)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			d, _ := keys.Packet(p.ID)
			q := d.Keys[0].Quota
			if q.Pending || q.Used != 0 || q.Uncertain != (status == 200) {
				t.Fatalf("wrong settlement %+v body=%s", q, w.Body.String())
			}
			if q.UnsettledRequests != int64((400-status)/200) {
				t.Fatalf("missing credit warning not recorded: %+v", q)
			}
			lease, err := keys.BeginQuota(c.Key.ID)
			if err != nil {
				t.Fatalf("missing credit locked key: %v", err)
			}
			if err := lease.Finish(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPacketIncompleteStreamWithUsageDoesNotLockKey(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(strconv.FormatBool(stream), func(t *testing.T) {
			keys, _ := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
			p, err := keys.CreatePacket(httpauth.PacketInput{Kind: "credit", Mode: "equal", Total: 10, Shares: 1, Days: 1})
			if err != nil {
				t.Fatal(err)
			}
			c, _ := keys.ClaimPacket(p.ClaimToken, "ip")
			incomplete := "data: {\"choices\":[{\"delta\":{\"content\":\"partial answer\"}}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2,\"credit\":0}}\n\n"
			up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, incomplete, true })
			h := NewHandler(Config{APIKey: "admin", Keys: keys, Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}), Upstream: up})
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[],"stream":`+strconv.FormatBool(stream)+`}`))
			r.Header.Set("Authorization", "Bearer "+c.Secret)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			after, _ := keys.Packet(p.ID)
			if after.Keys[0].Quota.Uncertain || after.Keys[0].Quota.Pending || after.Used != 0 {
				t.Fatalf("measured zero credit incorrectly locked key: %+v", after)
			}
		})
	}
}

func TestPacketTokenFallbackAndFailedStreams(t *testing.T) {
	const content = "data: {\"choices\":[{\"delta\":{\"content\":\"hello world\"}}]}\n\n"
	cases := []struct {
		name, response string
		measured       int64
		estimated      bool
	}{
		{"missing_usage", content + "data: [DONE]\n\n", 0, true},
		{"interrupted_output", content, 0, true},
		{"partial_prompt_usage", content + "data: {\"usage\":{\"prompt_tokens\":100}}\n\ndata: [DONE]\n\n", 0, true},
		{"partial_completion_usage", content + "data: {\"usage\":{\"completion_tokens\":100}}\n\ndata: [DONE]\n\n", 0, true},
		{"measured_without_done", content + "data: {\"usage\":{\"total_tokens\":500}}\n\n", 500, false},
		{"measured_components", content + "data: {\"usage\":{\"prompt_tokens\":400,\"completion_tokens\":100}}\n\ndata: [DONE]\n\n", 500, false},
		{"error_after_output", content + "data: {\"error\":{\"message\":\"broken\"}}\n\n", 0, true},
		{"error_without_output", "data: {\"error\":{\"message\":\"rate limited\"}}\n\ndata: [DONE]\n\n", 0, false},
		{"empty_stream", "data: [DONE]\n\n", 0, false},
		{"malformed_stream", "data: invalid json\n\n", 0, false},
	}
	for _, stream := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(tc.name+strconv.FormatBool(stream), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "keys.json")
				keys, _ := httpauth.OpenKeys(path)
				p, err := keys.CreatePacket(httpauth.PacketInput{Kind: "tokens", Mode: "equal", Total: 10000, Shares: 1, Days: 1, AllowedModels: []string{"glm-5.2"}})
				if err != nil {
					t.Fatal(err)
				}
				c, err := keys.ClaimPacket(p.ClaimToken, "ip")
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				up := newFakeUpstream(t, func(string) (int, string, bool) {
					calls++
					return 200, tc.response, true
				})
				h := NewHandler(Config{APIKey: "admin", Keys: keys, Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}), Upstream: up})
				for i := 0; i < 2; i++ {
					r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"test"}],"stream":`+strconv.FormatBool(stream)+`}`))
					r.Header.Set("Authorization", "Bearer "+c.Secret)
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code == 402 || calls != i+1 {
						t.Fatalf("previous request locked key: calls=%d %d %s", calls, w.Code, w.Body.String())
					}
				}
				reopened, err := httpauth.OpenKeys(path)
				if err != nil {
					t.Fatal(err)
				}
				d, _ := reopened.Packet(p.ID)
				q := d.Keys[0].Quota
				if q.Pending || q.Uncertain || q.UnsettledRequests != 0 {
					t.Fatalf("request did not finish settlement: %+v", q)
				}
				if tc.estimated {
					if q.Used <= 0 || q.Estimated != q.Used {
						t.Fatalf("missing usage not labeled and charged: %+v", q)
					}
					if strings.HasPrefix(tc.name, "partial_") && q.Used <= 200 {
						t.Fatalf("partial measured usage was discarded: %+v", q)
					}
				} else if q.Estimated != 0 || q.Used != 2*tc.measured {
					t.Fatalf("measured or failed request billed incorrectly: %+v", q)
				}
			})
		}
	}
}

func TestPacketTransportFailureDoesNotConsumeOrLockKey(t *testing.T) {
	for _, kind := range []string{"tokens", "credit"} {
		t.Run(kind, func(t *testing.T) {
			keys, _ := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
			p, err := keys.CreatePacket(httpauth.PacketInput{Kind: kind, Mode: "equal", Total: 100, Shares: 1, Days: 1, AllowedModels: []string{"glm-5.2"}})
			if err != nil {
				t.Fatal(err)
			}
			c, _ := keys.ClaimPacket(p.ClaimToken, "ip")
			up := &upstream.Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("dial tcp: connect: connection refused")
			})}, ChatBaseCN: "https://fake.example"}
			h := NewHandler(Config{APIKey: "admin", Keys: keys, MaxRotate: 1, Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}), Upstream: up})
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`))
			r.Header.Set("Authorization", "Bearer "+c.Secret)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 503 {
				t.Fatalf("unexpected transport response: %d %s", w.Code, w.Body.String())
			}
			d, _ := keys.Packet(p.ID)
			if q := d.Keys[0].Quota; q.Pending || q.Uncertain || q.Used != 0 || q.UnsettledRequests != 0 {
				t.Fatalf("transport failure affected quota: %+v", q)
			}
		})
	}
}

type disconnectedQuotaWriter struct{ *httptest.ResponseRecorder }

func (w disconnectedQuotaWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestPacketClientDisconnectSettlesPartialOutput(t *testing.T) {
	for _, kind := range []string{"tokens", "credit"} {
		t.Run(kind, func(t *testing.T) {
			keys, _ := httpauth.OpenKeys(filepath.Join(t.TempDir(), "keys.json"))
			p, _ := keys.CreatePacket(httpauth.PacketInput{Kind: kind, Mode: "equal", Total: 100, Shares: 1, Days: 1, AllowedModels: []string{"glm-5.2"}})
			c, _ := keys.ClaimPacket(p.ClaimToken, "ip")
			up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
			h := NewHandler(Config{APIKey: "admin", Keys: keys, Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}), Upstream: up})
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[],"stream":true}`))
			r.Header.Set("Authorization", "Bearer "+c.Secret)
			h.ServeHTTP(disconnectedQuotaWriter{httptest.NewRecorder()}, r)
			d, _ := keys.Packet(p.ID)
			q := d.Keys[0].Quota
			if q.Pending || (kind == "tokens" && (q.Used <= 0 || q.Estimated != q.Used)) || (kind == "credit" && (q.Used != 0 || q.UnsettledRequests != 1)) {
				t.Fatalf("disconnected request not settled: %+v", q)
			}
			lease, err := keys.BeginQuota(c.Key.ID)
			if err != nil {
				t.Fatalf("client disconnect locked key: %v", err)
			}
			if err := lease.Finish(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestUsageCreditTotalRequiresExplicitCredit(t *testing.T) {
	if _, _, known := usageCreditTotal(map[string]any{"usage": map[string]any{"total_tokens": float64(2)}}); known {
		t.Fatal("missing credit treated as free")
	}
	if credit, _, known := usageCreditTotal(map[string]any{"usage": map[string]any{"total_tokens": float64(2), "credit": float64(0)}}); !known || credit != 0 {
		t.Fatal("explicit zero rejected")
	}
}
