// device_id.go — dev_pub_0.9「一 Key 一裝置」的 client 端裝置指紋。
//
// device_id = SHA-256(Windows MachineGuid + "|" + username)：
//   - 跨重裝程式穩定（MachineGuid 綁作業系統安裝）
//   - 不含明文 PII（gateway 只看到 hash）
//
// 取不到 MachineGuid 時回 ("", false)，呼叫端退回「隨機 UUID 存 config.json」
// （換機視同新裝置，符合一 Key 一裝置語意）。
package services

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os/user"

	"golang.org/x/sys/windows/registry"
)

// StableDeviceID 由機器特徵推導穩定 device id；ok=false 表示取不到機器特徵。
func StableDeviceID() (id string, ok bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", false
	}
	defer k.Close()
	guid, _, err := k.GetStringValue("MachineGuid")
	if err != nil || guid == "" {
		return "", false
	}
	username := ""
	if u, uerr := user.Current(); uerr == nil {
		username = u.Username
	}
	sum := sha256.Sum256([]byte(guid + "|" + username))
	return hex.EncodeToString(sum[:]), true
}

// RandomDeviceID 產生一次性的隨機 device id（fallback，呼叫端負責存進 config）。
func RandomDeviceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "rnd_" + hex.EncodeToString(b)
}
