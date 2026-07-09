# rag_client

外網版 Windows 桌面 client。**所有後端流量都走單一 gRPC 連線**到 `agent_gateway`
（dev_pub_0.9），gateway 再以內部 mTLS/HTTP 轉發給後端的 agents / agent_graph / agent_embedding。

Go + [Fyne](https://fyne.io) v2.4.5 + HypGo v0.8.11。本專案是外網使用者版，僅保留一般使用者功能。

範例圖片((https://raw.githubusercontent.com/maoxiaoyue/Rag_client/refs/heads/main/螢幕擷取畫面%202026-07-07%20185608.png)
## 網路架構

```
pub_client
   │ gRPC + TLS
   │ metadata: x-api-key + x-device-id + x-device-info
   ▼
agent_gateway (ClientGateway)
   ├── agents        (Chat / ChatStatus / ChatImage / UploadIngest / GetIngestJob / Personas)
   ├── agent_graph   (GraphSearch / Context / HybridSearch / Proposed×3)
   └── agent_embedding (VerifySearch)
```

**認證**：`gateway apikey create` 產生 `sak_…` 明文（DB 只存 SHA-256），首次呼叫綁定裝置
（TOFU，SHA-256(MachineGuid+username)）；同一 key 只綁一台裝置，換機需在部署機 `gateway apikey unbind`。

## 使用的 ClientGateway RPC

| 分頁 / 功能 | RPC | 說明 |
|-------------|-----|------|
| 設定頁 Test Connection | `Ping` | 同時驗 API Key + 傳回綁定 key 名稱 + RTT |
| 對話 | `Chat` | 同步對話 + client 端工具回合制（continuation） |
| 對話（狀態列） | `ChatStatus` | 每秒輪詢，顯示 server 正處理的 Agent 1-4 階段（例 `Agent 2 · ToolLoop (3s)`） |
| 對話（附圖） | `ChatImage` | 送圖 → server 視覺模型抽文字 + ingest 進 RAG，文字併入本輪訊息 |
| 上傳 | `UploadIngest` (streaming) / `GetIngestJob` | 檔案分塊上傳 + 攝取任務進度輪詢 |
| 知識圖譜 (待優化) | `GraphSearch` / `GraphContext` / `GraphHybridSearch` | 關鍵字搜尋 / 語意搜尋 / 區域鄰居 |
| 圖譜審核（Pending Relations） | `GraphListProposed` / `GraphApproveProposed` / `GraphRejectProposed` | 人工核准低信心關係 |
| 驗證檢索（上傳後） | `VerifySearch` | 內部 HTTPS 打 agent_embedding |

**啟動時健康檢查**：視窗進入前景後 goroutine `HealthCheck` ping gateway；`Unavailable`/`DeadlineExceeded`
→ 跳警示「**RAG主機已關機**」（`Unauthenticated` 不算關機，只是 key 問題）。時機延到 lifecycle
`OnEnteredForeground`，避免 Fyne painter 未就緒導致 CJK 對話框顯示成豆腐。

## 分頁

| 分頁 | 說明 |
|------|------|
| **Chat** | 同步對話、client 工具回合制、Normal／Code 模式切換、附加圖片（縮圖列，左上 X 移除、點縮圖看原尺寸）、狀態列顯示目前 Agent 階段 |
| **Upload** | 多選檔案（**Windows 原生檔案總管**）+ tags → 上傳攝取 → 輪詢進度 |
| **Tools** | 登錄/編輯/刪除本地可執行工具（argv 樣板 + JSON Schema） |
| **Knowledge Graph** | 進頁即載入最近 15 個實體；關鍵字/語意搜尋、選中查看區域鄰居；Pending Relations 子分頁做人工審核 |
| **Settings** | Gateway Address、API Key（PasswordEntry）、Agent ID（下拉/自填）、TLS、Coding 工作目錄、溫度、Test Connection |

## 儲存（DuckDB 加密庫）

本地設定（gateway 位址 / API Key / device_id / 工具清單…）與對話 session 全部存在單一
**加密 DuckDB 檔** `%APPDATA%\pub_client\store.duckdb`（DuckDB `ATTACH ENCRYPTION_KEY`，AES）。
金鑰來源優先序：
1. 機器特徵（`services.StableDeviceID` 同源：SHA-256(MachineGuid+username)）—— 不落地、跨重裝穩定；DB 檔被單獨複製走無法解開
2. Fallback：`%APPDATA%\pub_client\store.key`（隨機 32 bytes）—— 取不到機器特徵時才產生

首次啟動會**一次性遷移**舊的 `config.json` / `sessions/*.md`（如果存在）→ DuckDB。舊檔保留不刪。

## 建置需求

Fyne 預設 GL driver 需要 **CGO** + Windows gcc（MinGW-w64）；已驗證 Go 1.26 + MinGW-w64 UCRT gcc 15。

```powershell
cd D:\GoProjects\single_agent\pub_client
.\build.ps1        # go build -ldflags "-H=windowsgui" -o pub_client.exe .（無 console 視窗）
.\pub_client.exe
```

測試：
```powershell
go test ./...
```

## Gateway Address 小提醒

Gateway Address 是 **gRPC** 目標，格式 `host:port` 。若貼上帶 `https://`
的 URL，client 會自動去掉 scheme 再連（`normalizeGatewayAddr`），避免 gRPC 誤把整串當主機名再補
`:443` 而報 `too many colons in address`。

## 本地工具與 Coding Agent

Tools 頁登錄的本地工具會以 `client_tools` manifest 隨每輪首請求送給 agent；
agent 決定呼叫時，client 在本機 `exec.CommandContext` 執行後把結果經 `Chat` 的 continuation 回注。
Normal/Code 模式切換、內建 Coding Agent（`code_list_files` / `code_search` / `code_read_file` /
`code_edit_file` / `code_write_file`，寫入前 diff 審核）語意完全一致，詳見 `rag_admin/README.md`。

**視窗程式**（記事本、瀏覽器等 GUI）勾選「視窗程式」→ `Background=true` → `cmd.Start()` 不等待、
不套 timeout；一般命令列工具走 `exec.CommandContext` 等待完成，argv 樣板不經 shell 插值，預設 30s 逾時。

## 幾個 UI 細節

- **CJK 字型自動載入**：Fyne 內建字型無中文字形（且 ParseTTF 不吃 `.ttc` 集合），啟動時載入
  系統純 TTF（NotoSansTC-VF → simhei → kaiu，可用 `FYNE_FONT` 覆寫）。
- **字級**：文字類尺寸 × 0.85，接近 Claude Desktop 觀感（padding/icon 不動）。
- **檔案對話框**：用 Windows 原生檔案總管（comdlg32 `GetOpenFileNameW`，多選 + filter），
  取代 Fyne 內建版本。
- **History 按鈕**：先列最近 20 條 session 標題（第一則 `**You:**` 為標題），點一條才開內容。
- **附加圖片**：縮圖列（`canvas.Image` + 可點包裝），左上 X 移除、點縮圖看原尺寸。
- **輸入法（IME）已知限制**：Fyne 2.4.5 沒有 IME preedit 支援，中文輸入時組字過程會在 OS 輸入法
  浮動視窗顯示（不在輸入框內），確認後才進框；修復需升 Fyne 到 2.5+，待決。

## 注意

- 對話工具回合制只在後端 LLM 支援 tool calling 時生效（目前 Ollama provider）。
- 圖片辨識（`ChatImage` 與 Upload）需後端 `ingest.vision.enabled=true`；掃描型 PDF 沒 OCR。
- gateway 對外憑證預設可能為自簽 → 設定頁「Allow self-signed」預設開啟；正式部署改成 CA pinning。
