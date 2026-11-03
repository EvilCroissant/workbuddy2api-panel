package upstream

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// Capture real outbound calls to catch per-realm version drift and chat headers
// leaking into refresh, billing or model discovery through CommonHeaders.
func TestDesktopRequestProfiles(t *testing.T) {
	for _, realm := range []struct {
		name, domain, version, platform string
	}{
		{"cn", "copilot.tencent.com", "5.9.3", "WorkBuddy"},
		{"global", "www.workbuddy.ai", "5.9.4", "workbuddy-ai"},
	} {
		for _, profile := range []string{"default", "versions", "custom-ua", "legacy"} {
			for _, family := range []string{"chat", "refresh", "billing", "models"} {
				t.Run(realm.name+"/"+profile+"/"+family, func(t *testing.T) {
					a := &auth.Auth{UID: "test-user", Domain: realm.domain, AccessToken: "at", RefreshToken: "rt"}
					a.BackfillRealm()
					var got http.Header
					calls := 0
					httpClient := &http.Client{Transport: rtFunc(func(req *http.Request) (*http.Response, error) {
						calls++
						got = req.Header.Clone()
						return jsonResp(200, `{"code":0,"data":{"accessToken":"new-at","refreshToken":"new-rt","models":[{"id":"test-model","maxOutputTokens":8192}],"agents":[{"name":"cli","models":["test-model"]}],"response":{"data":{"accounts":[]}}}}`), nil
					})}
					c := &Client{HTTP: httpClient, ChatHTTP: httpClient, GlobalEnabled: true,
						ChatBaseCN: "https://cn.example", ChatBaseGlobal: "https://global.example",
						BillingBaseCN: "https://billing-cn.example", BillingBaseGlobal: "https://billing-global.example"}
					version, cliVersion := realm.version, "2.165.0"
					switch profile {
					case "versions":
						c.ClientVersion, c.CliVersion = "6.0.0", "3.0.0"
						version, cliVersion = "6.0.0", "3.0.0"
					case "custom-ua":
						c.UserAgent = "CustomAgent/1"
					case "legacy":
						c.ClientName = "SaaS"
					}
					var err error
					switch family {
					case "chat":
						body, status, _, chatErr := c.ChatStream(a, []byte(`{"model":"test-model","messages":[]}`), "", ChatMeta{})
						err = chatErr
						if body != nil {
							body.Close()
						}
						if status != http.StatusOK {
							t.Fatalf("chat status = %d", status)
						}
					case "refresh":
						err = c.RefreshToken(a)
					case "billing":
						_, _, err = c.UserResource(a)
					case "models":
						_, err = c.fetchEnterpriseModels(a)
					}
					if err != nil || calls != 1 {
						t.Fatalf("outbound calls = %d, err = %v", calls, err)
					}
					wantUA := fmt.Sprintf("WorkBuddy/%s %s/%s CLI/%s", version, realm.platform, version, cliVersion)
					if family == "billing" {
						wantUA = "WorkBuddy/" + version
						if profile == "legacy" {
							wantUA = ""
						}
					}
					if profile == "custom-ua" {
						wantUA = "CustomAgent/1"
					}
					if ua := got.Get("User-Agent"); ua != wantUA {
						t.Errorf("User-Agent = %q, want %q", ua, wantUA)
					}
					if accept := got.Get("Accept"); accept != "application/json" {
						t.Errorf("Accept = %q, want application/json", accept)
					}
					for key, value := range map[string]string{
						"X-Agent-Intent": "craft", "X-Agent-Type": "main", "X-Agent-Runtime": "other",
						"X-Agent-Purpose": "conversation", "X-IDE-Name": "WorkBuddy", "X-IDE-Type": "WorkBuddy",
						"X-IDE-Version": version, "X-Product": "SaaS",
					} {
						if family != "chat" || (profile == "legacy" && (key == "X-Agent-Purpose" || key == "X-IDE-Name" || key == "X-IDE-Type" || key == "X-IDE-Version")) {
							value = ""
						}
						if actual := got.Get(key); actual != value {
							t.Errorf("%s = %q, want %q", key, actual, value)
						}
					}
					wantRefreshToken := ""
					if family == "refresh" {
						wantRefreshToken = "rt"
					}
					if token := got.Get("X-Refresh-Token"); token != wantRefreshToken {
						t.Errorf("X-Refresh-Token = %q, want %q", token, wantRefreshToken)
					}
					if value := got.Get("X-Private-Data"); value != "" {
						t.Errorf("conditional model optimization flag must not be hard-coded: %q", value)
					}
				})
			}
		}
	}
}
