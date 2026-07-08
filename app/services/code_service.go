// code_service.go — 內建「Coding Agent」工具：讀檔 / 列目錄 / 搜尋 / 編輯 / 寫檔。
//
// 對應 Claude Code 的 Read/Grep/Glob/Edit/Write 語意，讓 agent（LLM）能在使用者
// 明確設定的「工作目錄」內讀取與修改程式碼。與 tool_service.go 的差異：
//   - tool_service.go：使用者手動登錄任意命令列程式，agent 決定呼叫、client 直接執行。
//   - code_service.go：client 內建、agent 只要指定路徑/內容，寫入前**強制**由使用者
//     審核 diff 才落地——這是把「本機檔案寫入」交給 LLM 時，安全上不能省的閘門。
//
// 安全邊界：
//   - 只在 WorkspaceRoot 非空時才把這組工具加進 manifest（未設定 = 功能關閉）。
//   - 所有路徑都必須解析在 WorkspaceRoot 之內，擋掉 ".." 逃逸。
//   - 只讀寫合法 UTF-8 文字內容；偵測到二進位/非 UTF-8 一律明確回錯，不塞進對話。
package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"

	"pub_client/app/models"
)

// DiffApproveFunc 在真正寫入磁碟前，把 unified diff 呈現給使用者審核；
// 回傳 false 表示使用者拒絕套用。
type DiffApproveFunc func(path, unifiedDiff string) bool

const (
	codeToolPrefix       = "code_"
	maxReadBytes         = 24000 // 讀檔回傳上限（原 8000 偏小，模型讀大檔要太多輪；SUGGEST §2.4 放寬）
	maxListEntries       = 500
	maxSearchMatches     = 50
	maxSearchFileBytes   = 2 << 20 // 略過超過 2MB 的檔案（多半是二進位）
	defaultReadLineLimit = 300
)

// skipDirs 遞迴列目錄 / 搜尋時要跳過的雜訊目錄。
var skipDirs = map[string]bool{
	".git": true, ".hyp": true, "node_modules": true, "vendor": true,
	"bin": true, "obj": true, ".vs": true, ".idea": true,
}

// isBackupFile 判斷是否為 applyWithDiff 產生的 .bak 備份檔——列目錄與搜尋都跳過，
// 避免 LLM 把上一版內容又讀回 context（讀檔工具仍可明確指定路徑讀取，供還原用）。
func isBackupFile(name string) bool { return strings.HasSuffix(name, ".bak") }

// ── manifest ────────────────────────────────────────────────────────────────

// BuiltinCodeTools 回傳內建碼工具的 /api/chat client_tools manifest。
// workspaceRoot 為空時回傳空陣列（功能關閉）。
func BuiltinCodeTools(workspaceRoot string) []models.ChatToolSpec {
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil
	}
	return []models.ChatToolSpec{
		{
			Name:        codeToolPrefix + "list_files",
			Description: "List files and subdirectories under a relative path within the workspace (non-recursive). Leave path empty for the workspace root.",
			Schema: rawSchema(`{"type":"object","properties":{
				"path":{"type":"string","description":"Path relative to the workspace root; leave empty for the root"}
			}}`),
		},
		{
			Name:        codeToolPrefix + "search",
			Description: "Recursively search text within the workspace (case-insensitive substring match), returning matching files and line numbers.",
			Schema: rawSchema(`{"type":"object","properties":{
				"query":{"type":"string","description":"Text to search for"},
				"path":{"type":"string","description":"Relative path to restrict the search to; leave empty to search the whole workspace"},
				"glob":{"type":"string","description":"Optional file-name glob filter, e.g. *.go or *_test.go"}
			},"required":["query"]}`),
		},
		{
			Name:        codeToolPrefix + "read_file",
			Description: "Read the contents of a text file within the workspace; offset/limit select a line-range window (to avoid exceeding context).",
			Schema: rawSchema(`{"type":"object","properties":{
				"path":{"type":"string","description":"File path relative to the workspace root"},
				"offset":{"type":"integer","description":"Starting line number (1-indexed), default 1"},
				"limit":{"type":"integer","description":"Maximum number of lines to read, default 300"}
			},"required":["path"]}`),
		},
		{
			Name: codeToolPrefix + "edit_file",
			Description: "Replace old_string with new_string in a file's contents. old_string must occur exactly once in the file, " +
				"otherwise include more context to narrow it down, or set replace_all=true to replace every occurrence. The user reviews a diff and must approve before it's applied.",
			Schema: rawSchema(`{"type":"object","properties":{
				"path":{"type":"string","description":"File path relative to the workspace root"},
				"old_string":{"type":"string","description":"Original text to be replaced; must match the file content exactly"},
				"new_string":{"type":"string","description":"Replacement text"},
				"replace_all":{"type":"boolean","description":"When true, replaces every occurrence; default false (requires uniqueness)"}
			},"required":["path","old_string","new_string"]}`),
		},
		{
			Name:        codeToolPrefix + "write_file",
			Description: "Overwrite a file entirely or create a new one (parent directories are created automatically). The user reviews a diff and must approve before it's applied.",
			Schema: rawSchema(`{"type":"object","properties":{
				"path":{"type":"string","description":"File path relative to the workspace root"},
				"content":{"type":"string","description":"Full new content of the file"}
			},"required":["path","content"]}`),
		},
	}
}

func rawSchema(s string) []byte { return []byte(s) }

// IsCodeTool 判斷某工具名是否屬於本檔的內建碼工具（用於 chat_service.go 分派）。
func IsCodeTool(name string) bool { return strings.HasPrefix(name, codeToolPrefix) }

// ── 分派 ────────────────────────────────────────────────────────────────────

// ExecuteCodeTool 執行一個內建碼工具呼叫；永不 panic，錯誤包進 ToolResult。
func ExecuteCodeTool(ctx context.Context, workspaceRoot string, call models.ToolCall, approve DiffApproveFunc) models.ToolResult {
	res := models.ToolResult{ToolCallID: call.ID, Name: call.Name}
	if strings.TrimSpace(workspaceRoot) == "" {
		return errResult(res, "workspace root not configured")
	}

	switch call.Name {
	case codeToolPrefix + "list_files":
		return listFiles(workspaceRoot, call, res)
	case codeToolPrefix + "search":
		return searchCode(workspaceRoot, call, res)
	case codeToolPrefix + "read_file":
		return readFile(workspaceRoot, call, res)
	case codeToolPrefix + "edit_file":
		return editFile(workspaceRoot, call, res, approve)
	case codeToolPrefix + "write_file":
		return writeFile(workspaceRoot, call, res, approve)
	default:
		return errResult(res, fmt.Sprintf("unknown code tool %q", call.Name))
	}
}

func errResult(res models.ToolResult, msg string) models.ToolResult {
	res.IsError = true
	res.Content = msg
	return res
}

// ── 路徑安全 ────────────────────────────────────────────────────────────────

// resolveInWorkspace 把相對路徑解析成絕對路徑，並確保結果落在 root 之內。
// 用 filepath.Rel 判斷（而非把輸入鉗制回根目錄內）：真的逃逸時明確回錯，
// 讓呼叫端（LLM）知道自己給錯路徑，而不是靜默改寫到別的地方。
func resolveInWorkspace(root, rel string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	full := filepath.Join(absRoot, rel)

	relCheck, err := filepath.Rel(absRoot, full)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace root", rel)
	}
	return full, nil
}

// ── list_files ──────────────────────────────────────────────────────────────

func listFiles(root string, call models.ToolCall, res models.ToolResult) models.ToolResult {
	rel, _ := call.Args["path"].(string)
	dir, err := resolveInWorkspace(root, rel)
	if err != nil {
		return errResult(res, err.Error())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errResult(res, fmt.Sprintf("list %s: %v", rel, err))
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var sb strings.Builder
	n := 0
	for _, e := range entries {
		if skipDirs[e.Name()] || isBackupFile(e.Name()) {
			continue
		}
		if n >= maxListEntries {
			sb.WriteString(fmt.Sprintf("... (exceeds %d entries, truncated)\n", maxListEntries))
			break
		}
		if e.IsDir() {
			sb.WriteString(e.Name() + "/\n")
		} else {
			info, statErr := e.Info()
			size := int64(0)
			if statErr == nil {
				size = info.Size()
			}
			sb.WriteString(fmt.Sprintf("%s (%d bytes)\n", e.Name(), size))
		}
		n++
	}
	if n == 0 {
		sb.WriteString("(empty directory)")
	}
	res.Content = sb.String()
	return res
}

// ── search ──────────────────────────────────────────────────────────────────

func searchCode(root string, call models.ToolCall, res models.ToolResult) models.ToolResult {
	query, _ := call.Args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return errResult(res, "query required")
	}
	rel, _ := call.Args["path"].(string)
	startDir, err := resolveInWorkspace(root, rel)
	if err != nil {
		return errResult(res, err.Error())
	}
	glob, _ := call.Args["glob"].(string)
	glob = strings.TrimSpace(glob)
	if glob != "" {
		if _, err := filepath.Match(glob, "probe"); err != nil {
			return errResult(res, fmt.Sprintf("invalid glob pattern %q: %v", glob, err))
		}
	}

	needle := strings.ToLower(query)
	var matches []string

	_ = filepath.Walk(startDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || len(matches) >= maxSearchMatches {
			return nil
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() > maxSearchFileBytes || isBackupFile(info.Name()) {
			return nil
		}
		if glob != "" {
			if ok, _ := filepath.Match(glob, info.Name()); !ok {
				return nil
			}
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || !utf8.Valid(data) {
			return nil
		}
		relPath, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(data), "\n") {
			if len(matches) >= maxSearchMatches {
				break
			}
			if strings.Contains(strings.ToLower(line), needle) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(relPath), i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})

	if len(matches) == 0 {
		res.Content = "(no matches)"
		return res
	}
	res.Content = strings.Join(matches, "\n")
	if len(matches) >= maxSearchMatches {
		res.Content += fmt.Sprintf("\n... (reached the %d match limit, there may be more)", maxSearchMatches)
	}
	return res
}

// ── read_file ───────────────────────────────────────────────────────────────

func readFile(root string, call models.ToolCall, res models.ToolResult) models.ToolResult {
	rel, _ := call.Args["path"].(string)
	if strings.TrimSpace(rel) == "" {
		return errResult(res, "path required")
	}
	abs, err := resolveInWorkspace(root, rel)
	if err != nil {
		return errResult(res, err.Error())
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return errResult(res, fmt.Sprintf("read %s: %v", rel, err))
	}
	if !utf8.Valid(data) {
		return errResult(res, fmt.Sprintf("%s appears to be a binary file or non-UTF-8 encoded; cannot read as text", rel))
	}

	offset := intArg(call.Args, "offset", 1)
	if offset < 1 {
		offset = 1
	}
	limit := intArg(call.Args, "limit", defaultReadLineLimit)
	if limit <= 0 {
		limit = defaultReadLineLimit
	}

	lines := strings.Split(string(data), "\n")
	start := offset - 1
	if start >= len(lines) {
		return errResult(res, fmt.Sprintf("offset %d exceeds the file's total line count %d", offset, len(lines)))
	}
	end := start + limit
	if end > len(lines) {
		end = len(lines)
	}

	var sb strings.Builder
	for i := start; i < end; i++ {
		sb.WriteString(strconv.Itoa(i + 1))
		sb.WriteString("\t")
		sb.WriteString(lines[i])
		sb.WriteString("\n")
	}
	content := sb.String()
	if len(content) > maxReadBytes {
		content = content[:maxReadBytes] + fmt.Sprintf("\n... (truncated, exceeds %d bytes)", maxReadBytes)
	}
	if end < len(lines) {
		content += fmt.Sprintf("\n... (%d more lines not shown; continue with offset=%d)", len(lines)-end, end+1)
	}
	res.Content = content
	return res
}

func intArg(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return def
}

// ── edit_file / write_file（共用 diff 審核） ─────────────────────────────────

func editFile(root string, call models.ToolCall, res models.ToolResult, approve DiffApproveFunc) models.ToolResult {
	rel, _ := call.Args["path"].(string)
	oldStr, _ := call.Args["old_string"].(string)
	newStr, _ := call.Args["new_string"].(string)
	replaceAll, _ := call.Args["replace_all"].(bool)

	if strings.TrimSpace(rel) == "" {
		return errResult(res, "path required")
	}
	if oldStr == "" {
		return errResult(res, "old_string required (use code_write_file for new files)")
	}

	abs, err := resolveInWorkspace(root, rel)
	if err != nil {
		return errResult(res, err.Error())
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return errResult(res, fmt.Sprintf("read %s: %v", rel, err))
	}
	if !utf8.Valid(data) {
		return errResult(res, fmt.Sprintf("%s appears to be a binary file or non-UTF-8 encoded; cannot edit", rel))
	}
	oldContent := string(data)

	count := strings.Count(oldContent, oldStr)
	if count == 0 {
		return errResult(res, "old_string not found in file; check that it matches exactly")
	}
	if count > 1 && !replaceAll {
		return errResult(res, fmt.Sprintf("old_string occurs %d times in the file, not unique; include more context, or set replace_all=true", count))
	}

	var newContent string
	if replaceAll {
		newContent = strings.ReplaceAll(oldContent, oldStr, newStr)
	} else {
		newContent = strings.Replace(oldContent, oldStr, newStr, 1)
	}

	return applyWithDiff(root, rel, abs, oldContent, newContent, call, res, approve)
}

func writeFile(root string, call models.ToolCall, res models.ToolResult, approve DiffApproveFunc) models.ToolResult {
	rel, _ := call.Args["path"].(string)
	content, _ := call.Args["content"].(string)
	if strings.TrimSpace(rel) == "" {
		return errResult(res, "path required")
	}

	abs, err := resolveInWorkspace(root, rel)
	if err != nil {
		return errResult(res, err.Error())
	}

	var oldContent string
	if data, readErr := os.ReadFile(abs); readErr == nil {
		if !utf8.Valid(data) {
			return errResult(res, fmt.Sprintf("%s appears to be a binary file or non-UTF-8 encoded; refusing to overwrite", rel))
		}
		oldContent = string(data)
	}
	// 檔案不存在時 oldContent 維持空字串（視為新檔），os.ReadFile 的錯誤在此忽略是刻意的。

	return applyWithDiff(root, rel, abs, oldContent, content, call, res, approve)
}

// applyWithDiff 產生 unified diff 交給使用者審核；核准才真的寫入磁碟。
func applyWithDiff(
	root, rel, abs, oldContent, newContent string,
	call models.ToolCall, res models.ToolResult, approve DiffApproveFunc,
) models.ToolResult {
	if oldContent == newContent {
		res.Content = fmt.Sprintf("%s content unchanged, nothing to do", rel)
		return res
	}

	diffText, err := buildUnifiedDiff(rel, oldContent, newContent)
	if err != nil {
		return errResult(res, fmt.Sprintf("build diff: %v", err))
	}

	if approve == nil || !approve(rel, diffText) {
		return errResult(res, "user declined to apply this change")
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return errResult(res, fmt.Sprintf("mkdir: %v", err))
	}
	// 覆寫前備援：既有內容先留成 .bak，讓使用者有辦法手動還原上一版；
	// 備份寫不進去就不動原檔（寧可失敗也不做沒有退路的覆寫）。新檔（oldContent 空）沒有東西可備。
	backedUp := false
	if oldContent != "" {
		if err := os.WriteFile(abs+".bak", []byte(oldContent), 0o644); err != nil {
			return errResult(res, fmt.Sprintf("write backup %s.bak: %v (original file left untouched)", rel, err))
		}
		backedUp = true
	}
	if err := os.WriteFile(abs, []byte(newContent), 0o644); err != nil {
		return errResult(res, fmt.Sprintf("write %s: %v", rel, err))
	}

	if backedUp {
		res.Content = fmt.Sprintf("Wrote %s (%d bytes); previous version saved to %s.bak", rel, len(newContent), rel)
	} else {
		res.Content = fmt.Sprintf("Wrote %s (%d bytes)", rel, len(newContent))
	}
	return res
}

// buildUnifiedDiff 用 difflib 產生給使用者看的 unified diff。
func buildUnifiedDiff(rel, oldContent, newContent string) (string, error) {
	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(oldContent),
		B:        difflib.SplitLines(newContent),
		FromFile: rel + " (original)",
		ToFile:   rel + " (modified)",
		Context:  3,
	}
	return difflib.GetUnifiedDiffString(diff)
}
