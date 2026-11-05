package panel

import (
	_ "embed"
	"errors"
	"log"
	"net/http"
	"net/netip"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
)

//go:embed claim.html
var claimHTML string

//go:embed claim.js
var claimJS []byte

//go:embed packets.js
var packetsJS []byte

func (p *Panel) packetRoutes() {
	p.mux.HandleFunc("GET /panel/packets.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(packetsJS)
	})
	p.mux.HandleFunc("GET /panel/api/red-packets", p.withAuth(p.listPackets))
	p.mux.HandleFunc("POST /panel/api/red-packets", p.withAuth(p.createPacket))
	p.mux.HandleFunc("GET /panel/api/red-packets/{id}", p.withAuth(p.packetDetail))
	p.mux.HandleFunc("GET /panel/api/red-packets/{id}/export", p.withAuth(p.exportPacket))
	p.mux.HandleFunc("POST /panel/api/red-packets/{id}/revoke", p.withAuth(p.revokePacket))
	p.mux.HandleFunc("POST /panel/api/red-packets/{id}/reconcile", p.withAuth(p.reconcilePacket))
	p.mux.HandleFunc("GET /panel/api/claim/{token}", p.publicPacket)
	p.mux.HandleFunc("POST /panel/api/claim/{token}", p.claimPacket)
	p.mux.HandleFunc("GET /panel/claim", p.claimPage)
	p.mux.HandleFunc("GET /panel/claim.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(claimJS)
	})
}
func (p *Panel) listPackets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"packets": p.cfg.Keys.Packets(), "available": p.cfg.Keys != nil, "admin_configured": p.apiKey() != ""})
}
func (p *Panel) createPacket(w http.ResponseWriter, r *http.Request) {
	p.keyMu.Lock()
	defer p.keyMu.Unlock()
	if p.cfg.Keys == nil {
		writeErr(w, 501, "key store not available")
		return
	}
	if p.apiKey() == "" {
		writeErr(w, 409, "请先在配置页设置管理员密钥")
		return
	}
	var in httpauth.PacketInput
	if !decodeKeyRequest(w, r, &in) {
		return
	}
	packet, err := p.cfg.Keys.CreatePacket(in)
	if err != nil {
		packetError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, packet)
}
func (p *Panel) packetDetail(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Keys == nil {
		writeErr(w, 501, "key store not available")
		return
	}
	packet, err := p.cfg.Keys.Packet(r.PathValue("id"))
	if err != nil {
		packetError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, packet)
}
func (p *Panel) exportPacket(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Keys == nil {
		writeErr(w, 501, "key store not available")
		return
	}
	keys, err := p.cfg.Keys.ExportPacket(r.PathValue("id"))
	if err != nil {
		packetError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"keys": keys})
}
func (p *Panel) revokePacket(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Keys == nil {
		writeErr(w, 501, "key store not available")
		return
	}
	if err := p.cfg.Keys.RevokePacket(r.PathValue("id")); err != nil {
		packetError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (p *Panel) reconcilePacket(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Keys == nil {
		writeErr(w, 501, "key store not available")
		return
	}
	var in struct {
		KeyID string  `json:"key_id"`
		Used  float64 `json:"used"`
	}
	if !decodeKeyRequest(w, r, &in) {
		return
	}
	if err := p.cfg.Keys.ReconcileQuota(r.PathValue("id"), in.KeyID, in.Used); err != nil {
		packetError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (p *Panel) publicPacket(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	packet, err := p.cfg.Keys.PublicPacket(r.PathValue("token"))
	if err != nil {
		packetError(w, err)
		return
	}
	// The public page needs no administrator metadata, key IDs, IPs, or used balances.
	writeJSON(w, 200, map[string]any{"title": packet.Title, "kind": packet.Kind, "mode": packet.Mode, "total": packet.Total, "shares": packet.Shares, "claimed": packet.Claimed, "remaining": packet.Remaining, "expires_at": packet.ExpiresAt, "allowed_models": packet.AllowedModels})
}
func (p *Panel) claimPacket(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// JSON-only POST blocks cross-origin HTML form submissions. Fetch is same-origin.
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeErr(w, 415, "请使用 JSON 请求领取")
		return
	}
	var in struct{}
	if !decodeKeyRequest(w, r, &in) {
		return
	}
	ip := p.claimIP(r)
	if ip == "" {
		writeErr(w, 400, "未识别到客户端 IP，请检查反向代理是否设置了单个有效的 X-Real-IP")
		return
	}
	result, err := p.cfg.Keys.ClaimPacket(r.PathValue("token"), ip)
	if err != nil {
		packetError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (p *Panel) claimPage(w http.ResponseWriter, r *http.Request) {
	// Reuse the official panel's head, including its design tokens, rather than duplicate its theme.
	head, _, _ := strings.Cut(string(indexHTML), "</head>")
	head = strings.ReplaceAll(head, "WorkBuddy2API · 控制台", "WorkBuddy2API · 红包")
	head = strings.ReplaceAll(head, `href="mobile.css"`, `href="/panel/mobile.css"`)
	head = strings.ReplaceAll(head, `href="console.css"`, `href="/panel/console.css"`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(head + "</head>\n" + claimHTML))
}
func (p *Panel) claimIP(r *http.Request) string {
	// The reverse proxy supplies the client IP; a shared gateway is never a claimant.
	values := r.Header.Values("X-Real-IP")
	if len(values) != 1 {
		return ""
	}
	real, err := netip.ParseAddr(strings.TrimSpace(values[0]))
	if err != nil || real.Zone() != "" {
		return ""
	}
	return real.Unmap().String()
}
func packetError(w http.ResponseWriter, err error) {
	status := 500
	switch {
	case errors.Is(err, httpauth.ErrPacketNotFound), errors.Is(err, httpauth.ErrKeyNotFound):
		status = 404
	case errors.Is(err, httpauth.ErrPacketClosed):
		status = 410
	case errors.Is(err, httpauth.ErrPacketInput), errors.Is(err, httpauth.ErrPacketModels), errors.Is(err, httpauth.ErrKeyModels):
		status = 400
	case errors.Is(err, httpauth.ErrQuotaBusy):
		status = 409
	}
	if status == 500 {
		log.Printf("ERR: [panel] red packet action failed: %v", err)
		writeErr(w, status, "红包保存失败，请检查服务日志与存储")
		return
	}
	writeErr(w, status, err.Error())
}
