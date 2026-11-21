package http

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/pkg/logger"
	"github.com/bosstest/nexus-core/pkg/providersig"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// CtxProviderID 驗證通過的遊戲商 ID
const CtxProviderID = "auth_provider_id"

// maxProviderBodyBytes 遊戲商請求 body 上限。驗簽需要先把整個 body 讀進記憶體，
// 不設上限的話，攻擊者送一個超大 body 就能耗盡伺服器記憶體。
const maxProviderBodyBytes = 1 << 20 // 1 MiB

// ProviderAuthMiddleware 以 HMAC 簽章驗證遊戲商請求。
//
// 簽章涵蓋 body，所以必須先讀出 body 驗簽，再把它放回去讓後面的 handler 照常解析 JSON。
// 簽章使用的 path 為 URL.Path (不含 query string)。
func ProviderAuthMiddleware(auth domain.ProviderAuthenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxProviderBodyBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
				return
			}
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		provider, err := auth.Authenticate(c.Request.Context(), domain.ProviderAuthRequest{
			ProviderID: c.GetHeader(providersig.HeaderProviderID),
			Timestamp:  c.GetHeader(providersig.HeaderTimestamp),
			Nonce:      c.GetHeader(providersig.HeaderNonce),
			Signature:  c.GetHeader(providersig.HeaderSignature),
			Method:     c.Request.Method,
			Path:       c.Request.URL.Path,
			Body:       body,
			ClientIP:   c.ClientIP(), // 是否採信 X-Forwarded-For 由 router 的 TrustedProxies 決定
		})
		if err != nil {
			// 詳細原因只寫進日誌；對外回應固定訊息，不給攻擊者線索
			logger.WithTraceID(c.Request.Context()).Warn("Provider authentication rejected",
				zap.String("provider_id", c.GetHeader(providersig.HeaderProviderID)),
				zap.String("client_ip", c.ClientIP()),
				zap.Error(err),
			)
			switch {
			case errors.Is(err, domain.ErrProviderUnauthorized):
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "provider authentication failed"})
			case errors.Is(err, domain.ErrProviderIPNotAllowed):
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			default:
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
			}
			return
		}

		c.Set(CtxProviderID, provider.ProviderID)
		c.Next()
	}
}
