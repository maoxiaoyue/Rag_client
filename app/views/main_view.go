// Package views 組裝 Fyne 桌面介面：對話 / 上傳 / 工具 / 設定 四個分頁。
package views

import (
	"context"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"

	hypconfig "github.com/maoxiaoyue/hypgo/pkg/config"
	"github.com/maoxiaoyue/hypgo/pkg/logger"

	"pub_client/app/models"
	"pub_client/app/services"
	"pub_client/app/store"
)

// State client 全域共享狀態。
// dev_pub_0.9：三個 REST client 收斂成單一 Gateway（gRPC → agent_gateway）。
type State struct {
	App      fyne.App
	Win      fyne.Window
	Cfg      *models.Config
	Gateway *services.GatewayClient // nil = 未設定 GatewayAddr，各分頁顯示未設定提示
	Log     *logger.Logger

	deviceID string // 一 Key 一裝置的本機裝置指紋（啟動時解析一次）

	cfgListeners []func() // 設定變更通知（例：對話頁的 RAG ID 快速切換器要刷新選項）
}

// DeviceID 回傳本機裝置指紋（Test Connection 等需要臨時建 client 的地方用）。
func (s *State) DeviceID() string {
	if s.deviceID == "" {
		s.deviceID = resolveDeviceID(s.Cfg)
	}
	return s.deviceID
}

// OnCfgChanged 註冊設定變更 listener（分頁建構時呼叫，跨分頁同步 UI 狀態用）。
func (s *State) OnCfgChanged(fn func()) { s.cfgListeners = append(s.cfgListeners, fn) }

// NotifyCfgChanged 廣播設定已變更（設定頁 Save、或任何改動 Cfg 的地方之後呼叫）。
func (s *State) NotifyCfgChanged() {
	for _, fn := range s.cfgListeners {
		fn()
	}
}

// RebuildClient 依目前設定重建 gateway 連線（設定變更後呼叫）。
func (s *State) RebuildClient() {
	if s.deviceID == "" {
		s.deviceID = resolveDeviceID(s.Cfg)
	}
	if s.Gateway != nil {
		_ = s.Gateway.Close()
	}
	s.Gateway = services.NewGatewayClient(
		s.Cfg.GatewayAddr, s.Cfg.AgentID, s.Cfg.APIKey, s.deviceID, s.Cfg.InsecureTLS)
}

// resolveDeviceID 取「一 Key 一裝置」的裝置指紋：優先由機器特徵推導（穩定、
// 不落地）；取不到時退回隨機 id 並持久化到 config（換機視同新裝置）。
func resolveDeviceID(cfg *models.Config) string {
	if id, ok := services.StableDeviceID(); ok {
		return id
	}
	if cfg.DeviceID == "" {
		cfg.DeviceID = services.RandomDeviceID()
		_ = store.SaveConfig(cfg) // best-effort；存不進去頂多下次換 id（視同換機）
	}
	return cfg.DeviceID
}

// SaveConfig 持久化設定並回報錯誤。
func (s *State) SaveConfig() {
	if err := store.SaveConfig(s.Cfg); err != nil {
		s.Log.Errorf("failed to save config: %v", err)
		dialog.ShowError(err, s.Win)
	}
}

// newAppLogger 依 app/config/config.yaml 的 logger 區段建立 *logger.Logger；
// 設定檔缺失或載入失敗時退回內建預設（不阻擋啟動）。
func newAppLogger() *logger.Logger {
	appCfg, err := hypconfig.LoadConfig("app/config/config.yaml")
	if err != nil {
		appCfg = &hypconfig.Config{}
	}
	appCfg.ApplyDefaults()

	log, err := logger.New(appCfg.Logger.GetLevel(), appCfg.Logger.GetOutput(), nil, appCfg.Logger.IsColorized())
	if err != nil {
		return logger.NewLogger()
	}
	if out := appCfg.Logger.GetOutput(); out != "" && out != "stdout" && out != "stderr" {
		_ = log.SetFile(out)
	}
	return log
}

// Run 啟動 GUI（阻塞直到視窗關閉）。
func Run() {
	log := newAppLogger()
	defer log.Close()

	cfg, loadErr := store.LoadConfig()
	if loadErr != nil {
		log.Warnf("failed to load config store, using defaults: %v", loadErr)
		cfg = models.DefaultConfig()
	}

	a := app.New()
	// Fyne 內建字型無中文字形，套用系統 CJK 字型（否則中文顯示成 □/�）。
	if !applyCJKFont(a) {
		log.Warnf("no CJK font found on this machine; 中文可能無法正常顯示")
	}
	w := a.NewWindow("Agent / RAG Client")

	st := &State{App: a, Win: w, Cfg: cfg, Log: log}
	st.RebuildClient()

	tabItems := []*container.TabItem{
		container.NewTabItem("Chat", chatTab(st)),
		container.NewTabItem("Upload", uploadTab(st)),
		container.NewTabItem("Tools", toolsTab(st)),
		container.NewTabItem("Knowledge Graph", graphTab(st)),
	}
	// 外網版（dev_pub_0.9）client 只能讀取知識圖譜，不提供直接寫入的 Graph Input 分頁。
	tabItems = append(tabItems, container.NewTabItem("Settings", settingsTab(st)))
	tabs := container.NewAppTabs(tabItems...)
	tabs.SetTabLocation(container.TabLocationTop)

	w.SetContent(tabs)
	w.Resize(fyne.NewSize(940, 700))

	if loadErr != nil {
		dialog.ShowError(loadErr, w)
	}

	// 外網版：開視窗後對 gateway 做健康檢查，連不上就跳警示「RAG主機已關機」。
	// 必須等視窗「進入前景」後才做——否則連線瞬間被拒時，dialog 會在 painter/字型還沒
	// 初始化前就彈出，中文渲染成 □（豆腐）。用 lifecycle 的 OnEnteredForeground（只跑一次）
	// 確保 dialog 在畫面就緒後才出現，中文才會正常。
	var healthOnce sync.Once
	a.Lifecycle().SetOnEnteredForeground(func() {
		healthOnce.Do(func() {
			if st.Gateway == nil {
				return // 尚未設定 gateway 位址，不做檢查
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
				defer cancel()
				if err := st.Gateway.HealthCheck(ctx); err != nil {
					dialog.ShowError(err, w) // err == services.ErrHostDown → "RAG主機已關機"
				}
			}()
		})
	})

	log.Info("pub_client starting", "gateway_addr", cfg.GatewayAddr, "agent_id", cfg.AgentID)
	w.ShowAndRun()
}
