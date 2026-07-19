package ddns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	dnspod "github.com/go-acme/tencentclouddnspod/v20210323"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

// tencentProvider 腾讯云 DNSPod(v3 OpenAPI),跟 nrdcg/dnspod-go(老 dnsapi.cn token API)是两套
// 凭据 JSON key 沿用 acme/dns_providers.go:
//
//	TENCENTCLOUD_SECRET_ID
//	TENCENTCLOUD_SECRET_KEY
//	TENCENTCLOUD_DNS_LINE   - 可选,默认 "默认";国际版账号要换 "Default"
type tencentProvider struct {
	client *dnspod.Client
	line   string
}

func newTencentProvider(creds map[string]string) (*tencentProvider, error) {
	id := strings.TrimSpace(creds["TENCENTCLOUD_SECRET_ID"])
	key := strings.TrimSpace(creds["TENCENTCLOUD_SECRET_KEY"])
	if id == "" || key == "" {
		return nil, errors.New("tencent: missing TENCENTCLOUD_SECRET_ID / TENCENTCLOUD_SECRET_KEY")
	}
	cred := common.NewCredential(id, key)
	cli, err := dnspod.NewClient(cred, "", profile.NewClientProfile())
	if err != nil {
		return nil, fmt.Errorf("tencent: NewClient: %w", err)
	}
	line := strings.TrimSpace(creds["TENCENTCLOUD_DNS_LINE"])
	if line == "" {
		line = "默认"
	}
	return &tencentProvider{client: cli, line: line}, nil
}

func (p *tencentProvider) UpsertRecord(ctx context.Context, fqdn string, recordType string, content string, ttl int) error {
	zone, sub, err := SplitFQDN(fqdn)
	if err != nil {
		return fmt.Errorf("split fqdn: %w", err)
	}
	if sub == "" {
		sub = "@"
	}

	listReq := dnspod.NewDescribeRecordListRequest()
	listReq.Domain = &zone
	listReq.Subdomain = &sub
	listReq.RecordType = &recordType
	listResp, err := dnspod.DescribeRecordListWithContext(ctx, p.client, listReq)
	if err != nil {
		// 「没有记录」会被 SDK 当 error 返回(ResourceNotFound.NoDataOfRecord);视为空 list
		if strings.Contains(err.Error(), "ResourceNotFound.NoDataOfRecord") {
			return p.createRecord(ctx, zone, sub, recordType, content, ttl)
		}
		return fmt.Errorf("describe record list: %w", err)
	}

	var existingID *uint64
	var existingValue string
	if listResp != nil && listResp.Response != nil {
		for _, rec := range listResp.Response.RecordList {
			if rec == nil {
				continue
			}
			// API 已按 Subdomain+RecordType 过滤,但同 sub+type 可能有多条线路记录,挑「默认」线路
			if rec.Name != nil && *rec.Name == sub && rec.Type != nil && *rec.Type == recordType {
				existingID = rec.RecordId
				if rec.Value != nil {
					existingValue = *rec.Value
				}
				if rec.Line != nil && *rec.Line == p.line {
					break // 优先「默认」线路
				}
			}
		}
	}

	if existingID != nil {
		if existingValue == content {
			return nil
		}
		modReq := dnspod.NewModifyRecordRequest()
		modReq.Domain = &zone
		modReq.SubDomain = &sub
		modReq.RecordType = &recordType
		modReq.RecordLine = &p.line
		modReq.RecordId = existingID
		modReq.Value = &content
		if ttl > 0 {
			t := uint64(ttl)
			modReq.TTL = &t
		}
		if _, err := dnspod.ModifyRecordWithContext(ctx, p.client, modReq); err != nil {
			return fmt.Errorf("modify record: %w", err)
		}
		return nil
	}
	return p.createRecord(ctx, zone, sub, recordType, content, ttl)
}

func (p *tencentProvider) createRecord(ctx context.Context, zone, sub, recordType, content string, ttl int) error {
	createReq := dnspod.NewCreateRecordRequest()
	createReq.Domain = &zone
	createReq.SubDomain = &sub
	createReq.RecordType = &recordType
	createReq.RecordLine = &p.line
	createReq.Value = &content
	if ttl > 0 {
		t := uint64(ttl)
		createReq.TTL = &t
	}
	if _, err := dnspod.CreateRecordWithContext(ctx, p.client, createReq); err != nil {
		return fmt.Errorf("create record: %w", err)
	}
	return nil
}
