package handler

// 妙妙屋(mmw)→ 妙妙屋X 迁移工具的后端实现。
// 当前只实现"自动拉取备份"接口,其余 (import-mmw / claim-*) 后续在引导页评审通过后逐步补。

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"miaomiaowux/internal/storage"
)

const (
	migrateWorkDir      = "/tmp/mmwx-migrate"
	defaultFetchTimeout = 5 * time.Minute // mmw 备份可能几十 MB,跨网络下载留足时间
	maxBackupSizeBytes  = 500 << 20       // 500 MB,防止恶意 URL 让主控 OOM
	migrateWorkPerm     = 0o755
	migrateTempFilePerm = 0o644
)

// MigrateHandler 处理 /api/admin/migrate/* 系列接口。
type MigrateHandler struct {
	repo *storage.TrafficRepository
	rm   *RemoteManageHandler
}

func NewMigrateHandler(repo *storage.TrafficRepository, rm *RemoteManageHandler) *MigrateHandler {
	return &MigrateHandler{repo: repo, rm: rm}
}

// ------- POST /api/admin/migrate/fetch-mmw-backup -------

type fetchMmwBackupReq struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	TOTP     string `json:"totp"`
}

type fetchMmwBackupResp struct {
	Success        bool   `json:"success"`
	BackupPath     string `json:"backup_path"`     // 完整 zip 暂存路径
	DBPath         string `json:"db_path"`         // 解压出来的 mmw.db 路径
	SubscribesDir  string `json:"subscribes_dir"`  // 解压出来的 subscribes/ 目录(可能为空字符串)
	SubscribeCount int    `json:"subscribe_count"` // subscribes 目录内文件数
	SizeBytes      int64  `json:"size_bytes"`      // zip 大小
	DBSizeBytes    int64  `json:"db_size_bytes"`   // 解压后 db 大小
}

func (h *MigrateHandler) FetchMmwBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "only POST")
		return
	}
	var req fetchMmwBackupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.URL = strings.TrimRight(strings.TrimSpace(req.URL), "/")
	req.Username = strings.TrimSpace(req.Username)
	if req.URL == "" || req.Username == "" || req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "url, username, password 必填")
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		writeJSONError(w, http.StatusBadRequest, "url 必须以 http(s):// 开头")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), defaultFetchTimeout)
	defer cancel()
	// 不在 client 上设 Timeout — 完全靠 ctx 控制,避免和 ctx 重复
	client := &http.Client{}

	// 1. 登录 mmw 拿 token
	token, err := mmwLogin(ctx, client, req.URL, req.Username, req.Password, req.TOTP)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("登录妙妙屋失败: %v", err))
		return
	}

	// 2. 准备暂存目录 + 随机文件名(防并发覆盖)
	if err := os.MkdirAll(migrateWorkDir, migrateWorkPerm); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("创建工作目录失败: %v", err))
		return
	}
	id, err := randomHex(8)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("生成随机 id 失败: %v", err))
		return
	}
	zipPath := filepath.Join(migrateWorkDir, id+".zip")

	// 3. 调 mmw 备份下载接口
	dlReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, req.URL+"/api/admin/backup/download", nil)
	dlReq.Header.Set("MM-Authorization", token)
	dlResp, err := client.Do(dlReq)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("下载备份失败: %v", err))
		return
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(dlResp.Body, 1024))
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("下载备份失败 status=%d body=%s", dlResp.StatusCode, string(b)))
		return
	}

	zf, err := os.OpenFile(zipPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, migrateTempFilePerm)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("打开本地暂存文件失败: %v", err))
		return
	}
	// 用 LimitReader 防"恶意巨大 zip"
	n, copyErr := io.Copy(zf, io.LimitReader(dlResp.Body, maxBackupSizeBytes+1))
	zf.Close()
	if copyErr != nil {
		os.Remove(zipPath)
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("写入本地暂存失败: %v", copyErr))
		return
	}
	if n > maxBackupSizeBytes {
		os.Remove(zipPath)
		writeJSONError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("备份大小超过限制 (%d MB)", maxBackupSizeBytes>>20))
		return
	}

	// 4. 解压 zip 中的 *.db 文件 + subscribes/ 目录到独立路径(后续 import 直接用)
	dbPath := filepath.Join(migrateWorkDir, id+".db")
	subsDir := filepath.Join(migrateWorkDir, id+"-subs")
	dbSize, subCount, err := extractMmwBackup(zipPath, dbPath, subsDir)
	if err != nil {
		os.Remove(zipPath)
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("解压备份失败: %v", err))
		return
	}

	log.Printf("[Migrate] fetched mmw backup from %s: zip=%d bytes db=%d bytes subs=%d files", req.URL, n, dbSize, subCount)

	respondJSON(w, http.StatusOK, fetchMmwBackupResp{
		Success:        true,
		BackupPath:     zipPath,
		DBPath:         dbPath,
		SubscribesDir:  subsDir,
		SubscribeCount: subCount,
		SizeBytes:      n,
		DBSizeBytes:    dbSize,
	})
}

// ------- POST /api/admin/migrate/upload-mmw-backup -------
// 用户上传妙妙屋后台备份 zip(同 fetch 接口拿到的格式)。
type uploadMmwBackupResp = fetchMmwBackupResp

const maxUploadBytes = 500 << 20

func (h *MigrateHandler) UploadMmwBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "only POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	file, header, err := r.FormFile("backup")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("读取上传文件失败: %v", err))
		return
	}
	defer file.Close()
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".zip") {
		writeJSONError(w, http.StatusBadRequest, "请上传妙妙屋后台导出的 zip 备份")
		return
	}

	if err := os.MkdirAll(migrateWorkDir, migrateWorkPerm); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("创建工作目录失败: %v", err))
		return
	}
	id, err := randomHex(8)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("生成随机 id 失败: %v", err))
		return
	}
	zipPath := filepath.Join(migrateWorkDir, id+".zip")
	out, err := os.OpenFile(zipPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, migrateTempFilePerm)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("写本地暂存失败: %v", err))
		return
	}
	n, copyErr := io.Copy(out, file)
	out.Close()
	if copyErr != nil {
		os.Remove(zipPath)
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("流式写入失败: %v", copyErr))
		return
	}

	dbPath := filepath.Join(migrateWorkDir, id+".db")
	subsDir := filepath.Join(migrateWorkDir, id+"-subs")
	dbSize, subCount, err := extractMmwBackup(zipPath, dbPath, subsDir)
	if err != nil {
		os.Remove(zipPath)
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("解压备份失败: %v", err))
		return
	}

	log.Printf("[Migrate] uploaded mmw backup: zip=%d bytes db=%d bytes subs=%d files", n, dbSize, subCount)

	respondJSON(w, http.StatusOK, uploadMmwBackupResp{
		Success:        true,
		BackupPath:     zipPath,
		DBPath:         dbPath,
		SubscribesDir:  subsDir,
		SubscribeCount: subCount,
		SizeBytes:      n,
		DBSizeBytes:    dbSize,
	})
}

// mmwLogin 调 $baseURL/api/login,若需 2FA 再调 /api/login/2fa,最终返回 access token。
func mmwLogin(ctx context.Context, client *http.Client, baseURL, username, password, totp string) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var first map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&first); err != nil {
		return "", err
	}
	// 需要 2FA
	if needs, _ := first["requires_2fa"].(bool); needs {
		tfToken, _ := first["two_factor_token"].(string)
		if tfToken == "" {
			return "", errors.New("mmw 要求 2FA 但未返回 two_factor_token")
		}
		if strings.TrimSpace(totp) == "" {
			return "", errors.New("管理员账号开启了 2FA,请填写两步验证码后重试")
		}
		body2, _ := json.Marshal(map[string]string{
			"two_factor_token": tfToken,
			"code":             strings.TrimSpace(totp),
		})
		req2, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/login/2fa", bytes.NewReader(body2))
		req2.Header.Set("Content-Type", "application/json")
		resp2, err := client.Do(req2)
		if err != nil {
			return "", err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp2.Body, 512))
			return "", fmt.Errorf("2fa status=%d body=%s", resp2.StatusCode, strings.TrimSpace(string(b)))
		}
		var second map[string]any
		if err := json.NewDecoder(resp2.Body).Decode(&second); err != nil {
			return "", err
		}
		tok, _ := second["token"].(string)
		if tok == "" {
			return "", errors.New("2fa 响应中缺少 token")
		}
		return tok, nil
	}
	tok, _ := first["token"].(string)
	if tok == "" {
		return "", errors.New("登录响应中缺少 token (可能不是妙妙屋实例?)")
	}
	return tok, nil
}

// extractMmwBackup 从 zip 提取 mmw 备份的两部分:
//   - data/*.db → outDBPath(取第一个找到的 db 文件)
//   - subscribes/* → outSubsDir/ (每个文件展平,忽略原 zip 内子目录)
//
// 返回 (dbSize, subsFileCount, error)。
//
// 防御:跳过含 "..", 跳过目录条目,subscribe 文件总大小受 maxBackupSizeBytes 约束(zip 自身已被外层限速)。
func extractMmwBackup(zipPath, outDBPath, outSubsDir string) (int64, int, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return 0, 0, err
	}
	defer zr.Close()

	if err := os.MkdirAll(outSubsDir, migrateWorkPerm); err != nil {
		return 0, 0, fmt.Errorf("创建 subscribes 目录失败: %w", err)
	}

	var dbSize int64
	var dbFound bool
	subCount := 0

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if strings.Contains(f.Name, "..") {
			continue
		}
		// 把 zip 路径分隔符(始终是 '/')统一处理
		clean := strings.TrimLeft(f.Name, "/")

		// data/*.db → outDBPath(取第一个)
		if !dbFound && strings.HasPrefix(strings.ToLower(clean), "data/") && strings.HasSuffix(strings.ToLower(clean), ".db") {
			n, err := copyZipEntry(f, outDBPath)
			if err != nil {
				return 0, 0, fmt.Errorf("copy db: %w", err)
			}
			dbSize = n
			dbFound = true
			continue
		}

		// subscribes/<filename> → outSubsDir/<filename>(扁平化)
		if strings.HasPrefix(strings.ToLower(clean), "subscribes/") {
			base := filepath.Base(clean)
			if base == "" || base == "." {
				continue
			}
			target := filepath.Join(outSubsDir, base)
			if _, err := copyZipEntry(f, target); err != nil {
				return 0, 0, fmt.Errorf("copy subscribe %s: %w", clean, err)
			}
			subCount++
		}
	}

	if !dbFound {
		return 0, 0, errors.New("zip 中没有 data/*.db,该归档可能不是有效的妙妙屋备份")
	}
	return dbSize, subCount, nil
}

func copyZipEntry(f *zip.File, outPath string) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, migrateTempFilePerm)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	return io.Copy(out, rc)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ------- POST /api/admin/migrate/import-mmw -------

type importMmwReq struct {
	DBPath        string `json:"db_path"`        // mmw.db 路径
	SubscribesDir string `json:"subscribes_dir"` // 可选:解压后的 subscribes/ 目录,内部文件会被拷到 mmwx subscribes/
}

type importMmwResp struct {
	Success           bool                     `json:"success"`
	Report            *storage.MmwImportReport `json:"report"`
	OwnedByAdmin      string                   `json:"owned_by_admin"`     // 订阅 / 模板等被分配给哪个 admin 用户
	SubscribesCopied  int                      `json:"subscribes_copied"`  // 拷到 mmwx subscribes/ 的文件数
	SubscribesSkipped []string                 `json:"subscribes_skipped"` // 跳过的文件(同名已存在)
}

const mmwxSubscribesDir = "subscribes" // mmwx 默认订阅文件目录(相对工作目录)

// ImportMmw 把 mmw 备份完整导入当前 mmwx 实例:
//   - db_path:mmw.db,核心 9 张表合并到 mmwx db(INSERT OR IGNORE)
//   - subscribes_dir(可选):订阅 yaml 文件复制到 mmwx 的 subscribes/ 目录
//   - 把 subscribe_files / templates 的 created_by 字段填成"系统第一个 admin"用户名
//     (mmw 没有 created_by 概念,默认归属管理员;这样能进入"我的订阅 / 我的模板"列表)
func (h *MigrateHandler) ImportMmw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "only POST")
		return
	}
	var req importMmwReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	dbPath := strings.TrimSpace(req.DBPath)
	if dbPath == "" {
		writeJSONError(w, http.StatusBadRequest, "db_path 必填")
		return
	}
	if _, err := os.Stat(dbPath); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("db_path 不可读: %v", err))
		return
	}

	// 1. 导入 db 数据
	report, err := h.repo.ImportFromMmw(r.Context(), dbPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("导入失败: %v", err))
		return
	}

	// 2. 把 subscribe_files / templates 的空 created_by 设为系统 admin
	admin := h.repo.GetSystemNodeOwner(r.Context()) // 第一个 role='admin' 用户名
	if err := h.repo.AssignOwnershipForMmwImported(r.Context(), admin); err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("分配 created_by=%s 失败: %v", admin, err))
	}

	// 2.5 妙妙屋备份可能带入它自己的 admin → 系统出现多个管理员。
	//     只保留本实例第一个用户,其余 admin 一律降级为普通用户。
	if demoted, err := h.repo.DemoteExtraAdmins(r.Context()); err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("降级多余管理员失败: %v", err))
	} else if demoted > 0 {
		log.Printf("[Migrate] demoted %d extra admin(s) to user", demoted)
	}

	// 3. 拷贝 subscribes/ 目录里的 yaml 文件到 mmwx subscribes/
	copied, skipped := 0, []string{}
	if subsDir := strings.TrimSpace(req.SubscribesDir); subsDir != "" {
		if _, err := os.Stat(subsDir); err == nil {
			copied, skipped, err = copySubscribesDir(subsDir, mmwxSubscribesDir)
			if err != nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("拷贝 subscribes 失败: %v", err))
			}
		} else {
			report.Warnings = append(report.Warnings, fmt.Sprintf("subscribes_dir 不存在: %s", subsDir))
		}
	}

	log.Printf("[Migrate] imported mmw: db=%s users=%d user_tokens=%d nodes=%d sub_files=%d subs_copied=%d",
		dbPath, report.Users, report.UserTokens, report.Nodes, report.SubscribeFiles, copied)
	respondJSON(w, http.StatusOK, importMmwResp{
		Success:           true,
		Report:            report,
		OwnedByAdmin:      admin,
		SubscribesCopied:  copied,
		SubscribesSkipped: skipped,
	})
}

// copySubscribesDir 把 src 目录里所有非隐藏常规文件拷贝到 dst。
// 同名文件已存在 → 跳过(保留 mmwx 现有的,不覆盖),并把文件名加入 skipped 列表。
// dst 目录不存在会自动建。
func copySubscribesDir(src, dst string) (int, []string, error) {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, nil, err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, nil, err
	}
	copied := 0
	var skipped []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		srcPath := filepath.Join(src, name)
		dstPath := filepath.Join(dst, name)
		if _, err := os.Stat(dstPath); err == nil {
			skipped = append(skipped, name)
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return copied, skipped, err
		}
		copied++
	}
	return copied, skipped, nil
}

// copyFile 复用 update.go 中的同名函数(同包内)。

// ------- POST /api/admin/migrate/takeover-external-xray -------

type takeoverResultPerServer struct {
	ServerID    int64  `json:"server_id"`
	ServerName  string `json:"server_name"`
	Detected    bool   `json:"detected"`
	ConfigPath  string `json:"config_path,omitempty"`
	ConfDir     string `json:"conf_dir,omitempty"`
	MergedFiles int    `json:"merged_files"`
	BackupDir   string `json:"backup_dir,omitempty"`
	Restarted   bool   `json:"restarted"`
	Message     string `json:"message"`
	Error       string `json:"error,omitempty"`
}

type takeoverExternalXrayResp struct {
	Success        bool                      `json:"success"`
	ServersScanned int                       `json:"servers_scanned"`
	Results        []takeoverResultPerServer `json:"results"`
}

// TakeoverExternalXray 对所有已添加的远程服务器,触发 agent 的
// /api/child/external-xray/takeover 接口:
//   - 让 agent 探测正在跑的外置 xray + 合并 -confdir 进单个 config.json + 重启 xray
//
// 用途:从妙妙屋迁移过来,被妙妙屋用 multi-conf 方式管理的 xray 服务器,
// 装上 mmw-agent 后需要先把多片配置合并成单文件,mmwx 主控的 /api/child/inbounds
// 等接口才能正确读写(后续 PatchClientEmails 也才能找到 client)。
func (h *MigrateHandler) TakeoverExternalXray(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "only POST")
		return
	}
	var req struct {
		ServerIDs []int64 `json:"server_ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	servers, err := h.repo.ListRemoteServers(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("list servers: %v", err))
		return
	}
	if len(req.ServerIDs) > 0 {
		want := make(map[int64]bool, len(req.ServerIDs))
		for _, id := range req.ServerIDs {
			want[id] = true
		}
		filtered := servers[:0]
		for _, s := range servers {
			if want[s.ID] {
				filtered = append(filtered, s)
			}
		}
		servers = filtered
	}

	resp := takeoverExternalXrayResp{
		Success:        true,
		ServersScanned: len(servers),
		Results:        []takeoverResultPerServer{},
	}
	for _, s := range servers {
		row := takeoverResultPerServer{ServerID: s.ID, ServerName: s.Name}
		raw, err := h.rm.forwardToRemoteServer(r.Context(), s.ID, "POST", "/api/child/external-xray/takeover", []byte("{}"))
		if err != nil {
			row.Error = err.Error()
			resp.Results = append(resp.Results, row)
			continue
		}
		var ar struct {
			Success     bool   `json:"success"`
			Detected    bool   `json:"detected"`
			ConfigPath  string `json:"config_path"`
			ConfDir     string `json:"conf_dir"`
			MergedFiles int    `json:"merged_files"`
			BackupDir   string `json:"backup_dir"`
			Restarted   bool   `json:"restarted"`
			Message     string `json:"message"`
		}
		if err := json.Unmarshal(raw, &ar); err != nil {
			row.Error = fmt.Sprintf("parse agent resp: %v", err)
			resp.Results = append(resp.Results, row)
			continue
		}
		row.Detected = ar.Detected
		row.ConfigPath = ar.ConfigPath
		row.ConfDir = ar.ConfDir
		row.MergedFiles = ar.MergedFiles
		row.BackupDir = ar.BackupDir
		row.Restarted = ar.Restarted
		row.Message = ar.Message
		resp.Results = append(resp.Results, row)
	}

	log.Printf("[Migrate] takeover-external-xray: scanned %d servers", resp.ServersScanned)
	respondJSON(w, http.StatusOK, resp)
}

// ------- POST /api/admin/migrate/patch-client-emails -------

type patchClientEmailsReq struct {
	ServerIDs []int64 `json:"server_ids"` // 可选,留空 = 处理全部已添加的远程服务器
}

type patchedClient struct {
	ServerID   int64  `json:"server_id"`
	ServerName string `json:"server_name"`
	InboundTag string `json:"inbound_tag"`
	OldEmail   string `json:"old_email"` // 通常为空字符串
	NewEmail   string `json:"new_email"`
}

type adminSubaccount struct {
	ServerID   int64  `json:"server_id"`
	ServerName string `json:"server_name"`
	InboundTag string `json:"inbound_tag"`
	Email      string `json:"email"`
	WasNew     bool   `json:"was_new"` // 本次操作是否首次绑定到 user_inbound_configs
}

type ss2022InboundInfo struct {
	ServerID   int64  `json:"server_id"`
	ServerName string `json:"server_name"`
	InboundTag string `json:"inbound_tag"`
	Method     string `json:"method"`
}

type patchClientEmailsResp struct {
	Success                bool                `json:"success"`
	OwnedByAdmin           string              `json:"owned_by_admin"`
	ServersScanned         int                 `json:"servers_scanned"`
	InboundsTotal          int                 `json:"inbounds_total"`
	ClientsPatched         []patchedClient     `json:"clients_patched"`
	AdminSubaccountsLinked []adminSubaccount   `json:"admin_subaccounts_linked"` // 所有 admin 持有的 client(含已有 email 的) → user_inbound_configs
	SS2022Inbounds         []ss2022InboundInfo `json:"ss2022_inbounds"`
	ServerErrors           []string            `json:"server_errors,omitempty"`
}

// PatchClientEmails 扫描所有已添加的远程服务器,给 xray inbound.clients[] 里无 email 的 client
// 补 email = 系统第一个 admin 的 username。
//
// 为什么需要:
//   - 妙妙屋时代 xray inbound 一般只配一个 client,没设 email
//   - mmwx 这边按 email 做流量统计 / routing 限定,缺 email 导致无法归属用户
//
// 处理逻辑(对每个 server):
//  1. forwardToRemoteServer GET /api/child/inbounds → 拿所有 inbound 定义
//  2. 对每个 inbound,遍历 settings.clients[](VLESS/VMess/Trojan)或 settings.users(SS multi-user)
//  3. 无 email 的 client → 设 email = admin (多个无 email 时第二个开始拼后缀:admin__2)
//  4. 整个 inbound 用 action="add" 提交回 agent(agent 内部"remove + add"实现原地替换)
//  5. 同时检测该 inbound 是不是 SS2022(method 以 "2022-" 开头)→ 加入 ss2022 列表
func (h *MigrateHandler) PatchClientEmails(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "only POST")
		return
	}
	var req patchClientEmailsReq
	_ = json.NewDecoder(r.Body).Decode(&req) // body 可空

	admin := h.repo.GetSystemNodeOwner(r.Context())
	if admin == "" {
		admin = "admin"
	}

	servers, err := h.repo.ListRemoteServers(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("list servers: %v", err))
		return
	}

	// 过滤 server_ids(如果给了)
	if len(req.ServerIDs) > 0 {
		want := make(map[int64]bool, len(req.ServerIDs))
		for _, id := range req.ServerIDs {
			want[id] = true
		}
		filtered := servers[:0]
		for _, s := range servers {
			if want[s.ID] {
				filtered = append(filtered, s)
			}
		}
		servers = filtered
	}

	resp := patchClientEmailsResp{
		Success:        true,
		OwnedByAdmin:   admin,
		ServersScanned: len(servers),
		// 显式初始化为空切片,防止 Go nil slice 序列化为 JSON null 导致前端 .length 炸
		ClientsPatched: []patchedClient{},
		SS2022Inbounds: []ss2022InboundInfo{},
		ServerErrors:   []string{},
	}

	for _, s := range servers {
		inbounds, err := fetchAgentInbounds(r.Context(), h.rm, s.ID)
		if err != nil {
			resp.ServerErrors = append(resp.ServerErrors, fmt.Sprintf("server %s(%d): %v", s.Name, s.ID, err))
			continue
		}
		resp.InboundsTotal += len(inbounds)
		for _, ib := range inbounds {
			tag, _ := ib["tag"].(string)
			protocol, _ := ib["protocol"].(string)
			patched, allClients, isSS2022, method, err := patchInboundClientEmails(ib, admin)
			if isSS2022 {
				resp.SS2022Inbounds = append(resp.SS2022Inbounds, ss2022InboundInfo{
					ServerID: s.ID, ServerName: s.Name, InboundTag: tag, Method: method,
				})
			}
			if err != nil {
				resp.ServerErrors = append(resp.ServerErrors, fmt.Sprintf("server %s inbound %s: patch err: %v", s.Name, tag, err))
				continue
			}
			// 只有真的 patch 了 email 才需要 write back 到 agent;否则只做 DB 侧 user_inbound_configs 绑定
			if len(patched) > 0 {
				body, _ := json.Marshal(map[string]any{"action": "add", "inbound": ib})
				if _, err := h.rm.forwardToRemoteServer(r.Context(), s.ID, "POST", "/api/child/inbounds", body); err != nil {
					resp.ServerErrors = append(resp.ServerErrors, fmt.Sprintf("server %s inbound %s: write back err: %v", s.Name, tag, err))
					continue
				}
			}
			for _, p := range patched {
				resp.ClientsPatched = append(resp.ClientsPatched, patchedClient{
					ServerID:   s.ID,
					ServerName: s.Name,
					InboundTag: tag,
					OldEmail:   p.oldEmail,
					NewEmail:   p.newEmail,
				})
			}
			// 把所有 client 登记到 user_inbound_configs(归属 admin),用于流量统计/管理
			for _, cli := range allClients {
				if strings.TrimSpace(cli.email) == "" {
					continue
				}
				credJSON, err := json.Marshal(cli.credential)
				if err != nil {
					continue
				}
				wasNew, err := h.repo.EnsureAdminInboundClient(r.Context(), admin, s.ID, tag, protocol, string(credJSON))
				if err != nil {
					resp.ServerErrors = append(resp.ServerErrors, fmt.Sprintf("server %s inbound %s email %s: bind admin err: %v", s.Name, tag, cli.email, err))
					continue
				}
				resp.AdminSubaccountsLinked = append(resp.AdminSubaccountsLinked, adminSubaccount{
					ServerID: s.ID, ServerName: s.Name, InboundTag: tag, Email: cli.email, WasNew: wasNew,
				})
			}
		}
	}

	log.Printf("[Migrate] patch-client-emails: scanned %d servers, patched %d clients, %d ss2022 inbounds, %d errors",
		resp.ServersScanned, len(resp.ClientsPatched), len(resp.SS2022Inbounds), len(resp.ServerErrors))
	respondJSON(w, http.StatusOK, resp)
}

// fetchAgentInbounds 从某 remote server 拿 inbounds 完整列表。
func fetchAgentInbounds(ctx context.Context, rm *RemoteManageHandler, serverID int64) ([]map[string]any, error) {
	if rm == nil {
		return nil, errors.New("remote manage handler 未初始化")
	}
	raw, err := rm.forwardToRemoteServer(ctx, serverID, "GET", "/api/child/inbounds", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Success  bool             `json:"success"`
		Inbounds []map[string]any `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse inbounds: %w", err)
	}
	if !resp.Success {
		return nil, errors.New("agent returned success=false")
	}
	return resp.Inbounds, nil
}

type clientPatchResult struct {
	oldEmail string
	newEmail string
}

type inboundClient struct {
	email      string
	credential map[string]any // 完整 client JSON,用于落 user_inbound_configs.credential_json
	wasPatched bool           // 本次 patchEmail 流程刚补的
	oldEmail   string         // 原始 email(空字符串表示原来无 email)
}

// patchInboundClientEmails 修改 inbound 里 client 数组的 email(原地修改 ib map)。
// 支持 VLESS/VMess/Trojan(settings.clients)和 SS(settings.users)的 multi-user 模式。
// 返回:
//   - patched 这次操作真的补了 email 的 client 列表
//   - allClients 这个 inbound 里**所有**(含原本已有 email)的 client 完整快照,
//     供 handler 把它们都登记到 user_inbound_configs 作为 admin 的子账户绑定
//   - isSS2022, method
func patchInboundClientEmails(ib map[string]any, admin string) ([]clientPatchResult, []inboundClient, bool, string, error) {
	settings, _ := ib["settings"].(map[string]any)
	if settings == nil {
		return nil, nil, false, "", nil
	}
	// 检测 ss2022
	method, _ := settings["method"].(string)
	isSS2022 := strings.HasPrefix(strings.ToLower(method), "2022-")

	// VLESS/VMess/Trojan/Shadowsocks-2022:settings.clients[]; SS multi-user 也可能用 settings.users
	var patched []clientPatchResult
	var allClients []inboundClient
	for _, key := range []string{"clients", "users"} {
		arr, ok := settings[key].([]any)
		if !ok || len(arr) == 0 {
			continue
		}
		usedEmails := map[string]bool{}
		// 第一遍:记下已用 email
		for _, e := range arr {
			m, _ := e.(map[string]any)
			if m == nil {
				continue
			}
			if s, _ := m["email"].(string); s != "" {
				usedEmails[s] = true
			}
		}
		// 第二遍:无 email 的逐个补 + 收集所有 client
		seq := 1
		for _, e := range arr {
			m, _ := e.(map[string]any)
			if m == nil {
				continue
			}
			oldEmail, _ := m["email"].(string)
			oldEmail = strings.TrimSpace(oldEmail)
			wasPatched := false
			if oldEmail == "" {
				candidate := admin
				for usedEmails[candidate] {
					seq++
					candidate = fmt.Sprintf("%s__%d", admin, seq)
				}
				usedEmails[candidate] = true
				m["email"] = candidate
				patched = append(patched, clientPatchResult{oldEmail: oldEmail, newEmail: candidate})
				wasPatched = true
			}
			currentEmail, _ := m["email"].(string)
			allClients = append(allClients, inboundClient{
				email:      currentEmail,
				credential: m, // 引用同 map,后续 marshal 即可
				wasPatched: wasPatched,
				oldEmail:   oldEmail,
			})
		}
		settings[key] = arr
	}
	ib["settings"] = settings
	return patched, allClients, isSS2022, method, nil
}

// ------- GET /api/admin/migrate/distinct-node-servers -------

type distinctServer struct {
	Address          string   `json:"address"`         // clash_config.server(域名或 IP)
	NodeCount        int      `json:"node_count"`      // 指向该 server 的节点数
	Ports            []int    `json:"ports"`           // 涉及到的端口集合
	Protocols        []string `json:"protocols"`       // 涉及到的协议集合
	ExistingServer   bool     `json:"existing_server"` // mmwx 已有同名 remote_server
	ExistingServerID int64    `json:"existing_server_id,omitempty"`
	SampleNodeName   string   `json:"sample_node_name"` // 任选一个节点名给用户做参考
}

type distinctServersResp struct {
	Success bool             `json:"success"`
	Servers []distinctServer `json:"servers"`
	Note    string           `json:"note"`
}

// DistinctNodeServers 列出当前 mmwx 数据库里所有外部节点(没有 original_server / inbound_tag 关联的)
// 的去重 server 地址,用于"待添加为远程服务器"清单。
//
// mmw 没有"远程服务器 / xray inbound"概念,节点只是 clash 配置;迁移到 mmwx 后这些节点是
// "外部节点",必须先把每个去重 server 加为 mmwx 远程服务器并装 agent,扫描其 inbound 与节点
// 凭据匹配后才能升级为"受管节点"。
func (h *MigrateHandler) DistinctNodeServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "only GET")
		return
	}
	servers, err := h.repo.ListDistinctNodeServers(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("查询失败: %v", err))
		return
	}
	out := make([]distinctServer, 0, len(servers))
	for _, s := range servers {
		out = append(out, distinctServer{
			Address:          s.Address,
			NodeCount:        s.NodeCount,
			Ports:            s.Ports,
			Protocols:        s.Protocols,
			ExistingServer:   s.ExistingServer,
			ExistingServerID: s.ExistingServerID,
			SampleNodeName:   s.SampleNodeName,
		})
	}
	respondJSON(w, http.StatusOK, distinctServersResp{
		Success: true,
		Servers: out,
		Note:    "对每个未关联的 server 地址,需要在「服务管理」创建对应的远程服务器并安装 mmw-agent;agent 接入后会自动扫描 inbound,主控按凭据匹配把节点从'外部'升级为'受管'。",
	})
}
