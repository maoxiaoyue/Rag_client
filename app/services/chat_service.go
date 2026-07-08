// 對話回合制引擎：送訊息 → 若 server 要求 client 工具則本地執行 →
// 帶 continuation_id + tool_results 接續 → 直到拿到 final 回覆。
package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"pub_client/app/models"
)

// NewSessionID 產生一個 client 端 session id。新對話首輪由 client 先產生（而非等 server
// 指派），狀態輪詢（ChatStatus）才有 key 可查——server 收到非空 session_id 會沿用。
func NewSessionID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "sess_local"
	}
	return "sess_" + hex.EncodeToString(b[:])
}

// maxAgentsMDBytes 是 AGENTS.md 注入 system 訊息的位元組上限，避免單一巨大檔案把
// context 預算全部吃掉；超過時截斷並註明，行為比照 code_service.go 的 read_file 慣例。
const maxAgentsMDBytes = 20000

// loadAgentsMDPrompt 讀取 workspaceRoot 下的 AGENTS.md（若存在），包成給 LLM 看的
// system 訊息內容；workspaceRoot 為空、檔案不存在、或非合法 UTF-8 時回傳空字串
// （呼叫端不送這則訊息，行為與改動前相同）。
func loadAgentsMDPrompt(workspaceRoot string) string {
	if strings.TrimSpace(workspaceRoot) == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(workspaceRoot, "AGENTS.md"))
	if err != nil || !utf8.Valid(data) {
		return ""
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return ""
	}
	if len(content) > maxAgentsMDBytes {
		content = content[:maxAgentsMDBytes] + "\n... (truncated, AGENTS.md exceeds " + fmt.Sprint(maxAgentsMDBytes) + " bytes)"
	}
	return "Project coding instructions (from AGENTS.md). Follow these for the rest of this coding session:\n\n" + content
}

// AgentsMDSize 回傳 AGENTS.md 將被注入的 system 訊息長度（bytes）；0 = 不會注入。
// 供 UI 在新對話首輪提示使用者 AGENTS.md 有無生效。
func AgentsMDSize(workspaceRoot string) int { return len(loadAgentsMDPrompt(workspaceRoot)) }

// ConfirmFunc 在執行 RequireConfirm 的工具前被呼叫；回 false 表示使用者拒絕。
type ConfirmFunc func(spec models.ToolSpec, args map[string]any) bool

// StatusFunc 用來回報中間進度（例如「執行工具 X …」）。
type StatusFunc func(msg string)

// ChatBackend 對話後端的最小介面——dev_pub_0.9 由 GatewayClient（gRPC → gateway）滿足。
type ChatBackend interface {
	Chat(ctx context.Context, req models.ChatRequest) (*models.ChatResponse, error)
	AgentID() string
}

// ChatEngine 持有一次對話 session 的狀態。
type ChatEngine struct {
	client             ChatBackend
	toolSpecs          []models.ToolSpec
	workspaceRoot      string
	sessionID          string
	preferredSessionID string  // 首輪起始 session id（client 產生，供狀態輪詢）；不影響 AGENTS.md 首輪判斷
	temperature        float32 // 0 = 不送（用 server 預設）
	lastUsage     *models.Usage
	confirm       ConfirmFunc
	status        StatusFunc
	diffApprove   DiffApproveFunc
}

// NewChatEngine 建立引擎。confirm / status / diffApprove 可為 nil；
// workspaceRoot 空字串表示不啟用內建 Coding Agent 工具（見 code_service.go）。
func NewChatEngine(
	client ChatBackend,
	toolSpecs []models.ToolSpec,
	workspaceRoot string,
	confirm ConfirmFunc,
	status StatusFunc,
	diffApprove DiffApproveFunc,
) *ChatEngine {
	return &ChatEngine{
		client:        client,
		toolSpecs:     toolSpecs,
		workspaceRoot: workspaceRoot,
		confirm:       confirm,
		status:        status,
		diffApprove:   diffApprove,
	}
}

// SessionID 回傳目前 session id（首輪後由 server 指派）。
func (e *ChatEngine) SessionID() string { return e.sessionID }

// SetSession 設定既有 session id（跨多次送出延續同一對話脈絡）。
func (e *ChatEngine) SetSession(id string) { e.sessionID = id }

// SetPreferredSessionID 設定新對話首輪要用的 session id（client 端產生）。
// 只在 sessionID 為空（新對話）時生效，且不改變 AGENTS.md 首輪注入的判斷。
func (e *ChatEngine) SetPreferredSessionID(id string) { e.preferredSessionID = id }

// SetTemperature 設定本輪對話的取樣溫度；0 表示不送、用 server 預設。
// 只需在首次請求帶上——後端 continuation 會記住首輪的 temperature。
func (e *ChatEngine) SetTemperature(t float32) { e.temperature = t }

// Usage 回傳最近一次 final 回覆的 token 用量（server 沒回時為 nil）。
func (e *ChatEngine) Usage() *models.Usage { return e.lastUsage }

// Send 送一則使用者訊息，跑完整工具回合制，回傳終局 assistant 文字。
func (e *ChatEngine) Send(ctx context.Context, userText string) (string, error) {
	clientTools := append(ToolManifest(e.toolSpecs), BuiltinCodeTools(e.workspaceRoot)...)

	var msgs []models.Msg
	// 只在新對話的第一輪注入 AGENTS.md：e.sessionID 為空代表這是本次對話脈絡的起點
	// （沿用同一個 session 的後續輪次不會重送，避免每輪都重複吃掉 context 預算）。
	if e.sessionID == "" {
		if sys := loadAgentsMDPrompt(e.workspaceRoot); sys != "" {
			msgs = append(msgs, models.Msg{Role: "system", Content: sys})
		}
	}
	msgs = append(msgs, models.Msg{Role: "user", Content: userText})

	// 新對話首輪：若 client 已預先產生 session id（供狀態輪詢）就帶上，否則留空由 server 指派。
	sid := e.sessionID
	if sid == "" {
		sid = e.preferredSessionID
	}
	req := models.ChatRequest{
		AgentID:     e.client.AgentID(),
		SessionID:   sid,
		Messages:    msgs,
		ClientTools: clientTools,
		Temperature: e.temperature,
	}

	const maxRounds = 12
	for round := 0; round < maxRounds; round++ {
		resp, err := e.client.Chat(ctx, req)
		if err != nil {
			return "", err
		}
		if e.sessionID == "" {
			e.sessionID = resp.SessionID
		}

		if resp.Status == "final" {
			e.lastUsage = resp.Usage
			return resp.Content, nil
		}
		// status == "tool_calls" → 本地執行
		results := e.runTools(ctx, resp.ToolCalls)
		req = models.ChatRequest{
			AgentID:        e.client.AgentID(),
			SessionID:      e.sessionID,
			ContinuationID: resp.ContinuationID,
			ToolResults:    results,
		}
	}
	return "", fmt.Errorf("%w (max=%d)", ErrChatRoundLimit, maxRounds)
}

func (e *ChatEngine) runTools(ctx context.Context, calls []models.ToolCall) []models.ToolResult {
	results := make([]models.ToolResult, 0, len(calls))
	for _, call := range calls {
		if IsCodeTool(call.Name) {
			if e.status != nil {
				e.status(fmt.Sprintf("Running built-in code tool %s...", call.Name))
			}
			results = append(results, ExecuteCodeTool(ctx, e.workspaceRoot, call, e.diffApprove))
			continue
		}

		spec, ok := FindTool(e.toolSpecs, call.Name)
		if !ok {
			results = append(results, models.ToolResult{
				ToolCallID: call.ID, Name: call.Name,
				Content: ErrToolNotFound.Message, IsError: true,
			})
			continue
		}
		if spec.RequireConfirm && e.confirm != nil && !e.confirm(spec, call.Args) {
			results = append(results, models.ToolResult{
				ToolCallID: call.ID, Name: call.Name,
				Content: ErrToolDenied.Message, IsError: true,
			})
			continue
		}
		if e.status != nil {
			e.status(fmt.Sprintf("Running local tool %s...", call.Name))
		}
		results = append(results, ExecuteTool(ctx, spec, call))
	}
	return results
}
