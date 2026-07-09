package views

import (
	"hash/fnv"
	"image/color"
	"math"
	"math/rand"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"pub_client/app/models"
)

// graphWidget 把一個 GraphContext（中心 + 鄰居 + 關係）畫成 Obsidian graph view
// 風格的力導向圖：節點半徑正比於子圖 degree、依 category 染色、支援拖曳節點 /
// 空白處 pan / 滾輪 zoom / 點擊選取。物理模擬（Coulomb 斥力 + Hooke 彈簧 +
// 向心力）在背景 goroutine 以 30fps 推進；動能低於門檻自動停跑省 CPU。
//
// 世界座標（node.x/y）與畫布座標互轉：
//
//	screen = center + (world * zoom) + pan
//	world  = (screen - center - pan) / zoom
type graphWidget struct {
	widget.BaseWidget
	mu sync.Mutex

	nodes []*gNode
	edges []gEdge
	byID  map[string]*gNode

	// 物理參數（透過 SetRepel/SetSpringK/... 由 sidebar 滑桿即時調整）
	repel     float64
	springK   float64
	springLen float64
	centerK   float64
	damping   float64

	// 視角變換（zoom 圍繞畫布中心，pan 為像素偏移）
	zoom       float64
	panX, panY float64

	// 互動狀態
	pressNode *gNode // MouseDown 時擊中的節點（僅用於區分 click vs drag）
	dragNode  *gNode // 目前正在被拖曳的節點
	isPanning bool
	dragMoved bool

	onSelect func(name string) // 節點被單擊時觸發

	stop     chan struct{}
	stopOnce sync.Once
}

type gNode struct {
	id       string
	label    string
	category string
	x, y     float64 // world 座標
	vx, vy   float64
	fx, fy   float64 // 每個 tick 累加的合力
	degree   int
	isCenter bool
	pinned   bool // 使用者正在拖曳時固定位置
}

type gEdge struct {
	from, to string
	typ      string
}

const (
	defaultRepel     = 8000.0
	defaultSpringK   = 0.05
	defaultSpringLen = 110.0
	defaultCenterK   = 0.012
	defaultDamping   = 0.82
	minZoom          = 0.25
	maxZoom          = 3.5
	minWidgetW       = float32(400)
	minWidgetH       = float32(320)
)

// 編譯期檢查：Fyne 靠 runtime type assertion 找 handler 介面，這裡強制斷言，
// 方法簽名寫錯（typo、參數型別漂移）會在 build 時就爆而不是 runtime 靜默失效。
var (
	_ fyne.Draggable     = (*graphWidget)(nil)
	_ fyne.Scrollable    = (*graphWidget)(nil)
	_ desktop.Mouseable  = (*graphWidget)(nil)
	_ fyne.WidgetRenderer = (*graphRenderer)(nil)
)

func newGraphWidget(onSelect func(name string)) *graphWidget {
	g := &graphWidget{
		byID:      make(map[string]*gNode),
		repel:     defaultRepel,
		springK:   defaultSpringK,
		springLen: defaultSpringLen,
		centerK:   defaultCenterK,
		damping:   defaultDamping,
		zoom:      1.0,
		onSelect:  onSelect,
		stop:      make(chan struct{}),
	}
	g.ExtendBaseWidget(g)
	return g
}

// SetContext 換載一組新的 GraphContext。物理模擬會用新的 nodes/edges 繼續跑，
// pan/zoom 保留（好處：跳到相鄰實體時視角不會突然重置）。
func (g *graphWidget) SetContext(c models.GraphContext) {
	g.mu.Lock()
	g.nodes = g.nodes[:0]
	g.edges = g.edges[:0]
	g.byID = make(map[string]*gNode)

	// backendDeg 後端回填的全圖 degree（与 Obsidian 一致：節點大小反映整個
	// 知識庫的引用數，而非目前子圖）；0/缺值時稍後退回子圖 degree。
	backendDeg := map[string]int{c.Center.Name: c.Center.Degree}

	center := &gNode{
		id:       c.Center.Name,
		label:    truncateRunes(c.Center.Name, 18),
		category: c.Center.Category,
		isCenter: true,
	}
	g.nodes = append(g.nodes, center)
	g.byID[center.id] = center

	// 初始位置：以 center 對應的字串為 seed 隨機環狀擺放鄰居，這樣同一實體
	// 反覆載入時佈局穩定（不是每次跳到完全不同的地方）。
	seed := int64(fnv1a(c.Center.Name))
	if seed == 0 {
		seed = 1
	}
	rng := rand.New(rand.NewSource(seed))
	for _, nb := range c.Neighbors {
		backendDeg[nb.Name] = nb.Degree
		if _, ok := g.byID[nb.Name]; ok {
			continue
		}
		angle := rng.Float64() * 2 * math.Pi
		radius := 90.0 + rng.Float64()*60.0
		n := &gNode{
			id:       nb.Name,
			label:    truncateRunes(nb.Name, 16),
			category: nb.Category,
			x:        radius * math.Cos(angle),
			y:        radius * math.Sin(angle),
		}
		g.nodes = append(g.nodes, n)
		g.byID[n.id] = n
	}

	for _, rel := range c.Relations {
		if _, ok := g.byID[rel.Source]; !ok {
			continue
		}
		if _, ok := g.byID[rel.Target]; !ok {
			continue
		}
		g.edges = append(g.edges, gEdge{from: rel.Source, to: rel.Target, typ: rel.Type})
	}
	for _, e := range g.edges {
		g.byID[e.from].degree++
		g.byID[e.to].degree++
	}
	// 後端全圖 degree 較大時採用（子圖 degree 是它的下界）
	for _, n := range g.nodes {
		if bd := backendDeg[n.id]; bd > n.degree {
			n.degree = bd
		}
	}
	// 清掉互動狀態，避免舊 pointer 指到被替換掉的節點
	g.pressNode, g.dragNode, g.isPanning, g.dragMoved = nil, nil, false, false
	g.mu.Unlock()

	g.Refresh()
}

// SetRepel/SetSpringK/SetSpringLen 供 sidebar 滑桿即時調整。
func (g *graphWidget) SetRepel(v float64)     { g.mu.Lock(); g.repel = v; g.mu.Unlock() }
func (g *graphWidget) SetSpringK(v float64)   { g.mu.Lock(); g.springK = v; g.mu.Unlock() }
func (g *graphWidget) SetSpringLen(v float64) { g.mu.Lock(); g.springLen = v; g.mu.Unlock() }

// ResetLayout 把所有節點重新灑到中心附近，並清空速度——LLM 抽出來的圖不好看時
// 讓使用者能一鍵重跑物理模擬。
func (g *graphWidget) ResetLayout() {
	g.mu.Lock()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	for _, n := range g.nodes {
		if n.isCenter {
			n.x, n.y = 0, 0
		} else {
			angle := rng.Float64() * 2 * math.Pi
			radius := 60.0 + rng.Float64()*90.0
			n.x = radius * math.Cos(angle)
			n.y = radius * math.Sin(angle)
		}
		n.vx, n.vy = 0, 0
		n.pinned = false
	}
	g.panX, g.panY = 0, 0
	g.zoom = 1.0
	g.mu.Unlock()
	g.Refresh()
}

// ZoomFit 依所有節點的 bounding box 調 zoom + pan，讓整張圖剛好裝進畫布。
func (g *graphWidget) ZoomFit() {
	g.mu.Lock()
	if len(g.nodes) == 0 {
		g.mu.Unlock()
		return
	}
	minX, minY := g.nodes[0].x, g.nodes[0].y
	maxX, maxY := minX, minY
	for _, n := range g.nodes[1:] {
		if n.x < minX {
			minX = n.x
		}
		if n.x > maxX {
			maxX = n.x
		}
		if n.y < minY {
			minY = n.y
		}
		if n.y > maxY {
			maxY = n.y
		}
	}
	worldW := maxX - minX
	worldH := maxY - minY
	if worldW < 1 {
		worldW = 1
	}
	if worldH < 1 {
		worldH = 1
	}
	size := g.Size()
	if size.Width < 10 || size.Height < 10 {
		g.mu.Unlock()
		return
	}
	// 保留邊界（node radius + label 空間）
	margin := 60.0
	zx := (float64(size.Width) - 2*margin) / worldW
	zy := (float64(size.Height) - 2*margin) / worldH
	z := math.Min(zx, zy)
	if z > maxZoom {
		z = maxZoom
	}
	if z < minZoom {
		z = minZoom
	}
	g.zoom = z
	// 讓 bounding box 中心對到畫布中心：pan = -(worldCenter * zoom)
	cx := (minX + maxX) / 2
	cy := (minY + maxY) / 2
	g.panX = -cx * z
	g.panY = -cy * z
	g.mu.Unlock()
	g.Refresh()
}

// ── Fyne Widget ────────────────────────────────────────────────────────────

// CreateRenderer 建立 renderer 並啟動物理模擬 goroutine（一顆 widget 一顆
// goroutine；Destroy() 觸發 stop channel 關閉時退出）。
func (g *graphWidget) CreateRenderer() fyne.WidgetRenderer {
	r := &graphRenderer{g: g}
	r.bg = canvas.NewRectangle(theme.BackgroundColor())
	r.rebuild()
	go g.simLoop()
	return r
}

// ── 互動：desktop.Mouseable + fyne.Draggable + fyne.Scrollable ─────────────

func (g *graphWidget) MouseDown(e *desktop.MouseEvent) {
	g.mu.Lock()
	g.dragMoved = false
	g.pressNode = g.hitTestLocked(e.Position)
	g.mu.Unlock()
}

func (g *graphWidget) MouseUp(e *desktop.MouseEvent) {
	g.mu.Lock()
	// 沒動過 = 純點擊；擊中節點就觸發 onSelect
	var clickedName string
	if !g.dragMoved && g.pressNode != nil {
		clickedName = g.pressNode.id
	}
	if g.dragNode != nil {
		g.dragNode.pinned = false
	}
	g.dragNode, g.pressNode, g.isPanning = nil, nil, false
	cb := g.onSelect
	g.mu.Unlock()

	if clickedName != "" && cb != nil {
		cb(clickedName)
	}
}

func (g *graphWidget) Dragged(e *fyne.DragEvent) {
	g.mu.Lock()
	g.dragMoved = true
	if g.dragNode == nil && !g.isPanning {
		if g.pressNode != nil {
			g.dragNode = g.pressNode
			g.dragNode.pinned = true
		} else {
			g.isPanning = true
		}
	}
	if g.dragNode != nil {
		// 螢幕像素 delta → world delta 需除以 zoom
		g.dragNode.x += float64(e.Dragged.DX) / g.zoom
		g.dragNode.y += float64(e.Dragged.DY) / g.zoom
		g.dragNode.vx, g.dragNode.vy = 0, 0
	} else if g.isPanning {
		g.panX += float64(e.Dragged.DX)
		g.panY += float64(e.Dragged.DY)
	}
	g.mu.Unlock()
	g.Refresh()
}

func (g *graphWidget) DragEnd() {
	g.mu.Lock()
	if g.dragNode != nil {
		g.dragNode.pinned = false
	}
	g.dragNode, g.isPanning = nil, false
	g.mu.Unlock()
}

// Scrolled 實作 fyne.Scrollable，處理滾輪縮放。以游標位置為錨點縮放（縮放時
// 游標指的世界座標不變），跟 Obsidian graph view 一樣。
func (g *graphWidget) Scrolled(e *fyne.ScrollEvent) {
	g.mu.Lock()
	oldZoom := g.zoom
	factor := 1.0 + float64(e.Scrolled.DY)*0.0015
	newZoom := oldZoom * factor
	if newZoom < minZoom {
		newZoom = minZoom
	}
	if newZoom > maxZoom {
		newZoom = maxZoom
	}
	if newZoom == oldZoom {
		g.mu.Unlock()
		return
	}
	// 保持游標下的世界座標不動：new_pan = pos - (world * new_zoom + center)
	size := g.Size()
	cx := float64(size.Width) / 2
	cy := float64(size.Height) / 2
	// 游標對應的世界座標（用舊 zoom）
	wx := (float64(e.Position.X) - cx - g.panX) / oldZoom
	wy := (float64(e.Position.Y) - cy - g.panY) / oldZoom
	// 用新 zoom 反算 pan
	g.panX = float64(e.Position.X) - cx - wx*newZoom
	g.panY = float64(e.Position.Y) - cy - wy*newZoom
	g.zoom = newZoom
	g.mu.Unlock()
	g.Refresh()
}

// hitTestLocked 找出給定畫布座標擊中的節點；呼叫時必須已握著 g.mu。
func (g *graphWidget) hitTestLocked(p fyne.Position) *gNode {
	size := g.Size()
	cx := float64(size.Width)/2 + g.panX
	cy := float64(size.Height)/2 + g.panY
	// 反向遍歷讓後畫的（視覺上在上層）優先命中
	for i := len(g.nodes) - 1; i >= 0; i-- {
		n := g.nodes[i]
		sx := cx + n.x*g.zoom
		sy := cy + n.y*g.zoom
		r := float64(nodeRadius(n)) + 3
		dx := float64(p.X) - sx
		dy := float64(p.Y) - sy
		if dx*dx+dy*dy <= r*r {
			return n
		}
	}
	return nil
}

// ── 物理模擬 ───────────────────────────────────────────────────────────────

func (g *graphWidget) simLoop() {
	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()
	idle := 0
	for {
		select {
		case <-g.stop:
			return
		case <-ticker.C:
			moved := g.step()
			// 靜止後多送幾張 refresh 讓最後幾個像素落到位，再停手
			if moved {
				idle = 0
				g.Refresh()
			} else if idle < 3 {
				idle++
				g.Refresh()
			}
		}
	}
}

// step 推進一格物理模擬；回傳 true 表示這一格有明顯位移（或使用者正在拖）。
func (g *graphWidget) step() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := len(g.nodes)
	if n < 2 {
		return g.dragNode != nil
	}
	// 累加合力
	for _, node := range g.nodes {
		node.fx, node.fy = 0, 0
	}
	// Coulomb 斥力：每對節點 O(N²)。N < 200 內 30fps 無壓力。
	for i := 0; i < n; i++ {
		a := g.nodes[i]
		for j := i + 1; j < n; j++ {
			b := g.nodes[j]
			dx := a.x - b.x
			dy := a.y - b.y
			d2 := dx*dx + dy*dy
			if d2 < 1 {
				d2 = 1
			}
			d := math.Sqrt(d2)
			f := g.repel / d2
			ux, uy := dx/d, dy/d
			a.fx += f * ux
			a.fy += f * uy
			b.fx -= f * ux
			b.fy -= f * uy
		}
	}
	// Hooke 彈簧：拉在 springLen 附近
	for _, e := range g.edges {
		a, b := g.byID[e.from], g.byID[e.to]
		if a == nil || b == nil {
			continue
		}
		dx := b.x - a.x
		dy := b.y - a.y
		d := math.Sqrt(dx*dx + dy*dy)
		if d < 1e-3 {
			continue
		}
		f := g.springK * (d - g.springLen)
		ux, uy := dx/d, dy/d
		a.fx += f * ux
		a.fy += f * uy
		b.fx -= f * ux
		b.fy -= f * uy
	}
	// 向心力（防止圖飄離視窗）
	for _, node := range g.nodes {
		node.fx += -g.centerK * node.x
		node.fy += -g.centerK * node.y
	}
	// 積分（Euler + damping）
	var ke float64
	for _, node := range g.nodes {
		if node.pinned {
			node.vx, node.vy = 0, 0
			continue
		}
		node.vx = (node.vx + node.fx) * g.damping
		node.vy = (node.vy + node.fy) * g.damping
		// 速度上限，避免物理爆炸
		speed := math.Sqrt(node.vx*node.vx + node.vy*node.vy)
		if speed > 25 {
			node.vx *= 25 / speed
			node.vy *= 25 / speed
		}
		node.x += node.vx
		node.y += node.vy
		ke += node.vx*node.vx + node.vy*node.vy
	}
	// 有拖曳中的節點時視為「還在動」，否則看動能
	return ke > 0.02 || g.dragNode != nil
}

// ── Renderer ───────────────────────────────────────────────────────────────

type graphRenderer struct {
	g *graphWidget

	bg      *canvas.Rectangle
	lines   []*canvas.Line
	circles []*canvas.Circle
	texts   []*canvas.Text

	// 上一次 rebuild 時的節點/邊數量，用來判斷 SetContext 之後要不要重建物件
	lastNodes int
	lastEdges int
}

// rebuild 依 g.nodes / g.edges 建立對應的 canvas primitives。呼叫方負責上鎖。
func (r *graphRenderer) rebuild() {
	r.lines = make([]*canvas.Line, len(r.g.edges))
	for i := range r.g.edges {
		l := canvas.NewLine(theme.DisabledColor())
		l.StrokeWidth = 1
		r.lines[i] = l
	}
	r.circles = make([]*canvas.Circle, len(r.g.nodes))
	r.texts = make([]*canvas.Text, len(r.g.nodes))
	for i, n := range r.g.nodes {
		c := canvas.NewCircle(nodeColor(n))
		c.StrokeColor = theme.ForegroundColor()
		c.StrokeWidth = 1
		r.circles[i] = c

		t := canvas.NewText(n.label, theme.ForegroundColor())
		t.TextSize = 11
		t.Alignment = fyne.TextAlignCenter
		r.texts[i] = t
	}
	r.lastNodes = len(r.g.nodes)
	r.lastEdges = len(r.g.edges)
}

// Objects 回傳目前所有 canvas 元件；順序決定 z-order：背景 → 邊 → 節點 → 標籤。
func (r *graphRenderer) Objects() []fyne.CanvasObject {
	// SetContext 之後 nodes/edges 數量可能對不上舊 primitives，這裡用讀鎖檢查
	r.g.mu.Lock()
	if len(r.g.nodes) != r.lastNodes || len(r.g.edges) != r.lastEdges {
		r.rebuild()
	}
	objs := make([]fyne.CanvasObject, 0, 1+len(r.lines)+len(r.circles)*2)
	objs = append(objs, r.bg)
	for _, l := range r.lines {
		objs = append(objs, l)
	}
	for i := range r.circles {
		objs = append(objs, r.circles[i])
	}
	for i := range r.texts {
		objs = append(objs, r.texts[i])
	}
	r.g.mu.Unlock()
	return objs
}

func (r *graphRenderer) MinSize() fyne.Size { return fyne.NewSize(minWidgetW, minWidgetH) }

func (r *graphRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.bg.Move(fyne.NewPos(0, 0))
	r.applyPositions(size)
}

// Refresh 由 g.Refresh() 觸發（sim goroutine + 互動事件都會呼叫）。這裡除了
// 更新位置也重新套 theme 顏色，這樣使用者切換 Fyne 深/淺主題後不用重啟就會生效。
func (r *graphRenderer) Refresh() {
	r.g.mu.Lock()
	if len(r.g.nodes) != r.lastNodes || len(r.g.edges) != r.lastEdges {
		r.rebuild()
	}
	r.g.mu.Unlock()
	r.bg.FillColor = theme.BackgroundColor()
	r.bg.Refresh()
	r.applyPositions(r.g.Size())
}

// applyPositions 把 world → screen 座標套到所有 canvas primitives 上。
func (r *graphRenderer) applyPositions(size fyne.Size) {
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	if size.Width < 1 || size.Height < 1 {
		return
	}
	cx := float64(size.Width)/2 + r.g.panX
	cy := float64(size.Height)/2 + r.g.panY
	z := r.g.zoom

	// 邊先畫（Objects 順序已保證在節點之下）
	for i, e := range r.g.edges {
		if i >= len(r.lines) {
			break
		}
		a, b := r.g.byID[e.from], r.g.byID[e.to]
		if a == nil || b == nil {
			continue
		}
		l := r.lines[i]
		l.StrokeColor = theme.DisabledColor()
		l.Position1 = fyne.NewPos(float32(cx+a.x*z), float32(cy+a.y*z))
		l.Position2 = fyne.NewPos(float32(cx+b.x*z), float32(cy+b.y*z))
		l.Refresh()
	}
	for i, n := range r.g.nodes {
		if i >= len(r.circles) {
			break
		}
		sx := float32(cx + n.x*z)
		sy := float32(cy + n.y*z)
		radius := nodeRadius(n)

		c := r.circles[i]
		c.FillColor = nodeColor(n)
		c.StrokeColor = theme.ForegroundColor()
		c.Resize(fyne.NewSize(radius*2, radius*2))
		c.Move(fyne.NewPos(sx-radius, sy-radius))
		c.Refresh()

		t := r.texts[i]
		t.Color = theme.ForegroundColor()
		// 文字寬度用固定值置中；Fyne 的 canvas.Text 沒有現成的 measure API 可
		// 便宜取到寬度，用「大致 = 文字長度 * 字高 * 0.6」估算就夠用。
		tw := float32(len([]rune(n.label))) * t.TextSize * 0.6
		t.Move(fyne.NewPos(sx-tw/2, sy+radius+2))
		t.Refresh()
	}
}

func (r *graphRenderer) Destroy() {
	r.g.stopOnce.Do(func() { close(r.g.stop) })
}

// ── helpers ────────────────────────────────────────────────────────────────

// nodeRadius: base + √degree * k。sqrt 是為了避免高 degree 節點大到蓋掉其他節點。
func nodeRadius(n *gNode) float32 {
	base := 7.0
	if n.isCenter {
		base = 11.0
	}
	return float32(base + math.Sqrt(float64(n.degree))*3.0)
}

// nodeColor: 中心用 theme.PrimaryColor() 突出，其他用 category hash 產生穩定色。
func nodeColor(n *gNode) color.Color {
	if n.isCenter {
		return theme.PrimaryColor()
	}
	return categoryColor(n.category)
}

// categoryColor: 對同一 category 每次都回同一色（fnv hash → HSL hue）；空
// category 回中性灰。saturation/lightness 選中等亮度，深/淺主題下都清楚可見。
func categoryColor(cat string) color.Color {
	if cat == "" {
		return color.NRGBA{R: 140, G: 148, B: 158, A: 220}
	}
	hue := float64(fnv1a(cat) % 360)
	return hslToRGB(hue, 0.55, 0.55)
}

func hslToRGB(h, s, l float64) color.NRGBA {
	c := (1 - math.Abs(2*l-1)) * s
	hp := math.Mod(h/60.0, 6.0)
	x := c * (1 - math.Abs(math.Mod(hp, 2)-1))
	var r1, g1, b1 float64
	switch {
	case hp < 1:
		r1, g1, b1 = c, x, 0
	case hp < 2:
		r1, g1, b1 = x, c, 0
	case hp < 3:
		r1, g1, b1 = 0, c, x
	case hp < 4:
		r1, g1, b1 = 0, x, c
	case hp < 5:
		r1, g1, b1 = x, 0, c
	default:
		r1, g1, b1 = c, 0, x
	}
	m := l - c/2
	return color.NRGBA{
		R: uint8((r1 + m) * 255),
		G: uint8((g1 + m) * 255),
		B: uint8((b1 + m) * 255),
		A: 220,
	}
}

func fnv1a(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
