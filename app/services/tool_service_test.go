package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"pub_client/app/models"
)

func TestSubstituteArgs(t *testing.T) {
	got := substituteArgs([]string{"/c", "echo {{text}}", "--n={{count}}"},
		map[string]any{"text": "hello world", "count": 3})
	want := []string{"/c", "echo hello world", "--n=3"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSubstituteArgs_StripsUnresolved(t *testing.T) {
	got := substituteArgs([]string{"prefix-{{missing}}-suffix"}, map[string]any{})
	if got[0] != "prefix--suffix" {
		t.Errorf("unresolved placeholder not stripped: %q", got[0])
	}
}

func TestExecuteTool_NoCommand(t *testing.T) {
	res := ExecuteTool(context.Background(), models.ToolSpec{Name: "x"}, models.ToolCall{ID: "c1", Name: "x"})
	if !res.IsError {
		t.Error("expected error when command empty")
	}
	if res.ToolCallID != "c1" {
		t.Errorf("tool_call_id not propagated: %q", res.ToolCallID)
	}
}

// 視窗程式（Background=true）：只負責啟動，立刻回傳，不等待行程結束。
func TestExecuteTool_Background_ReturnsImmediately(t *testing.T) {
	spec := models.ToolSpec{
		Name:       "open_notepad",
		Command:    "cmd",
		Args:       []string{"/c", "timeout /t 5 >nul"}, // 模擬「不會馬上結束」的視窗程式
		Background: true,
	}
	done := make(chan models.ToolResult, 1)
	go func() {
		done <- ExecuteTool(context.Background(), spec, models.ToolCall{ID: "c1", Name: "open_notepad"})
	}()

	select {
	case res := <-done:
		if res.IsError {
			t.Errorf("background launch should not error: %+v", res)
		}
		if res.ToolCallID != "c1" {
			t.Errorf("tool_call_id not propagated: %q", res.ToolCallID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("background launch should return immediately, but it blocked")
	}
}

func TestExecuteTool_Background_LaunchError(t *testing.T) {
	res := ExecuteTool(context.Background(),
		models.ToolSpec{Name: "bad", Command: "this-binary-does-not-exist-xyz", Background: true},
		models.ToolCall{ID: "c2", Name: "bad"})
	if !res.IsError {
		t.Error("expected error when background command does not exist")
	}
}

func TestToolManifest_SkipsEmptyNames(t *testing.T) {
	specs := []models.ToolSpec{
		{Name: "ok", Description: "d", Schema: json.RawMessage(`{"type":"object"}`)},
		{Name: ""},
	}
	m := ToolManifest(specs)
	if len(m) != 1 || m[0].Name != "ok" {
		t.Fatalf("manifest = %+v", m)
	}
}
