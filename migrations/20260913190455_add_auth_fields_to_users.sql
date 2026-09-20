-- +goose Up
-- 為 Users 表擴充認證與角色權限欄位
ALTER TABLE users
ADD COLUMN IF NOT EXISTS password_hash VARCHAR(255) NOT NULL DEFAULT '',
ADD COLUMN IF NOT EXISTS role VARCHAR(30) NOT NULL DEFAULT 'player';

CREATE INDEX IF NOT EXISTS idx_users_tenant_role ON users(tenant_id, role);

-- +goose Down
DROP INDEX IF EXISTS idx_users_tenant_role;

ALTER TABLE users
DROP COLUMN IF EXISTS role,
DROP COLUMN IF EXISTS password_hash;
