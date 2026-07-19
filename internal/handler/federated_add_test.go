package handler

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func TestValidateFederationOwnerURLRejectsUnsafeTargets(t *testing.T) {
	tests := []string{
		"http://127.0.0.1",
		"http://10.0.0.1",
		"http://169.254.169.254",
		"http://[::1]",
		"ftp://8.8.8.8",
		"https://user:pass@8.8.8.8",
		"https://8.8.8.8/private/path",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := validateFederationOwnerURL(context.Background(), raw); err == nil {
				t.Fatalf("validateFederationOwnerURL(%q) unexpectedly succeeded", raw)
			}
		})
	}
}

func TestValidateFederationOwnerURLAcceptsPublicRoot(t *testing.T) {
	parsed, err := validateFederationOwnerURL(context.Background(), "https://8.8.8.8:8443/")
	if err != nil {
		t.Fatalf("validateFederationOwnerURL() error = %v", err)
	}
	if parsed.String() != "https://8.8.8.8:8443/" {
		t.Fatalf("parsed URL = %q", parsed.String())
	}
}

func TestFederationClientRejectsRedirects(t *testing.T) {
	client := newFederationHTTPClient()
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "8.8.8.8"}}
	if err := client.CheckRedirect(req, nil); err == nil {
		t.Fatal("CheckRedirect() unexpectedly allowed redirect")
	}
}
