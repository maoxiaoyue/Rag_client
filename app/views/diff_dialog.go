package views

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// diffRichText 把 unified diff 渲染成著色的唯讀 RichText：
// 新增行綠（Success）、刪除行紅（Error）、hunk 標頭藍（Primary）、檔頭粗體。
func diffRichText(diffText string) *widget.RichText {
	lines := strings.Split(strings.TrimRight(diffText, "\n"), "\n")
	segs := make([]widget.RichTextSegment, 0, len(lines))
	for _, line := range lines {
		style := widget.RichTextStyle{TextStyle: fyne.TextStyle{Monospace: true}}
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			style.TextStyle.Bold = true
		case strings.HasPrefix(line, "@@"):
			style.ColorName = theme.ColorNamePrimary
		case strings.HasPrefix(line, "+"):
			style.ColorName = theme.ColorNameSuccess
		case strings.HasPrefix(line, "-"):
			style.ColorName = theme.ColorNameError
		}
		segs = append(segs, &widget.TextSegment{Text: line, Style: style})
	}
	rt := widget.NewRichText(segs...)
	rt.Wrapping = fyne.TextWrapOff // 長行走水平捲動，不折行破壞 diff 對齊
	return rt
}

// showDiffApproval 彈出一個顯示 unified diff 的視窗，等待使用者按「Apply」或「Reject」。
// 阻塞呼叫（透過 channel），適合從 ChatEngine 的背景 goroutine 呼叫——與 chat_view.go
// 既有的 RequireConfirm 工具確認框走同一種同步等待模式。
func showDiffApproval(win fyne.Window, path, diffText string) bool {
	scroll := container.NewScroll(diffRichText(diffText))
	scroll.SetMinSize(fyne.NewSize(760, 480))

	content := container.NewBorder(
		widget.NewLabelWithStyle(fmt.Sprintf("Agent wants to modify: %s", path), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		nil, nil, nil, scroll,
	)

	result := make(chan bool, 1)
	d := dialog.NewCustomConfirm("Review Change", "Apply", "Reject", content, func(approved bool) {
		result <- approved
	}, win)
	d.Resize(fyne.NewSize(800, 560))
	d.Show()

	return <-result
}
