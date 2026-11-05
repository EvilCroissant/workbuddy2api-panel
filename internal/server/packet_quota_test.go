package server

import (
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
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
func TestPacketUnknownUsagePausesAndHTTPFailureDoesNotConsume(t *testing.T) {
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
		})
	}
}

func TestPacketIncompleteStreamWithEarlyUsagePauses(t *testing.T) {
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
			if !after.Keys[0].Quota.Uncertain {
				t.Fatal("early usage incorrectly treated as final billing")
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
