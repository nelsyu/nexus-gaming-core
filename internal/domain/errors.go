package domain

import "errors"

var (
	ErrUserNotFound        = errors.New("user not found")
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrInsufficientFunds   = errors.New("insufficient funds")
	ErrDuplicateTransaction = errors.New("duplicate transaction") // 冪等性防重複
	ErrOptimisticLock      = errors.New("optimistic lock error")  // 併發更新衝突
)
