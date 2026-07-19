package ddns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	openapimodels "github.com/alibabacloud-go/darabonba-openapi/v2/models"
	"github.com/alibabacloud-go/tea/dara"
	alidns "github.com/go-acme/alidns-20150109/v4/client"
)

// alidnsProvider 阿里云 DNS。凭据 JSON key 沿用 acme/dns_providers.go:
//
//	ALICLOUD_ACCESS_KEY    — AccessKey ID
//	ALICLOUD_SECRET_KEY    — AccessKey Secret
//
// 阿里 RR 字段是 sub(不含 zone),根域用 "@"。
type alidnsProvider struct {
	client *alidns.Client
}

func newAlidnsProvider(creds map[string]string) (*alidnsProvider, error) {
	ak := strings.TrimSpace(creds["ALICLOUD_ACCESS_KEY"])
	sk := strings.TrimSpace(creds["ALICLOUD_SECRET_KEY"])
	if ak == "" || sk == "" {
		return nil, errors.New("alidns: missing ALICLOUD_ACCESS_KEY / ALICLOUD_SECRET_KEY")
	}
	cfg := &openapimodels.Config{
		AccessKeyId:     dara.String(ak),
		AccessKeySecret: dara.String(sk),
		Endpoint:        dara.String("alidns.aliyuncs.com"),
	}
	cli, err := alidns.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("alidns: NewClient: %w", err)
	}
	return &alidnsProvider{client: cli}, nil
}

func (p *alidnsProvider) UpsertRecord(ctx context.Context, fqdn string, recordType string, content string, ttl int) error {
	zone, sub, err := SplitFQDN(fqdn)
	if err != nil {
		return fmt.Errorf("split fqdn: %w", err)
	}
	rr := sub
	if rr == "" {
		rr = "@"
	}

	// 找已有 record
	listReq := &alidns.DescribeDomainRecordsRequest{
		DomainName:  dara.String(zone),
		RRKeyWord:   dara.String(rr),
		TypeKeyWord: dara.String(recordType),
	}
	listResp, err := alidns.DescribeDomainRecordsWithContext(ctx, p.client, listReq, &dara.RuntimeOptions{})
	if err != nil {
		return fmt.Errorf("describe domain records: %w", err)
	}

	var existingID, existingValue string
	if listResp != nil && listResp.Body != nil && listResp.Body.DomainRecords != nil {
		for _, rec := range listResp.Body.DomainRecords.Record {
			if rec == nil {
				continue
			}
			// API 是 keyword 模糊匹配,必须二次校验精确等于 rr+type
			if dara.StringValue(rec.RR) == rr && dara.StringValue(rec.Type) == recordType {
				existingID = dara.StringValue(rec.RecordId)
				existingValue = dara.StringValue(rec.Value)
				break
			}
		}
	}

	if existingID != "" {
		if existingValue == content {
			return nil // 无变更
		}
		updReq := &alidns.UpdateDomainRecordRequest{
			RecordId: dara.String(existingID),
			RR:       dara.String(rr),
			Type:     dara.String(recordType),
			Value:    dara.String(content),
		}
		if ttl > 0 {
			updReq.TTL = dara.Int64(int64(ttl))
		}
		if _, err := alidns.UpdateDomainRecordWithContext(ctx, p.client, updReq, &dara.RuntimeOptions{}); err != nil {
			return fmt.Errorf("update domain record: %w", err)
		}
		return nil
	}

	addReq := &alidns.AddDomainRecordRequest{
		DomainName: dara.String(zone),
		RR:         dara.String(rr),
		Type:       dara.String(recordType),
		Value:      dara.String(content),
	}
	if ttl > 0 {
		addReq.TTL = dara.Int64(int64(ttl))
	}
	if _, err := alidns.AddDomainRecordWithContext(ctx, p.client, addReq, &dara.RuntimeOptions{}); err != nil {
		return fmt.Errorf("add domain record: %w", err)
	}
	return nil
}
