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

-- 建立示範遊戲商 (本機開發用)
-- 允許的 IP：本機 + Docker 預設網段 (從 host 經 port mapping 呼叫時，來源會是 Docker bridge 的 IP)
-- 正式環境務必更換密鑰，並只開放遊戲商實際的伺服器 IP
INSERT INTO game_providers (provider_id, name, api_secret, allowed_ips)
VALUES ('DEMO', 'Demo Gaming', 'demo-secret-change-me', '127.0.0.1,::1,172.16.0.0/12')
ON CONFLICT (provider_id) DO NOTHING;
