# agent_client

連線到 single_agent / RAG 平台（`agents` 服務 :18080）的 Windows 桌面 client。
Go + [Fyne](https://fyne.io) v2.4.5 + HypGo v0.8.11（logger / errors / schema）。

## 功能

| 分頁 | 說明 | 對接後端 |
|------|------|----------|
| **對話** | 與 agent 同步對話；agent 要求時於本機執行已登錄工具（回合制）；**Normal／Code 模式切換按鈕** | `POST /api/chat` |
| **上傳文件** | 選 PDF / 圖片 / 文字 → 上傳攝取 → 輪詢進度 | `POST /api/ingest/upload`、`GET /api/ingest/jobs/:id` |
| **工具** | 登錄 / 編輯 / 刪除本地可執行工具（command + argv 樣板 + JSON Schema） | （manifest 隨對話送出） |
| **設定** | Agent Base URL、Agent ID、自簽 TLS、**Coding 工作目錄** | — |
| **(內建) Coding Agent** | 工作目錄已設 **且** 對話頁切到 Code 模式時才啟用：agent 可讀檔/列目錄/搜尋/編輯/寫檔，**寫入前強制彈 diff 給你審核**；Normal 模式下完全不會出現，不會進入編輯狀態 | （manifest 隨對話送出，見下方專節） |

使用者設定持久化於 `%APPDATA%\agent_client\config.json`；runtime 設定（logger）在 `app/config/config.yaml`。

## 目錄結構（HypGo 風格）

```
agent_client/
  main.go                    # RegisterSchemas() → views.Run()
  app/
    models/                  # 資料結構
      config.go              #   Config + ToolSpec + %APPDATA% 持久化
      chat.go                #   /api/chat、/api/ingest 的 DTO
    services/                # 業務邏輯（views 不直接做 I/O）
      api_service.go         #   APIClient：Chat / Upload(multipart) / GetJob
      chat_service.go        #   ChatEngine：對話回合制引擎（工具迴圈，上限 12 回合）
      tool_service.go        #   本地工具執行：manifest / find / execute（argv 樣板）
      code_service.go        #   內建 Coding Agent 工具：read/list/search/edit/write + diff 審核
      errors.go              #   typed error catalog（E3001–E3005，hypgo/pkg/errors）
    views/                   # Fyne 畫面
      main_view.go           #   State + logger + 四分頁組裝
      chat_view.go / upload_view.go / tools_view.go / settings_view.go
      diff_dialog.go         #   showDiffApproval()：寫入前的 diff 審核彈窗
      schema.go              #   schema.RegisterDesktop()（Protocol "desktop"）
    config/config.yaml       # logger 區段（桌面 app 不開 HTTP server）
  tools/genctx/              # CLI：產生 .hyp/context.yaml（Schema manifest，可 LLM 增強）
  .hyp/                      # hypgo 工具鏈設定（config / llm / context / comment）
```

## 建置需求

Fyne 預設 GL driver 需要 **CGO**；Windows 上需安裝 gcc（MinGW-w64）。本機已驗證可建置（go 1.26 + MinGW-w64 UCRT gcc 15）。

```powershell
cd D:\GoProjects\single_agent\agent_client
go build -o agent_client.exe .
.\agent_client.exe
```

測試與 manifest：

```powershell
go test ./...            # tool_service 單元測試（argv 套版 / timeout / manifest）
go run ./tools/genctx    # 重新產生 .hyp/context.yaml
```

## 本地工具如何運作（client 端執行）

1. 在「工具」頁登錄工具：名稱、描述、**命令**（可執行檔）、**參數樣板**（每行一個 argv，用 `{{參數名}}` 代入）、**參數 Schema**（JSON Schema object）。
2. 每次對話時，這些工具的 name/description/schema 會作為 `client_tools` 送給 agent。
3. agent（LLM）若決定呼叫某工具，後端回 `status=tool_calls` + `continuation_id`；client 在本機執行（`exec.Command`，**不經 shell**），把 stdout/stderr 以 `tool_results` 回送；後端接續推理直到 `final`。

### 安全
- 只執行使用者明確登錄的工具（allowlist）。
- 參數以 argv 陣列代入，不做 shell 字串插值（避免注入）。
- 可勾選「執行前需我確認」，每次執行前彈確認框。
- 每個工具有硬性逾時（預設 30s）。

### 範例工具（Windows echo）
| 欄位 | 值 |
|------|----|
| 名稱 | `echo` |
| 命令 | `cmd` |
| 參數樣板 | `/c`<br>`echo {{text}}` |
| 參數 Schema | `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}` |

### 視窗（GUI）工具怎麼用

一般工具用 `exec.Command` 執行並**等待它結束**才回傳結果（記事本、小畫家、瀏覽器這類程式不會自己結束，等到逾時就會被砍掉、回傳 timeout 錯誤）。

登錄時勾選「**視窗程式（只負責開啟，不等待關閉）**」（`ToolSpec.Background`），executor 改用 `cmd.Start()`：啟動後立刻回傳成功，不等待、不套用逾時，視窗會正常留著讓你操作。

範例（開記事本）：
| 欄位 | 值 |
|------|----|
| 名稱 | `open_notepad` |
| 命令 | `notepad` |
| 參數樣板 | `{{path}}`（可留空） |
| 參數 Schema | `{"type":"object","properties":{"path":{"type":"string","description":"要開啟的檔案路徑，可空"}}}` |
| 視窗程式 | ☑ |

## 內建 Coding Agent（讀檔 / 搜尋 / 編輯，diff 審核後套用）

「工具」頁是使用者手動登錄任意命令列程式；**Coding Agent 是內建的**，不用登錄。啟用需要**兩個條件同時成立**：

1. 「設定」頁選好**工作目錄**
2. 「對話」頁的模式切換按鈕切到 **💻 Code 模式**（預設是 **🔧 Normal 模式**，即使已設工作目錄也不會生效）

**Normal 模式**下 agent 完全看不到、也不可能呼叫這些工具——只會用「工具」頁登錄的一般命令列工具或指令；**不會進入任何編輯狀態**。切到 **Code 模式**才會把碼工具交給 agent。若還沒設工作目錄就點切換，會提示先去設定頁選。

比照 Claude Code 的 Read/Grep/Edit/Write 語意，Code 模式下 agent 可以：

| 工具 | 用途 | 需要審核？ |
|------|------|:---:|
| `code_list_files` | 列出某相對目錄下的檔案/子目錄 | 否 |
| `code_search` | 遞迴文字搜尋（不分大小寫） | 否 |
| `code_read_file` | 讀檔內容，可用 `offset`/`limit` 分段讀取大檔 | 否 |
| `code_edit_file` | `old_string` → `new_string` 精確字串替換 | **是** |
| `code_write_file` | 整檔覆寫 / 建立新檔 | **是** |

**安全設計**：
- **雙重開關**：未設工作目錄，或設了但停留在 Normal 模式，manifest 都不會帶這些工具給 agent。
- 所有路徑都被限制在工作目錄之內，`..` 逃逸會被明確拒絕（不會靜默改寫到別處）。
- `code_edit_file` / `code_write_file` **寫入前一定會跳出 diff 審核視窗**，你按「套用」才會真的寫入磁碟，按「拒絕」檔案完全不變、agent 會收到「使用者拒絕」的訊息可以自己調整方案。
- 只處理合法 UTF-8 文字檔；偵測到二進位/非文字內容一律明確拒絕，不會塞壞資料進對話。

**用法**：設定頁「Coding 工作目錄」選一個資料夾（例如你的專案根目錄）→ 存檔 → 回對話頁點模式切換按鈕切到 **Code 模式** → 直接說「幫我讀一下 xxx.go」或「把 yyy 函式改成……」→ agent 會自己判斷呼叫對的工具 → 修改類操作會先讓你看 diff 再決定套用。改完可以隨時點回 **Normal 模式**，之後的訊息就不會再帶碼工具給 agent。

### 已知限制
- 這是**輕量版**改碼流程，不是完整 IDE 體驗：本地小模型（如 `gemma4:e4b`，8B 參數）在多檔案、複雜重構任務上的可靠度遠不如雲端大模型；適合單檔小改動、加函式、修 bug 這類任務。
- 後端 `config.yaml` 的 `tools.max_iterations`（預設 8）是每輪對話的工具呼叫上限；複雜任務（讀→改→驗證多步）可能不夠跑完就被打斷，需要可調大該設定。

## 注意
- 對話回合制只在後端 LLM 支援 tool calling（目前 Ollama provider，如 `gemma4:e4b`）時生效；其他 provider 會退回純對話、忽略工具。
- 圖片上傳需後端 `ingest.vision.enabled=true`（用 Ollama 視覺模型抽文字，如 `qwen3-vl:4b`）；掃描型 PDF 目前不 OCR。
- 後端預設自簽 TLS（HTTP/2），設定頁的「允許自簽」預設開啟。
