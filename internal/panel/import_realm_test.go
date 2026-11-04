package panel

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func importTokenWithIssuer(t *testing.T, issuer string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"iss": issuer})
	if err != nil {
		t.Fatal(err)
	}
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestResolveImportRealmPrefersKnownIssuer(t *testing.T) {
	cases := []struct {
		name   string
		issuer string
		domain string
		want   string
	}{
		{"global issuer overrides cn domain", "https://www.codebuddy.ai/auth/realms/copilot", "www.codebuddy.cn", "global"},
		{"cn issuer overrides global domain", "https://www.workbuddy.cn/auth/realms/copilot", "www.workbuddy.ai", "cn"},
		{"unknown issuer falls back to global domain", "https://issuer.example/auth/realms/copilot", "www.workbuddy.ai", "global"},
		{"non-JWT falls back to cn domain", "", "www.codebuddy.cn", "cn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := ""
			if tc.issuer != "" {
				token = importTokenWithIssuer(t, tc.issuer)
			}
			if got := resolveImportRealm(token, tc.domain); got != tc.want {
				t.Fatalf("resolveImportRealm()=%q, want %q", got, tc.want)
			}
		})
	}
}
