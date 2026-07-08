// Package store — pub_client 的嵌入式加密儲存（DuckDB）。
//
// 設定（settings 表）與對話 session（chat_sessions 表）都存在單一
// DuckDB 檔案（%APPDATA%\pub_client\store.duckdb），以 DuckDB 內建加密
//（ATTACH ... ENCRYPTION_KEY，AES）落地——取代先前的明文 config.json 與
// sessions/*.md 檔案（API Key 等敏感設定不再明文落地）。
//
// 金鑰來源（優先序）：
//  1. 機器特徵（services.StableDeviceID 同源：SHA-256(MachineGuid+username)）——
//     不落地、跨重裝程式穩定；DB 檔被單獨複製走時無法解開
//  2. fallback：%APPDATA%\pub_client\store.key（隨機 32 bytes hex）——取不到
//     機器特徵時才產生；威脅模型退化為「檔案 + 金鑰同放使用者目錄」
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"

	"pub_client/app/models"
	"pub_client/app/services"
)

var (
	mu     sync.Mutex
	handle *sql.DB

	// baseDirOverride / keyOverride 測試注入點（正式路徑走 UserConfigDir + 機器金鑰）。
	baseDirOverride string
	keyOverride     string
)

// BaseDir 儲存目錄（%APPDATA%\pub_client）。
func BaseDir() string {
	if baseDirOverride != "" {
		return baseDirOverride
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "pub_client")
}

// StorePath DuckDB 檔案路徑（設定頁顯示用）。
func StorePath() string { return filepath.Join(BaseDir(), "store.duckdb") }

// encryptionKey 解析加密金鑰（見套件註解的優先序）。
func encryptionKey() (string, error) {
	if keyOverride != "" {
		return keyOverride, nil
	}
	if id, ok := services.StableDeviceID(); ok {
		return id, nil
	}
	keyPath := filepath.Join(BaseDir(), "store.key")
	if raw, err := os.ReadFile(keyPath); err == nil && len(strings.TrimSpace(string(raw))) >= 32 {
		return strings.TrimSpace(string(raw)), nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate store key: %w", err)
	}
	key := hex.EncodeToString(b)
	if err := os.MkdirAll(BaseDir(), 0o755); err != nil {
		return "", fmt.Errorf("mkdir store dir: %w", err)
	}
	if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
		return "", fmt.Errorf("persist store key: %w", err)
	}
	return key, nil
}

// open 開啟（或回傳已開啟的）加密 store。單連線池——桌面 app 沒有並發壓力，
// 也避免 ATTACH 的連線範圍問題。
func open() (*sql.DB, error) {
	mu.Lock()
	defer mu.Unlock()
	if handle != nil {
		return handle, nil
	}

	if err := os.MkdirAll(BaseDir(), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir store dir: %w", err)
	}
	key, err := encryptionKey()
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("open duckdb: %w", err)
	}
	db.SetMaxOpenConns(1)

	// 路徑進 SQL 字面量：正斜線 + 單引號跳脫（Windows 路徑不含單引號，防禦性處理）
	p := strings.ReplaceAll(strings.ReplaceAll(StorePath(), "\\", "/"), "'", "''")
	k := strings.ReplaceAll(key, "'", "''")
	if _, err := db.Exec(fmt.Sprintf("ATTACH '%s' AS store (ENCRYPTION_KEY '%s')", p, k)); err != nil {
		db.Close()
		return nil, fmt.Errorf("attach encrypted store: %w", err)
	}

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS store.settings (k TEXT PRIMARY KEY, v TEXT)`,
		`CREATE TABLE IF NOT EXISTS store.chat_sessions (
			session_id TEXT PRIMARY KEY,
			agent_id   TEXT,
			transcript TEXT,
			updated_at TIMESTAMP
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, fmt.Errorf("init schema: %w", err)
		}
	}

	handle = db
	return handle, nil
}

// Close 收掉 store（測試用；App 結束時 OS 會回收）。
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if handle != nil {
		handle.Close()
		handle = nil
	}
}

// ── 設定 ────────────────────────────────────────────────────────────────────

// LoadConfig 讀取設定；store 內沒有時嘗試從舊版明文 config.json 遷移
//（%APPDATA%\pub_client\config.json 與更早的 %APPDATA%\agent_client\config.json），
// 遷移後把舊檔改名 .imported（不再被讀取，留檔供人工確認後刪除）。
func LoadConfig() (*models.Config, error) {
	db, err := open()
	if err != nil {
		return nil, err
	}

	var raw string
	err = db.QueryRow(`SELECT v FROM store.settings WHERE k = 'config'`).Scan(&raw)
	if err == nil {
		cfg := models.DefaultConfig()
		if jerr := json.Unmarshal([]byte(raw), cfg); jerr != nil {
			return nil, fmt.Errorf("parse stored config: %w", jerr)
		}
		return cfg, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("read config: %w", err)
	}

	// 首次啟動：嘗試遷移舊明文設定
	cfg := models.DefaultConfig()
	if legacy, path := loadLegacyConfig(); legacy != nil {
		cfg = legacy
		_ = os.Rename(path, path+".imported") // best-effort；改名失敗只是下次不會再讀
	}
	if err := SaveConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadLegacyConfig 找舊版明文 config.json（新舊兩個目錄名都查）。
func loadLegacyConfig() (*models.Config, string) {
	userDir, err := os.UserConfigDir()
	if err != nil || userDir == "" {
		return nil, ""
	}
	for _, dir := range []string{"pub_client", "agent_client"} {
		p := filepath.Join(userDir, dir, "config.json")
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			continue
		}
		cfg := models.DefaultConfig()
		if jerr := json.Unmarshal(raw, cfg); jerr != nil {
			continue
		}
		return cfg, p
	}
	return nil, ""
}

// SaveConfig 持久化設定（整包 JSON upsert）。
func SaveConfig(cfg *models.Config) error {
	db, err := open()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	_, err = db.Exec(`
		INSERT INTO store.settings (k, v) VALUES ('config', ?)
		ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v`, string(raw))
	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// ── 對話 session ─────────────────────────────────────────────────────────────

// SessionMeta 一筆 session 的清單資訊。
type SessionMeta struct {
	SessionID string
	AgentID   string
	UpdatedAt time.Time
	Chars     int
}

// SaveSession 每輪結束後整份轉錄 upsert（best-effort 呼叫端自行決定）。
func SaveSession(sessionID, agentID, transcript string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("empty session id")
	}
	db, err := open()
	if err != nil {
		return err
	}
	_, err = db.Exec(`
		INSERT INTO store.chat_sessions (session_id, agent_id, transcript, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (session_id) DO UPDATE
		SET agent_id = EXCLUDED.agent_id, transcript = EXCLUDED.transcript, updated_at = EXCLUDED.updated_at`,
		sessionID, agentID, transcript, time.Now())
	if err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}

// ListSessions 最近的 session（新的在前）。
func ListSessions(limit int) ([]SessionMeta, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	db, err := open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT session_id, coalesce(agent_id, ''), length(transcript), updated_at
		FROM store.chat_sessions ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var out []SessionMeta
	for rows.Next() {
		var m SessionMeta
		if err := rows.Scan(&m.SessionID, &m.AgentID, &m.Chars, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetSession 取回整份轉錄（Markdown 原文）。
func GetSession(sessionID string) (string, error) {
	db, err := open()
	if err != nil {
		return "", err
	}
	var transcript string
	err = db.QueryRow(`SELECT transcript FROM store.chat_sessions WHERE session_id = ?`, sessionID).Scan(&transcript)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("session %s not found", sessionID)
	}
	if err != nil {
		return "", err
	}
	return transcript, nil
}
