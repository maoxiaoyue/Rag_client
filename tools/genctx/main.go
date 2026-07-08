// Command genctx 產生 .hyp/context.yaml（Schema manifest）。
// 用法：go run ./tools/genctx
//
// 若 .hyp/llm.yaml 啟用 LLM（mode=ollama / api），manifest 會自動帶 LLM 增強的
// Summary、Tags、Description；否則退回純 Go 推斷。
// LLM 失敗的詳細原因（HTTP 4xx/5xx、JSON 解析錯誤等）會印到 stderr。
package main

import (
	"fmt"
	"os"

	"github.com/maoxiaoyue/hypgo/pkg/logger"
	"github.com/maoxiaoyue/hypgo/pkg/manifest"
	"github.com/maoxiaoyue/hypgo/pkg/router"

	"pub_client/app/views"
)

func main() {
	views.RegisterSchemas() // 把桌面視圖 schema 填進 schema.Global()

	// stderr logger：讓 AutoSync 的 LLM 啟用訊息與失敗原因顯示出來，
	// 避免「設定錯誤但 LLM 靜默退回純推斷」的盲區
	log, _ := logger.New("info", "stderr", os.Stderr, false)

	// AutoSync 會自動探測同目錄的 .hyp/llm.yaml；無檔或 mode=none 時退回純 Go 推斷。
	// 桌面專案沒有 REST router，傳空 router.New() 即可——desktop schema 來自全域 schema.Global()。
	as := manifest.NewAutoSync(manifest.AutoSyncConfig{Enabled: true}, router.New(), nil, log)
	if err := as.Sync(); err != nil {
		fmt.Fprintln(os.Stderr, "genctx:", err)
		os.Exit(1)
	}
	fmt.Println("manifest written to", manifest.DefaultContextPath)
}
