package providersig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	secret = "demo-secret"
	ts     = "1763700000"
	nonce  = "n-123"
	method = "POST"
	path   = "/api/v1/provider/transaction"
)

var body = []byte(`{"amount":"100.00"}`)

// 用標準函式庫獨立算一次，確認實作符合文件定義的格式 (避免實作與規格悄悄分歧)
func TestSign_MatchesDocumentedFormat(t *testing.T) {
	bodyHash := sha256.Sum256(body)
	canonical := ts + "\n" + nonce + "\nPOST\n" + path + "\n" + hex.EncodeToString(bodyHash[:])
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonical))

	assert.Equal(t, hex.EncodeToString(mac.Sum(nil)), Sign(secret, ts, nonce, method, path, body))
}

func TestVerify_RoundTrip(t *testing.T) {
	sig := Sign(secret, ts, nonce, method, path, body)
	assert.True(t, Verify(secret, ts, nonce, method, path, body, sig))
}

func TestVerify_AcceptsUppercaseHex(t *testing.T) {
	sig := Sign(secret, ts, nonce, method, path, body)
	upper := []byte(sig)
	for i, c := range upper {
		if c >= 'a' && c <= 'f' {
			upper[i] = c - 32
		}
	}
	assert.True(t, Verify(secret, ts, nonce, method, path, body, string(upper)))
}

// 任何一個欄位被改動，簽章都必須失效
func TestVerify_DetectsTampering(t *testing.T) {
	sig := Sign(secret, ts, nonce, method, path, body)

	tests := []struct {
		name                         string
		secret, ts, nonce, method, p string
		body                         []byte
	}{
		{"wrong secret", "other-secret", ts, nonce, method, path, body},
		{"timestamp changed", secret, "1763700001", nonce, method, path, body},
		{"nonce changed", secret, ts, "n-124", method, path, body},
		{"method changed", secret, ts, nonce, "PUT", path, body},
		{"path changed", secret, ts, nonce, method, "/api/v1/wallet/transaction", body},
		{"body amount changed", secret, ts, nonce, method, path, []byte(`{"amount":"10000.00"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, Verify(tt.secret, tt.ts, tt.nonce, tt.method, tt.p, tt.body, sig))
		})
	}
}
