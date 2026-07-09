package views

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"pub_client/app/models"
	"pub_client/app/services"
)

// graphTab 知識圖譜頁：Explore（搜尋 + 區域圖視覺化）與 Pending Relations（待審關係審核）兩個子分頁。
// st.Gateway 為 nil（設定頁 Gateway Address 留空）時顯示停用提示，不建查詢 UI。
func graphTab(st *State) fyne.CanvasObject {
	if st.Gateway == nil {
		return container.NewCenter(widget.NewLabel(
			"Knowledge Graph is disabled — set the Gateway Address on the Settings tab"))
	}
	sub := container.NewAppTabs(
		container.NewTabItem("Explore", graphExploreView(st)),
		container.NewTabItem("Pending Relations", graphReviewView(st)),
	)
	sub.SetTabLocation(container.TabLocationTop)
	return sub
}

// selectedInt 把 Select 目前選值解析成 int；解析失敗回 def。
func selectedInt(s *widget.Select, def int) int {
	n, err := strconv.Atoi(s.Selected)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// ── Explore 子分頁 ────────────────────────────────────────────────────────────

func graphExploreView(st *State) fyne.CanvasObject {
	var results []models.GraphEntity

	statusLabel := widget.NewLabel("")

	searchEntry := widget.NewEntry()
	searchEntry.SetPlaceHolder("Search entity name or description...")

	// §5.1：top-k（語意搜尋種子數）與 hops（圖遍歷深度）改為可調，預設維持原寫死值。
	topKSel := widget.NewSelect([]string{"5", "10", "20"}, nil)
	topKSel.Selected = "5"
	hopsSel := widget.NewSelect([]string{"1", "2", "3"}, nil)
	hopsSel.Selected = "1"

	var resultList *widget.List

	// ── 右側：區域圖視覺化 + 實體詳情 + 關係 ──
	centerLabel := widget.NewRichTextFromMarkdown("*Select a result on the left to see details*")
	centerLabel.Wrapping = fyne.TextWrapWord

	var loadContext func(entityName string)

	// 力導向圖 widget（Obsidian graph view 風格：拖曳節點、pan、zoom、physics）。
	// onSelect 用閉包延後綁到 loadContext（loadContext 在下面才定義）。
	gw := newGraphWidget(func(name string) { loadContext(name) })
	graphHint := container.NewCenter(widget.NewLabel(
		"Search and select a result to see its neighborhood graph"))
	graphOverlay := container.NewStack(gw, graphHint)

	relationsList := widget.NewList(
		func() int { return 0 },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(widget.ListItemID, fyne.CanvasObject) {},
	)
	var relations []models.GraphRelation

	showContext := func(ctxResult models.GraphContext) {
		center := ctxResult.Center
		var b strings.Builder
		fmt.Fprintf(&b, "## %s\n", orPlaceholder(center.Name, "(unnamed)"))
		if center.Category != "" {
			fmt.Fprintf(&b, "**Category**: %s\n\n", center.Category)
		}
		if center.Description != "" {
			fmt.Fprintf(&b, "%s\n\n", center.Description)
		}
		if center.SourceFile != "" {
			fmt.Fprintf(&b, "*Source: %s*\n", center.SourceFile)
		}
		centerLabel.ParseMarkdown(b.String())

		relations = ctxResult.Relations
		relationsList.Length = func() int { return len(relations) }
		relationsList.UpdateItem = func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < len(relations) {
				r := relations[id]
				obj.(*widget.Label).SetText(fmt.Sprintf("%s —[%s]→ %s", r.Source, r.Type, r.Target))
			}
		}
		relationsList.Refresh()

		// 首次載入後隱藏提示文字，讓 graphWidget 全屏顯示。
		if graphHint.Visible() {
			graphHint.Hide()
		}
		gw.SetContext(ctxResult)
	}

	loadContext = func(entityName string) {
		statusLabel.SetText("Loading...")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			ctxResult, err := st.Gateway.GetLocalContext(ctx, entityName, selectedInt(hopsSel, 1), 20)
			if err != nil {
				statusLabel.SetText("Load failed: " + err.Error())
				return
			}
			statusLabel.SetText("")
			showContext(ctxResult)
		}()
	}

	resultList = widget.NewList(
		func() int { return len(results) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < len(results) {
				e := results[id]
				text := e.Name
				if e.Category != "" {
					text += " (" + e.Category + ")"
				}
				obj.(*widget.Label).SetText(text)
			}
		},
	)
	resultList.OnSelected = func(id widget.ListItemID) {
		if id < len(results) {
			loadContext(results[id].Name)
		}
	}

	runSearch := func(hybrid bool) {
		query := strings.TrimSpace(searchEntry.Text)
		if query == "" {
			return
		}
		statusLabel.SetText("Searching...")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			var entities []models.GraphEntity
			var err error
			if hybrid {
				var res models.GraphHybridSearchResult
				res, err = st.Gateway.HybridSearch(ctx, query, selectedInt(topKSel, 5), selectedInt(hopsSel, 1))
				entities = res.Seeds
			} else {
				entities, err = st.Gateway.SearchEntities(ctx, query, 20)
			}
			if err != nil {
				statusLabel.SetText("Search failed: " + err.Error())
				return
			}
			results = entities
			resultList.Refresh()
			// 自動選取「最接近搜尋結果的關鍵節點」：相關性 × 連結度最高的實體，
			// 選取即觸發 loadContext，圖譜直接跳到該節點的鄰域。
			if key := keyNodeIndex(results); key >= 0 {
				resultList.Select(key)
				statusLabel.SetText(fmt.Sprintf("%d results — key node: %s (degree %d)",
					len(results), results[key].Name, results[key].Degree))
			} else {
				statusLabel.SetText(fmt.Sprintf("%d results", len(results)))
			}
		}()
	}

	searchEntry.OnSubmitted = func(string) { runSearch(false) }
	keywordBtn := widget.NewButton("Keyword Search", func() { runSearch(false) })
	hybridBtn := widget.NewButton("Semantic Search (vector)", func() { runSearch(true) })
	hybridBtn.Importance = widget.MediumImportance

	params := container.NewHBox(
		widget.NewLabel("top-k"), topKSel,
		widget.NewLabel("hops"), hopsSel,
		keywordBtn, hybridBtn,
	)
	searchBar := container.NewBorder(nil, nil, nil, params, searchEntry)

	left := container.NewBorder(searchBar, statusLabel, nil, nil, resultList)

	// 圖區上方的控制列：兩顆快捷按鈕 + 三條 Force 滑桿（Obsidian graph view 的
	// Forces 面板小巧化版本）。滑桿即時更新 graphWidget 內的物理參數。
	mkSlider := func(min, max, val float64, onChange func(float64)) *widget.Slider {
		s := widget.NewSlider(min, max)
		s.Step = (max - min) / 100
		s.Value = val
		s.OnChanged = onChange
		return s
	}
	fitBtn := widget.NewButton("Fit", gw.ZoomFit)
	resetBtn := widget.NewButton("Reset", gw.ResetLayout)

	// Sync Vault：把 server 端 Obsidian vault 的 .md 單向 pull 到設定頁指定的
	// 本地資料夾（增量：只抓 sha256 不同的檔），下載完可直接用 Obsidian 開。
	var syncBtn *widget.Button
	syncBtn = widget.NewButton("Sync Vault", func() {
		if strings.TrimSpace(st.Cfg.VaultDir) == "" {
			dialog.ShowInformation("Vault folder not set",
				"Set the Vault Folder on the Settings tab first — Sync Vault downloads the knowledge-base notes there.",
				st.Win)
			return
		}
		syncBtn.Disable()
		go func() {
			defer syncBtn.Enable()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			res, err := services.SyncVault(ctx, st.Gateway, st.Cfg.VaultDir,
				func(msg string) { statusLabel.SetText(msg) })
			if err != nil {
				statusLabel.SetText("Vault sync failed: " + err.Error())
				return
			}
			statusLabel.SetText(fmt.Sprintf("Vault synced: %d downloaded, %d unchanged (%d total) → %s",
				res.Downloaded, res.Unchanged, res.Total, st.Cfg.VaultDir))
		}()
	})

	repelSlider := mkSlider(500, 30000, defaultRepel, gw.SetRepel)
	springSlider := mkSlider(0.005, 0.3, defaultSpringK, gw.SetSpringK)
	distSlider := mkSlider(30, 300, defaultSpringLen, gw.SetSpringLen)

	sliderWithLabel := func(name string, s *widget.Slider) fyne.CanvasObject {
		// Grid 給 label 固定寬度，讓三條滑桿對齊
		return container.NewGridWithColumns(2, widget.NewLabel(name), s)
	}
	controlsBar := container.NewBorder(nil, nil,
		container.NewHBox(fitBtn, resetBtn, syncBtn, widget.NewSeparator()),
		nil,
		container.NewGridWithColumns(3,
			sliderWithLabel("Repel", repelSlider),
			sliderWithLabel("Spring", springSlider),
			sliderWithLabel("Dist", distSlider),
		),
	)
	graphContainer := container.NewBorder(controlsBar, nil, nil, nil, graphOverlay)

	details := container.NewVSplit(
		container.NewVScroll(centerLabel),
		container.NewBorder(widget.NewLabel("Relations"), nil, nil, nil, relationsList),
	)
	right := container.NewVSplit(graphContainer, details)
	right.Offset = 0.6

	// 進入分頁先載入幾筆實體，避免一開始空白（空 query 的 CONTAINS "" 命中全部，取前 N）。
	go func() {
		statusLabel.SetText("Loading entities...")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		entities, err := st.Gateway.SearchEntities(ctx, "", 15)
		if err != nil {
			statusLabel.SetText("Load failed: " + err.Error())
			return
		}
		results = entities
		if len(results) == 0 {
			statusLabel.SetText("No entities yet")
		} else {
			statusLabel.SetText(fmt.Sprintf("%d entities (showing latest)", len(results)))
		}
		resultList.Refresh()
	}()

	split := container.NewHSplit(left, right)
	split.Offset = 0.3
	return split
}

// ── Pending Relations 子分頁（§5.3）─────────────────────────────────────────

func graphReviewView(st *State) fyne.CanvasObject {
	var items []models.GraphProposedRelation
	selected := -1

	statusLabel := widget.NewLabel("Click Refresh to load pending relations")
	statusLabel.Wrapping = fyne.TextWrapWord

	var list *widget.List
	list = widget.NewList(
		func() int { return len(items) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < len(items) {
				it := items[id]
				from := it.ExtractedBy
				if it.SourceFile != "" {
					from += " @ " + it.SourceFile
				}
				obj.(*widget.Label).SetText(fmt.Sprintf("%s —[%s]→ %s   (conf %.2f, %s)",
					it.SourceEntity, it.RelationType, it.TargetEntity, it.Confidence, from))
			}
		},
	)
	list.OnSelected = func(id widget.ListItemID) { selected = id }

	var refreshBtn, approveBtn, rejectBtn *widget.Button
	setBusy := func(busy bool) {
		for _, b := range []*widget.Button{refreshBtn, approveBtn, rejectBtn} {
			if busy {
				b.Disable()
			} else {
				b.Enable()
			}
		}
	}

	refresh := func() {
		setBusy(true)
		statusLabel.SetText("Loading pending relations...")
		go func() {
			defer setBusy(false)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			got, err := st.Gateway.ListProposed(ctx, "", 100)
			if err != nil {
				statusLabel.SetText("Load failed: " + err.Error())
				return
			}
			items = got
			selected = -1
			list.UnselectAll()
			list.Refresh()
			statusLabel.SetText(fmt.Sprintf("%d pending relations", len(items)))
		}()
	}

	review := func(approve bool) {
		if selected < 0 || selected >= len(items) {
			statusLabel.SetText("Select a relation first")
			return
		}
		idx := selected
		it := items[idx]
		setBusy(true)
		verb, done := "Rejecting", "Rejected"
		if approve {
			verb, done = "Approving", "Approved (merged into graph)"
		}
		statusLabel.SetText(fmt.Sprintf("%s %s —[%s]→ %s ...", verb, it.SourceEntity, it.RelationType, it.TargetEntity))
		go func() {
			defer setBusy(false)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var err error
			if approve {
				_, err = st.Gateway.ApproveProposed(ctx, it.ID)
			} else {
				_, err = st.Gateway.RejectProposed(ctx, it.ID)
			}
			if err != nil {
				statusLabel.SetText("Review failed: " + err.Error())
				return
			}
			items = append(items[:idx], items[idx+1:]...)
			selected = -1
			list.UnselectAll()
			list.Refresh()
			statusLabel.SetText(fmt.Sprintf("%s: %s —[%s]→ %s (%d left)",
				done, it.SourceEntity, it.RelationType, it.TargetEntity, len(items)))
		}()
	}

	refreshBtn = widget.NewButton("Refresh", refresh)
	approveBtn = widget.NewButton("Approve", func() { review(true) })
	approveBtn.Importance = widget.HighImportance
	rejectBtn = widget.NewButton("Reject", func() { review(false) })
	rejectBtn.Importance = widget.DangerImportance

	toolbar := container.NewHBox(refreshBtn, approveBtn, rejectBtn)
	return container.NewBorder(toolbar, statusLabel, nil, nil, list)
}

func orPlaceholder(s, placeholder string) string {
	if strings.TrimSpace(s) == "" {
		return placeholder
	}
	return s
}

// keyNodeIndex 挑「最接近搜尋結果的關鍵節點」：相關性 ×（1 + ln(1+全圖 degree)）
// 最高者。語意搜尋用向量相似度當相關性；關鍵字搜尋沒有分數（score=0）視為同分，
// 等同純比連結度。無結果回 -1。
func keyNodeIndex(entities []models.GraphEntity) int {
	best, bestRank := -1, -1.0
	for i, e := range entities {
		score := e.Score
		if score <= 0 {
			score = 1
		}
		rank := score * (1 + math.Log1p(float64(e.Degree)))
		if rank > bestRank {
			best, bestRank = i, rank
		}
	}
	return best
}
