package views

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"pub_client/app/services"
	"pub_client/app/store"
)

// settingsTab 連線設定（dev_pub_0.9）：Gateway Address + API Key 取代原本三個
// 後端 URL——client 只認識 agent_gateway 一個位址，全部流量走 gRPC。
func settingsTab(st *State) *container.Scroll {
	gatewayEntry := widget.NewEntry()
	gatewayEntry.SetText(st.Cfg.GatewayAddr)
	gatewayEntry.SetPlaceHolder("host:port, e.g. 127.0.0.1:9090")

	apiKeyEntry := widget.NewPasswordEntry()
	apiKeyEntry.SetText(st.Cfg.APIKey)
	apiKeyEntry.SetPlaceHolder("sak_... (issued by `gateway apikey create`; bound to this device on first use)")

	// RAG ID 用可編輯下拉（SelectEntry）：可直接輸入新 ID，也可從用過的清單挑（§6.2）。
	agentEntry := widget.NewSelectEntry(st.Cfg.AgentIDOptions())
	agentEntry.SetText(st.Cfg.AgentID)
	agentEntry.SetPlaceHolder("agent-main")

	insecure := widget.NewCheck("Allow self-signed / skip TLS verification", nil)
	insecure.SetChecked(st.Cfg.InsecureTLS)

	// folderPickerRow 資料夾輸入 + Browse 按鈕（Coding Workspace 與 Vault Folder 共用樣式）。
	folderPickerRow := func(entry *widget.Entry, initial string) fyne.CanvasObject {
		browse := widget.NewButton("Browse...", func() {
			fd := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
				if err != nil || uri == nil {
					return
				}
				entry.SetText(uri.Path())
			}, st.Win)
			if initial != "" {
				if listable, err := storage.ListerForURI(storage.NewFileURI(initial)); err == nil {
					fd.SetLocation(listable)
				}
			}
			fd.Show()
		})
		return container.NewBorder(nil, nil, nil, browse, entry)
	}

	workspaceEntry := widget.NewEntry()
	workspaceEntry.SetText(st.Cfg.WorkspaceRoot)
	workspaceEntry.SetPlaceHolder("Leave empty to disable file read/edit tools")
	workspaceRow := folderPickerRow(workspaceEntry, st.Cfg.WorkspaceRoot)

	vaultEntry := widget.NewEntry()
	vaultEntry.SetText(st.Cfg.VaultDir)
	vaultEntry.SetPlaceHolder("Local folder for Sync Vault downloads (open it with Obsidian)")
	vaultRow := folderPickerRow(vaultEntry, st.Cfg.VaultDir)

	tempValue := widget.NewLabel(fmt.Sprintf("%.2f", st.Cfg.Temperature))
	tempSlider := widget.NewSlider(0, 1)
	tempSlider.Step = 0.05
	tempSlider.Value = float64(st.Cfg.Temperature)
	tempSlider.OnChanged = func(v float64) { tempValue.SetText(fmt.Sprintf("%.2f", v)) }
	tempRow := container.NewBorder(nil, nil, nil, tempValue, tempSlider)
	tempItem := widget.NewFormItem("Temperature", tempRow)
	tempItem.HintText = "Chat sampling temperature; 0 = use the server default"

	pathLabel := widget.NewLabel("Encrypted store: " + store.StorePath())

	vaultItem := widget.NewFormItem("Vault Folder", vaultRow)
	vaultItem.HintText = "Knowledge Graph tab's Sync Vault downloads the server-side Obsidian vault here"

	form := widget.NewForm(
		widget.NewFormItem("Gateway Address", gatewayEntry),
		widget.NewFormItem("API Key", apiKeyEntry),
		widget.NewFormItem("RAG ID", agentEntry),
		widget.NewFormItem("TLS", insecure),
		widget.NewFormItem("Coding Workspace", workspaceRow),
		vaultItem,
		tempItem,
	)

	save := widget.NewButton("Save", func() {
		st.Cfg.GatewayAddr = strings.TrimSpace(gatewayEntry.Text)
		st.Cfg.APIKey = strings.TrimSpace(apiKeyEntry.Text)
		st.Cfg.AgentID = strings.TrimSpace(agentEntry.Text)
		st.Cfg.RememberAgentID(st.Cfg.AgentID)
		st.Cfg.InsecureTLS = insecure.Checked
		st.Cfg.WorkspaceRoot = workspaceEntry.Text
		st.Cfg.VaultDir = strings.TrimSpace(vaultEntry.Text)
		st.Cfg.Temperature = float32(tempSlider.Value)
		st.RebuildClient()
		st.SaveConfig()
		agentEntry.SetOptions(st.Cfg.AgentIDOptions())
		st.NotifyCfgChanged()
		dialog.ShowInformation("Saved", "Connection settings have been updated and saved.", st.Win)
	})

	// Test Connection：用表單目前輸入值打 gateway 的 Ping（同時驗 API Key 與裝置綁定）。
	testResult := widget.NewLabel("")
	testResult.Wrapping = fyne.TextWrapWord
	var testBtn *widget.Button
	testBtn = widget.NewButton("Test Connection", func() {
		gw := services.NewGatewayClient(
			strings.TrimSpace(gatewayEntry.Text), strings.TrimSpace(agentEntry.Text),
			strings.TrimSpace(apiKeyEntry.Text), st.DeviceID(), insecure.Checked)
		if gw == nil {
			testResult.SetText("Gateway Address is empty")
			return
		}
		testBtn.Disable()
		testResult.SetText("Testing...")
		go func() {
			defer testBtn.Enable()
			defer gw.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			latency, keyName, err := gw.Ping(ctx)
			if err != nil {
				testResult.SetText("Gateway: FAIL — " + err.Error())
				return
			}
			name := keyName
			if name == "" {
				name = "(auth disabled)"
			}
			testResult.SetText(fmt.Sprintf("Gateway: OK (%d ms, key: %s)", latency.Milliseconds(), name))
		}()
	})

	buttons := container.NewHBox(save, testBtn)
	return container.NewVScroll(container.NewVBox(form, buttons, testResult, pathLabel))
}
