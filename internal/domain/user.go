package domain

import (
	"context"
	"time"
)

type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RolePlayer   Role = "player"
)

// User 代表租戶下的使用者（玩家、營運人員或系統管理員）
type User struct {
	ID           int64     `json:"id" db:"id"`
	TenantID     string    `json:"tenant_id" db:"tenant_id"`
	Username     string    `json:"username" db:"username"`
	PasswordHash string    `json:"-" db:"password_hash"`
	Role         Role      `json:"role" db:"role"`
	Currency     string    `json:"currency" db:"currency"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" db:"updated_at"`
}

// UserRepository 定義使用者資料存取介面
type UserRepository interface {
	GetByID(ctx context.Context, id int64) (*User, error)
	GetByUsername(ctx context.Context, tenantID, username string) (*User, error)
	CreateUser(ctx context.Context, user *User) error
}
