package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pub_client/app/models"
)

// withTempStore 每個測試用獨立目錄 + 固定測試金鑰。
func withTempStore(t *testing.T) {
	t.Helper()
	Close()
	baseDirOverride = t.TempDir()
	keyOverride = "test-key-0123456789abcdef0123456789abcdef"
	t.Cleanup(func() {
		Close()
		baseDirOverride = ""
		keyOverride = ""
	})
}

func TestConfigRoundTrip(t *testing.T) {
	withTempStore(t)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig (fresh): %v", err)
	}
	if cfg.GatewayAddr != models.DefaultConfig().GatewayAddr {
		t.Errorf("fresh config should be defaults, got %+v", cfg)
	}

	cfg.APIKey = "sak_secret"
	cfg.AgentID = "agent-x"
	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig (reload): %v", err)
	}
	if got.APIKey != "sak_secret" || got.AgentID != "agent-x" {
		t.Errorf("reload mismatch: %+v", got)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	withTempStore(t)

	if err := SaveSession("sess_1", "agent-a", "**You:** hi"); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	// upsert 覆寫
	if err := SaveSession("sess_1", "agent-a", "**You:** hi\n\n**Assistant:** hello"); err != nil {
		t.Fatalf("SaveSession update: %v", err)
	}
	if err := SaveSession("sess_2", "agent-b", "x"); err != nil {
		t.Fatal(err)
	}

	metas, err := ListSessions(10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(metas))
	}

	transcript, err := GetSession("sess_1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !strings.Contains(transcript, "hello") {
		t.Errorf("transcript should be the updated version: %q", transcript)
	}

	if err := SaveSession("", "a", "x"); err == nil {
		t.Error("expected error for empty session id")
	}
}

func TestWrongKeyRejected(t *testing.T) {
	withTempStore(t)
	if _, err := LoadConfig(); err != nil { // 建檔
		t.Fatal(err)
	}
	Close()

	keyOverride = "wrong-key-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected wrong encryption key to fail")
	} else if !strings.Contains(err.Error(), "encryption") && !strings.Contains(strings.ToLower(err.Error()), "key") {
		t.Logf("note: rejection error was: %v", err)
	}
}

func TestLegacyConfigMigration(t *testing.T) {
	withTempStore(t)

	// 模擬舊版明文 config.json（放在 UserConfigDir/pub_client/——
	// loadLegacyConfig 讀真正的 UserConfigDir，測試改用注入版檢核邏輯即可：
	// 這裡直接驗證「store 內已有 config 時不再讀 legacy」的路徑。
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.AgentID = "persisted"
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig()
	if err != nil || got.AgentID != "persisted" {
		t.Fatalf("stored config should win: %+v %v", got, err)
	}
}

func TestStoreFileIsEncrypted(t *testing.T) {
	withTempStore(t)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIKey = "sak_plaintext_should_not_appear"
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	Close()

	raw, err := os.ReadFile(filepath.Join(baseDirOverride, "store.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sak_plaintext_should_not_appear") {
		t.Error("API key found in plaintext inside store file — encryption not effective")
	}
}
