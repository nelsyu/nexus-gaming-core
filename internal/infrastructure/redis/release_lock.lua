-- 安全釋放分散式鎖 (Compare-And-Delete)
-- 只有鎖的值仍等於自己的 token 時才刪除。
-- 「比對」與「刪除」必須在同一個 Lua script 內原子執行：
-- 若在 Go 端先 GET 再 DEL，兩個指令之間鎖可能已過期並被別人取得，DEL 就會誤刪別人的鎖。
local lockKey = KEYS[1]
local token   = ARGV[1]

if redis.call('GET', lockKey) == token then
    return redis.call('DEL', lockKey)
end
return 0
