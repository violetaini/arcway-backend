package handler

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func generatedSnapshotFunction(t *testing.T) string {
	t.Helper()
	t.Setenv(panelSourceIPsEnv, "203.0.113.10")
	handler, token := newExpiryGuardAssetHandler(t)
	request := httptest.NewRequest(http.MethodGet, "https://panel.example/api/remote/install.sh", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.GetRemoteInstallScript(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("generate installer status=%d body=%s", response.Code, response.Body.String())
	}

	const startMarker = "snapshot_quiesced_mutable_state() {"
	const endMarker = "\n}\nfor TRACKED_PATH in"
	start := strings.Index(response.Body.String(), startMarker)
	if start < 0 {
		t.Fatal("generated installer is missing snapshot_quiesced_mutable_state")
	}
	tail := response.Body.String()[start:]
	end := strings.Index(tail, endMarker)
	if end < 0 {
		t.Fatal("generated snapshot function has no terminator")
	}
	return tail[:end+2]
}

func TestQuiescedSnapshotIsGroupedAndRestoresPreviousGenerationOnSwapFailure(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	root := t.TempDir()
	stateOne := filepath.Join(root, "state-one")
	stateTwo := filepath.Join(root, "state-two")
	external := filepath.Join(root, "external-state")
	link := filepath.Join(root, "state-link")
	missing := filepath.Join(root, "missing-state")
	download := filepath.Join(root, "download")

	harness := `set -euo pipefail
DOWNLOAD_DIR=` + shellSingleQuote(download) + `
BACKUP_DIR="$DOWNLOAD_DIR/backup"
STATE_ONE=` + shellSingleQuote(stateOne) + `
STATE_TWO=` + shellSingleQuote(stateTwo) + `
EXTERNAL=` + shellSingleQuote(external) + `
LINK=` + shellSingleQuote(link) + `
MISSING=` + shellSingleQuote(missing) + `
TRACKED_PATHS=("$STATE_ONE" "$STATE_TWO" "$LINK" "$EXTERNAL" "$MISSING")
mkdir -p "$STATE_ONE" "$STATE_TWO" "$EXTERNAL" "$BACKUP_DIR"
printf '%s\n' generation-one-a > "$STATE_ONE/value"
printf '%s\n' generation-one-b > "$STATE_TWO/value"
printf '%s\n' generation-one-external > "$EXTERNAL/value"
ln -s "$EXTERNAL" "$LINK"
printf '%s\n' stale-backup > "$BACKUP_DIR/stale"
` + generatedSnapshotFunction(t) + `
snapshot_quiesced_mutable_state
test "$(cat "$BACKUP_DIR$STATE_ONE/value")" = generation-one-a
test "$(cat "$BACKUP_DIR$STATE_TWO/value")" = generation-one-b
test "$(cat "$BACKUP_DIR$EXTERNAL/value")" = generation-one-external
test -L "$BACKUP_DIR$LINK"
test "$(readlink "$BACKUP_DIR$LINK")" = "$EXTERNAL"
test -f "$BACKUP_DIR$MISSING.arcway-missing"
test ! -e "$BACKUP_DIR/stale"

printf '%s\n' generation-two-a > "$STATE_ONE/value"
printf '%s\n' generation-two-b > "$STATE_TWO/value"
printf '%s\n' generation-two-external > "$EXTERNAL/value"
swap_failed=0
mv() {
    if [ "$swap_failed" = 0 ] && [ "${2:-}" = "$BACKUP_DIR" ] && [ "${1:-}" != "$BACKUP_DIR" ]; then
        swap_failed=1
        return 1
    fi
    command mv "$@"
}
if snapshot_quiesced_mutable_state; then
    echo 'snapshot unexpectedly survived injected group-swap failure' >&2
    exit 1
fi
unset -f mv
test "$swap_failed" = 1
test "$(cat "$BACKUP_DIR$STATE_ONE/value")" = generation-one-a
test "$(cat "$BACKUP_DIR$STATE_TWO/value")" = generation-one-b
test "$(cat "$BACKUP_DIR$EXTERNAL/value")" = generation-one-external
test -L "$BACKUP_DIR$LINK"
test -f "$BACKUP_DIR$MISSING.arcway-missing"
`

	command := exec.Command(bash, "-c", harness)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("snapshot behavior failed: %v\n%s", err, output)
	}
}
