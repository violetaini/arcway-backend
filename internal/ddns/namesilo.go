package ddns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nrdcg/namesilo"
)

// namesiloProvider Namesilo DNS。凭据 JSON key 沿用 acme/dns_providers.go:
//
//	NAMESILO_API_KEY
//
// 注意:Namesilo 最小 TTL 3600,小于会被 API 拒。
type namesiloProvider struct {
	client *namesilo.Client
}

func newNamesiloProvider(creds map[string]string) (*namesiloProvider, error) {
	apiKey := strings.TrimSpace(creds["NAMESILO_API_KEY"])
	if apiKey == "" {
		return nil, errors.New("namesilo: missing NAMESILO_API_KEY")
	}
	return &namesiloProvider{client: namesilo.NewClient(apiKey)}, nil
}

func (p *namesiloProvider) UpsertRecord(ctx context.Context, fqdn string, recordType string, content string, ttl int) error {
	zone, sub, err := SplitFQDN(fqdn)
	if err != nil {
		return fmt.Errorf("split fqdn: %w", err)
	}

	// Namesilo API 不接受小于 3600 的 TTL
	if ttl > 0 && ttl < 3600 {
		ttl = 3600
	}

	listResp, err := p.client.DnsListRecords(ctx, &namesilo.DnsListRecordsParams{Domain: zone})
	if err != nil {
		return fmt.Errorf("list records: %w", err)
	}
	if listResp == nil || listResp.Reply.Code != "300" {
		return fmt.Errorf("namesilo list reply: code=%s detail=%s", listResp.Reply.Code, listResp.Reply.Detail)
	}

	// Namesilo 返回的 host 是 FQDN(含 zone)。根域 host == zone,sub 域 host == "sub.zone"。
	expectHost := zone
	if sub != "" {
		expectHost = sub + "." + zone
	}

	var existingID, existingValue string
	for _, rec := range listResp.Reply.ResourceRecord {
		if rec.Host == expectHost && strings.EqualFold(rec.Type, recordType) {
			existingID = rec.RecordID
			existingValue = rec.Value
			break
		}
	}

	if existingID != "" {
		if existingValue == content {
			return nil
		}
		// Update 的 host 字段是 sub(不含 zone)— 跟 Add 一致
		updResp, err := p.client.DnsUpdateRecord(ctx, &namesilo.DnsUpdateRecordParams{
			Domain: zone,
			ID:     existingID,
			Host:   sub,
			Value:  content,
			TTL:    ttl,
		})
		if err != nil {
			return fmt.Errorf("update record: %w", err)
		}
		if updResp == nil || updResp.Reply.Code != "300" {
			return fmt.Errorf("namesilo update reply: code=%s detail=%s", updResp.Reply.Code, updResp.Reply.Detail)
		}
		return nil
	}

	addResp, err := p.client.DnsAddRecord(ctx, &namesilo.DnsAddRecordParams{
		Domain: zone,
		Type:   recordType,
		Host:   sub,
		Value:  content,
		TTL:    ttl,
	})
	if err != nil {
		return fmt.Errorf("add record: %w", err)
	}
	if addResp == nil || addResp.Reply.Code != "300" {
		return fmt.Errorf("namesilo add reply: code=%s detail=%s", addResp.Reply.Code, addResp.Reply.Detail)
	}
	return nil
}
