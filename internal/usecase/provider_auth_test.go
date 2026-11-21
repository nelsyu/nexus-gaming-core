package usecase

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/internal/domain/mocks"
	"github.com/bosstest/nexus-core/pkg/providersig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const (
	testSecret = "s3cret"
	testPath   = "/api/v1/provider/transaction"
)

var fixedNow = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

func demoProvider() *domain.GameProvider {
	return &domain.GameProvider{ProviderID: "DEMO", APISecret: testSecret, AllowedIPs: "203.0.113.10, 198.51.100.0/24", Enabled: true}
}

func setupAuth(t *testing.T) (*mocks.MockProviderRepository, *mocks.MockNonceStore, *ProviderAuthenticator) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockProviderRepository(ctrl)
	nonces := mocks.NewMockNonceStore(ctrl)
	a := NewProviderAuthenticator(repo, nonces)
	a.now = func() time.Time { return fixedNow }
	return repo, nonces, a
}

// signedRequest 產生一個由合法遊戲商正確簽章的請求，測試再從這裡改出各種攻擊情境
func signedRequest() domain.ProviderAuthRequest {
	ts := strconv.FormatInt(fixedNow.Unix(), 10)
	body := []byte(`{"type":"BET","amount":"100.00"}`)
	return domain.ProviderAuthRequest{
		ProviderID: "DEMO", Timestamp: ts, Nonce: "nonce-1",
		Signature: providersig.Sign(testSecret, ts, "nonce-1", "POST", testPath, body),
		Method:    "POST", Path: testPath, Body: body, ClientIP: "203.0.113.10",
	}
}

func TestAuth_ValidRequest(t *testing.T) {
	repo, nonces, a := setupAuth(t)
	repo.EXPECT().GetByID(gomock.Any(), "DEMO").Return(demoProvider(), nil)
	nonces.EXPECT().MarkUsed(gomock.Any(), "DEMO", "nonce-1", 2*DefaultTimestampWindow).Return(true, nil)

	p, err := a.Authenticate(context.Background(), signedRequest())
	require.NoError(t, err)
	assert.Equal(t, "DEMO", p.ProviderID)
}

func TestAuth_CIDRWhitelist(t *testing.T) {
	tests := []struct {
		ip      string
		allowed bool
	}{
		{"198.51.100.77", true},       // 在 198.51.100.0/24 內
		{"::ffff:203.0.113.10", true}, // IPv4-mapped IPv6 視同 IPv4
		{"198.51.101.1", false},       // 網段外
		{"203.0.113.11", false},       // 只開放 .10
		{"not-an-ip", false},
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			repo, nonces, a := setupAuth(t)
			repo.EXPECT().GetByID(gomock.Any(), "DEMO").Return(demoProvider(), nil)
			if tt.allowed {
				nonces.EXPECT().MarkUsed(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil)
			}
			req := signedRequest()
			req.ClientIP = tt.ip

			_, err := a.Authenticate(context.Background(), req)
			if tt.allowed {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, domain.ErrProviderIPNotAllowed)
			}
		})
	}
}

// 預設空白名單 = 全部拒絕 (Deny by Default)
func TestAuth_EmptyWhitelistDeniesAll(t *testing.T) {
	repo, _, a := setupAuth(t)
	p := demoProvider()
	p.AllowedIPs = ""
	repo.EXPECT().GetByID(gomock.Any(), "DEMO").Return(p, nil)

	_, err := a.Authenticate(context.Background(), signedRequest())
	require.ErrorIs(t, err, domain.ErrProviderIPNotAllowed)
}

// 以下情境都應回傳 ErrProviderUnauthorized，而且 nonce 不能被消耗
func TestAuth_Rejections(t *testing.T) {
	tests := []struct {
		name     string
		provider func() *domain.GameProvider // nil 代表不會查 DB
		notFound bool
		mutate   func(*domain.ProviderAuthRequest)
	}{
		{name: "missing signature header", mutate: func(r *domain.ProviderAuthRequest) { r.Signature = "" }},
		{name: "unknown provider", notFound: true, mutate: func(r *domain.ProviderAuthRequest) {}},
		{name: "disabled provider", provider: func() *domain.GameProvider { p := demoProvider(); p.Enabled = false; return p },
			mutate: func(r *domain.ProviderAuthRequest) {}},
		{name: "timestamp too old (replay of an old capture)", provider: demoProvider,
			mutate: func(r *domain.ProviderAuthRequest) { resign(r, fixedNow.Add(-6*time.Minute)) }},
		{name: "timestamp too far in future", provider: demoProvider,
			mutate: func(r *domain.ProviderAuthRequest) { resign(r, fixedNow.Add(6*time.Minute)) }},
		{name: "malformed timestamp", provider: demoProvider,
			mutate: func(r *domain.ProviderAuthRequest) { r.Timestamp = "yesterday" }},
		{name: "body tampered after signing (amount changed)", provider: demoProvider,
			mutate: func(r *domain.ProviderAuthRequest) { r.Body = []byte(`{"type":"BET","amount":"1.00"}`) }},
		{name: "signed with wrong secret", provider: demoProvider,
			mutate: func(r *domain.ProviderAuthRequest) {
				r.Signature = providersig.Sign("guessed-secret", r.Timestamp, r.Nonce, r.Method, r.Path, r.Body)
			}},
		{name: "signature for another path", provider: demoProvider,
			mutate: func(r *domain.ProviderAuthRequest) { r.Path = "/api/v1/wallet/transaction" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, _, a := setupAuth(t)
			switch {
			case tt.notFound:
				repo.EXPECT().GetByID(gomock.Any(), "DEMO").Return(nil, domain.ErrProviderNotFound)
			case tt.provider != nil:
				repo.EXPECT().GetByID(gomock.Any(), "DEMO").Return(tt.provider(), nil)
			}
			// 未設定 MarkUsed：驗證失敗時若消耗了 nonce，gomock 會讓測試失敗
			req := signedRequest()
			tt.mutate(&req)

			_, err := a.Authenticate(context.Background(), req)
			require.ErrorIs(t, err, domain.ErrProviderUnauthorized)
		})
	}
}

// 時間戳在窗口邊緣 (4 分鐘前) 仍可接受，容許遊戲商與我們的時鐘有些誤差
func TestAuth_TimestampWithinWindow(t *testing.T) {
	repo, nonces, a := setupAuth(t)
	repo.EXPECT().GetByID(gomock.Any(), "DEMO").Return(demoProvider(), nil)
	nonces.EXPECT().MarkUsed(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil)
	req := signedRequest()
	resign(&req, fixedNow.Add(-4*time.Minute))

	_, err := a.Authenticate(context.Background(), req)
	require.NoError(t, err)
}

// 核心案例：完全相同的合法請求被錄下來重送 → 簽章正確，但 nonce 已用過 → 拒絕
func TestAuth_ReplayRejected(t *testing.T) {
	repo, nonces, a := setupAuth(t)
	repo.EXPECT().GetByID(gomock.Any(), "DEMO").Return(demoProvider(), nil)
	gomock.InOrder(
		nonces.EXPECT().MarkUsed(gomock.Any(), "DEMO", "nonce-1", gomock.Any()).Return(true, nil),
		nonces.EXPECT().MarkUsed(gomock.Any(), "DEMO", "nonce-1", gomock.Any()).Return(false, nil),
	)
	req := signedRequest()

	_, err := a.Authenticate(context.Background(), req)
	require.NoError(t, err)

	_, err = a.Authenticate(context.Background(), req)
	require.ErrorIs(t, err, domain.ErrProviderUnauthorized)
	assert.Contains(t, err.Error(), "replay")
}

// 遊戲商資料有快取：快取期間不重查 DB，過期後才重查
func TestAuth_ProviderCached(t *testing.T) {
	repo, nonces, a := setupAuth(t)
	repo.EXPECT().GetByID(gomock.Any(), "DEMO").Return(demoProvider(), nil).Times(2)
	nonces.EXPECT().MarkUsed(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil).Times(3)

	now := fixedNow
	a.now = func() time.Time { return now }
	send := func(nonce string) {
		req := signedRequest()
		req.Nonce = nonce
		req.Timestamp = strconv.FormatInt(now.Unix(), 10)
		req.Signature = providersig.Sign(testSecret, req.Timestamp, nonce, req.Method, req.Path, req.Body)
		_, err := a.Authenticate(context.Background(), req)
		require.NoError(t, err)
	}

	send("n1") // 第 1 次：查 DB
	send("n2") // 快取命中
	now = now.Add(DefaultProviderCacheTTL + time.Second)
	send("n3") // 快取過期：再查 DB
}

// DB 故障不是「驗證失敗」，不能回 401 讓遊戲商以為密鑰錯了
func TestAuth_RepositoryError_NotUnauthorized(t *testing.T) {
	repo, _, a := setupAuth(t)
	repo.EXPECT().GetByID(gomock.Any(), "DEMO").Return(nil, errors.New("db down"))

	_, err := a.Authenticate(context.Background(), signedRequest())
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrProviderUnauthorized)
}

// resign 改用指定時間重新簽章 (模擬遊戲商在那個時間點發出的合法請求)
func resign(r *domain.ProviderAuthRequest, at time.Time) {
	r.Timestamp = strconv.FormatInt(at.Unix(), 10)
	r.Signature = providersig.Sign(testSecret, r.Timestamp, r.Nonce, r.Method, r.Path, r.Body)
}
