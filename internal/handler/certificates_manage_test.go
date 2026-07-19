package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"miaomiaowux/internal/storage"
)

func TestPublicDNSProviderNeverSerializesCredentials(t *testing.T) {
	provider := storage.DNSProvider{
		ID:           7,
		Name:         "Cloudflare",
		ProviderType: "cloudflare",
		Credentials:  `{"CF_DNS_API_TOKEN":"top-secret-token"}`,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	payload, err := json.Marshal(publicDNSProvider(provider))
	if err != nil {
		t.Fatalf("marshal public provider: %v", err)
	}
	text := string(payload)
	if strings.Contains(text, "top-secret-token") || strings.Contains(text, "credentials\"") {
		t.Fatalf("response leaked credentials: %s", text)
	}
	if !strings.Contains(text, `"credentials_configured":true`) {
		t.Fatalf("response should retain non-secret configured state: %s", text)
	}
}
