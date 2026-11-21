// Package providersig 定義遊戲商呼叫錢包 API 時使用的 HMAC-SHA256 請求簽章。
//
// 雙方事先共享一把密鑰 (secret)，密鑰本身從不在網路上傳輸。
// 發送方對「請求的關鍵內容」計算 HMAC，接收方用同一把密鑰重算並比對：
//   - 沒有密鑰就算不出正確簽章 → 無法冒充
//   - 內容被改動任何一個位元組，簽章就完全不同 → 竄改會被發現
//
// 簽章本身無法防止「原封不動重送」，因此簽章內容包含時間戳與一次性 nonce，
// 由接收方檢查時間窗口並記錄用過的 nonce。
//
// 此套件同時給伺服器端 (驗證) 與遊戲商模擬器 (簽章) 使用，確保兩邊算法一致。
package providersig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HTTP Header 名稱
const (
	HeaderProviderID = "X-Provider-ID"
	HeaderTimestamp  = "X-Timestamp" // Unix 秒
	HeaderNonce      = "X-Nonce"     // 每個請求唯一的隨機字串
	HeaderSignature  = "X-Signature" // hex(HMAC-SHA256(secret, canonical))
)

// CanonicalString 組出要簽章的標準字串，每個欄位以換行分隔：
//
//	{timestamp}\n{nonce}\n{METHOD}\n{path}\n{hex(sha256(body))}
//
// 為什麼要「標準化」？簽章比對的是位元組，雙方只要格式差一點 (大小寫、順序、分隔符號) 就會不一致，
// 所以必須明確規定每個欄位的順序與格式。body 先取雜湊再放入，避免大型 body 讓標準字串過長。
func CanonicalString(timestamp, nonce, method, path string, body []byte) string {
	bodyHash := sha256.Sum256(body)
	return strings.Join([]string{
		timestamp,
		nonce,
		strings.ToUpper(method),
		path,
		hex.EncodeToString(bodyHash[:]),
	}, "\n")
}

// Sign 計算請求簽章 (hex 編碼)
func Sign(secret, timestamp, nonce, method, path string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(CanonicalString(timestamp, nonce, method, path, body)))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify 以固定時間比對簽章。
//
// 不能用 == 比較字串：== 在第一個不同的字元就會提早回傳，攻擊者可以量測回應時間，
// 一個字元一個字元猜出正確簽章 (Timing Attack)。hmac.Equal 無論哪裡不同，花費的時間都一樣。
func Verify(secret, timestamp, nonce, method, path string, body []byte, signature string) bool {
	expected := Sign(secret, timestamp, nonce, method, path, body)
	return hmac.Equal([]byte(expected), []byte(strings.ToLower(signature)))
}
