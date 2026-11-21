package usecase

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/pkg/providersig"
)

const (
	// DefaultTimestampWindow 請求時間戳允許與伺服器時間相差的範圍 (前後各 5 分鐘，容許時鐘誤差)
	DefaultTimestampWindow = 5 * time.Minute
	// DefaultProviderCacheTTL 遊戲商資料在記憶體中的快取時間。
	// 停用遊戲商或更換密鑰後，最多需要這麼久才會在所有實例生效。
	DefaultProviderCacheTTL = 1 * time.Minute
)

// ProviderAuthenticator 驗證遊戲商請求：身分、來源 IP、時間戳、簽章、重放。
type ProviderAuthenticator struct {
	repo     domain.ProviderRepository
	nonces   domain.NonceStore
	window   time.Duration
	cacheTTL time.Duration
	now      func() time.Time // 可注入，方便測試時間相關邏輯

	mu    sync.RWMutex
	cache map[string]cachedProvider
}

type cachedProvider struct {
	provider  *domain.GameProvider
	allowed   []netip.Prefix
	expiresAt time.Time
}

// NewProviderAuthenticator 建立 ProviderAuthenticator
func NewProviderAuthenticator(repo domain.ProviderRepository, nonces domain.NonceStore) *ProviderAuthenticator {
	return &ProviderAuthenticator{
		repo:     repo,
		nonces:   nonces,
		window:   DefaultTimestampWindow,
		cacheTTL: DefaultProviderCacheTTL,
		now:      time.Now,
		cache:    make(map[string]cachedProvider),
	}
}

// Authenticate 依序驗證，任何一步失敗就停止。順序本身是安全設計：
//
//  1. 欄位齊全
//  2. 遊戲商存在且啟用
//  3. 來源 IP 在白名單內
//  4. 時間戳在允許窗口內
//  5. 簽章正確
//  6. nonce 沒用過 —— 必須在簽章驗證「之後」：
//     若先記錄 nonce，攻擊者不需要密鑰，只要搶先用合法的 nonce 送出亂簽的請求，就能讓真正的請求被當成重放而拒絕。
func (a *ProviderAuthenticator) Authenticate(ctx context.Context, req domain.ProviderAuthRequest) (*domain.GameProvider, error) {
	if req.ProviderID == "" || req.Timestamp == "" || req.Nonce == "" || req.Signature == "" {
		return nil, fmt.Errorf("%w: missing signature headers", domain.ErrProviderUnauthorized)
	}

	entry, err := a.lookup(ctx, req.ProviderID)
	if err != nil {
		if errors.Is(err, domain.ErrProviderNotFound) {
			return nil, fmt.Errorf("%w: unknown provider %q", domain.ErrProviderUnauthorized, req.ProviderID)
		}
		return nil, err // DB 錯誤：不是驗證失敗，交給上層回 500
	}
	if !entry.provider.Enabled {
		return nil, fmt.Errorf("%w: provider %q is disabled", domain.ErrProviderUnauthorized, req.ProviderID)
	}

	if !ipAllowed(req.ClientIP, entry.allowed) {
		return nil, fmt.Errorf("%w: %s not in whitelist of %q", domain.ErrProviderIPNotAllowed, req.ClientIP, req.ProviderID)
	}

	if err := a.checkTimestamp(req.Timestamp); err != nil {
		return nil, err
	}

	if !providersig.Verify(entry.provider.APISecret, req.Timestamp, req.Nonce, req.Method, req.Path, req.Body, req.Signature) {
		return nil, fmt.Errorf("%w: invalid signature", domain.ErrProviderUnauthorized)
	}

	// TTL 取兩倍窗口：時間戳可能落在「過去 5 分鐘到未來 5 分鐘」，nonce 至少要記到它過期為止
	first, err := a.nonces.MarkUsed(ctx, req.ProviderID, req.Nonce, 2*a.window)
	if err != nil {
		return nil, err
	}
	if !first {
		return nil, fmt.Errorf("%w: nonce already used (replay)", domain.ErrProviderUnauthorized)
	}

	return entry.provider, nil
}

func (a *ProviderAuthenticator) checkTimestamp(raw string) error {
	sec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: malformed timestamp", domain.ErrProviderUnauthorized)
	}
	skew := a.now().Sub(time.Unix(sec, 0))
	if skew > a.window || skew < -a.window {
		return fmt.Errorf("%w: timestamp outside allowed window (skew %s)", domain.ErrProviderUnauthorized, skew.Round(time.Second))
	}
	return nil
}

// lookup 先查記憶體快取，過期才查 DB。白名單在快取時就先解析好，避免每個請求重新解析字串。
func (a *ProviderAuthenticator) lookup(ctx context.Context, providerID string) (cachedProvider, error) {
	now := a.now()

	a.mu.RLock()
	entry, ok := a.cache[providerID]
	a.mu.RUnlock()
	if ok && now.Before(entry.expiresAt) {
		return entry, nil
	}

	p, err := a.repo.GetByID(ctx, providerID)
	if err != nil {
		return cachedProvider{}, err
	}
	entry = cachedProvider{provider: p, allowed: parseAllowedIPs(p.AllowedIPs), expiresAt: now.Add(a.cacheTTL)}

	a.mu.Lock()
	a.cache[providerID] = entry
	a.mu.Unlock()
	return entry, nil
}

// parseAllowedIPs 解析逗號分隔的 IP / CIDR。無法解析的項目會被略過 (寧可少放行，不要誤放行)。
func parseAllowedIPs(raw string) []netip.Prefix {
	var prefixes []netip.Prefix
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			if p, err := netip.ParsePrefix(item); err == nil {
				prefixes = append(prefixes, p.Masked())
			}
			continue
		}
		if addr, err := netip.ParseAddr(item); err == nil {
			prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen())) // 單一 IP 視為 /32 或 /128
		}
	}
	return prefixes
}

func ipAllowed(clientIP string, allowed []netip.Prefix) bool {
	addr, err := netip.ParseAddr(clientIP)
	if err != nil {
		return false
	}
	addr = addr.Unmap() // "::ffff:1.2.3.4" (IPv4-mapped IPv6) 視為 1.2.3.4
	for _, p := range allowed {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
