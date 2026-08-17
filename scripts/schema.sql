-- Users 表 (玩家或代理)
CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(50) NOT NULL,
    username VARCHAR(100) NOT NULL,
    currency VARCHAR(10) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(tenant_id, username)
);

-- Wallets 表 (錢包)
CREATE TABLE IF NOT EXISTS wallets (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    balance NUMERIC(20, 4) NOT NULL DEFAULT 0.0000,
    currency VARCHAR(10) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    version INT NOT NULL DEFAULT 1, -- 用於樂觀鎖
    UNIQUE(user_id, currency)
);

-- Transactions 表 (流水帳/交易明細)
CREATE TABLE IF NOT EXISTS transactions (
    id BIGSERIAL PRIMARY KEY,
    wallet_id BIGINT NOT NULL REFERENCES wallets(id),
    type VARCHAR(20) NOT NULL,
    amount NUMERIC(20, 4) NOT NULL,
    balance_before NUMERIC(20, 4) NOT NULL,
    balance_after NUMERIC(20, 4) NOT NULL,
    provider_id VARCHAR(50) NOT NULL,
    provider_tx_id VARCHAR(100) NOT NULL, -- 外部遊戲商的交易序號
    reference_id VARCHAR(100),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    -- 最重要的唯一約束：防止同一個遊戲商的同一筆交易被重複結算（冪等性）
    UNIQUE(provider_id, provider_tx_id)
);

-- 索引優化
CREATE INDEX idx_wallets_user_id ON wallets(user_id);
CREATE INDEX idx_transactions_wallet_id ON transactions(wallet_id);
CREATE INDEX idx_transactions_provider_tx_id ON transactions(provider_tx_id);
