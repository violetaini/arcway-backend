package handler

import (
	"errors"
	"testing"
)

func TestClassifyForwardingGuardPortConflicts(t *testing.T) {
	for _, body := range []string{
		`expiry guard returned HTTP 409: {"code":"port_in_use"}`,
		`expiry guard returned HTTP 409: {"code": "port_reserved"}`,
	} {
		if err := classifyForwardingGuardError(errors.New(body)); !errors.Is(err, ErrForwardTunnelPortInUse) {
			t.Fatalf("expected port conflict sentinel for %q, got %v", body, err)
		}
	}
	if err := classifyForwardingGuardError(errors.New("network unavailable")); errors.Is(err, ErrForwardTunnelPortInUse) {
		t.Fatalf("unexpected port conflict classification: %v", err)
	}
}

func TestValidateForwardingGuardACK(t *testing.T) {
	valid := `{"success":true,"resource":{"resource_id":"rd_12345678","tag":"rd-tun-test","generation":3,"state":"active"}}`
	if err := validateForwardingGuardACK([]byte(valid), "rd_12345678", "rd-tun-test", 3, "active"); err != nil {
		t.Fatalf("expected successful acknowledgement: %v", err)
	}
	for _, body := range []string{
		`{"success":true}`,
		`{"success":false}`,
		`{"success":true,"resource":{"resource_id":"rd_12345678","tag":"rd-tun-test","generation":3,"state":"delete_pending"}}`,
		`{"success":true,"resource":{"resource_id":"other","tag":"rd-tun-test","generation":3,"state":"active"}}`,
		`{}`,
		`not-json`,
	} {
		if err := validateForwardingGuardACK([]byte(body), "rd_12345678", "rd-tun-test", 3, "active"); err == nil {
			t.Fatalf("expected acknowledgement rejection for %q", body)
		}
	}
}

func TestForwardingGuardErrorCode(t *testing.T) {
	if got := forwardingGuardErrorCode([]byte(`{"success":false,"code":"not_found"}`)); got != "not_found" {
		t.Fatalf("unexpected code %q", got)
	}
	if got := forwardingGuardErrorCode([]byte(`not-json`)); got != "" {
		t.Fatalf("unexpected malformed response code %q", got)
	}
}
