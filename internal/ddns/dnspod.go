package ddns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nrdcg/dnspod-go"
)

// dnspodProvider 国内 dnspod.cn 老 token API(走 dnsapi.cn),跟腾讯云 v3 SDK 是两套
// 凭据 JSON key 沿用 acme/dns_providers.go:
//
//	DNSPOD_API_KEY  - 合并 token 格式 "token_id,token"
//	DNSPOD_LINE     - 可选,默认 "默认"
//
// dnspod-go 不支持 context,这里在 select { case <-ctx.Done() } 外面包一层 hand-rolled 超时,
// 同步阻塞调用,失败不影响主流程。
type dnspodProvider struct {
	client *dnspod.Client
	line   string
}

func newDNSPodProvider(creds map[string]string) (*dnspodProvider, error) {
	token := strings.TrimSpace(creds["DNSPOD_API_KEY"])
	if token == "" {
		return nil, errors.New("dnspod: missing DNSPOD_API_KEY (format: token_id,token)")
	}
	if !strings.Contains(token, ",") {
		return nil, errors.New("dnspod: DNSPOD_API_KEY must be \"<token_id>,<token>\" format")
	}
	cli := dnspod.NewClient(dnspod.CommonParams{LoginToken: token, Format: "json"})
	line := strings.TrimSpace(creds["DNSPOD_LINE"])
	if line == "" {
		line = "默认"
	}
	return &dnspodProvider{client: cli, line: line}, nil
}

func (p *dnspodProvider) UpsertRecord(ctx context.Context, fqdn string, recordType string, content string, ttl int) error {
	zone, sub, err := SplitFQDN(fqdn)
	if err != nil {
		return fmt.Errorf("split fqdn: %w", err)
	}
	if sub == "" {
		sub = "@"
	}

	// dnspod-go 的 Records.List/Create/Update 都用 domain name 字符串(SDK 内部 add domain 字段),
	// 不需要先 Domains.List 查 id。
	if err := ctx.Err(); err != nil {
		return err
	}
	records, _, err := p.client.Records.List(zone, sub)
	if err != nil {
		// 部分错误信息含「记录数据为空」,视为无记录走 create 分支
		if strings.Contains(err.Error(), "Record list is empty") || strings.Contains(err.Error(), "No records") {
			return p.createRecord(zone, sub, recordType, content, ttl)
		}
		return fmt.Errorf("list records: %w", err)
	}

	var existingID, existingValue string
	for _, rec := range records {
		if rec.Name == sub && strings.EqualFold(rec.Type, recordType) {
			existingID = rec.ID
			existingValue = rec.Value
			if rec.Line == p.line {
				break
			}
		}
	}

	if existingID != "" {
		if existingValue == content {
			return nil
		}
		rec := dnspod.Record{
			Name:  sub,
			Type:  recordType,
			Line:  p.line,
			Value: content,
		}
		if ttl > 0 {
			rec.TTL = fmt.Sprintf("%d", ttl)
		}
		if _, _, err := p.client.Records.Update(zone, existingID, rec); err != nil {
			return fmt.Errorf("update record: %w", err)
		}
		return nil
	}
	return p.createRecord(zone, sub, recordType, content, ttl)
}

func (p *dnspodProvider) createRecord(zone, sub, recordType, content string, ttl int) error {
	rec := dnspod.Record{
		Name:  sub,
		Type:  recordType,
		Line:  p.line,
		Value: content,
	}
	if ttl > 0 {
		rec.TTL = fmt.Sprintf("%d", ttl)
	}
	if _, _, err := p.client.Records.Create(zone, rec); err != nil {
		return fmt.Errorf("create record: %w", err)
	}
	return nil
}
