package views

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"pub_client/app/models"
	"pub_client/app/services"
	"pub_client/app/store"
)

// chatTab 對話頁：送訊息、顯示回覆（Markdown 渲染），並在 server 要求時本地執行工具（回合制）。
func chatTab(st *State) fyne.CanvasObject {
	history := widget.NewRichTextFromMarkdown("")
	history.Wrapping = fyne.TextWrapWord
	scroll := container.NewVScroll(history)

	// sb 累積整份轉錄的 Markdown 原文——既是畫面渲染來源，也是轉錄存檔（sessions/*.md）的內容。
	var sb strings.Builder
	appendMD := func(md string) {
		sb.WriteString(md)
		sb.WriteString("\n\n")
		history.ParseMarkdown(sb.String())
		scroll.ScrollToBottom()
	}

	status := widget.NewLabel("")
	status.Wrapping = fyne.TextWrapWord

	input := widget.NewEntry()
	input.SetPlaceHolder("Type a message, press Enter to send...")

	// 附加圖片：send 時逐張送 server 做視覺辨識（抽文字併進本輪訊息 + 同時 ingest 進 RAG）。
	var attachedImages []string
	attachRow := container.NewHBox()
	attachScroll := container.NewHScroll(attachRow)
	attachScroll.SetMinSize(fyne.NewSize(0, attachThumbSize+20))
	attachScroll.Hide()
	var refreshAttach func()
	refreshAttach = func() {
		attachRow.Objects = attachRow.Objects[:0]
		for i := range attachedImages {
			i := i
			p := attachedImages[i]
			cell := buildAttachThumb(st.Win, p, func() {
				attachedImages = append(attachedImages[:i], attachedImages[i+1:]...)
				refreshAttach()
			})
			attachRow.Objects = append(attachRow.Objects, cell)
		}
		attachRow.Refresh()
		if len(attachedImages) == 0 {
			attachScroll.Hide()
		} else {
			attachScroll.Show()
		}
	}

	// 跨多次送出保留 session id（同一對話脈絡）。
	sessionID := ""
	lastReply := ""

	copyBtn := widget.NewButton("Copy Reply", nil)
	copyBtn.OnTapped = func() {
		if lastReply != "" {
			st.Win.Clipboard().SetContent(lastReply)
			status.SetText("Reply copied to clipboard")
		}
	}
	copyBtn.Disable()

	historyBtn := widget.NewButton("History", func() {
		showSessionBrowser(st)
	})

	// saveTranscript 每輪結束後整份轉錄 upsert 進加密 store（best-effort，失敗只記 log）。
	saveTranscript := func() {
		if sessionID == "" {
			return
		}
		if err := store.SaveSession(sessionID, st.Cfg.AgentID, sb.String()); err != nil {
			st.Log.Errorf("save session: %v", err)
		}
	}

	// RAG ID 快速切換器（§6.2）：多 persona 部署時不用進設定頁；切換即開新對話脈絡
	//（session 是 per-agent 的，跨 persona 延用同一 session id 沒有意義）。
	agentSel := widget.NewSelect(st.Cfg.AgentIDOptions(), nil)
	agentSel.Selected = st.Cfg.AgentID
	agentSel.OnChanged = func(id string) {
		if id == "" || id == st.Cfg.AgentID {
			return
		}
		st.Cfg.AgentID = id
		st.Cfg.RememberAgentID(id)
		st.RebuildClient()
		st.SaveConfig()
		sessionID = ""
		appendMD(fmt.Sprintf("*(switched to RAG ID %q — new conversation)*", id))
		status.SetText("Switched RAG ID to " + id)
	}
	// 設定頁 Save 之後同步選項與目前值（直接設欄位不觸發 OnChanged）。
	st.OnCfgChanged(func() {
		agentSel.Options = st.Cfg.AgentIDOptions()
		agentSel.Selected = st.Cfg.AgentID
		agentSel.Refresh()
	})

	// codeMode 為 false（預設 Normal 模式）時，即使設定頁已設工作目錄，
	// 也不會把內建碼工具（讀檔/搜尋/編輯/寫檔）交給 agent——只會用一般登錄工具或指令。
	codeMode := false
	modeBtn := widget.NewButton("", nil)
	updateModeBtn := func() {
		if codeMode {
			modeBtn.SetText("Code mode (can read/edit files, click to switch to Normal)")
			modeBtn.Importance = widget.WarningImportance
		} else {
			modeBtn.SetText("Normal mode (tools/commands only, click to switch to Code)")
			modeBtn.Importance = widget.MediumImportance
		}
		modeBtn.Refresh()
	}
	modeBtn.OnTapped = func() {
		if !codeMode && strings.TrimSpace(st.Cfg.WorkspaceRoot) == "" {
			dialog.ShowInformation("Workspace not set",
				"Please pick a coding workspace directory on the Settings tab before switching to Code mode.", st.Win)
			return
		}
		codeMode = !codeMode
		updateModeBtn()
		if codeMode {
			status.SetText("Switched to Code mode: the agent can read/edit files (writes still need your diff approval)")
		} else {
			status.SetText("Switched to Normal mode: the agent can only use registered tools or run commands")
		}
	}
	updateModeBtn()

	// 送出期間狀態：cancelSend 非 nil 代表有一輪請求在跑（擋重複送出 + 供 Stop 中斷）。
	var cancelSend context.CancelFunc
	var sendBtn *widget.Button

	stopBtn := widget.NewButton("Stop", func() {
		if cancelSend != nil {
			cancelSend()
		}
	})
	stopBtn.Disable()

	var send func()
	send = func() {
		if cancelSend != nil { // 上一輪還在跑（輸入框已 disable，此為保險）
			return
		}
		text := strings.TrimSpace(input.Text)
		if text == "" && len(attachedImages) == 0 {
			return
		}
		if st.Gateway == nil {
			dialog.ShowError(services.ErrNoConnection, st.Win)
			return
		}
		// 擷取本輪附加圖片並清空（下一輪重新附加）。
		imgs := attachedImages
		attachedImages = nil
		refreshAttach()
		// 工作目錄在切到 Code 模式後可能又在設定頁被清空——此時碼工具會靜默消失，
		// 按鈕卻仍顯示 Code mode。這裡主動退回 Normal 並提示，讓畫面狀態跟實際行為一致。
		if codeMode && strings.TrimSpace(st.Cfg.WorkspaceRoot) == "" {
			codeMode = false
			updateModeBtn()
			status.SetText("Workspace was cleared in Settings — reverted to Normal mode")
		}
		input.SetText("")
		display := text
		if len(imgs) > 0 {
			names := make([]string, len(imgs))
			for i, p := range imgs {
				names[i] = filepath.Base(p)
			}
			note := "*(附加圖片: " + strings.Join(names, ", ") + ")*"
			if display == "" {
				display = note
			} else {
				display = display + "\n\n" + note
			}
		}
		appendMD("**You:** " + display)

		confirm := func(spec models.ToolSpec, args map[string]any) bool {
			ch := make(chan bool, 1)
			msg := fmt.Sprintf("The agent wants to run a local tool:\n\nName: %s\nCommand: %s\nArgs: %v\n\nAllow it to run?",
				spec.Name, spec.Command, args)
			dialog.ShowConfirm("Confirm local tool execution", msg, func(ok bool) { ch <- ok }, st.Win)
			return <-ch
		}
		// phase 保存目前階段文字（Thinking... / Running tool X...），
		// 由 statusFn 更新、計時器 goroutine 每秒讀出並補上已等待秒數。
		var phase atomic.Value
		phase.Store("Thinking...")
		statusFn := func(m string) {
			phase.Store(m)
			status.SetText(m)
		}
		diffApprove := func(path, diffText string) bool {
			return showDiffApproval(st.Win, path, diffText)
		}

		// Normal 模式一律不帶工作目錄給引擎 → BuiltinCodeTools 回空 → agent 收不到碼工具、
		// 也就不可能進入編輯狀態；Code 模式才傳入設定頁的工作目錄。
		workspaceRoot := ""
		if codeMode {
			workspaceRoot = st.Cfg.WorkspaceRoot
		}

		// 新對話首輪：client 端先產生 session id，讓狀態輪詢（ChatStatus）首輪就有 key 可查。
		newConversation := sessionID == ""
		if newConversation {
			sessionID = services.NewSessionID()
		}

		// 新對話首輪且 Code 模式時，在轉錄上註記 AGENTS.md 是否會被注入（§2.5：讓使用者知道有無生效）。
		if newConversation && workspaceRoot != "" {
			if n := services.AgentsMDSize(workspaceRoot); n > 0 {
				appendMD(fmt.Sprintf("*(AGENTS.md loaded: %d bytes)*", n))
			}
		}

		eng := services.NewChatEngine(st.Gateway, st.Cfg.Tools, workspaceRoot, confirm, statusFn, diffApprove)
		if newConversation {
			eng.SetPreferredSessionID(sessionID) // 首輪帶 client 產生的 id（不影響 AGENTS.md 首輪注入）
		} else {
			eng.SetSession(sessionID)
		}
		eng.SetTemperature(st.Cfg.Temperature)

		ctx, cancel := context.WithCancel(context.Background())
		cancelSend = cancel
		input.Disable()
		sendBtn.Disable()
		stopBtn.Enable()

		// 便宜版串流回饋（§1.4）：每秒把「階段 (已等待秒數)」寫進狀態列，長回應期間看得出沒卡死。
		tickerDone := make(chan struct{})
		stopTicker := sync.OnceFunc(func() { close(tickerDone) })
		go func() {
			start := time.Now()
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-tickerDone:
					return
				case <-t.C:
					// 每秒向 gateway 問「這個 session 目前哪個 Agent 在處理」；
					// active 時把階段寫進 phase（秒數前顯示），非 active（例如等本地工具）維持本地階段。
					if sessionID != "" && st.Gateway != nil {
						pctx, pcancel := context.WithTimeout(context.Background(), 2*time.Second)
						if stage, active, perr := st.Gateway.ChatStatus(pctx, sessionID); perr == nil && active && stage != "" {
							phase.Store(stage)
						}
						pcancel()
					}
					status.SetText(fmt.Sprintf("%s (%ds)", phase.Load().(string), int(time.Since(start).Seconds())))
				}
			}
		}()

		go func() {
			defer func() {
				stopTicker()
				cancel()
				cancelSend = nil
				stopBtn.Disable()
				sendBtn.Enable()
				input.Enable()
				st.Win.Canvas().Focus(input)
			}()
			status.SetText("Thinking...")
			// 附加圖片：先送 server 做視覺辨識，把抽出的文字併進本輪訊息（讓 agent 辨識圖片內容）。
			sentText := text
			if len(imgs) > 0 {
				var b strings.Builder
				for i, p := range imgs {
					statusFn(fmt.Sprintf("Recognizing image %d/%d...", i+1, len(imgs)))
					data, rerr := os.ReadFile(p)
					if rerr != nil {
						appendMD(fmt.Sprintf("*(image read failed: %s — %v)*", filepath.Base(p), rerr))
						continue
					}
					txt, cerr := st.Gateway.ChatImage(ctx, st.Cfg.AgentID, filepath.Base(p), data)
					if cerr != nil {
						appendMD(fmt.Sprintf("*(image recognition failed: %s — %v)*", filepath.Base(p), cerr))
						continue
					}
					fmt.Fprintf(&b, "[附加圖片: %s]\n%s\n\n", filepath.Base(p), txt)
				}
				sentText = b.String() + text
			}
			reply, err := eng.Send(ctx, sentText)
			stopTicker() // 先停計時器再寫終局狀態，避免最後一 tick 蓋掉結果
			sessionID = eng.SessionID()
			if err != nil {
				if errors.Is(err, context.Canceled) {
					status.SetText("Stopped")
					appendMD("*(request stopped by user)*")
				} else {
					status.SetText("")
					st.Log.Errorf("chat failed: %v", err)
					appendMD("**Error:** " + err.Error())
				}
				saveTranscript()
				return
			}
			lastReply = reply
			copyBtn.Enable()
			if u := eng.Usage(); u != nil {
				status.SetText(fmt.Sprintf("tokens: %d prompt + %d completion = %d total",
					u.PromptTokens, u.CompletionTokens, u.TotalTokens))
			} else {
				status.SetText("")
			}
			appendMD("**Assistant:**\n\n" + reply)
			saveTranscript()
		}()
	}
	input.OnSubmitted = func(string) { send() }

	attachBtn := widget.NewButton("Attach Image", func() {
		paths, ok := nativeOpenFiles("Select image(s)",
			[][2]string{{"Images", "*.png;*.jpg;*.jpeg;*.webp;*.gif;*.bmp"}, {"All files", "*.*"}}, true)
		if !ok || len(paths) == 0 {
			return
		}
		attachedImages = append(attachedImages, paths...)
		refreshAttach()
	})

	sendBtn = widget.NewButton("Send", send)
	newConv := widget.NewButton("New Chat", func() {
		sessionID = ""
		lastReply = ""
		attachedImages = nil
		refreshAttach()
		copyBtn.Disable()
		sb.Reset()
		history.ParseMarkdown("")
		status.SetText("Started a new conversation")
	})

	topBar := container.NewHBox(agentSel, modeBtn, copyBtn, historyBtn)
	inputBar := container.NewBorder(nil, nil, nil, container.NewHBox(attachBtn, sendBtn, stopBtn, newConv), input)
	bottom := container.NewVBox(attachScroll, status, inputBar)

	return container.NewBorder(topBar, bottom, nil, nil, scroll)
}
