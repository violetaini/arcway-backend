package handler

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const expiryGuardAssetDirEnv = "ARCWAY_GUARD_ASSET_DIR"

var errExpiryGuardAssetNotFound = errors.New("expiry guard asset not found")

// GetExpiryGuardAsset serves the panel-built expiry guard to an authenticated
// remote server. The architecture allow-list also makes the requested filename
// independent from untrusted path input.
func (h *XrayServerHandler) GetExpiryGuardAsset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token, ok := remoteBearerToken(r.Header.Get("Authorization"))
	if !ok || h == nil || h.repo == nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="remote-server"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	server, err := h.repo.GetRemoteServerByToken(r.Context(), token)
	if err != nil || (server.TokenExpiresAt != nil && !server.TokenExpiresAt.After(time.Now())) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="remote-server"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	arch := strings.TrimSpace(r.URL.Query().Get("arch"))
	if arch != "amd64" && arch != "arm64" {
		http.Error(w, "Unsupported architecture", http.StatusBadRequest)
		return
	}
	name := "arcway-expiry-guard-linux-" + arch
	asset, err := openExpiryGuardAsset(name)
	if errors.Is(err, errExpiryGuardAssetNotFound) {
		http.Error(w, "Expiry guard asset unavailable", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Expiry guard asset unavailable", http.StatusInternalServerError)
		return
	}
	defer asset.Close()

	info, err := asset.Stat()
	if err != nil {
		http.Error(w, "Expiry guard asset unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	http.ServeContent(w, r, name, info.ModTime(), asset)
}

func remoteBearerToken(authorization string) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(authorization), " ")
	token = strings.TrimSpace(token)
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}

func expiryGuardAssetDirectories() []string {
	directories := make([]string, 0, 4)
	if configured := strings.TrimSpace(os.Getenv(expiryGuardAssetDirEnv)); configured != "" {
		directories = append(directories, configured)
	}
	if executable, err := os.Executable(); err == nil {
		directory := filepath.Dir(executable)
		directories = append(directories, directory, filepath.Join(directory, "guard-assets"))
	}
	if workingDirectory, err := os.Getwd(); err == nil {
		directories = append(directories, filepath.Join(workingDirectory, "guard-assets"))
	}

	seen := make(map[string]struct{}, len(directories))
	unique := directories[:0]
	for _, directory := range directories {
		absolute, err := filepath.Abs(directory)
		if err != nil {
			continue
		}
		absolute = filepath.Clean(absolute)
		if _, exists := seen[absolute]; exists {
			continue
		}
		seen[absolute] = struct{}{}
		unique = append(unique, absolute)
	}
	return unique
}

func openExpiryGuardAsset(name string) (*os.File, error) {
	if name != "arcway-expiry-guard-linux-amd64" && name != "arcway-expiry-guard-linux-arm64" {
		return nil, errExpiryGuardAssetNotFound
	}
	for _, directory := range expiryGuardAssetDirectories() {
		path := filepath.Join(directory, name)
		linkInfo, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect expiry guard asset: %w", err)
		}
		if !linkInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("expiry guard asset is not a regular file")
		}
		asset, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open expiry guard asset: %w", err)
		}
		openedInfo, err := asset.Stat()
		if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(linkInfo, openedInfo) {
			asset.Close()
			return nil, fmt.Errorf("expiry guard asset changed while opening")
		}
		return asset, nil
	}
	return nil, errExpiryGuardAssetNotFound
}
