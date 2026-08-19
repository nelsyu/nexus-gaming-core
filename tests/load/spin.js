import http from 'k6/http';
import { check, sleep } from 'k6';

// 壓力測試設定：模擬 50 個使用者，持續打 10 秒
export const options = {
  vus: 50,
  duration: '10s',
};

// 如果沒有指定環境變數 API_URL，則預設使用 localhost
const baseURL = __ENV.API_URL || 'http://localhost:8080';

export default function () {
  const url = `${baseURL}/api/v1/wallet/transaction`;

  // 構造每一次請求的唯一 ID (使用 VU 編號 + 迴圈編號組合，確保不重複，測試併發扣款)
  // 如果你想測試「冪等性防重」，可以把這個 ID 寫死成同一個字串
  const providerTxId = `k6_tx_${__VU}_${__ITER}`;

  const payload = JSON.stringify({
    user_id: 1, // 測試玩家 1 號
    currency: 'TWD',
    type: 'BET',
    amount: '1.00', // 每次旋轉扣 1 元
    provider_id: 'K6_LOAD_TEST',
    provider_tx_id: providerTxId,
    reference_id: 'round_k6_001',
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
    },
  };

  const res = http.post(url, payload, params);

  // 驗證 API 回應
  // 我們預期 200 (扣款成功) 或 402 (餘額不足)，只要不是 500 都算系統正確阻擋
  check(res, {
    'is status 200 (Success) or 402 (Insufficient Funds)': (r) => r.status === 200 || r.status === 402,
    'is not 500 (Internal Error)': (r) => r.status !== 500,
  });

  // 短暫休息，模擬真實機台 Spin 的間隔，如果不加 sleep 就是無限極限狂打
  sleep(0.1); 
}
