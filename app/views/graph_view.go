package views

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"pub_client/app/models"
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

	graphHolder := container.NewStack(container.NewCenter(
		widget.NewLabel("Select a result to see its neighborhood graph")))

	relationsList := widget.NewList(
		func() int { return 0 },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(widget.ListItemID, fyne.CanvasObject) {},
	)
	var relations []models.GraphRelation

	var loadContext func(entityName string)

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

		// §5.2：放射狀 node-edge 圖，點鄰居節點跳轉重新查詢。
		graphHolder.Objects = []fyne.CanvasObject{graphCanvas(ctxResult, func(name string) { loadContext(name) })}
		graphHolder.Refresh()
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
			statusLabel.SetText(fmt.Sprintf("%d results", len(results)))
			resultList.Refresh()
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

	details := container.NewVSplit(
		container.NewVScroll(centerLabel),
		container.NewBorder(widget.NewLabel("Relations"), nil, nil, nil, relationsList),
	)
	right := container.NewVSplit(graphHolder, details)
	right.Offset = 0.55

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

// graphCanvas 把區域圖遍歷結果畫成放射狀 node-edge 圖：中心實體置中、鄰居環狀排列、
// 關係畫成連線（兩端都有畫出來的才畫）。點鄰居節點觸發 onSelect 以該實體重新查詢。
func graphCanvas(ctxResult models.GraphContext, onSelect func(name string)) fyne.CanvasObject {
	const (
		width    = float32(680)
		height   = float32(460)
		radius   = 165.0
		maxNodes = 16 // 超過就不畫（環狀排列擠不下），以註記代替
	)
	inner := container.NewWithoutLayout()
	centerPos := fyne.NewPos(width/2, height/2)

	neighbors := ctxResult.Neighbors
	note := ""
	if len(neighbors) > maxNodes {
		note = fmt.Sprintf("(+%d more neighbors not drawn)", len(neighbors)-maxNodes)
		neighbors = neighbors[:maxNodes]
	}

	pos := map[string]fyne.Position{ctxResult.Center.Name: centerPos}
	for i, nb := range neighbors {
		angle := 2 * math.Pi * float64(i) / float64(len(neighbors))
		pos[nb.Name] = fyne.NewPos(
			centerPos.X+float32(radius*math.Cos(angle)),
			centerPos.Y+float32(radius*math.Sin(angle)),
		)
	}

	// 邊要先畫（在按鈕下層）。
	for _, r := range ctxResult.Relations {
		p1, ok1 := pos[r.Source]
		p2, ok2 := pos[r.Target]
		if !ok1 || !ok2 {
			continue
		}
		line := canvas.NewLine(theme.DisabledColor())
		line.StrokeWidth = 1
		line.Position1, line.Position2 = p1, p2
		inner.Add(line)
	}

	nodeBtn := func(name string, w float32, imp widget.Importance, tap func()) *widget.Button {
		label := name
		if r := []rune(label); len(r) > 18 {
			label = string(r[:17]) + "…"
		}
		b := widget.NewButton(label, tap)
		b.Importance = imp
		b.Resize(fyne.NewSize(w, 30))
		return b
	}

	for _, nb := range neighbors {
		name := nb.Name
		b := nodeBtn(name, 120, widget.MediumImportance, func() { onSelect(name) })
		p := pos[name]
		b.Move(fyne.NewPos(p.X-60, p.Y-15))
		inner.Add(b)
	}
	centerBtn := nodeBtn(ctxResult.Center.Name, 150, widget.HighImportance, nil)
	centerBtn.Move(fyne.NewPos(centerPos.X-75, centerPos.Y-15))
	inner.Add(centerBtn)

	if note != "" {
		noteLabel := widget.NewLabel(note)
		noteLabel.Move(fyne.NewPos(8, height-34))
		inner.Add(noteLabel)
	}

	// GridWrap 給自由佈局容器一個固定 MinSize，外層 Scroll 負責視窗小於畫布時的捲動。
	return container.NewScroll(container.NewGridWrap(fyne.NewSize(width, height), inner))
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
