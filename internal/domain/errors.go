package domain

import "errors"

var (
	ErrUserNotFound        = errors.New("user not found")
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrInsufficientFunds   = errors.New("insufficient funds")
	ErrDuplicateTransaction = errors.New("duplicate transaction") // 冪等性防重複
	ErrOptimisticLock       = errors.New("optimistic lock error") // 併發更新衝突
	ErrCommitFailed         = errors.New("commit transaction failed")
	ErrUserAlreadyExists    = errors.New("user already exists")
	ErrInvalidCredentials   = errors.New("invalid username or password")
	ErrInvalidToken         = errors.New("invalid token")
	ErrTokenExpired         = errors.New("token has expired")
	ErrTokenRevoked         = errors.New("token has been revoked")
	ErrUnauthorized         = errors.New("unauthorized")
	ErrForbidden            = errors.New("forbidden")
	ErrInvalidAmount        = errors.New("invalid amount: must be greater than zero")

	// 注單與退款規則
	ErrRoundIDRequired       = errors.New("round_id is required for BET and WIN")
	ErrRefundTargetRequired  = errors.New("related_provider_tx_id is required for REFUND")
	ErrRefundTargetNotFound  = errors.New("original bet not found; bet has been voided") // 退款先到：已記錄作廢，之後的 BET 會被拒絕
	ErrInvalidRefundTarget   = errors.New("refund target is not a BET of this wallet")
	ErrBetAlreadyRefunded    = errors.New("bet has already been refunded")
	ErrBetAlreadySettled     = errors.New("bet has already been settled; cannot refund")
	ErrBetVoided             = errors.New("bet has been voided by an earlier refund")
	ErrWinWithoutBet         = errors.New("no active bet found in this round")
)
