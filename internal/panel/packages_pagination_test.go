package panel

import (
	"net/http/httptest"
	"testing"
)

func TestPackagePage(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		total      int
		wantOffset int
		wantLimit  int
	}{
		{name: "legacy full response", total: 1380, wantOffset: 0, wantLimit: 1380},
		{name: "default page", query: "?offset=0", total: 1380, wantOffset: 0, wantLimit: 10},
		{name: "middle page", query: "?offset=400&limit=20", total: 1380, wantOffset: 400, wantLimit: 20},
		{name: "limit capped", query: "?offset=10&limit=500", total: 1380, wantOffset: 10, wantLimit: 50},
		{name: "offset clamped", query: "?offset=5000&limit=20", total: 1380, wantOffset: 1380, wantLimit: 20},
		{name: "invalid values", query: "?offset=-2&limit=invalid", total: 25, wantOffset: 0, wantLimit: 10},
		{name: "empty pool", query: "?offset=0&limit=10", total: 0, wantOffset: 0, wantLimit: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/panel/api/packages"+tt.query, nil)
			offset, limit := packagePage(r, tt.total)
			if offset != tt.wantOffset || limit != tt.wantLimit {
				t.Fatalf("packagePage()=(%d,%d), want (%d,%d)", offset, limit, tt.wantOffset, tt.wantLimit)
			}
		})
	}
}
