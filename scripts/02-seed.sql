-- 建立測試玩家
INSERT INTO users (tenant_id, username, currency)
VALUES ('TENANT_A', 'testplayer01', 'TWD')
ON CONFLICT DO NOTHING;

-- 建立測試錢包，預設給予 1000 元餘額測試
INSERT INTO wallets (user_id, balance, currency, version)
SELECT id, 1000.0000, 'TWD', 1
FROM users
WHERE username = 'testplayer01'
ON CONFLICT (user_id, currency) DO NOTHING;
