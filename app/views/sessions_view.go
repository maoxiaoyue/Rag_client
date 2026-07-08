// sessions_view.go — 對話 session 瀏覽器（History 按鈕）：先列最近幾條的標題，
// 點某條才開該對話的完整內容（從加密 store 讀取）。
package views

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"pub_client/app/store"
)

type sessionItem struct {
	meta  store.SessionMeta
	title string
}

// showSessionBrowser 彈出最近對話的標題清單；點一條開內容。
func showSessionBrowser(st *State) {
	metas, err := store.ListSessions(20)
	if err != nil {
		dialog.ShowError(fmt.Errorf("load sessions: %w", err), st.Win)
		return
	}
	if len(metas) == 0 {
		dialog.ShowInformation("Chat Sessions", "No saved conversations yet.", st.Win)
		return
	}

	// 預取標題（每筆讀一次轉錄；限 20 筆可接受）。
	items := make([]sessionItem, 0, len(metas))
	for _, m := range metas {
		title := m.AgentID
		if tr, gerr := store.GetSession(m.SessionID); gerr == nil {
			title = transcriptTitle(tr)
		}
		items = append(items, sessionItem{meta: m, title: title})
	}

	list := widget.NewList(
		func() int { return len(items) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < len(items) {
				it := items[id]
				obj.(*widget.Label).SetText(it.meta.UpdatedAt.Format("01-02 15:04") + "  " + it.title)
			}
		},
	)
	d := dialog.NewCustom("Chat Sessions — click a title to open", "Close", container.NewVScroll(list), st.Win)
	list.OnSelected = func(id widget.ListItemID) {
		if id < len(items) {
			showSessionContent(st, items[id])
		}
		list.UnselectAll()
	}
	d.Resize(fyne.NewSize(560, 460))
	d.Show()
}

// showSessionContent 顯示單一 session 的完整轉錄（Markdown 渲染）。
func showSessionContent(st *State, it sessionItem) {
	transcript, err := store.GetSession(it.meta.SessionID)
	if err != nil {
		dialog.ShowError(fmt.Errorf("read transcript: %w", err), st.Win)
		return
	}
	viewer := widget.NewRichTextFromMarkdown(transcript)
	viewer.Wrapping = fyne.TextWrapWord
	d := dialog.NewCustom(it.title, "Close", container.NewVScroll(viewer), st.Win)
	d.Resize(fyne.NewSize(820, 560))
	d.Show()
}

// transcriptTitle 從轉錄推導標題：取第一則使用者訊息（"**You:** …"）首段，截斷。
func transcriptTitle(md string) string {
	for _, line := range strings.Split(md, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "**You:**"); ok {
			rest = strings.TrimSpace(rest)
			if rest != "" && !strings.HasPrefix(rest, "*(") {
				return truncateTitle(rest)
			}
		}
	}
	for _, line := range strings.Split(md, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return truncateTitle(line)
		}
	}
	return "(empty)"
}

func truncateTitle(s string) string {
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}
