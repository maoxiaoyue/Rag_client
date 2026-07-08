package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pub_client/app/models"
)

func TestBuiltinCodeTools_EmptyRootDisabled(t *testing.T) {
	if got := BuiltinCodeTools(""); len(got) != 0 {
		t.Errorf("empty root should disable code tools, got %d specs", len(got))
	}
	if got := BuiltinCodeTools("  "); len(got) != 0 {
		t.Errorf("whitespace-only root should disable code tools, got %d specs", len(got))
	}
}

func TestBuiltinCodeTools_WithRoot(t *testing.T) {
	got := BuiltinCodeTools(t.TempDir())
	if len(got) != 5 {
		t.Fatalf("want 5 code tool specs, got %d", len(got))
	}
	for _, s := range got {
		if !IsCodeTool(s.Name) {
			t.Errorf("spec %q should satisfy IsCodeTool", s.Name)
		}
		if len(s.Schema) == 0 {
			t.Errorf("spec %q missing schema", s.Name)
		}
	}
}

func TestIsCodeTool(t *testing.T) {
	if !IsCodeTool("code_read_file") {
		t.Error("code_read_file should be a code tool")
	}
	if IsCodeTool("my_custom_tool") {
		t.Error("my_custom_tool should not be a code tool")
	}
}

func TestResolveInWorkspace_BlocksEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := resolveInWorkspace(root, "../outside.txt"); err == nil {
		t.Error("expected error for path escaping workspace root")
	}
	if _, err := resolveInWorkspace(root, "../../etc/passwd"); err == nil {
		t.Error("expected error for deep path escape")
	}
	if _, err := resolveInWorkspace(root, "sub/file.txt"); err != nil {
		t.Errorf("normal relative path should resolve fine: %v", err)
	}
}

func TestReadFile_NotFoundAndEscape(t *testing.T) {
	root := t.TempDir()

	res := ExecuteCodeTool(context.Background(), root,
		models.ToolCall{ID: "1", Name: "code_read_file", Args: map[string]any{"path": "../outside.txt"}}, nil)
	if !res.IsError {
		t.Error("expected error for escaping path")
	}

	res2 := ExecuteCodeTool(context.Background(), root,
		models.ToolCall{ID: "2", Name: "code_read_file", Args: map[string]any{"path": "nope.txt"}}, nil)
	if !res2.IsError {
		t.Error("expected error for missing file")
	}
}

func TestReadFile_OffsetLimitAndTruncation(t *testing.T) {
	root := t.TempDir()
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, "line"+string(rune('0'+i%10)))
	}
	content := strings.Join(lines, "\n")
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	res := ExecuteCodeTool(context.Background(), root, models.ToolCall{
		ID: "1", Name: "code_read_file",
		Args: map[string]any{"path": "f.txt", "offset": float64(3), "limit": float64(2)},
	}, nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "3\t") || !strings.Contains(res.Content, "4\t") {
		t.Errorf("expected lines 3-4 in output, got: %q", res.Content)
	}
	if strings.Contains(res.Content, "1\tline1") {
		t.Errorf("offset should have skipped line 1: %q", res.Content)
	}
	if !strings.Contains(res.Content, "more lines not shown") {
		t.Errorf("expected remaining-lines hint, got: %q", res.Content)
	}
}

func TestReadFile_RejectsBinary(t *testing.T) {
	root := t.TempDir()
	binPath := filepath.Join(root, "bin.dat")
	if err := os.WriteFile(binPath, []byte{0xff, 0xfe, 0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	res := ExecuteCodeTool(context.Background(), root,
		models.ToolCall{ID: "1", Name: "code_read_file", Args: map[string]any{"path": "bin.dat"}}, nil)
	if !res.IsError {
		t.Error("expected error reading binary file as text")
	}
}

func TestListFiles(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	os.MkdirAll(filepath.Join(root, ".git"), 0o755) // 應被跳過

	res := ExecuteCodeTool(context.Background(), root,
		models.ToolCall{ID: "1", Name: "code_list_files", Args: map[string]any{}}, nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go") || !strings.Contains(res.Content, "sub/") {
		t.Errorf("expected a.go and sub/ in listing, got: %q", res.Content)
	}
	if strings.Contains(res.Content, ".git") {
		t.Errorf(".git should be skipped, got: %q", res.Content)
	}
}

func TestSearchCode(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\nfunc Hello() {}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "b.go"), []byte("package b\n// nothing here\n"), 0o644)

	res := ExecuteCodeTool(context.Background(), root,
		models.ToolCall{ID: "1", Name: "code_search", Args: map[string]any{"query": "hello"}}, nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go:2") {
		t.Errorf("expected match in a.go line 2, got: %q", res.Content)
	}
	if strings.Contains(res.Content, "b.go") {
		t.Errorf("b.go should not match, got: %q", res.Content)
	}
}

func TestSearchCode_GlobFilter(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("needle here\n"), 0o644)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("needle here too\n"), 0o644)

	res := ExecuteCodeTool(context.Background(), root, models.ToolCall{
		ID: "1", Name: "code_search",
		Args: map[string]any{"query": "needle", "glob": "*.go"},
	}, nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go") {
		t.Errorf("expected a.go match, got: %q", res.Content)
	}
	if strings.Contains(res.Content, "b.txt") {
		t.Errorf("b.txt should be filtered out by glob, got: %q", res.Content)
	}

	bad := ExecuteCodeTool(context.Background(), root, models.ToolCall{
		ID: "2", Name: "code_search",
		Args: map[string]any{"query": "needle", "glob": "[unclosed"},
	}, nil)
	if !bad.IsError {
		t.Error("expected error for invalid glob pattern")
	}
}

func TestEditFile_NotFoundOrAmbiguous(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f.go"), []byte("foo\nfoo\nbar\n"), 0o644)

	// old_string 找不到
	res := ExecuteCodeTool(context.Background(), root, models.ToolCall{
		ID: "1", Name: "code_edit_file",
		Args: map[string]any{"path": "f.go", "old_string": "zzz", "new_string": "yyy"},
	}, alwaysApprove)
	if !res.IsError {
		t.Error("expected error when old_string not found")
	}

	// old_string 不唯一且未 replace_all
	res2 := ExecuteCodeTool(context.Background(), root, models.ToolCall{
		ID: "2", Name: "code_edit_file",
		Args: map[string]any{"path": "f.go", "old_string": "foo", "new_string": "baz"},
	}, alwaysApprove)
	if !res2.IsError {
		t.Error("expected error when old_string is not unique")
	}
}

func TestEditFile_ApproveAndReject(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.go")
	os.WriteFile(path, []byte("hello world\n"), 0o644)

	// 拒絕 → 檔案不變
	res := ExecuteCodeTool(context.Background(), root, models.ToolCall{
		ID: "1", Name: "code_edit_file",
		Args: map[string]any{"path": "f.go", "old_string": "hello", "new_string": "goodbye"},
	}, neverApprove)
	if !res.IsError {
		t.Error("expected IsError when user rejects diff")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "hello world\n" {
		t.Errorf("file should be unchanged after rejection, got: %q", data)
	}

	// 核准 → 檔案真的被改
	res2 := ExecuteCodeTool(context.Background(), root, models.ToolCall{
		ID: "2", Name: "code_edit_file",
		Args: map[string]any{"path": "f.go", "old_string": "hello", "new_string": "goodbye"},
	}, alwaysApprove)
	if res2.IsError {
		t.Fatalf("unexpected error: %s", res2.Content)
	}
	data2, _ := os.ReadFile(path)
	if string(data2) != "goodbye world\n" {
		t.Errorf("file should be updated after approval, got: %q", data2)
	}
}

func TestEditFile_ReplaceAll(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.go")
	os.WriteFile(path, []byte("foo foo foo\n"), 0o644)

	res := ExecuteCodeTool(context.Background(), root, models.ToolCall{
		ID: "1", Name: "code_edit_file",
		Args: map[string]any{"path": "f.go", "old_string": "foo", "new_string": "bar", "replace_all": true},
	}, alwaysApprove)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "bar bar bar\n" {
		t.Errorf("expected all occurrences replaced, got: %q", data)
	}
}

func TestWriteFile_NewFileWithParentDirs(t *testing.T) {
	root := t.TempDir()

	res := ExecuteCodeTool(context.Background(), root, models.ToolCall{
		ID: "1", Name: "code_write_file",
		Args: map[string]any{"path": "a/b/c.go", "content": "package c\n"},
	}, alwaysApprove)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	data, err := os.ReadFile(filepath.Join(root, "a", "b", "c.go"))
	if err != nil {
		t.Fatalf("expected file to be created: %v", err)
	}
	if string(data) != "package c\n" {
		t.Errorf("content mismatch: %q", data)
	}
}

func TestWriteFile_RejectDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	res := ExecuteCodeTool(context.Background(), root, models.ToolCall{
		ID: "1", Name: "code_write_file",
		Args: map[string]any{"path": "new.go", "content": "package x\n"},
	}, neverApprove)
	if !res.IsError {
		t.Error("expected IsError when user rejects")
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); err == nil {
		t.Error("file should not exist after rejection")
	}
}

func TestEditFile_CreatesBackup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.go")
	os.WriteFile(path, []byte("hello world\n"), 0o644)

	res := ExecuteCodeTool(context.Background(), root, models.ToolCall{
		ID: "1", Name: "code_edit_file",
		Args: map[string]any{"path": "f.go", "old_string": "hello", "new_string": "goodbye"},
	}, alwaysApprove)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("expected .bak backup to exist: %v", err)
	}
	if string(bak) != "hello world\n" {
		t.Errorf(".bak should hold the previous content, got: %q", bak)
	}
	if !strings.Contains(res.Content, ".bak") {
		t.Errorf("result message should mention the backup, got: %q", res.Content)
	}
}

func TestWriteFile_NewFile_NoBackup(t *testing.T) {
	root := t.TempDir()
	res := ExecuteCodeTool(context.Background(), root, models.ToolCall{
		ID: "1", Name: "code_write_file",
		Args: map[string]any{"path": "new.go", "content": "package x\n"},
	}, alwaysApprove)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if _, err := os.Stat(filepath.Join(root, "new.go.bak")); err == nil {
		t.Error("new file should not produce a .bak backup")
	}
}

func TestListAndSearch_SkipBackupFiles(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	os.WriteFile(filepath.Join(root, "a.go.bak"), []byte("package a_old\n"), 0o644)

	list := ExecuteCodeTool(context.Background(), root,
		models.ToolCall{ID: "1", Name: "code_list_files", Args: map[string]any{}}, nil)
	if strings.Contains(list.Content, ".bak") {
		t.Errorf(".bak files should be hidden from listing, got: %q", list.Content)
	}

	search := ExecuteCodeTool(context.Background(), root,
		models.ToolCall{ID: "2", Name: "code_search", Args: map[string]any{"query": "a_old"}}, nil)
	if strings.Contains(search.Content, "a.go.bak") {
		t.Errorf(".bak files should be excluded from search, got: %q", search.Content)
	}
}

func TestBuildUnifiedDiff_Format(t *testing.T) {
	diff, err := buildUnifiedDiff("f.go", "old line\n", "new line\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, marker := range []string{"---", "+++", "@@"} {
		if !strings.Contains(diff, marker) {
			t.Errorf("diff missing marker %q:\n%s", marker, diff)
		}
	}
}

func TestExecuteCodeTool_UnknownName(t *testing.T) {
	res := ExecuteCodeTool(context.Background(), t.TempDir(),
		models.ToolCall{ID: "1", Name: "code_does_not_exist"}, nil)
	if !res.IsError {
		t.Error("expected error for unknown code tool")
	}
}

func TestExecuteCodeTool_NoWorkspaceRoot(t *testing.T) {
	res := ExecuteCodeTool(context.Background(), "",
		models.ToolCall{ID: "1", Name: "code_read_file", Args: map[string]any{"path": "f.go"}}, nil)
	if !res.IsError {
		t.Error("expected error when workspace root not configured")
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func alwaysApprove(_ string, _ string) bool { return true }
func neverApprove(_ string, _ string) bool  { return false }
