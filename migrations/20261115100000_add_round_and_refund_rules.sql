-- +goose Up

-- round_id：同一局遊戲的識別碼。BET 與 WIN 必須屬於同一局，用來防止「沒下注就派彩」。
-- related_provider_tx_id：REFUND 指向的原始 BET 交易序號。
ALTER TABLE transactions
    ADD COLUMN IF NOT EXISTS round_id VARCHAR(100),
    ADD COLUMN IF NOT EXISTS related_provider_tx_id VARCHAR(100);

-- 一筆 BET 只能被退款一次：由 DB 保證，即使程式有併發漏洞也不會重複退款。
-- Partial index (WHERE type = 'REFUND') 只約束退款紀錄，其他類型不受影響。
CREATE UNIQUE INDEX IF NOT EXISTS uq_transactions_refund_target
    ON transactions (provider_id, related_provider_tx_id)
    WHERE type = 'REFUND';

-- 查詢「這一局有沒有下注 / 派彩」用
CREATE INDEX IF NOT EXISTS idx_transactions_round
    ON transactions (provider_id, round_id, wallet_id)
    WHERE round_id IS NOT NULL;

-- 作廢注單 (tombstone)：退款比下注先到時，先記下「這筆 BET 已作廢」，
-- 之後 BET 才到時直接拒絕，避免「先退款、後扣款」導致玩家平白多拿一筆錢。
CREATE TABLE IF NOT EXISTS voided_bets (
    provider_id              VARCHAR(50)  NOT NULL,
    provider_tx_id           VARCHAR(100) NOT NULL, -- 被作廢的 BET 交易序號
    voided_by_provider_tx_id VARCHAR(100) NOT NULL, -- 造成作廢的 REFUND 交易序號
    wallet_id                BIGINT       NOT NULL REFERENCES wallets(id),
    created_at               TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (provider_id, provider_tx_id)
);

-- +goose Down
DROP TABLE IF EXISTS voided_bets;
DROP INDEX IF EXISTS idx_transactions_round;
DROP INDEX IF EXISTS uq_transactions_refund_target;
ALTER TABLE transactions
    DROP COLUMN IF EXISTS related_provider_tx_id,
    DROP COLUMN IF EXISTS round_id;
