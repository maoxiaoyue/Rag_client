// pub_client — Windows 桌面 client，連線到 agents/RAG 平台。
//
// 功能：
//   - 設定後端 URL/port（設定頁）
//   - 與 agent 同步對話，並在 agent 要求時於本機執行已登錄的工具（對話頁 + 工具頁）
//   - 上傳 PDF / 圖片 / 文字做知識攝取（上傳頁）
//
// 建置需求：CGO（Fyne 預設 GL driver），Windows 上需 gcc（MinGW-w64）。
package main

import "pub_client/app/views"

func main() {
	views.RegisterSchemas()
	views.Run()
}
