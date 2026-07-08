// 把使用者登錄的本地工具轉成給 agent 的 manifest，並在本機執行。
//
// 安全設計：
//   - 只執行使用者明確登錄的工具（allowlist）。
//   - 參數以 argv 樣板 {{name}} 替換，不經 shell 解析（避免注入）。
//   - 硬性 timeout。
//   - RequireConfirm 的工具由呼叫端（view）在執行前彈確認框。
package services

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"pub_client/app/models"
)

// ToolManifest 把登錄工具轉成 /api/chat 的 client_tools。
func ToolManifest(specs []models.ToolSpec) []models.ChatToolSpec {
	out := make([]models.ChatToolSpec, 0, len(specs))
	for _, s := range specs {
		if strings.TrimSpace(s.Name) == "" {
			continue
		}
		out = append(out, models.ChatToolSpec{
			Name:        s.Name,
			Description: s.Description,
			Schema:      s.Schema,
		})
	}
	return out
}

// FindTool 依名稱找登錄工具。
func FindTool(specs []models.ToolSpec, name string) (models.ToolSpec, bool) {
	for _, s := range specs {
		if s.Name == name {
			return s, true
		}
	}
	return models.ToolSpec{}, false
}

// ExecuteTool 在本機執行一個工具呼叫，回傳結果（永不 panic；錯誤包進 ToolResult）。
//
// spec.Background == true（視窗程式）：只負責啟動，不等待結束、不套用 timeout——
// 否則 GUI 程式（記事本、瀏覽器…）不會自己退出，會被 context timeout 強制砍掉。
func ExecuteTool(ctx context.Context, spec models.ToolSpec, call models.ToolCall) models.ToolResult {
	res := models.ToolResult{ToolCallID: call.ID, Name: call.Name}

	if strings.TrimSpace(spec.Command) == "" {
		res.IsError = true
		res.Content = "tool has no command configured"
		return res
	}

	argv := substituteArgs(spec.Args, call.Args)

	if spec.Background {
		return launchBackground(spec, call, argv)
	}

	timeout := time.Duration(spec.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, spec.Command, argv...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	content := strings.TrimRight(out.String(), "\r\n")

	if runCtx.Err() == context.DeadlineExceeded {
		res.IsError = true
		res.Content = fmt.Sprintf("timeout after %s\n%s", timeout, content)
		return res
	}
	if err != nil {
		res.IsError = true
		res.Content = fmt.Sprintf("exit error: %v\n%s", err, content)
		return res
	}
	if content == "" {
		content = "(no output; exit 0)"
	}
	res.Content = content
	return res
}

// launchBackground 啟動視窗 / 常駐程式後立刻回傳，不等待其結束。
// 用獨立 goroutine Wait() 回收子行程，避免 zombie；不綁 ctx，程式壽命與工具呼叫無關。
func launchBackground(spec models.ToolSpec, call models.ToolCall, argv []string) models.ToolResult {
	res := models.ToolResult{ToolCallID: call.ID, Name: call.Name}

	cmd := exec.Command(spec.Command, argv...)
	if err := cmd.Start(); err != nil {
		res.IsError = true
		res.Content = fmt.Sprintf("launch error: %v", err)
		return res
	}
	go func() { _ = cmd.Wait() }()

	res.Content = fmt.Sprintf("Launched %s (PID %d, running in background, not waiting for it to exit)", spec.Command, cmd.Process.Pid)
	return res
}

// substituteArgs 把 argv 樣板中的 {{key}} 換成 args[key]（fmt.Sprint）。
// 未對應的 {{key}} 換成空字串。
func substituteArgs(template []string, args map[string]any) []string {
	out := make([]string, 0, len(template))
	for _, a := range template {
		for k, v := range args {
			a = strings.ReplaceAll(a, "{{"+k+"}}", fmt.Sprint(v))
		}
		out = append(out, stripUnresolved(a))
	}
	return out
}

// stripUnresolved 移除剩餘未替換的 {{...}} 佔位。
func stripUnresolved(s string) string {
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "}}")
		if j < 0 {
			return s
		}
		s = s[:i] + s[i+j+2:]
	}
}
