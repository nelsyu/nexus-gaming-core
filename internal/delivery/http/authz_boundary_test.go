package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/internal/infrastructure/auth"
	"github.com/bosstest/nexus-core/internal/usecase"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 權限邊界測試：走真正的 router、JWT、註冊與授權邏輯，只替換儲存層與錢包。
// 每個案例都對應一種「不走正門拿到錢或資料」的方法。

const (
	alicePlayerID = int64(101) // TENANT_A
	bobPlayerID   = int64(102) // TENANT_A
	carolPlayerID = int64(201) // TENANT_B
	brokenUserID  = int64(500) // 查詢時 DB 故障
	missingUserID = int64(404)
)

type boundaryUsers struct {
	domain.UserRepository
	created *domain.User
}

func (r *boundaryUsers) GetByID(_ context.Context, id int64) (*domain.User, error) {
	switch id {
	case alicePlayerID, bobPlayerID:
		return &domain.User{ID: id, TenantID: "TENANT_A", Role: domain.RolePlayer}, nil
	case carolPlayerID:
		return &domain.User{ID: id, TenantID: "TENANT_B", Role: domain.RolePlayer}, nil
	case brokenUserID:
		return nil, errors.New("db down")
	}
	return nil, domain.ErrUserNotFound
}

func (r *boundaryUsers) CreateUser(_ context.Context, u *domain.User) error {
	u.ID = 900
	r.created = u
	return nil
}

type boundaryWallets struct {
	domain.WalletRepository
	created *domain.Wallet
}

func (r *boundaryWallets) CreateWallet(_ context.Context, w *domain.Wallet) error {
	r.created = w
	return nil
}

type boundaryUoW struct{}

func (boundaryUoW) DoTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

type boundaryBlacklist struct{}

func (boundaryBlacklist) RevokeToken(context.Context, string, time.Duration) error { return nil }
func (boundaryBlacklist) IsRevoked(context.Context, string) (bool, error)          { return false, nil }

// recordingWallet 記錄真正送進錢包 usecase 的內容
type recordingWallet struct {
	readUserID int64
	tx         *domain.TransactionRequest
}

func (w *recordingWallet) GetBalance(_ context.Context, userID int64, currency string) (*domain.Wallet, error) {
	w.readUserID = userID
	return &domain.Wallet{UserID: userID, Currency: currency}, nil
}

func (w *recordingWallet) ProcessTransaction(_ context.Context, req *domain.TransactionRequest) (*domain.Transaction, error) {
	w.tx = req
	return &domain.Transaction{ID: 1, Type: req.Type, Amount: req.Amount, ProviderID: req.ProviderID}, nil
}

type boundaryEnv struct {
	t       *testing.T
	router  http.Handler
	tokens  domain.TokenManager
	users   *boundaryUsers
	wallets *boundaryWallets
	wallet  *recordingWallet
}

func newBoundaryEnv(t *testing.T) *boundaryEnv {
	t.Helper()
	registerValidators(t)
	e := &boundaryEnv{
		t:       t,
		tokens:  auth.NewJWTManager("test-secret-key-1234567890123456", 5*time.Minute, time.Hour),
		users:   &boundaryUsers{},
		wallets: &boundaryWallets{},
		wallet:  &recordingWallet{},
	}
	r, err := SetupRouter(RouterDeps{
		WalletHandler: NewWalletHandler(e.wallet, usecase.NewWalletAuthorizer(e.users)),
		AuthHandler: NewAuthHandler(usecase.NewAuthUsecase(
			boundaryUoW{}, e.users, e.wallets, auth.NewBcryptHasher(4), e.tokens, nil, boundaryBlacklist{},
		)),
		TokenManager: e.tokens,
		Blacklist:    boundaryBlacklist{},
		ProviderAuth: &fakeProviderAuth{},
	})
	require.NoError(t, err)
	e.router = r
	return e
}

// do 以指定身分送出請求；actor 為 nil 代表未登入
func (e *boundaryEnv) do(method, path, body string, actor *domain.User) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if actor != nil {
		pair, err := e.tokens.GenerateTokenPair(actor)
		require.NoError(e.t, err)
		req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

func staff(tenant string, role domain.Role) *domain.User {
	return &domain.User{ID: 999, TenantID: tenant, Role: role}
}

func alice() *domain.User {
	return &domain.User{ID: alicePlayerID, TenantID: "TENANT_A", Role: domain.RolePlayer}
}

// A：公開註冊不能決定自己的角色
func TestBoundary_RegisterAlwaysCreatesPlayer(t *testing.T) {
	for _, role := range []string{"", `,"role":"admin"`, `,"role":"operator"`, `,"role":"vip"`, `,"Role":"admin"`} {
		t.Run("extra="+role, func(t *testing.T) {
			e := newBoundaryEnv(t)
			w := e.do(http.MethodPost, "/api/v1/auth/register",
				`{"tenant_id":"TENANT_A","username":"mallory","password":"password123","currency":"TWD"`+role+`}`, nil)

			require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
			require.NotNil(t, e.users.created)
			assert.Equal(t, domain.RolePlayer, e.users.created.Role)
			assert.NotNil(t, e.wallets.created, "every registered player gets a wallet")
		})
	}
}

// B、C：查餘額
func TestBoundary_ReadBalance(t *testing.T) {
	tests := []struct {
		name     string
		actor    *domain.User
		userID   string // 原封不動放進 query string
		wantCode int
	}{
		{"player reads self", alice(), "101", 200},
		{"player reads another player", alice(), "102", 403},
		{"player reads another tenant", alice(), "201", 403},
		// gin 綁定時會去掉空白：授權若自己另外解析一次，這些值就會繞過檢查
		{"leading space", alice(), "%20102", 403},
		{"trailing space", alice(), "102%20", 403},
		{"tab", alice(), "%09102", 403},
		{"newline", alice(), "102%0A", 403},
		{"plus sign", alice(), "%2B102", 403},
		{"repeated parameter", alice(), "102&user_id=101", 403},

		{"admin reads same tenant", staff("TENANT_A", domain.RoleAdmin), "101", 200},
		{"operator reads same tenant", staff("TENANT_A", domain.RoleOperator), "101", 200},
		{"admin reads another tenant", staff("TENANT_A", domain.RoleAdmin), "201", 404},
		{"operator reads another tenant", staff("TENANT_A", domain.RoleOperator), "201", 404},
		{"admin reads missing user", staff("TENANT_A", domain.RoleAdmin), "404", 404},
		{"lookup failure fails closed", staff("TENANT_A", domain.RoleAdmin), "500", 500},

		{"unknown role", staff("TENANT_A", "vip"), "101", 403},
		{"empty role", staff("TENANT_A", ""), "101", 403},
		{"role differs by case", staff("TENANT_A", "Admin"), "101", 403},
		{"staff without tenant", staff("", domain.RoleAdmin), "101", 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newBoundaryEnv(t)
			w := e.do(http.MethodGet, "/api/v1/wallet/balance?currency=TWD&user_id="+tt.userID, "", tt.actor)

			assert.Equal(t, tt.wantCode, w.Code, w.Body.String())
			if tt.wantCode != http.StatusOK {
				assert.Zero(t, e.wallet.readUserID, "wallet must not be read")
			}
		})
	}
}

func adjustBody(userID int64, typ, extra string) string {
	return fmt.Sprintf(`{"user_id":%d,"currency":"TWD","type":%q,"amount":"100","provider_tx_id":"adj-1"%s}`, userID, typ, extra)
}

// C：人工存提款
func TestBoundary_AdjustBalance(t *testing.T) {
	tests := []struct {
		name     string
		actor    *domain.User
		target   int64
		wantCode int
	}{
		{"admin same tenant", staff("TENANT_A", domain.RoleAdmin), alicePlayerID, 200},
		{"operator same tenant", staff("TENANT_A", domain.RoleOperator), alicePlayerID, 200},
		{"admin another tenant", staff("TENANT_A", domain.RoleAdmin), carolPlayerID, 404},
		{"operator another tenant", staff("TENANT_B", domain.RoleOperator), alicePlayerID, 404},
		{"tenant differs by case", staff("tenant_a", domain.RoleAdmin), alicePlayerID, 404},
		{"missing user", staff("TENANT_A", domain.RoleAdmin), missingUserID, 404},
		{"lookup failure fails closed", staff("TENANT_A", domain.RoleAdmin), brokenUserID, 500},
		{"player", alice(), alicePlayerID, 403},
		{"unknown role", staff("TENANT_A", "vip"), alicePlayerID, 403},
		{"staff without tenant", staff("", domain.RoleAdmin), alicePlayerID, 403},
	}
	for _, typ := range []string{"DEPOSIT", "WITHDRAW"} {
		for _, tt := range tests {
			t.Run(typ+"/"+tt.name, func(t *testing.T) {
				e := newBoundaryEnv(t)
				w := e.do(http.MethodPost, "/api/v1/wallet/transaction",
					adjustBody(tt.target, typ, `,"provider_id":"ADMIN"`), tt.actor)

				assert.Equal(t, tt.wantCode, w.Code, w.Body.String())
				assert.Equal(t, tt.wantCode == http.StatusOK, e.wallet.tx != nil)
			})
		}
	}
}

// 「別站的玩家」和「不存在的玩家」回應必須一模一樣，否則可以用來探測別站有哪些玩家
func TestBoundary_CrossTenantLooksLikeNotFound(t *testing.T) {
	admin := staff("TENANT_A", domain.RoleAdmin)
	get := func(id int64) string {
		w := newBoundaryEnv(t).do(http.MethodGet, fmt.Sprintf("/api/v1/wallet/balance?currency=TWD&user_id=%d", id), "", admin)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		return fmt.Sprint(w.Code, body)
	}
	assert.Equal(t, get(missingUserID), get(carolPlayerID))
}

// D：後台交易的 provider_id 由伺服器決定
func TestBoundary_BackofficeProviderIDSetByServer(t *testing.T) {
	for _, extra := range []string{"", `,"provider_id":"DEMO"`, `,"provider_id":"BACKOFFICE#TENANT_B"`} {
		t.Run("extra="+extra, func(t *testing.T) {
			e := newBoundaryEnv(t)
			w := e.do(http.MethodPost, "/api/v1/wallet/transaction",
				adjustBody(alicePlayerID, "DEPOSIT", extra), staff("TENANT_A", domain.RoleAdmin))

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.NotNil(t, e.wallet.tx)
			assert.Equal(t, "BACKOFFICE#TENANT_A", e.wallet.tx.ProviderID)
		})
	}
}

// 後台交易不能帶遊戲局欄位混進注單資料
func TestBoundary_BackofficeIgnoresGameFields(t *testing.T) {
	e := newBoundaryEnv(t)
	w := e.do(http.MethodPost, "/api/v1/wallet/transaction",
		adjustBody(alicePlayerID, "DEPOSIT", `,"provider_id":"ADMIN","round_id":"r1","related_provider_tx_id":"bet-1"`),
		staff("TENANT_A", domain.RoleAdmin))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotNil(t, e.wallet.tx)
	assert.Empty(t, e.wallet.tx.RoundID)
	assert.Empty(t, e.wallet.tx.RelatedProviderTxID)
}

// 即使有遊戲商的 ID 被設成後台的保留名稱，也不能用它送出交易
func TestBoundary_ProviderCannotUseBackofficeName(t *testing.T) {
	e := newBoundaryEnv(t)
	const reserved = "BACKOFFICE#TENANT_A"
	req := providerRequest(txBody("BET", reserved), reserved)
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Nil(t, e.wallet.tx)
}
