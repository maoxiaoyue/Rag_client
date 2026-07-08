package views

import (
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// cjkTheme 包住 Fyne 內建主題，把「非等寬、非符號」的文字字型換成含 CJK 字形的系統字型。
//
// 為什麼需要：Fyne v2.4.5 內建字型不含中文字形，且其文字渲染走 go-text 的
// font.ParseTTF——而 ParseTTF 不支援 .ttc 字型集合（msjh.ttc/msyh.ttc/mingliu.ttc 皆會解析失敗）。
// 兩者疊加，導致中文（例如對話框輸入）顯示成 □/�。這裡載入系統的「純 TTF」中文字型即可解決。
type cjkTheme struct {
	fyne.Theme
	cjk fyne.Resource
}

// Font 對一般/粗體/斜體文字回傳 CJK 字型（確保中文一定有字形）；等寬與符號維持內建，
// 以保留程式碼區塊的等寬與內建 icon 字型。
func (t *cjkTheme) Font(s fyne.TextStyle) fyne.Resource {
	if s.Monospace || s.Symbol {
		return t.Theme.Font(s)
	}
	return t.cjk
}

// textSizeScale 把文字字級縮小到接近 Claude Desktop 的觀感（Fyne 預設 14 偏大）。
const textSizeScale = 0.85

// Size 只縮小「文字類」尺寸（不動 padding / icon / scrollbar，避免版面過擠）。
func (t *cjkTheme) Size(name fyne.ThemeSizeName) float32 {
	base := t.Theme.Size(name)
	switch name {
	case theme.SizeNameText, theme.SizeNameCaptionText,
		theme.SizeNameHeadingText, theme.SizeNameSubHeadingText:
		return base * textSizeScale
	}
	return base
}

// cjkFontCandidates 依序嘗試的系統中文字型。只列「純 TTF」——Fyne 的 ParseTTF 不吃 .ttc，
// 故 msjh.ttc / msyh.ttc / mingliu.ttc 一律不放。
var cjkFontCandidates = []string{
	`C:\Windows\Fonts\NotoSansTC-VF.ttf`, // Noto Sans 繁中（sans UI，最佳觀感）
	`C:\Windows\Fonts\simhei.ttf`,        // 黑體（涵蓋 CJK，各版 Windows 幾乎都有）
	`C:\Windows\Fonts\kaiu.ttf`,          // 標楷體（繁中，最後備援）
}

// loadCJKFont 回傳第一個可讀取的中文字型資源；全都找不到時回 nil（維持 Fyne 內建行為）。
// 可用環境變數 FYNE_FONT 覆寫（須指向一個 TTF，非 TTC）。
func loadCJKFont() fyne.Resource {
	paths := cjkFontCandidates
	if env := os.Getenv("FYNE_FONT"); env != "" {
		paths = append([]string{env}, paths...)
	}
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			return fyne.NewStaticResource(filepath.Base(p), b)
		}
	}
	return nil
}

// applyCJKFont 若找得到中文字型就套用 cjkTheme，回傳是否成功套用。
func applyCJKFont(a fyne.App) bool {
	f := loadCJKFont()
	if f == nil {
		return false
	}
	a.Settings().SetTheme(&cjkTheme{Theme: theme.DefaultTheme(), cjk: f})
	return true
}
