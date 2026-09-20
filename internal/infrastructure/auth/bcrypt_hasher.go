package auth

import (
	"golang.org/x/crypto/bcrypt"
)

// BcryptHasher 使用 bcrypt 演算法進行密碼雜湊與比對
type BcryptHasher struct {
	cost int
}

func NewBcryptHasher(cost int) *BcryptHasher {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		cost = bcrypt.DefaultCost
	}
	return &BcryptHasher{cost: cost}
}

// HashPassword 產生帶有隨機 Salt 的不可逆密碼雜湊
func (h *BcryptHasher) HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), h.cost)
	return string(bytes), err
}

// ComparePassword 比對明文密碼與雜湊值
func (h *BcryptHasher) ComparePassword(hashedPassword, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
}
