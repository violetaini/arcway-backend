package guardwire

import (
	"bytes"
	"testing"
	"time"
)

func TestSealOpenAndTamperResistance(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	body, metadata, err := Seal("stable-secret", "PUT", "/v1/schedules", []byte(`{"secret":"credential"}`), now)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := Open("stable-secret", "PUT", "/v1/schedules", body, metadata, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plaintext, []byte(`{"secret":"credential"}`)) {
		t.Fatalf("plaintext = %q", plaintext)
	}
	body[len(body)-1] ^= 1
	if _, err := Open("stable-secret", "PUT", "/v1/schedules", body, metadata, now); err == nil {
		t.Fatal("tampered payload was accepted")
	}
}

func TestOpenRejectsExpiredMetadata(t *testing.T) {
	now := time.Now().UTC()
	body, metadata, err := Seal("stable-secret", "GET", "/v1/capabilities", []byte(`{}`), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open("stable-secret", "GET", "/v1/capabilities", body, metadata, now.Add(MaxClockSkew+time.Second)); err == nil {
		t.Fatal("expired request was accepted")
	}
}
