//go:build !windows

package views

// nativeOpenFiles 非 Windows 平台的空實作（本 client 只出 Windows；此檔僅為跨平台可編譯）。
func nativeOpenFiles(title string, filterPairs [][2]string, multi bool) ([]string, bool) {
	return nil, false
}
