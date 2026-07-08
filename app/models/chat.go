package models

import "encoding/json"

// 對應後端（agents 服務 :18080）：
//   POST /api/chat            （ChatRequest / ChatResponse）
//   POST /api/ingest/upload   （multipart：agent_id, tags, files[]）
//   GET  /api/ingest/jobs/:id （Job）

// Msg 一則對話訊息。
type Msg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatToolSpec 送給 agent 的本地工具描述（/api/chat 的 client_tools）。
type ChatToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

// ToolResult 本地工具執行結果，回送給 agent。
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error,omitempty"`
}

// ChatRequest /api/chat 請求 body。
type ChatRequest struct {
	AgentID        string         `json:"agent_id"`
	SessionID      string         `json:"session_id,omitempty"`
	Messages       []Msg          `json:"messages,omitempty"`
	ClientTools    []ChatToolSpec `json:"client_tools,omitempty"`
	ContinuationID string         `json:"continuation_id,omitempty"`
	ToolResults    []ToolResult   `json:"tool_results,omitempty"`
	Temperature    float32        `json:"temperature,omitempty"`
}

// ToolCall agent 要求本機執行的工具呼叫。
type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// Usage token 用量統計。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse /api/chat 回應 body。
type ChatResponse struct {
	Status         string     `json:"status"` // "final" | "tool_calls"
	Content        string     `json:"content,omitempty"`
	SessionID      string     `json:"session_id"`
	ContinuationID string     `json:"continuation_id,omitempty"`
	ToolCalls      []ToolCall `json:"tool_calls,omitempty"`
	Usage          *Usage     `json:"usage,omitempty"`
}

// Job 攝取任務狀態（/api/ingest/jobs/:id）。
type Job struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	TotalChunks int    `json:"total_chunks"`
	DoneChunks  int    `json:"done_chunks"`
	Error       string `json:"error"`
}
