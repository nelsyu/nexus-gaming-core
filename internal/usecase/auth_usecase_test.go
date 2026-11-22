package usecase_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/internal/infrastructure/auth"
	"github.com/bosstest/nexus-core/internal/usecase"
	"github.com/stretchr/testify/assert"
)

// Mock UoW
type mockUoW struct{}

func (m *mockUoW) DoTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	return fn(ctx)
}

// Mock UserRepository
type mockUserRepo struct {
	byUsername map[string]*domain.User
	byID       map[int64]*domain.User
	nextID     int64
}

func newMockUserRepo() *mockUserRepo {
	return &mockUserRepo{
		byUsername: make(map[string]*domain.User),
		byID:       make(map[int64]*domain.User),
		nextID:     1,
	}
}

func (m *mockUserRepo) CreateUser(ctx context.Context, user *domain.User) error {
	key := fmt.Sprintf("%s:%s", user.TenantID, user.Username)
	if _, exists := m.byUsername[key]; exists {
		return domain.ErrUserAlreadyExists
	}
	user.ID = m.nextID
	m.nextID++
	m.byUsername[key] = user
	m.byID[user.ID] = user
	return nil
}

func (m *mockUserRepo) GetByID(ctx context.Context, id int64) (*domain.User, error) {
	if u, exists := m.byID[id]; exists {
		return u, nil
	}
	return nil, domain.ErrUserNotFound
}

func (m *mockUserRepo) GetByUsername(ctx context.Context, tenantID, username string) (*domain.User, error) {
	key := fmt.Sprintf("%s:%s", tenantID, username)
	if u, exists := m.byUsername[key]; exists {
		return u, nil
	}
	return nil, domain.ErrUserNotFound
}

// Mock WalletRepository
type mockWalletRepo struct {
	wallets []*domain.Wallet
}

func (m *mockWalletRepo) CreateWallet(ctx context.Context, wallet *domain.Wallet) error {
	wallet.ID = int64(len(m.wallets) + 1)
	m.wallets = append(m.wallets, wallet)
	return nil
}

func (m *mockWalletRepo) GetWalletByID(ctx context.Context, id int64) (*domain.Wallet, error) {
	return nil, nil
}

func (m *mockWalletRepo) GetWalletByUserID(ctx context.Context, userID int64, currency string) (*domain.Wallet, error) {
	return nil, nil
}

func (m *mockWalletRepo) GetWalletByIDForUpdate(ctx context.Context, id int64) (*domain.Wallet, error) {
	return nil, nil
}

func (m *mockWalletRepo) UpdateBalance(ctx context.Context, wallet *domain.Wallet) error {
	return nil
}

// Mock TokenBlacklist
type mockBlacklist struct {
	revoked map[string]bool
}

func newMockBlacklist() *mockBlacklist {
	return &mockBlacklist{revoked: make(map[string]bool)}
}

func (m *mockBlacklist) RevokeToken(ctx context.Context, jti string, ttl time.Duration) error {
	m.revoked[jti] = true
	return nil
}

func (m *mockBlacklist) IsRevoked(ctx context.Context, jti string) (bool, error) {
	return m.revoked[jti], nil
}

// Mock RefreshTokenRepository
type mockRefreshTokenRepo struct {
	tokens map[string]int64
}

func newMockRefreshTokenRepo() *mockRefreshTokenRepo {
	return &mockRefreshTokenRepo{tokens: make(map[string]int64)}
}

func (m *mockRefreshTokenRepo) StoreRefreshToken(ctx context.Context, token string, userID int64, ttl time.Duration) error {
	m.tokens[token] = userID
	return nil
}

func (m *mockRefreshTokenRepo) GetUserIDByRefreshToken(ctx context.Context, token string) (int64, error) {
	if uid, exists := m.tokens[token]; exists {
		return uid, nil
	}
	return 0, domain.ErrInvalidToken
}

func (m *mockRefreshTokenRepo) RevokeRefreshToken(ctx context.Context, token string) error {
	delete(m.tokens, token)
	return nil
}

func TestAuthUsecase_RegisterAndLogin(t *testing.T) {
	uow := &mockUoW{}
	userRepo := newMockUserRepo()
	walletRepo := &mockWalletRepo{}
	hasher := auth.NewBcryptHasher(4) // 測試環境使用最低 cost 加速
	jwtManager := auth.NewJWTManager("test-secret-key-1234567890123456", 5*time.Minute, 24*time.Hour)
	refreshTokenRepo := newMockRefreshTokenRepo()
	blacklist := newMockBlacklist()

	authUc := usecase.NewAuthUsecase(uow, userRepo, walletRepo, hasher, jwtManager, refreshTokenRepo, blacklist)
	ctx := context.Background()

	// 1. 測試成功註冊 (Player)
	regReq := &domain.RegisterRequest{
		TenantID: "TENANT_A",
		Username: "alice",
		Password: "password123",
		Currency: "USD",
	}
	user, err := authUc.Register(ctx, regReq)
	assert.NoError(t, err)
	assert.Equal(t, "alice", user.Username)
	assert.Equal(t, domain.RolePlayer, user.Role)
	assert.NotEmpty(t, user.PasswordHash)
	assert.NotEqual(t, "password123", user.PasswordHash) // 確保密碼雜湊
	assert.Len(t, walletRepo.wallets, 1)                  // 玩家自動建立錢包

	// 2. 測試重複註冊相同帳號 -> ErrUserAlreadyExists
	_, err = authUc.Register(ctx, regReq)
	assert.ErrorIs(t, err, domain.ErrUserAlreadyExists)

	// 3. 測試登入成功
	loginReq := &domain.LoginRequest{
		TenantID: "TENANT_A",
		Username: "alice",
		Password: "password123",
	}
	tokenPair, err := authUc.Login(ctx, loginReq)
	assert.NoError(t, err)
	assert.NotEmpty(t, tokenPair.AccessToken)
	assert.NotEmpty(t, tokenPair.RefreshToken)
	assert.Equal(t, "Bearer", tokenPair.TokenType)

	// 驗證 Refresh Token 成功持久化存入 Store
	storedUID, err := refreshTokenRepo.GetUserIDByRefreshToken(ctx, tokenPair.RefreshToken)
	assert.NoError(t, err)
	assert.Equal(t, user.ID, storedUID)

	// 4. 測試密碼錯誤 -> ErrInvalidCredentials
	badLoginReq := &domain.LoginRequest{
		TenantID: "TENANT_A",
		Username: "alice",
		Password: "wrongpassword",
	}
	_, err = authUc.Login(ctx, badLoginReq)
	assert.ErrorIs(t, err, domain.ErrInvalidCredentials)

	// 5. 測試不存在用戶 -> ErrInvalidCredentials (防止使用者枚舉)
	nonExistReq := &domain.LoginRequest{
		TenantID: "TENANT_A",
		Username: "bob",
		Password: "password123",
	}
	_, err = authUc.Login(ctx, nonExistReq)
	assert.ErrorIs(t, err, domain.ErrInvalidCredentials)
}

func TestAuthUsecase_RefreshToken_And_Logout(t *testing.T) {
	uow := &mockUoW{}
	userRepo := newMockUserRepo()
	walletRepo := &mockWalletRepo{}
	hasher := auth.NewBcryptHasher(4)
	jwtManager := auth.NewJWTManager("test-secret-key-1234567890123456", 5*time.Minute, 24*time.Hour)
	refreshTokenRepo := newMockRefreshTokenRepo()
	blacklist := newMockBlacklist()

	authUc := usecase.NewAuthUsecase(uow, userRepo, walletRepo, hasher, jwtManager, refreshTokenRepo, blacklist)
	ctx := context.Background()

	// 建立使用者
	regReq := &domain.RegisterRequest{
		TenantID: "TENANT_A",
		Username: "bob",
		Password: "password123",
		Currency: "EUR",
	}
	_, err := authUc.Register(ctx, regReq)
	assert.NoError(t, err)

	tokens, err := authUc.Login(ctx, &domain.LoginRequest{
		TenantID: "TENANT_A",
		Username: "bob",
		Password: "password123",
	})
	assert.NoError(t, err)

	// 1. 測試 RefreshToken 續期
	newTokens, err := authUc.RefreshToken(ctx, tokens.RefreshToken)
	assert.NoError(t, err)
	assert.NotEmpty(t, newTokens.AccessToken)
	assert.NotEmpty(t, newTokens.RefreshToken)
	assert.NotEqual(t, tokens.RefreshToken, newTokens.RefreshToken) // 產生全新 Refresh Token

	// 2. 測試 Token 輪換：舊的 Refresh Token 應該已經被銷毀，無法重複使用
	_, err = authUc.RefreshToken(ctx, tokens.RefreshToken)
	assert.ErrorIs(t, err, domain.ErrInvalidToken)

	// 3. 測試 Logout：Refresh Token 被撤銷 + Access Token 加入黑名單
	err = authUc.Logout(ctx, newTokens.AccessToken, newTokens.RefreshToken)
	assert.NoError(t, err)

	// 3a. 登出後 Refresh Token 應無法再換發新 Token（這才是真正的登出）
	_, err = authUc.RefreshToken(ctx, newTokens.RefreshToken)
	assert.ErrorIs(t, err, domain.ErrInvalidToken)

	// 3b. Access Token 的 JTI 應已在黑名單中（供敏感路由即時阻斷）
	claims, err := jwtManager.ValidateAccessToken(newTokens.AccessToken)
	assert.NoError(t, err)
	revoked, err := blacklist.IsRevoked(ctx, claims.TokenID)
	assert.NoError(t, err)
	assert.True(t, revoked)

	// 3c. 重複登出應為冪等操作
	err = authUc.Logout(ctx, newTokens.AccessToken, newTokens.RefreshToken)
	assert.NoError(t, err)

	// 4. 防混淆資安測試：Opaque Refresh Token 絕無法被當作 Access Token 驗證
	_, err = jwtManager.ValidateAccessToken(newTokens.RefreshToken)
	assert.Error(t, err)
}

func TestAuthUsecase_Logout_RejectsOtherUsersRefreshToken(t *testing.T) {
	userRepo := newMockUserRepo()
	jwtManager := auth.NewJWTManager("test-secret-key-1234567890123456", 5*time.Minute, 24*time.Hour)
	refreshTokenRepo := newMockRefreshTokenRepo()
	authUc := usecase.NewAuthUsecase(&mockUoW{}, userRepo, &mockWalletRepo{}, auth.NewBcryptHasher(4), jwtManager, refreshTokenRepo, newMockBlacklist())
	ctx := context.Background()

	for _, name := range []string{"alice", "mallory"} {
		_, err := authUc.Register(ctx, &domain.RegisterRequest{TenantID: "TENANT_A", Username: name, Password: "password123", Currency: "USD"})
		assert.NoError(t, err)
	}
	alice, err := authUc.Login(ctx, &domain.LoginRequest{TenantID: "TENANT_A", Username: "alice", Password: "password123"})
	assert.NoError(t, err)
	mallory, err := authUc.Login(ctx, &domain.LoginRequest{TenantID: "TENANT_A", Username: "mallory", Password: "password123"})
	assert.NoError(t, err)

	// mallory 用自己的 Access Token 嘗試撤銷 alice 的 Refresh Token -> 拒絕
	err = authUc.Logout(ctx, mallory.AccessToken, alice.RefreshToken)
	assert.ErrorIs(t, err, domain.ErrForbidden)

	// alice 的工作階段不受影響
	_, err = authUc.RefreshToken(ctx, alice.RefreshToken)
	assert.NoError(t, err)
}
