package services

import "github.com/maoxiaoyue/hypgo/pkg/errors"

// Typed Error Catalog（category="client"）。涵蓋已知的業務錯誤狀態；
// 網路 / 解碼等不可預期的 I/O 錯誤仍用 fmt.Errorf 包裝回傳。
var (
	ErrNoConnection   = errors.Define("E3001", 400, "Connection not set up yet, please check the Settings tab", "client")
	ErrChatRoundLimit = errors.Define("E3002", 500, "Chat round limit exceeded", "client")
	ErrToolNotFound   = errors.Define("E3003", 404, "Tool is not registered on this client", "client")
	ErrToolDenied     = errors.Define("E3004", 403, "User declined to run this tool", "client")
	ErrUploadEmpty    = errors.Define("E3005", 400, "Please add at least one file first", "client")
)
