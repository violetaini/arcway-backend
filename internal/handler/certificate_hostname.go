package handler

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"
)

func certificateLeaf(certPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("certificate PEM has no leaf certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func certificateDNSNames(certPEM string) []string {
	leaf, err := certificateLeaf(certPEM)
	if err != nil {
		return nil
	}
	return append([]string(nil), leaf.DNSNames...)
}

func certificateCoversHostname(certPEM, keyPEM, hostname string, now time.Time) bool {
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil || len(pair.Certificate) == 0 {
		return false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return false
	}
	return leaf.VerifyHostname(strings.ToLower(strings.TrimSpace(hostname))) == nil
}

func certificateNameCoversHostname(name, hostname string) bool {
	name = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(name, ".")))
	hostname = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(hostname, ".")))
	if name == "" || hostname == "" {
		return false
	}
	if name == hostname {
		return true
	}
	if !strings.HasPrefix(name, "*.") {
		return false
	}
	suffix := strings.TrimPrefix(name, "*.")
	return strings.HasSuffix(hostname, "."+suffix) &&
		len(strings.Split(hostname, ".")) == len(strings.Split(suffix, "."))+1
}
