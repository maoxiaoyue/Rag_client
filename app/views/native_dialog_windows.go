//go:build windows

// native_dialog_windows.go — 用 Windows 檔案總管的原生「開啟檔案」對話框（comdlg32
// GetOpenFileNameW），取代 Fyne 內建的非原生檔案選取器。支援多選。
package views

import (
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	modComdlg32          = syscall.NewLazyDLL("comdlg32.dll")
	procGetOpenFileNameW = modComdlg32.NewProc("GetOpenFileNameW")
)

type openfilenameW struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        uintptr
	dwReserved        uint32
	flagsEx           uint32
}

const (
	ofnPathMustExist    = 0x00000800
	ofnFileMustExist    = 0x00001000
	ofnExplorer         = 0x00080000
	ofnAllowMultiselect = 0x00000200
	ofnNoChangeDir      = 0x00000008
)

// nativeOpenFiles 開 Windows 原生檔案對話框；multi=true 可多選。取消/錯誤回 (nil,false)。
func nativeOpenFiles(title string, filterPairs [][2]string, multi bool) ([]string, bool) {
	const bufLen = 1 << 15 // 32K uint16，容多選路徑串
	buf := make([]uint16, bufLen)

	var ofn openfilenameW
	ofn.lStructSize = uint32(unsafe.Sizeof(ofn))
	ofn.lpstrFile = &buf[0]
	ofn.nMaxFile = uint32(bufLen)
	ofn.flags = ofnPathMustExist | ofnFileMustExist | ofnExplorer | ofnNoChangeDir
	if multi {
		ofn.flags |= ofnAllowMultiselect
	}
	if f := buildFilter(filterPairs); f != nil {
		ofn.lpstrFilter = f
	}
	if title != "" {
		if t, err := syscall.UTF16PtrFromString(title); err == nil {
			ofn.lpstrTitle = t
		}
	}

	ret, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if ret == 0 {
		return nil, false // 使用者取消或對話框錯誤
	}
	return parseOpenResult(buf), true
}

// buildFilter 把 (label, pattern) 組成 GetOpenFileName 需要的雙 null 結尾 UTF16 filter。
func buildFilter(pairs [][2]string) *uint16 {
	if len(pairs) == 0 {
		return nil
	}
	var u []uint16
	for _, p := range pairs {
		u = append(u, utf16.Encode([]rune(p[0]))...)
		u = append(u, 0)
		u = append(u, utf16.Encode([]rune(p[1]))...)
		u = append(u, 0)
	}
	u = append(u, 0) // 整體以額外一個 null 收尾
	return &u[0]
}

// parseOpenResult 解析回傳緩衝：單選=完整路徑；多選=dir\0file1\0file2\0\0。
func parseOpenResult(buf []uint16) []string {
	parts := splitNulls(buf)
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 {
		return []string{parts[0]} // 單選：整段就是完整路徑
	}
	dir := parts[0]
	out := make([]string, 0, len(parts)-1)
	for _, name := range parts[1:] {
		out = append(out, dir+`\`+name)
	}
	return out
}

func splitNulls(buf []uint16) []string {
	var out []string
	start := 0
	for i, c := range buf {
		if c != 0 {
			continue
		}
		if i == start { // 連續 null → 結束
			break
		}
		out = append(out, string(utf16.Decode(buf[start:i])))
		start = i + 1
	}
	return out
}
