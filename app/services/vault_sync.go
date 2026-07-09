// vault_sync.go — 「Sync Vault」：把 server 端 Obsidian vault 的 .md 鏡像到本地
// 資料夾（之後可直接用 Obsidian 開）。流程：VaultManifest 拿全量清單（含 sha256）
// → 與本地檔比對 → 只下載缺少/內容不同的檔。本地多出來的檔不動（使用者可能自己
// 加筆記），所以這是單向 pull，不是雙向同步。
package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pub_client/app/models"
)

// vaultBackend GatewayClient 的 vault 面窄介面（測試用 stub 替身）。
type vaultBackend interface {
	VaultManifest(ctx context.Context) ([]models.VaultFileInfo, error)
	FetchVaultFiles(ctx context.Context, paths []string, onFile func(path string, data []byte) error) error
}

// VaultSyncResult 一次同步的統計。
type VaultSyncResult struct {
	Total      int // manifest 檔案總數
	Downloaded int // 實際下載寫入
	Unchanged  int // 本地已是最新
}

// vaultFetchBatch 一次 VaultFetch 的路徑數上限——檔案很多時分批，避免單一
// stream 撐過 gateway 的 call timeout。
const vaultFetchBatch = 200

// SyncVault 執行一次單向 pull。progress 每個階段/每個檔會被呼叫（可為 nil）。
func SyncVault(ctx context.Context, backend vaultBackend, localDir string, progress func(msg string)) (VaultSyncResult, error) {
	report := func(msg string) {
		if progress != nil {
			progress(msg)
		}
	}
	var res VaultSyncResult
	if strings.TrimSpace(localDir) == "" {
		return res, fmt.Errorf("vault folder is not set")
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return res, fmt.Errorf("create vault folder: %w", err)
	}

	report("Fetching vault manifest...")
	manifest, err := backend.VaultManifest(ctx)
	if err != nil {
		return res, fmt.Errorf("manifest: %w", err)
	}
	res.Total = len(manifest)

	var need []string
	for _, f := range manifest {
		local, perr := safeVaultLocalPath(localDir, f.Path)
		if perr != nil {
			// server 給了逃逸路徑——不能靜默跳過（那會讓使用者以為同步完整）
			return res, perr
		}
		sum, herr := localFileSHA256(local)
		if herr == nil && sum == f.SHA256 {
			res.Unchanged++
			continue
		}
		need = append(need, f.Path)
	}
	if len(need) == 0 {
		report(fmt.Sprintf("Vault up to date (%d files)", res.Total))
		return res, nil
	}

	done := 0
	for start := 0; start < len(need); start += vaultFetchBatch {
		end := start + vaultFetchBatch
		if end > len(need) {
			end = len(need)
		}
		err := backend.FetchVaultFiles(ctx, need[start:end], func(path string, data []byte) error {
			local, perr := safeVaultLocalPath(localDir, path)
			if perr != nil {
				return perr
			}
			if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
				return fmt.Errorf("create dir for %s: %w", path, err)
			}
			if err := os.WriteFile(local, data, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			done++
			res.Downloaded++
			report(fmt.Sprintf("Downloading %d/%d: %s", done, len(need), path))
			return nil
		})
		if err != nil {
			return res, fmt.Errorf("fetch: %w", err)
		}
	}
	report(fmt.Sprintf("Vault synced: %d downloaded, %d unchanged", res.Downloaded, res.Unchanged))
	return res, nil
}

// safeVaultLocalPath 把 manifest 的相對路徑映射到本地 vault 資料夾內；
// 絕對路徑或 .. 逃逸（惡意/失控 server）明確回錯。
func safeVaultLocalPath(localDir, rel string) (string, error) {
	native := filepath.FromSlash(rel)
	if rel == "" || filepath.IsAbs(native) {
		return "", fmt.Errorf("server sent invalid vault path %q", rel)
	}
	full := filepath.Join(localDir, native)
	r, err := filepath.Rel(localDir, full)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("server sent escaping vault path %q", rel)
	}
	return full, nil
}

func localFileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
