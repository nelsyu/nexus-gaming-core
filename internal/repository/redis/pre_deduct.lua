local txLockKey = KEYS[1]
local walletKey = KEYS[2]
local amount = tonumber(ARGV[2])

-- 1. 檢查防重鎖 (Idempotency)
-- SETNX 如果回傳 1 代表鎖成功(交易沒處理過)，如果回傳 0 代表鎖失敗(已經處理過)
local lockAcquired = redis.call('SETNX', txLockKey, '1')
if lockAcquired == 0 then
    return 1 -- 重複交易
end
-- 給鎖設定 5 分鐘過期時間，避免死鎖
redis.call('EXPIRE', txLockKey, 300)

-- 2. 檢查錢包是否存在於 Redis
local currentBalanceStr = redis.call('HGET', walletKey, 'balance')
if not currentBalanceStr then
    -- Redis 沒有快取，通知 Go 去查 DB。
    -- 注意：這裡刻意「不刪除」防重鎖。
    -- 鎖的存在確保了第 2 次使用相同 TxID 的請求，無論走 Cache Hit 或 Cache Miss 路徑，
    -- 都會被擋在這裡，直到 Worker 把鎖清除（DB 成功後鎖留著直到過期即可）。
    return 3 -- 快取未擊中 (Cache Miss)，鎖已持有
end

-- 3. 檢查餘額
local currentBalance = tonumber(currentBalanceStr)
if currentBalance < amount then
    -- 餘額不足，也要解鎖，因為這筆交易實質上沒有成功，未來如果有補款可能可以重試
	-- 但實務上 providerTxID 可能是唯一的，失敗就不該重試，這邊可以依據商業邏輯決定要不要 DEL
    return 2 -- 餘額不足
end

-- 4. 執行扣款
redis.call('HINCRBYFLOAT', walletKey, 'balance', -amount)

return 0 -- 成功
