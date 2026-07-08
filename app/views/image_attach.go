// image_attach.go — 對話附件圖片的縮圖 cell（左上角 X 取消、點縮圖看原尺寸）。
package views

import (
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const attachThumbSize = 72

// tappableImage 可點擊的縮圖（canvas.Image 本身不接受點擊事件）。
type tappableImage struct {
	widget.BaseWidget
	img   *canvas.Image
	onTap func()
}

func newTappableImage(path string, onTap func()) *tappableImage {
	img := canvas.NewImageFromFile(path)
	img.FillMode = canvas.ImageFillContain
	img.SetMinSize(fyne.NewSize(attachThumbSize, attachThumbSize))
	t := &tappableImage{img: img, onTap: onTap}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tappableImage) CreateRenderer() fyne.WidgetRenderer { return widget.NewSimpleRenderer(t.img) }

func (t *tappableImage) Tapped(_ *fyne.PointEvent) {
	if t.onTap != nil {
		t.onTap()
	}
}

// buildAttachThumb 一個附件縮圖 cell：點縮圖看原尺寸；左上角 X 取消附加。
func buildAttachThumb(win fyne.Window, path string, onRemove func()) fyne.CanvasObject {
	thumb := newTappableImage(path, func() { showFullImage(win, path) })

	xBtn := widget.NewButtonWithIcon("", theme.CancelIcon(), onRemove)
	xBtn.Importance = widget.LowImportance
	// X 疊在縮圖左上角（HBox+VBox spacer 把它推到角落；非 tappable 的 spacer 不擋縮圖點擊）。
	overlay := container.NewVBox(container.NewHBox(xBtn, layout.NewSpacer()), layout.NewSpacer())

	return container.NewStack(thumb, overlay)
}

// showFullImage 以原始像素尺寸顯示圖片（超出對話框大小時以捲動檢視）。
func showFullImage(win fyne.Window, path string) {
	img := canvas.NewImageFromFile(path)
	img.FillMode = canvas.ImageFillOriginal // 用原圖尺寸當 min size
	d := dialog.NewCustom(filepath.Base(path), "Close", container.NewScroll(img), win)
	d.Resize(fyne.NewSize(900, 680)) // 視窗上限；原圖更大時用捲動
	d.Show()
}
