package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pub_client/app/models"
)

// stubVaultBackend 以記憶體 map 模擬 server 端 vault。
type stubVaultBackend struct {
	files      map[string]string // rel path → content
	fetchCalls int
}

func (s *stubVaultBackend) VaultManifest(context.Context) ([]models.VaultFileInfo, error) {
	var out []models.VaultFileInfo
	for p, content := range s.files {
		sum := sha256.Sum256([]byte(content))
		out = append(out, models.VaultFileInfo{
			Path: p, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]),
		})
	}
	return out, nil
}

func (s *stubVaultBackend) FetchVaultFiles(_ context.Context, paths []string, onFile func(string, []byte) error) error {
	s.fetchCalls++
	for _, p := range paths {
		if err := onFile(p, []byte(s.files[p])); err != nil {
			return err
		}
	}
	return nil
}

func TestSyncVaultFreshAndIncremental(t *testing.T) {
	dir := t.TempDir()
	backend := &stubVaultBackend{files: map[string]string{
		"notes/a.md":    "# A",
		"entities/b.md": "# B with [[A]]",
	}}

	// 首次：全部下載
	res, err := SyncVault(context.Background(), backend, dir, nil)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Downloaded != 2 || res.Unchanged != 0 || res.Total != 2 {
		t.Fatalf("fresh sync = %+v, want 2 downloaded", res)
	}
	got, err := os.ReadFile(filepath.Join(dir, "entities", "b.md"))
	if err != nil || string(got) != "# B with [[A]]" {
		t.Fatalf("b.md content = %q, err=%v", got, err)
	}

	// 第二次：hash 全中，零下載
	res, err = SyncVault(context.Background(), backend, dir, nil)
	if err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if res.Downloaded != 0 || res.Unchanged != 2 {
		t.Fatalf("incremental sync = %+v, want all unchanged", res)
	}

	// server 端變更一檔 → 只抓那檔
	backend.files["notes/a.md"] = "# A v2"
	res, err = SyncVault(context.Background(), backend, dir, nil)
	if err != nil {
		t.Fatalf("delta sync: %v", err)
	}
	if res.Downloaded != 1 || res.Unchanged != 1 {
		t.Fatalf("delta sync = %+v, want 1 downloaded 1 unchanged", res)
	}
	got, _ = os.ReadFile(filepath.Join(dir, "notes", "a.md"))
	if string(got) != "# A v2" {
		t.Fatalf("a.md not updated: %q", got)
	}

	// 本地自加的檔不被動
	extra := filepath.Join(dir, "mine.md")
	if err := os.WriteFile(extra, []byte("local note"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncVault(context.Background(), backend, dir, nil); err != nil {
		t.Fatalf("sync with local extras: %v", err)
	}
	if _, err := os.Stat(extra); err != nil {
		t.Error("local-only file was removed by sync")
	}
}

func TestSyncVaultRejectsEscapingPaths(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"../evil.md", "a/../../evil.md", "C:/evil.md"} {
		backend := &stubVaultBackend{files: map[string]string{bad: "x"}}
		if _, err := SyncVault(context.Background(), backend, dir, nil); err == nil {
			t.Errorf("path %q should have failed the sync", bad)
		}
	}
	// 確認沒有東西被寫到 dir 外面（TempDir 的上層）
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.md")); !os.IsNotExist(err) {
		t.Error("escaping file was written outside vault dir")
	}
}

func TestSyncVaultEmptyDirErrors(t *testing.T) {
	if _, err := SyncVault(context.Background(), &stubVaultBackend{}, "  ", nil); err == nil ||
		!strings.Contains(err.Error(), "not set") {
		t.Errorf("empty dir should error, got %v", err)
	}
}
