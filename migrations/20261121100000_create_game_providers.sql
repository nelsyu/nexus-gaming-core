-- +goose Up

-- 遊戲商 (呼叫錢包 API 的外部系統) 的驗證資料
CREATE TABLE IF NOT EXISTS game_providers (
    provider_id VARCHAR(50)  PRIMARY KEY,
    name        VARCHAR(100) NOT NULL,
    -- HMAC 共享密鑰。伺服器必須能取得原文才能重算簽章，因此不能像密碼一樣只存雜湊。
    -- 示範專案以明文儲存；正式環境應放在 Secrets Manager / Vault，或以 KMS 加密後存放。
    api_secret  VARCHAR(255) NOT NULL,
    -- 允許的來源 IP 或 CIDR，以逗號分隔 (例如 '203.0.113.10,198.51.100.0/24')。
    -- 預設空字串 = 全部拒絕 (Deny by Default)：忘了設定時寧可擋住，也不要誤放行。
    allowed_ips TEXT         NOT NULL DEFAULT '',
    enabled     BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE IF EXISTS game_providers;
