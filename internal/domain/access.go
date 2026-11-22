package domain

import (
	"context"
	"strings"
	"unicode/utf8"
)

// Actor 發起操作的人。所有欄位都只能來自驗證過的 token，不能來自請求內容。
type Actor struct {
	UserID   int64
	TenantID string
	Role     Role
}

// WalletAuthorizer 決定「誰能對誰的錢包做什麼」。
// 不允許時回傳 ErrForbidden；目標不存在或屬於其他租戶時，一律回傳 ErrWalletNotFound。
type WalletAuthorizer interface {
	AuthorizeRead(ctx context.Context, actor Actor, targetUserID int64) error
	AuthorizeAdjust(ctx context.Context, actor Actor, targetUserID int64) error
}

const (
	// '#' 不在遊戲商 provider_id 允許的字元內 (alphanum_underscore)，所以遊戲商無法使用這個名稱
	backofficeProviderPrefix = "BACKOFFICE#"
	// transactions.provider_id 是 VARCHAR(50)
	maxProviderIDLength = 50
)

// BackofficeProviderID 後台人工交易使用的 provider_id。
//
// 冪等鍵是 (provider_id, provider_tx_id)：
//   - 不能由呼叫者指定，否則後台可以先占用遊戲商的交易序號，讓之後真正的下注「回成功但沒扣款」
//   - 每個租戶各自獨立，否則兩個租戶用到相同序號時會互相撞號
//
// 租戶含有 ':' (Redis key 的分隔符號) 或長度超出欄位時拒絕，不做截斷：截斷會讓不同租戶撞在一起。
func BackofficeProviderID(tenantID string) (string, error) {
	id := backofficeProviderPrefix + tenantID
	if tenantID == "" || strings.Contains(tenantID, ":") || utf8.RuneCountInString(id) > maxProviderIDLength {
		return "", ErrForbidden
	}
	return id, nil
}
