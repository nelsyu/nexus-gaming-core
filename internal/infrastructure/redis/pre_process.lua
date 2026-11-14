local txLockKey = KEYS[1]
local walletKey = KEYS[2]
local op          = ARGV[1]  -- "DEBIT" 或 "CREDIT"
local amountStr   = ARGV[2]  -- 整數最小單位 (例如 10.5 元 = "105000")

-- 1. 檢查防重鎖 (Idempotency)：SET NX EX 一個指令完成「上鎖 + 設定過期」
-- 鎖失敗代表這筆交易已處理過或正在處理
if not redis.call('SET', txLockKey, '1', 'NX', 'EX', 300) then
    return 1 -- 重複交易
end

-- 2. 檢查錢包是否存在於 Redis
local currentBalanceStr = redis.call('HGET', walletKey, 'balance')
if not currentBalanceStr then
    -- 快取未擊中 (Cache Miss)，主動刪除防重鎖。
    -- 讓 Go 層在等待快取重建完成後，可以進行一次乾淨的 PreProcess 重試。
    redis.call('DEL', txLockKey)
    return 3
end

-- 3. 根據操作類型執行對應的餘額變更
-- 注意：HINCRBY 的參數直接傳字串，不要把 Lua number 傳回 Redis。
-- Redis 的 Lua 會用 "%.14g" 格式化數字，大金額會變成 "1e+15" 導致 HINCRBY 失敗。
if op == 'DEBIT' then
    -- 扣款：需要先驗證餘額是否足夠 (tonumber 僅用於比較)
    if tonumber(currentBalanceStr) < tonumber(amountStr) then
        -- 餘額不足：鎖保留 (等 TTL 自然過期)，此筆 provider_tx_id 視為失敗不重試
        return 2 -- 餘額不足
    end
    redis.call('HINCRBY', walletKey, 'balance', '-' .. amountStr)
else
    -- CREDIT (WIN/Deposit/Refund)：直接加款，不需要檢查餘額
    redis.call('HINCRBY', walletKey, 'balance', amountStr)
end

return 0 -- 成功
