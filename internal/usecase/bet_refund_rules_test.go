package usecase

import (
	"context"
	"testing"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/internal/domain/mocks"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// runTxFn 讓 UoW mock 真的執行交易閉包；閉包回傳錯誤時視同 rollback
func runTxFn(mockUoW *mocks.MockUnitOfWork) {
	mockUoW.EXPECT().DoTx(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
		return fn(ctx)
	})
}

// expectLockWallet 錢包 (id=10, user=1) 被鎖定，餘額為 balance
func expectLockWallet(mockWalletRepo *mocks.MockWalletRepository, balance int64) *domain.Wallet {
	w := &domain.Wallet{ID: 10, UserID: 1, Currency: "TWD", Balance: decimal.NewFromInt(balance)}
	mockWalletRepo.EXPECT().GetWalletByUserID(gomock.Any(), int64(1), "TWD").Return(w, nil)
	mockWalletRepo.EXPECT().GetWalletByIDForUpdate(gomock.Any(), int64(10)).Return(w, nil)
	return w
}

func refundReq(amount int64) *domain.TransactionRequest {
	return &domain.TransactionRequest{
		UserID: 1, Currency: "TWD", Type: domain.TxTypeRefund, Amount: decimal.NewFromInt(amount),
		ProviderID: "pg", ProviderTxID: "refund-1", RelatedProviderTxID: "bet-1",
	}
}

func originalBet() *domain.Transaction {
	return &domain.Transaction{
		ID: 5, WalletID: 10, Type: domain.TxTypeBet, Amount: decimal.NewFromInt(100),
		ProviderID: "pg", ProviderTxID: "bet-1", RoundID: "round-1",
	}
}

// ───────────── 必填欄位 ─────────────

func TestRules_BetAndWinRequireRoundID(t *testing.T) {
	for _, typ := range []domain.TransactionType{domain.TxTypeBet, domain.TxTypeWin} {
		t.Run(string(typ), func(t *testing.T) {
			_, _, _, _, _, uc := setupTest(t)
			req := &domain.TransactionRequest{UserID: 1, Currency: "TWD", Type: typ, Amount: decimal.NewFromInt(10)}

			_, err := uc.ProcessTransaction(context.Background(), req)
			require.ErrorIs(t, err, domain.ErrRoundIDRequired)
		})
	}
}

func TestRules_RefundRequiresTarget(t *testing.T) {
	_, _, _, _, _, uc := setupTest(t)
	req := refundReq(100)
	req.RelatedProviderTxID = ""

	_, err := uc.ProcessTransaction(context.Background(), req)
	require.ErrorIs(t, err, domain.ErrRefundTargetRequired)
}

// ───────────── BET / WIN ─────────────

// 退款先到過 (tombstone)，之後 BET 才到：拒絕扣款，並刪除 Redis 已預扣的快取
func TestRules_VoidedBetRejected(t *testing.T) {
	mockWalletRepo, mockTxRepo, mockCache, _, mockUoW, uc := setupTest(t)
	req := &domain.TransactionRequest{UserID: 1, Currency: "TWD", Type: domain.TxTypeBet, Amount: decimal.NewFromInt(100),
		ProviderID: "pg", ProviderTxID: "bet-1", RoundID: "round-1"}

	mockCache.EXPECT().PreProcess(gomock.Any(), domain.OpDebit, "pg", "bet-1", int64(1), "TWD", int64(1_000_000)).Return(domain.ResultSuccess, nil)
	runTxFn(mockUoW)
	expectLockWallet(mockWalletRepo, 500)
	mockTxRepo.EXPECT().IsBetVoided(gomock.Any(), "pg", "bet-1").Return(true, nil)
	mockCache.EXPECT().Invalidate(gomock.Any(), int64(1), "TWD", "pg", "bet-1").Return(nil)

	_, err := uc.ProcessTransaction(context.Background(), req)
	require.ErrorIs(t, err, domain.ErrBetVoided)
}

// 沒下注就派彩：拒絕，並刪除 Redis 已預加的快取
func TestRules_WinWithoutActiveBetRejected(t *testing.T) {
	mockWalletRepo, mockTxRepo, mockCache, _, mockUoW, uc := setupTest(t)
	req := &domain.TransactionRequest{UserID: 1, Currency: "TWD", Type: domain.TxTypeWin, Amount: decimal.NewFromInt(500),
		ProviderID: "pg", ProviderTxID: "win-1", RoundID: "round-x"}

	mockCache.EXPECT().PreProcess(gomock.Any(), domain.OpCredit, gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultSuccess, nil)
	runTxFn(mockUoW)
	expectLockWallet(mockWalletRepo, 0)
	mockTxRepo.EXPECT().HasActiveBetInRound(gomock.Any(), int64(10), "pg", "round-x").Return(false, nil)
	mockCache.EXPECT().Invalidate(gomock.Any(), int64(1), "TWD", "pg", "win-1").Return(nil)

	_, err := uc.ProcessTransaction(context.Background(), req)
	require.ErrorIs(t, err, domain.ErrWinWithoutBet)
}

func TestRules_WinWithActiveBetSucceeds(t *testing.T) {
	mockWalletRepo, mockTxRepo, mockCache, mockPub, mockUoW, uc := setupTest(t)
	req := &domain.TransactionRequest{UserID: 1, Currency: "TWD", Type: domain.TxTypeWin, Amount: decimal.NewFromInt(50),
		ProviderID: "pg", ProviderTxID: "win-1", RoundID: "round-1"}

	mockCache.EXPECT().PreProcess(gomock.Any(), domain.OpCredit, gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultSuccess, nil)
	runTxFn(mockUoW)
	expectLockWallet(mockWalletRepo, 100)
	mockTxRepo.EXPECT().HasActiveBetInRound(gomock.Any(), int64(10), "pg", "round-1").Return(true, nil)
	mockTxRepo.EXPECT().CreateTransaction(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, tx *domain.Transaction) error {
		assert.Equal(t, "round-1", tx.RoundID)
		assert.Equal(t, "150", tx.BalanceAfter.String())
		return nil
	})
	mockWalletRepo.EXPECT().UpdateBalance(gomock.Any(), gomock.Any()).Return(nil)
	mockPub.EXPECT().PublishTransactionCompleted(gomock.Any(), gomock.Any()).Return(nil)

	_, err := uc.ProcessTransaction(context.Background(), req)
	require.NoError(t, err)
}

// ───────────── REFUND ─────────────

// 核心案例：退款金額以原注單為準，不採信請求金額；不走 Redis 預處理，成功後刪除快取
func TestRefund_UsesOriginalBetAmount(t *testing.T) {
	mockWalletRepo, mockTxRepo, mockCache, mockPub, mockUoW, uc := setupTest(t)

	runTxFn(mockUoW)
	expectLockWallet(mockWalletRepo, 400)
	mockTxRepo.EXPECT().GetByProviderTxID(gomock.Any(), "pg", "bet-1").Return(originalBet(), nil)
	mockTxRepo.EXPECT().HasWinInRound(gomock.Any(), int64(10), "pg", "round-1").Return(false, nil)
	mockTxRepo.EXPECT().CreateTransaction(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, tx *domain.Transaction) error {
		assert.Equal(t, domain.TxTypeRefund, tx.Type)
		assert.Equal(t, "100", tx.Amount.String(), "must refund the original bet amount, not the requested 999")
		assert.Equal(t, "500", tx.BalanceAfter.String())
		assert.Equal(t, "bet-1", tx.RelatedProviderTxID)
		assert.Equal(t, "round-1", tx.RoundID)
		return nil
	})
	mockWalletRepo.EXPECT().UpdateBalance(gomock.Any(), gomock.Any()).Return(nil)
	mockCache.EXPECT().Invalidate(gomock.Any(), int64(1), "TWD", "pg", "refund-1").Return(nil)
	mockPub.EXPECT().PublishTransactionCompleted(gomock.Any(), gomock.Any()).Return(nil)
	// 沒有設定 PreProcess：若退款走了 Redis 預處理，gomock 會讓測試失敗

	tx, err := uc.ProcessTransaction(context.Background(), refundReq(999))
	require.NoError(t, err)
	assert.Equal(t, "100", tx.Amount.String())
}

// 退款比下注先到：記錄作廢並回報找不到原注單；餘額沒變，不刪快取也不發事件
func TestRefund_BetNotFound_VoidsBet(t *testing.T) {
	mockWalletRepo, mockTxRepo, _, _, mockUoW, uc := setupTest(t)

	runTxFn(mockUoW)
	expectLockWallet(mockWalletRepo, 400)
	mockTxRepo.EXPECT().GetByProviderTxID(gomock.Any(), "pg", "bet-1").Return(nil, nil)
	mockTxRepo.EXPECT().VoidBet(gomock.Any(), int64(10), "pg", "bet-1", "refund-1").Return(nil)

	_, err := uc.ProcessTransaction(context.Background(), refundReq(100))
	require.ErrorIs(t, err, domain.ErrRefundTargetNotFound)
}

func TestRefund_InvalidTarget(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.Transaction)
	}{
		{name: "target is a WIN, not a BET", mutate: func(tx *domain.Transaction) { tx.Type = domain.TxTypeWin }},
		{name: "target belongs to another wallet", mutate: func(tx *domain.Transaction) { tx.WalletID = 99 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockWalletRepo, mockTxRepo, _, _, mockUoW, uc := setupTest(t)
			target := originalBet()
			tt.mutate(target)

			runTxFn(mockUoW)
			expectLockWallet(mockWalletRepo, 400)
			mockTxRepo.EXPECT().GetByProviderTxID(gomock.Any(), "pg", "bet-1").Return(target, nil)

			_, err := uc.ProcessTransaction(context.Background(), refundReq(100))
			require.ErrorIs(t, err, domain.ErrInvalidRefundTarget)
		})
	}
}

// 已派彩的注單不能再退款，否則玩家同時拿到派彩和退款
func TestRefund_AlreadySettledRejected(t *testing.T) {
	mockWalletRepo, mockTxRepo, _, _, mockUoW, uc := setupTest(t)

	runTxFn(mockUoW)
	expectLockWallet(mockWalletRepo, 400)
	mockTxRepo.EXPECT().GetByProviderTxID(gomock.Any(), "pg", "bet-1").Return(originalBet(), nil)
	mockTxRepo.EXPECT().HasWinInRound(gomock.Any(), int64(10), "pg", "round-1").Return(true, nil)

	_, err := uc.ProcessTransaction(context.Background(), refundReq(100))
	require.ErrorIs(t, err, domain.ErrBetAlreadySettled)
}

// 同一筆 BET 被另一個退款請求 (不同序號) 再退一次：回傳第一次的退款，不重複加錢
func TestRefund_BetAlreadyRefunded_ReturnsFirstRefund(t *testing.T) {
	mockWalletRepo, mockTxRepo, _, _, mockUoW, uc := setupTest(t)

	runTxFn(mockUoW)
	expectLockWallet(mockWalletRepo, 500)
	mockTxRepo.EXPECT().GetByProviderTxID(gomock.Any(), "pg", "bet-1").Return(originalBet(), nil)
	mockTxRepo.EXPECT().HasWinInRound(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(false, nil)
	mockTxRepo.EXPECT().CreateTransaction(gomock.Any(), gomock.Any()).Return(domain.ErrBetAlreadyRefunded)

	first := &domain.Transaction{ID: 77, Type: domain.TxTypeRefund, ProviderTxID: "refund-0"}
	mockTxRepo.EXPECT().GetRefundByTarget(gomock.Any(), "pg", "bet-1").Return(first, nil)

	tx, err := uc.ProcessTransaction(context.Background(), refundReq(100))
	require.NoError(t, err)
	assert.Equal(t, first, tx)
}

// 同一個退款請求 (相同序號) 重送：回傳第一次的結果
func TestRefund_SameRequestRetried_ReturnsExisting(t *testing.T) {
	mockWalletRepo, mockTxRepo, _, _, mockUoW, uc := setupTest(t)

	runTxFn(mockUoW)
	expectLockWallet(mockWalletRepo, 500)
	mockTxRepo.EXPECT().GetByProviderTxID(gomock.Any(), "pg", "bet-1").Return(originalBet(), nil)
	mockTxRepo.EXPECT().HasWinInRound(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(false, nil)
	mockTxRepo.EXPECT().CreateTransaction(gomock.Any(), gomock.Any()).Return(domain.ErrDuplicateTransaction)

	existing := &domain.Transaction{ID: 78, Type: domain.TxTypeRefund, ProviderTxID: "refund-1"}
	mockTxRepo.EXPECT().GetByProviderTxID(gomock.Any(), "pg", "refund-1").Return(existing, nil)

	tx, err := uc.ProcessTransaction(context.Background(), refundReq(100))
	require.NoError(t, err)
	assert.Equal(t, existing, tx)
}
