package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	customvalidator "github.com/bosstest/nexus-core/internal/delivery/http/validator"
	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProviderAuth 記錄收到的驗證請求，回傳預設結果
type fakeProviderAuth struct {
	got domain.ProviderAuthRequest
	err error
}

func (f *fakeProviderAuth) Authenticate(_ context.Context, req domain.ProviderAuthRequest) (*domain.GameProvider, error) {
	f.got = req
	if f.err != nil {
		return nil, f.err
	}
	return &domain.GameProvider{ProviderID: req.ProviderID}, nil
}

// fakeWalletUsecase 記錄是否被呼叫
type fakeWalletUsecase struct{ called bool }

func (f *fakeWalletUsecase) GetBalance(context.Context, int64, string) (*domain.Wallet, error) {
	return nil, nil
}
func (f *fakeWalletUsecase) ProcessTransaction(_ context.Context, req *domain.TransactionRequest) (*domain.Transaction, error) {
	f.called = true
	return &domain.Transaction{ID: 1, Type: req.Type, Amount: req.Amount, ProviderID: req.ProviderID}, nil
}

// allowAllAuthorizer 授權規則另有測試 (authz_boundary_test.go)，這裡只測遊戲商與交易類型
type allowAllAuthorizer struct{}

func (allowAllAuthorizer) AuthorizeRead(context.Context, domain.Actor, int64) error   { return nil }
func (allowAllAuthorizer) AuthorizeAdjust(context.Context, domain.Actor, int64) error { return nil }

var registerValidatorsOnce sync.Once

// 與 cmd/server/main.go 相同的驗證器設定，handler 才能正確解析 DTO
func registerValidators(t *testing.T) {
	registerValidatorsOnce.Do(func() {
		v, ok := binding.Validator.Engine().(*validator.Validate)
		require.True(t, ok)
		v.SetTagName("validate")
		require.NoError(t, customvalidator.RegisterCustomValidators(v))
	})
}

func newProviderRouter(t *testing.T, auth domain.ProviderAuthenticator, trusted []string) (*gin.Engine, *fakeWalletUsecase) {
	gin.SetMode(gin.TestMode)
	registerValidators(t)
	uc := &fakeWalletUsecase{}
	h := NewWalletHandler(uc, allowAllAuthorizer{})

	r := gin.New()
	require.NoError(t, r.SetTrustedProxies(trusted))
	r.POST("/api/v1/provider/transaction", ProviderAuthMiddleware(auth), h.ProcessProviderTransaction)
	// 省略 JWT，直接放入身分，只測交易類型限制
	r.POST("/api/v1/wallet/transaction", func(c *gin.Context) { c.Set(CtxTenantID, "TENANT_A") }, h.ProcessAdminTransaction)
	return r, uc
}

func txBody(typ, providerID string) string {
	return fmt.Sprintf(`{"user_id":1,"currency":"TWD","type":"%s","amount":"10.00","provider_id":"%s","provider_tx_id":"tx-1","round_id":"r1"}`, typ, providerID)
}

func providerRequest(body, providerID string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider/transaction", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Provider-ID", providerID)
	req.Header.Set("X-Timestamp", "1763700000")
	req.Header.Set("X-Nonce", "n-1")
	req.Header.Set("X-Signature", "deadbeef")
	req.RemoteAddr = "203.0.113.10:5555"
	return req
}

// 驗證通過後，handler 仍能讀到完整的 body (middleware 讀過之後有放回去)
func TestProviderMiddleware_PassesHeadersAndRestoresBody(t *testing.T) {
	auth := &fakeProviderAuth{}
	r, uc := newProviderRouter(t, auth, nil)
	body := txBody("BET", "DEMO")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, providerRequest(body, "DEMO"))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.True(t, uc.called)
	assert.Equal(t, domain.ProviderAuthRequest{
		ProviderID: "DEMO", Timestamp: "1763700000", Nonce: "n-1", Signature: "deadbeef",
		Method: "POST", Path: "/api/v1/provider/transaction", Body: []byte(body), ClientIP: "203.0.113.10",
	}, auth.got)
}

func TestProviderMiddleware_ErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"unauthorized", fmt.Errorf("%w: invalid signature", domain.ErrProviderUnauthorized), http.StatusUnauthorized},
		{"ip not allowed", fmt.Errorf("%w: 1.2.3.4", domain.ErrProviderIPNotAllowed), http.StatusForbidden},
		{"infrastructure error", errors.New("redis down"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, uc := newProviderRouter(t, &fakeProviderAuth{err: tt.err}, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, providerRequest(txBody("BET", "DEMO"), "DEMO"))

			assert.Equal(t, tt.wantCode, w.Code)
			assert.False(t, uc.called)
			// 對外訊息不能洩漏內部原因 (例如「invalid signature」或 IP)
			assert.NotContains(t, w.Body.String(), "signature")
			assert.NotContains(t, w.Body.String(), "1.2.3.4")
		})
	}
}

func TestProviderMiddleware_BodyTooLarge(t *testing.T) {
	r, uc := newProviderRouter(t, &fakeProviderAuth{}, nil)
	huge := `{"pad":"` + strings.Repeat("x", maxProviderBodyBytes) + `"}`

	w := httptest.NewRecorder()
	r.ServeHTTP(w, providerRequest(huge, "DEMO"))

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.False(t, uc.called)
}

// 核心安全案例：不信任代理時，攻擊者偽造 X-Forwarded-For 冒充遊戲商 IP 無效
func TestProviderMiddleware_ForgedForwardedForIgnored(t *testing.T) {
	auth := &fakeProviderAuth{}
	r, _ := newProviderRouter(t, auth, nil) // 不信任任何代理

	req := providerRequest(txBody("BET", "DEMO"), "DEMO")
	req.RemoteAddr = "198.51.100.66:4444"             // 攻擊者真正的 IP
	req.Header.Set("X-Forwarded-For", "203.0.113.10") // 偽造成遊戲商的 IP
	r.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, "198.51.100.66", auth.got.ClientIP, "must use the TCP peer address, not the forged header")
}

// 在自己的負載平衡器後面時，才採信它填寫的 X-Forwarded-For
func TestProviderMiddleware_TrustedProxyForwardedForUsed(t *testing.T) {
	auth := &fakeProviderAuth{}
	r, _ := newProviderRouter(t, auth, []string{"10.0.0.0/8"}) // 內網 LB

	req := providerRequest(txBody("BET", "DEMO"), "DEMO")
	req.RemoteAddr = "10.0.0.5:4444"                  // 請求來自 LB
	req.Header.Set("X-Forwarded-For", "203.0.113.10") // LB 填入的真實客戶端 IP
	r.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, "203.0.113.10", auth.got.ClientIP)
}

// 遊戲商 A 通過驗證後，不能在 body 裡冒用遊戲商 B 的名義
func TestProviderHandler_ProviderIDMismatchForbidden(t *testing.T) {
	r, uc := newProviderRouter(t, &fakeProviderAuth{}, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, providerRequest(txBody("BET", "OTHER_PROVIDER"), "DEMO"))

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, uc.called)
}

// 職責分離：遊戲商不能存提款，後台不能下注
func TestHandlers_TransactionTypeSeparation(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		typ      string
		wantCode int
	}{
		{"provider BET allowed", "provider", "BET", http.StatusOK},
		{"provider DEPOSIT rejected", "provider", "DEPOSIT", http.StatusBadRequest},
		{"provider WITHDRAW rejected", "provider", "WITHDRAW", http.StatusBadRequest},
		{"admin DEPOSIT allowed", "admin", "DEPOSIT", http.StatusOK},
		{"admin BET rejected", "admin", "BET", http.StatusBadRequest},
		{"admin REFUND rejected", "admin", "REFUND", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, uc := newProviderRouter(t, &fakeProviderAuth{}, nil)
			var req *http.Request
			if tt.endpoint == "provider" {
				req = providerRequest(txBody(tt.typ, "DEMO"), "DEMO")
			} else {
				req = httptest.NewRequest(http.MethodPost, "/api/v1/wallet/transaction", strings.NewReader(txBody(tt.typ, "ADMIN")))
				req.Header.Set("Content-Type", "application/json")
			}

			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantCode, w.Code, w.Body.String())
			assert.Equal(t, tt.wantCode == http.StatusOK, uc.called)
		})
	}
}

// 確認 fake 型別符合介面 (介面變更時編譯期就會發現)
var (
	_ domain.ProviderAuthenticator = (*fakeProviderAuth)(nil)
	_ domain.WalletUsecase         = (*fakeWalletUsecase)(nil)
)
