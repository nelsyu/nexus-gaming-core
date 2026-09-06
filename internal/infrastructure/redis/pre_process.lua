local txLockKey = KEYS[1]
local walletKey = KEYS[2]
local op     = ARGV[1]  -- "DEBIT" 或 "CREDIT"
local amount = tonumber(ARGV[2])

-- 1. 檢查防重鎖 (Idempotency)
-- SETNX 回傳 1 代表鎖成功 (交易第一次處理)，回傳 0 代表已處理過
local lockAcquired = redis.call('SETNX', txLockKey, '1')
if lockAcquired == 0 then
    return 1 -- 重複交易
end
-- 給鎖設定 5 分鐘過期時間，避免死鎖
redis.call('EXPIRE', txLockKey, 300)

-- 2. 檢查錢包是否存在於 Redis
local currentBalanceStr = redis.call('HGET', walletKey, 'balance')
if not currentBalanceStr then
    -- 快取未擊中 (Cache Miss)，主動刪除防重鎖。
    -- 讓 Go 層在等待快取重建完成後，可以進行一次乾淨的 PreProcess 重試。
    redis.call('DEL', txLockKey)
    return 3 
end

-- 3. 根據操作類型執行對應的餘額變更
local currentBalance = tonumber(currentBalanceStr)

if op == 'DEBIT' then
    -- 扣款：需要先驗證餘額是否足夠
    if currentBalance < amount then
        -- 餘額不足：鎖保留 (等 TTL 自然過期)，此筆 provider_tx_id 視為失敗不重試
        return 2 -- 餘額不足
    end
    redis.call('HINCRBYFLOAT', walletKey, 'balance', -amount)
else
    -- CREDIT (WIN/Deposit/Refund)：直接加款，不需要檢查餘額
    redis.call('HINCRBYFLOAT', walletKey, 'balance', amount)
end

return 0 -- 成功
