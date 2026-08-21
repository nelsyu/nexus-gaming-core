local txLockKey = KEYS[1]
local walletKey = KEYS[2]
local amount = tonumber(ARGV[1])

-- 檢查錢包是否存在於 Redis
local currentBalanceStr = redis.call('HGET', walletKey, 'balance')
if currentBalanceStr then
    -- 加回金額
    redis.call('HINCRBYFLOAT', walletKey, 'balance', amount)
end

-- 無論如何都移除防重鎖
redis.call('DEL', txLockKey)

return 0
