package views

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"pub_client/app/models"
	"pub_client/app/services"
)

// toolsTab 工具頁：登錄 / 編輯 / 刪除本地可執行工具。
//
// 登錄的工具會在每次對話時作為 client_tools 送給 agent；agent 決定呼叫時，
// 由本機 client 執行（見 app/services/tool_service.go）。
func toolsTab(st *State) fyne.CanvasObject {
	selected := -1

	// ── 右側編輯表單 ──
	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("Tool name (letters/digits/underscore, e.g. run_python)")
	descEntry := widget.NewMultiLineEntry()
	descEntry.SetPlaceHolder("Description shown to the LLM: what this tool does, when to use it")
	cmdEntry := widget.NewEntry()
	cmdEntry.SetPlaceHolder(`Executable, e.g. C:\Windows\System32\cmd.exe or python`)
	argsEntry := widget.NewMultiLineEntry()
	argsEntry.SetPlaceHolder("argv template, one per line; use {{param_name}} for substitution, e.g.:\n/c\necho {{text}}")
	schemaEntry := widget.NewMultiLineEntry()
	schemaEntry.SetPlaceHolder(`Argument JSON Schema, e.g.:
{"type":"object","properties":{"text":{"type":"string","description":"text to echo back"}},"required":["text"]}`)
	confirmCheck := widget.NewCheck("Ask for confirmation before running", nil)
	timeoutEntry := widget.NewEntry()
	timeoutEntry.SetPlaceHolder("Timeout in seconds (default 30; not used for window programs)")
	backgroundCheck := widget.NewCheck("Window program (just launch, don't wait for it to close)", func(checked bool) {
		if checked {
			timeoutEntry.Disable()
		} else {
			timeoutEntry.Enable()
		}
	})

	form := widget.NewForm(
		widget.NewFormItem("Name", nameEntry),
		widget.NewFormItem("Description", descEntry),
		widget.NewFormItem("Command", cmdEntry),
		widget.NewFormItem("Arg template", argsEntry),
		widget.NewFormItem("Arg Schema", schemaEntry),
		widget.NewFormItem("Confirm", confirmCheck),
		widget.NewFormItem("Type", backgroundCheck),
		widget.NewFormItem("Timeout", timeoutEntry),
	)

	// ── 左側清單 ──
	var list *widget.List
	list = widget.NewList(
		func() int { return len(st.Cfg.Tools) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < len(st.Cfg.Tools) {
				obj.(*widget.Label).SetText(st.Cfg.Tools[id].Name)
			}
		},
	)

	loadForm := func(t models.ToolSpec) {
		nameEntry.SetText(t.Name)
		descEntry.SetText(t.Description)
		cmdEntry.SetText(t.Command)
		argsEntry.SetText(strings.Join(t.Args, "\n"))
		if len(t.Schema) > 0 {
			schemaEntry.SetText(string(t.Schema))
		} else {
			schemaEntry.SetText("")
		}
		confirmCheck.SetChecked(t.RequireConfirm)
		backgroundCheck.SetChecked(t.Background)
		if t.TimeoutSec > 0 {
			timeoutEntry.SetText(strconv.Itoa(t.TimeoutSec))
		} else {
			timeoutEntry.SetText("")
		}
	}
	clearForm := func() {
		loadForm(models.ToolSpec{})
	}

	list.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(st.Cfg.Tools) {
			selected = id
			loadForm(st.Cfg.Tools[id])
		}
	}

	// ── 按鈕 ──
	addBtn := widget.NewButton("Add", func() {
		selected = -1
		list.UnselectAll()
		clearForm()
		nameEntry.SetText("new_tool")
	})

	saveBtn := widget.NewButton("Save", func() {
		t, err := collectTool(nameEntry, descEntry, cmdEntry, argsEntry, schemaEntry, confirmCheck, backgroundCheck, timeoutEntry)
		if err != nil {
			dialog.ShowError(err, st.Win)
			return
		}
		// 重名檢查（排除自己）：FindTool 只取第一個命中，重名的第二個工具永遠不會被執行。
		for i, ex := range st.Cfg.Tools {
			if i != selected && ex.Name == t.Name {
				dialog.ShowError(fmt.Errorf("a tool named %q already exists", t.Name), st.Win)
				return
			}
		}
		if selected >= 0 && selected < len(st.Cfg.Tools) {
			st.Cfg.Tools[selected] = t
		} else {
			st.Cfg.Tools = append(st.Cfg.Tools, t)
			selected = len(st.Cfg.Tools) - 1
		}
		st.SaveConfig()
		list.Refresh()
		dialog.ShowInformation("Saved", "Tool \""+t.Name+"\" has been saved.", st.Win)
	})

	delBtn := widget.NewButton("Delete", func() {
		if selected < 0 || selected >= len(st.Cfg.Tools) {
			return
		}
		name := st.Cfg.Tools[selected].Name
		dialog.ShowConfirm("Delete Tool", "Delete \""+name+"\"?", func(ok bool) {
			if !ok {
				return
			}
			st.Cfg.Tools = append(st.Cfg.Tools[:selected], st.Cfg.Tools[selected+1:]...)
			selected = -1
			st.SaveConfig()
			list.Refresh()
			clearForm()
		}, st.Win)
	})

	// Test Run（§4.3）：用表單目前內容（不必先儲存）本地跑一次，先給 JSON 參數、再顯示輸出。
	testBtn := widget.NewButton("Test Run", func() {
		t, err := collectTool(nameEntry, descEntry, cmdEntry, argsEntry, schemaEntry, confirmCheck, backgroundCheck, timeoutEntry)
		if err != nil {
			dialog.ShowError(err, st.Win)
			return
		}
		argsInput := widget.NewMultiLineEntry()
		argsInput.SetText("{}")
		argsInput.SetPlaceHolder(`{"param": "value"}`)
		argsScroll := container.NewVScroll(argsInput)
		argsScroll.SetMinSize(fyne.NewSize(480, 180))

		dialog.ShowCustomConfirm("Test Run: "+t.Name, "Run", "Cancel", argsScroll, func(ok bool) {
			if !ok {
				return
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(argsInput.Text), &args); err != nil {
				dialog.ShowError(fmt.Errorf("args is not valid JSON: %w", err), st.Win)
				return
			}
			go func() {
				res := services.ExecuteTool(context.Background(), t,
					models.ToolCall{ID: "test", Name: t.Name, Args: args})
				out := widget.NewMultiLineEntry()
				out.SetText(res.Content)
				out.TextStyle = fyne.TextStyle{Monospace: true}
				out.Disable()
				outScroll := container.NewVScroll(out)
				outScroll.SetMinSize(fyne.NewSize(560, 320))
				title := "Test Result — OK"
				if res.IsError {
					title = "Test Result — ERROR"
				}
				dialog.ShowCustom(title, "Close", outScroll, st.Win)
			}()
		}, st.Win)
	})

	buttons := container.NewHBox(addBtn, saveBtn, delBtn, testBtn)

	left := container.NewBorder(
		widget.NewLabelWithStyle("Registered Tools", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		nil, nil, nil, list,
	)
	right := container.NewBorder(nil, buttons, nil, nil, container.NewVScroll(form))

	split := container.NewHSplit(left, right)
	split.Offset = 0.3
	return split
}

// collectTool 從表單欄位組出 ToolSpec，並驗證 schema JSON。
func collectTool(name, desc, cmd, args, schema *widget.Entry, confirm, background *widget.Check, timeout *widget.Entry) (models.ToolSpec, error) {
	t := models.ToolSpec{
		Name:           strings.TrimSpace(name.Text),
		Description:    strings.TrimSpace(desc.Text),
		Command:        strings.TrimSpace(cmd.Text),
		RequireConfirm: confirm.Checked,
		Background:     background.Checked,
	}
	if t.Name == "" {
		return t, fmt.Errorf("tool name must not be empty")
	}
	if t.Command == "" {
		return t, fmt.Errorf("command must not be empty")
	}
	for _, line := range strings.Split(args.Text, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			t.Args = append(t.Args, s)
		}
	}
	if s := strings.TrimSpace(schema.Text); s != "" {
		if !json.Valid([]byte(s)) {
			return t, fmt.Errorf("argument schema is not valid JSON")
		}
		t.Schema = json.RawMessage(s)
	}
	if !t.Background {
		if s := strings.TrimSpace(timeout.Text); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 {
				return t, fmt.Errorf("timeout must be a non-negative integer")
			}
			t.TimeoutSec = n
		}
	}
	return t, nil
}
