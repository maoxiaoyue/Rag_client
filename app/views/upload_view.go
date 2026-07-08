package views

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"pub_client/app/services"
)

// uploadTab 上傳頁：選檔（PDF/圖片/文字）→ 上傳攝取 → 輪詢 job 狀態 → Verify 檢索驗證。
func uploadTab(st *State) fyne.CanvasObject {
	var selected []string

	listLabel := widget.NewLabel("No files selected")
	listLabel.Wrapping = fyne.TextWrapWord
	status := widget.NewLabel("")
	status.Wrapping = fyne.TextWrapWord

	refreshList := func() {
		if len(selected) == 0 {
			listLabel.SetText("No files selected")
			return
		}
		listLabel.SetText(strings.Join(selected, "\n"))
	}

	pick := widget.NewButton("Add Files...", func() {
		paths, ok := nativeOpenFiles("Select files to ingest",
			[][2]string{
				{"Documents & images", "*.pdf;*.png;*.jpg;*.jpeg;*.webp;*.gif;*.bmp;*.txt;*.md;*.json"},
				{"All files", "*.*"},
			}, true)
		if !ok || len(paths) == 0 {
			return
		}
		selected = append(selected, paths...)
		refreshList()
	})

	clear := widget.NewButton("Clear", func() {
		selected = nil
		refreshList()
	})

	tagsEntry := widget.NewEntry()
	tagsEntry.SetPlaceHolder("Tags (comma-separated, optional)")

	// Verify（§3.3）：攝取完成後用「檔名（去副檔名）+ tags」打一次 agent_embedding 的向量
	// 搜尋，顯示命中數——確認剛入庫的內容真的檢索得到。EmbeddingBaseURL 未設定時整顆鈕停用。
	var lastFiles []string
	var lastTags []string
	var verifyBtn *widget.Button
	verifyBtn = widget.NewButton("Verify", func() {
		if st.Gateway == nil || len(lastFiles) == 0 {
			return
		}
		terms := make([]string, 0, len(lastFiles)+len(lastTags))
		for _, f := range lastFiles {
			base := filepath.Base(f)
			terms = append(terms, strings.TrimSuffix(base, filepath.Ext(base)))
		}
		terms = append(terms, lastTags...)
		query := strings.Join(terms, " ")

		verifyBtn.Disable()
		status.SetText("Verifying retrieval...")
		go func() {
			defer verifyBtn.Enable()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			n, err := st.Gateway.SearchCount(ctx, st.Cfg.AgentID, query, 5)
			if err != nil {
				status.SetText("Verify failed: " + err.Error())
				return
			}
			if n == 0 {
				status.SetText(fmt.Sprintf("Verify: 0 hits for %q — embedding may still be in flight, try again shortly", query))
				return
			}
			status.SetText(fmt.Sprintf("Verify: %d hits for %q — content is retrievable", n, query))
		}()
	})
	verifyBtn.Disable()

	// 上傳（§3.2）：in-flight 期間 disable 按鈕，避免兩個 pollJob goroutine 交錯覆寫同一個狀態列。
	var uploadBtn *widget.Button
	uploadBtn = widget.NewButton("Upload & Ingest", func() {
		if st.Gateway == nil {
			dialog.ShowError(services.ErrNoConnection, st.Win)
			return
		}
		if len(selected) == 0 {
			dialog.ShowError(services.ErrUploadEmpty, st.Win)
			return
		}
		files := append([]string{}, selected...)
		tags := splitComma(tagsEntry.Text)

		uploadBtn.Disable()
		verifyBtn.Disable()
		go func() {
			defer uploadBtn.Enable()
			status.SetText("Uploading...")
			jobID, err := st.Gateway.Upload(context.Background(), st.Cfg.AgentID, files, tags)
			if err != nil {
				st.Log.Errorf("upload failed: %v", err)
				status.SetText("Upload failed: " + err.Error())
				return
			}
			status.SetText(fmt.Sprintf("Ingest job %s created, polling status...", jobID))
			if pollJob(st, jobID, status) {
				lastFiles, lastTags = files, tags
				if st.Gateway != nil {
					verifyBtn.Enable()
				}
			}
		}()
	})

	controls := container.NewVBox(
		container.NewHBox(pick, clear),
		tagsEntry,
		container.NewHBox(uploadBtn, verifyBtn),
		status,
	)

	return container.NewBorder(controls, nil, nil, nil, container.NewVScroll(listLabel))
}

// pollJob 每 1.5s 查一次 job，更新 status，直到 completed/failed 或逾時；completed 回傳 true。
func pollJob(st *State, jobID string, status *widget.Label) bool {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		job, err := st.Gateway.GetJob(context.Background(), jobID)
		if err != nil {
			st.Log.Errorf("job query failed: %v", err)
			status.SetText("Job query failed: " + err.Error())
			return false
		}
		switch job.Status {
		case "completed":
			status.SetText(fmt.Sprintf("Done: %d/%d chunks ingested (job %s)", job.DoneChunks, job.TotalChunks, jobID))
			return true
		case "failed":
			status.SetText(fmt.Sprintf("Failed: %s (job %s)", job.Error, jobID))
			return false
		default:
			status.SetText(fmt.Sprintf("Ingesting... %d/%d chunks (status %s)", job.DoneChunks, job.TotalChunks, job.Status))
		}
		time.Sleep(1500 * time.Millisecond)
	}
	status.SetText("Polling timed out; check again later (job " + jobID + ")")
	return false
}

func splitComma(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
