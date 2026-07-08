package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pub_client/app/models"
)

func TestLoadAgentsMDPrompt_EmptyRootDisabled(t *testing.T) {
	if got := loadAgentsMDPrompt(""); got != "" {
		t.Errorf("empty root should return empty prompt, got %q", got)
	}
	if got := loadAgentsMDPrompt("  "); got != "" {
		t.Errorf("whitespace-only root should return empty prompt, got %q", got)
	}
}

func TestLoadAgentsMDPrompt_MissingFile(t *testing.T) {
	if got := loadAgentsMDPrompt(t.TempDir()); got != "" {
		t.Errorf("missing AGENTS.md should return empty prompt, got %q", got)
	}
}

func TestLoadAgentsMDPrompt_ReadsContent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Use tabs, not spaces."), 0o644); err != nil {
		t.Fatal(err)
	}
	got := loadAgentsMDPrompt(root)
	if !strings.Contains(got, "Use tabs, not spaces.") {
		t.Errorf("expected AGENTS.md content in prompt, got %q", got)
	}
	if !strings.Contains(got, "AGENTS.md") {
		t.Errorf("expected prompt to reference AGENTS.md as the source, got %q", got)
	}
}

func TestLoadAgentsMDPrompt_EmptyFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("   \n  "), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadAgentsMDPrompt(root); got != "" {
		t.Errorf("blank AGENTS.md should return empty prompt, got %q", got)
	}
}

func TestLoadAgentsMDPrompt_RejectsBinary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte{0xff, 0xfe, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadAgentsMDPrompt(root); got != "" {
		t.Errorf("non-UTF-8 AGENTS.md should return empty prompt, got %q", got)
	}
}

func TestLoadAgentsMDPrompt_TruncatesLongFile(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("x", maxAgentsMDBytes+500)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	got := loadAgentsMDPrompt(root)
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected truncation note for oversized AGENTS.md, got length %d", len(got))
	}
	if len(got) > maxAgentsMDBytes+300 {
		t.Errorf("expected prompt to be capped near maxAgentsMDBytes, got length %d", len(got))
	}
}

// stubBackend 最小 ChatBackend fake（dev_pub_0.9 之後真後端是 GatewayClient/gRPC，
// engine 測試不再走 HTTP）。
type stubBackend struct {
	gotReq models.ChatRequest
	resp   *models.ChatResponse
}

func (s *stubBackend) Chat(_ context.Context, req models.ChatRequest) (*models.ChatResponse, error) {
	s.gotReq = req
	return s.resp, nil
}
func (s *stubBackend) AgentID() string { return "agent-x" }

func TestChatEngine_SendsTemperatureAndCapturesUsage(t *testing.T) {
	backend := &stubBackend{resp: &models.ChatResponse{
		Status:    "final",
		Content:   "hi there",
		SessionID: "sess_test1",
		Usage:     &models.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}}

	eng := NewChatEngine(backend, nil, "", nil, nil, nil)
	eng.SetTemperature(0.7)

	reply, err := eng.Send(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply != "hi there" {
		t.Errorf("unexpected reply: %q", reply)
	}
	if backend.gotReq.Temperature != 0.7 {
		t.Errorf("expected temperature 0.7 in request, got %v", backend.gotReq.Temperature)
	}
	if eng.SessionID() != "sess_test1" {
		t.Errorf("expected session id captured, got %q", eng.SessionID())
	}
	u := eng.Usage()
	if u == nil || u.TotalTokens != 15 {
		t.Errorf("expected usage captured (total=15), got %+v", u)
	}
}
