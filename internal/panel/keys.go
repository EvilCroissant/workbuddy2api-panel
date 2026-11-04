package panel

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
)

func (p *Panel) listKeys(w http.ResponseWriter, r *http.Request) {
	hours, ok := keyUsageHours(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"keys": p.cfg.Keys.List(), "available": p.cfg.Keys != nil, "admin_configured": p.apiKey() != "",
		"usage": p.cfg.KeyUsage.Summary(hours), "usage_available": p.cfg.KeyUsage != nil,
	})
}

func keyUsageHours(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.URL.Query().Get("hours")
	if v == "" {
		return 0, true
	}
	hours, err := strconv.Atoi(v)
	if err != nil || (hours != 0 && hours != 24 && hours != 168 && hours != 720) {
		writeErr(w, http.StatusBadRequest, "hours must be 0, 24, 168 or 720")
		return 0, false
	}
	return hours, true
}

func (p *Panel) keyUsage(w http.ResponseWriter, r *http.Request) {
	hours, ok := keyUsageHours(w, r)
	if !ok {
		return
	}
	if p.cfg.KeyUsage == nil {
		writeErr(w, http.StatusNotImplemented, "key usage not available")
		return
	}
	id := r.PathValue("id")
	var key *httpauth.KeyInfo
	for _, k := range p.cfg.Keys.List() {
		if k.ID == id {
			info := k
			key = &info
			break
		}
	}
	if key == nil {
		// Revocation removes credentials, not the independent historical ledger.
		if _, exists := p.cfg.KeyUsage.Summary(0)[id]; !exists {
			writeErr(w, http.StatusNotFound, "key not found")
			return
		}
		key = &httpauth.KeyInfo{ID: id, Name: "已删除密钥"}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"key": key, "usage": p.cfg.KeyUsage.Snapshot(id, hours), "available": true,
	})
}

func decodeKeyRequest(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid key request")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid key request")
		return false
	}
	return true
}

func (p *Panel) createKey(w http.ResponseWriter, r *http.Request) {
	// Serialize with config saves, preventing a concurrent administrator-key clear.
	p.keyMu.Lock()
	defer p.keyMu.Unlock()
	if p.cfg.Keys == nil {
		writeErr(w, http.StatusNotImplemented, "key store not available")
		return
	}
	if p.apiKey() == "" {
		writeErr(w, http.StatusConflict, "请先在配置页设置管理员密钥")
		return
	}
	var in struct {
		Name          string   `json:"name"`
		AllowedModels []string `json:"allowed_models"`
	}
	if !decodeKeyRequest(w, r, &in) {
		return
	}
	info, secret, err := p.cfg.Keys.CreateWithModels(in.Name, in.AllowedModels)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, httpauth.ErrKeyName) || errors.Is(err, httpauth.ErrKeyModels) {
			status = http.StatusBadRequest
		}
		writeErr(w, status, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"key": info, "secret": secret})
}

func (p *Panel) updateKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled       *bool     `json:"enabled"`
		AllowedModels *[]string `json:"allowed_models"`
	}
	if !decodeKeyRequest(w, r, &in) {
		return
	}
	if in.Enabled == nil && in.AllowedModels == nil {
		writeErr(w, http.StatusBadRequest, "enabled or allowed_models is required")
		return
	}
	if p.cfg.Keys == nil {
		writeErr(w, http.StatusNotImplemented, "key store not available")
		return
	}
	p.keyResult(w, p.cfg.Keys.Update(r.PathValue("id"), in.Enabled, in.AllowedModels))
}

func (p *Panel) revealKey(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Keys == nil {
		writeErr(w, http.StatusNotImplemented, "key store not available")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	secret, err := p.cfg.Keys.Reveal(r.PathValue("id"))
	if err != nil {
		p.keyResult(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret})
}

func (p *Panel) rotateKey(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Keys == nil {
		writeErr(w, http.StatusNotImplemented, "key store not available")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	secret, err := p.cfg.Keys.Rotate(r.PathValue("id"))
	if err != nil {
		p.keyResult(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret})
}

func (p *Panel) deleteKey(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Keys == nil {
		writeErr(w, http.StatusNotImplemented, "key store not available")
		return
	}
	p.keyResult(w, p.cfg.Keys.Delete(r.PathValue("id")))
}

func (p *Panel) keyResult(w http.ResponseWriter, err error) {
	if errors.Is(err, httpauth.ErrKeyNotFound) {
		writeErr(w, http.StatusNotFound, "key not found")
	} else if errors.Is(err, httpauth.ErrKeyModels) {
		writeErr(w, http.StatusBadRequest, err.Error())
	} else if errors.Is(err, httpauth.ErrKeySecretUnavailable) {
		writeErr(w, http.StatusConflict, err.Error())
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "save key: "+err.Error())
	} else {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
