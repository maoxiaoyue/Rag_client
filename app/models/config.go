// Package models 持有 pub_client 的資料結構：client 設定與後端 DTO。
// 設定的持久化在 app/store（DuckDB 加密庫），本套件只有純資料結構。
package models

import (
	"encoding/json"
)

// ToolSpec 一個使用者登錄的本地工具（由本機 client 執行）。
//
//	Schema     : 給 LLM 看的 JSON Schema（object），描述參數。
//	Command    : 要執行的程式（絕對路徑或 PATH 中可找到的名稱）。
//	Args       : argv 樣板；元素中的 {{name}} 會以 tool_call 的對應參數替換。
//	             不經 shell 解析（避免注入）。
//	Background : true 表示這是「視窗程式」（記事本、瀏覽器、小畫家…）——只負責啟動，
//	             不等待它結束、不套用 TimeoutSec；否則一般命令列工具會被等到逾時砍掉。
type ToolSpec struct {
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Schema         json.RawMessage `json:"schema,omitempty"`
	Command        string          `json:"command"`
	Args           []string        `json:"args,omitempty"`
	RequireConfirm bool            `json:"require_confirm"`
	TimeoutSec     int             `json:"timeout_sec"`
	Background     bool            `json:"background,omitempty"`
}

// Config client 全域設定。
//
// dev_pub_0.9：三個後端 URL（agents/graph/embedding）收斂成單一 GatewayAddr +
// APIKey——client 只認識 agent_gateway 一個位址，全部流量走 gRPC。
type Config struct {
	// GatewayAddr agent_gateway gRPC 位址（host:port，不帶 scheme），例 "127.0.0.1:9090"
	GatewayAddr string `json:"gateway_addr"`
	// APIKey gateway 簽發的 API Key（sak_ 開頭；一 Key 只能綁一台裝置）
	APIKey string `json:"api_key,omitempty"`
	// DeviceID 隨機 fallback 裝置 id——只在取不到 Windows MachineGuid 時產生並持久化；
	// 正常情況由機器特徵推導、不落地（見 services.StableDeviceID）
	DeviceID string `json:"device_id,omitempty"`

	AgentID       string     `json:"agent_id"`     // 例 agent-main
	InsecureTLS   bool       `json:"insecure_tls"` // 自簽憑證時需開（正式上線改 CA pinning）
	Tools         []ToolSpec `json:"tools"`
	WorkspaceRoot string     `json:"workspace_root,omitempty"` // 內建 Coding Agent 工具的根目錄；空 = 停用該功能
	VaultDir      string     `json:"vault_dir,omitempty"`      // Sync Vault 下載目的資料夾（可用 Obsidian 開）；空 = 功能停用
	Temperature   float32    `json:"temperature,omitempty"`    // 對話取樣溫度（0–1）；0 = 用 server 預設
	// KnownAgentIDs 用過的 RAG ID 清單（多 persona 部署時供下拉快速切換）。
	KnownAgentIDs []string `json:"known_agent_ids,omitempty"`
}

// RememberAgentID 把 id 記進 KnownAgentIDs（空字串或已存在時不動作）。
func (c *Config) RememberAgentID(id string) {
	if id == "" {
		return
	}
	for _, known := range c.KnownAgentIDs {
		if known == id {
			return
		}
	}
	c.KnownAgentIDs = append(c.KnownAgentIDs, id)
}

// AgentIDOptions 回傳下拉選項：KnownAgentIDs ∪ 目前 AgentID（保證目前值一定在列）。
func (c *Config) AgentIDOptions() []string {
	opts := append([]string{}, c.KnownAgentIDs...)
	for _, o := range opts {
		if o == c.AgentID {
			return opts
		}
	}
	if c.AgentID != "" {
		opts = append(opts, c.AgentID)
	}
	return opts
}

// DefaultConfig 回傳預設設定。
func DefaultConfig() *Config {
	return &Config{
		GatewayAddr: "127.0.0.1:9090",
		AgentID:     "agent-main",
		InsecureTLS: true,
	}
}
