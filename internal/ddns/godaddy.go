package ddns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// godaddyProvider GoDaddy DNS。lego v4 的 godaddy 包是 internal/,外部不可 import,直连 HTTP。
// 凭据 JSON key 沿用 acme/dns_providers.go:
//
//	GODADDY_API_KEY
//	GODADDY_API_SECRET
//
// 关键:GoDaddy 已在 2024 收紧 API 访问(<10 域名账号申不到 key),失败时把官方错误码原样透出。
type godaddyProvider struct {
	httpClient *http.Client
	baseURL    string
	authHeader string
}

func newGoDaddyProvider(creds map[string]string) (*godaddyProvider, error) {
	key := strings.TrimSpace(creds["GODADDY_API_KEY"])
	secret := strings.TrimSpace(creds["GODADDY_API_SECRET"])
	if key == "" || secret == "" {
		return nil, errors.New("godaddy: missing GODADDY_API_KEY / GODADDY_API_SECRET")
	}
	return &godaddyProvider{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		baseURL:    "https://api.godaddy.com/v1",
		authHeader: fmt.Sprintf("sso-key %s:%s", key, secret),
	}, nil
}

type godaddyRecord struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Data string `json:"data"`
	TTL  int    `json:"ttl,omitempty"`
}

func (p *godaddyProvider) UpsertRecord(ctx context.Context, fqdn string, recordType string, content string, ttl int) error {
	zone, sub, err := SplitFQDN(fqdn)
	if err != nil {
		return fmt.Errorf("split fqdn: %w", err)
	}
	if sub == "" {
		sub = "@"
	}
	if ttl == 0 {
		ttl = 600
	}
	// GoDaddy PUT /domains/{zone}/records/{type}/{name} 是 replace 语义:
	// 同 name+type 不存在 → 自动创建;存在 → 用 body 替换全部
	// 比先查再决定简单且原子
	url := fmt.Sprintf("%s/domains/%s/records/%s/%s", p.baseURL, zone, recordType, sub)
	body := []godaddyRecord{{
		Type: recordType,
		Name: sub,
		Data: content,
		TTL:  ttl,
	}}
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", p.authHeader)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// GoDaddy 错误体 {"code":"...","message":"..."}
	var errResp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(respBytes, &errResp)
	if errResp.Code != "" {
		return fmt.Errorf("godaddy HTTP %d: [%s] %s", resp.StatusCode, errResp.Code, errResp.Message)
	}
	return fmt.Errorf("godaddy HTTP %d: %s", resp.StatusCode, truncate(string(respBytes), 200))
}
