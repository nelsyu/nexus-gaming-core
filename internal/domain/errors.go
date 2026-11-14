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
)
