package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"miaomiaowux/internal/storage"
)

func newExpiryGuardAssetHandler(t *testing.T) (*XrayServerHandler, string) {
	t.Helper()
	repo, err := storage.NewTrafficRepository(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatalf("NewTrafficRepository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	token := "remote-asset-token"
	server := &storage.RemoteServer{
		Name: "asset-edge", Token: token, Status: storage.RemoteServerStatusConnected,
		ConnectionMode: storage.ConnectionModeWebSocket,
	}
	if err := repo.CreateRemoteServer(context.Background(), server); err != nil {
		t.Fatalf("CreateRemoteServer: %v", err)
	}
	return NewXrayServerHandler(repo, nil, nil), token
}

func requestExpiryGuardAsset(handler *XrayServerHandler, method, arch, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "/api/remote/expiry-guard?arch="+arch, nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	handler.GetExpiryGuardAsset(response, request)
	return response
}

func TestExpiryGuardAssetRequiresRemoteBearerToken(t *testing.T) {
	handler, token := newExpiryGuardAssetHandler(t)

	for _, authorization := range []string{"", "Bearer invalid-token", token, "Basic " + token} {
		response := requestExpiryGuardAsset(handler, http.MethodGet, "amd64", authorization)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("Authorization %q status=%d want=%d", authorization, response.Code, http.StatusUnauthorized)
		}
		if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("security headers missing: %#v", response.Header())
		}
	}
}

func TestExpiryGuardAssetValidatesArchitectureAndMissingAsset(t *testing.T) {
	handler, token := newExpiryGuardAssetHandler(t)
	t.Setenv(expiryGuardAssetDirEnv, t.TempDir())

	invalid := requestExpiryGuardAsset(handler, http.MethodGet, "386", "Bearer "+token)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid architecture status=%d want=%d", invalid.Code, http.StatusBadRequest)
	}

	missing := requestExpiryGuardAsset(handler, http.MethodGet, "arm64", "Bearer "+token)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing asset status=%d want=%d body=%s", missing.Code, http.StatusNotFound, missing.Body.String())
	}
}

func TestExpiryGuardAssetServesConfiguredBinary(t *testing.T) {
	handler, token := newExpiryGuardAssetHandler(t)
	directory := t.TempDir()
	t.Setenv(expiryGuardAssetDirEnv, directory)
	const content = "guard-binary-content"
	name := "arcway-expiry-guard-linux-amd64"
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0755); err != nil {
		t.Fatal(err)
	}

	response := requestExpiryGuardAsset(handler, http.MethodGet, "amd64", "bearer "+token)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if response.Body.String() != content {
		t.Fatalf("body=%q want=%q", response.Body.String(), content)
	}
	if got := response.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type=%q", got)
	}
	if got := response.Header().Get("Content-Disposition"); got != `attachment; filename="`+name+`"` {
		t.Fatalf("Content-Disposition=%q", got)
	}
}

func TestExpiryGuardAssetRejectsNonRegularAsset(t *testing.T) {
	handler, token := newExpiryGuardAssetHandler(t)
	directory := t.TempDir()
	t.Setenv(expiryGuardAssetDirEnv, directory)
	name := "arcway-expiry-guard-linux-amd64"
	if err := os.Symlink(filepath.Join(directory, "does-not-matter"), filepath.Join(directory, name)); err != nil {
		t.Fatal(err)
	}

	response := requestExpiryGuardAsset(handler, http.MethodGet, "amd64", "Bearer "+token)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("symlink status=%d want=%d", response.Code, http.StatusInternalServerError)
	}
}

func TestRemoteInstallScriptInstallsExpiryGuard(t *testing.T) {
	handler, token := newExpiryGuardAssetHandler(t)
	request := httptest.NewRequest(http.MethodGet,
		"https://panel.example/api/remote/install.sh?listen_port=25000", nil)
	request.Host = "panel.example"
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.GetRemoteInstallScript(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", response.Code, http.StatusOK, response.Body.String())
	}

	script := response.Body.String()
	for _, expected := range []string{
		"github.com/iluobei/mmw-agent/releases/download/${AGENT_VERSION}/mmw-agent-linux-${ARCH_NAME}",
		"AGENT_VERSION=\"v0.3.7\"",
		"6ce2faac96f82a501ab86b1817c332bf05239ba10e36b5be0dd11995a5a1bf2f",
		"04ba897947923592846d3e57282d5ac80c213892b125c1575a8664abb770168f",
		"mmw-agent SHA-256 校验失败",
		"/api/remote/expiry-guard?arch=${ARCH_NAME}",
		"Authorization: Bearer ${TOKEN}",
		"TRY_GUARD_PORT=$((TRY_PORT + 1))",
		"ARCWAY_GUARD_LISTEN=[::]:${GUARD_PORT}",
		"ARCWAY_GUARD_SECRET=${GUARD_SECRET}",
		"ARCWAY_AGENT_TOKEN=${TOKEN}",
		"hide_port_on_ws: false",
		"mktemp -d /tmp/arcway-install.XXXXXX",
		"validate_elf()",
		"chmod 0600 /var/lib/arcway-expiry-guard/state.json",
		"ufw allow \"${GUARD_PORT}/tcp\"",
		"/etc/systemd/system/arcway-expiry-guard.service",
		"/etc/init.d/arcway-expiry-guard",
		"/usr/local/bin/arcway-expiry-guard-supervisor.sh",
	} {
		if !strings.Contains(script, expected) {
			t.Errorf("install script missing %q", expected)
		}
	}
	if strings.Contains(script, "ARCWAY_GUARD_SECRET=${TOKEN}") {
		t.Fatal("install script reused the rotating Agent token as the guard secret")
	}
	for _, forbidden := range []string{"gh-proxy.com", "mirror.ghproxy.com", "1ms.cc"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("install script uses untrusted download proxy %q", forbidden)
		}
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		command := exec.Command(bash, "-n")
		command.Stdin = strings.NewReader(script)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("generated install script failed bash -n: %v\n%s", err, output)
		}
	}
}

func TestRemoteInstallScriptRejectsQueryToken(t *testing.T) {
	handler, token := newExpiryGuardAssetHandler(t)
	request := httptest.NewRequest(http.MethodGet,
		"https://panel.example/api/remote/install.sh?token="+token, nil)
	response := httptest.NewRecorder()
	handler.GetRemoteInstallScript(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want=%d", response.Code, http.StatusUnauthorized)
	}
}
