package http

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/pkg/logger"
	"github.com/bosstest/nexus-core/pkg/metrics"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	CtxUserID   = "auth_user_id"
	CtxTenantID = "auth_tenant_id"
	CtxRole     = "auth_role"
	CtxUsername = "auth_username"
	CtxTokenID  = "auth_token_id" // Access Token 的 jti，供 RevocationCheckMiddleware 使用
)

// TraceMiddleware 為每個請求產生一個 TraceID，並注入到 context 中
func TraceMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		traceID := c.GetHeader("X-Trace-ID")
		if traceID == "" {
			traceID = uuid.New().String()
		}

		// 注入到 Gin context 供日誌紀錄使用
		c.Set(string(logger.TraceIDKey), traceID)

		// 同時注入到標準的 context.Context，以便傳遞給 Usecase
		ctx := context.WithValue(c.Request.Context(), logger.TraceIDKey, traceID)
		c.Request = c.Request.WithContext(ctx)

		// 在 Response Header 中回傳 TraceID
		c.Header("X-Trace-ID", traceID)

		c.Next()
	}
}

// LoggerMiddleware 使用 Zap 紀錄進入的 HTTP 請求
func LoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		duration := time.Since(start)
		log := logger.WithTraceID(c.Request.Context())

		log.Info("HTTP Request",
			zap.Int("status", c.Writer.Status()),
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.String("ip", c.ClientIP()),
			zap.Duration("latency", duration),
			zap.String("errors", c.Errors.ByType(gin.ErrorTypePrivate).String()),
		)
	}
}

// MetricsMiddleware 紀錄 Prometheus 效能指標
func MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// 等待請求處理完畢
		c.Next()

		// 排除 /metrics 端點本身，避免產生過多雜訊
		if c.Request.URL.Path != "/metrics" {
			duration := time.Since(start).Seconds()
			status := strconv.Itoa(c.Writer.Status())

			metrics.HttpRequestsTotal.WithLabelValues(c.Request.Method, c.Request.URL.Path, status).Inc()
			metrics.HttpRequestDuration.WithLabelValues(c.Request.Method, c.Request.URL.Path).Observe(duration)
		}
	}
}

// SecurityHeadersMiddleware 注入 OWASP 推薦的基礎 HTTP 安全標頭
func SecurityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Header("Content-Security-Policy", "default-src 'self'")
		c.Header("Referrer-Policy", "no-referrer")
		c.Next()
	}
}

// AuthMiddleware 純無狀態驗證 JWT Access Token（只驗簽章與效期，不查任何儲存層）
// 撤銷延遲由 Access Token 的短效期兜底；需要即時阻斷的敏感路由請再掛上 RevocationCheckMiddleware
func AuthMiddleware(tokenManager domain.TokenManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid authorization header format"})
			return
		}

		tokenString := parts[1]
		claims, err := tokenManager.ValidateAccessToken(tokenString)
		if err != nil {
			if errors.Is(err, domain.ErrTokenExpired) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token has expired"})
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}

		// 注入 Gin 請求上下文
		c.Set(CtxUserID, claims.UserID)
		c.Set(CtxTenantID, claims.TenantID)
		c.Set(CtxRole, claims.Role)
		c.Set(CtxUsername, claims.Username)
		c.Set(CtxTokenID, claims.TokenID)

		c.Next()
	}
}

// RevocationCheckMiddleware 檢查 Access Token 是否已被吊銷（查 Redis 黑名單）
// 僅掛在涉及資金或高風險操作的路由上（例如下注、派彩、提款），一般讀取 API 保持無狀態
// 必須放在 AuthMiddleware 之後
func RevocationCheckMiddleware(blacklist domain.TokenBlacklist) gin.HandlerFunc {
	return func(c *gin.Context) {
		jti := c.GetString(CtxTokenID)
		if jti == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		revoked, err := blacklist.IsRevoked(c.Request.Context(), jti)
		if err != nil {
			// Fail-closed：無法確認狀態時一律拒絕，避免 Redis 故障時放行已被吊銷的 Token
			c.Error(err)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "failed to verify token status"})
			return
		}
		if revoked {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token has been revoked"})
			return
		}

		c.Next()
	}
}

// RBACMiddleware 角色存取控制：僅允許指定角色進入
func RBACMiddleware(allowedRoles ...domain.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		roleVal, exists := c.Get(CtxRole)
		if !exists {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		userRole, ok := roleVal.(domain.Role)
		if !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "invalid role"})
			return
		}

		if slices.Contains(allowedRoles, userRole) {
			c.Next()
			return
		}

		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
	}
}

// actorFromContext 取出 AuthMiddleware 驗證過的身分。
// 缺少任何欄位時會是零值，授權規則對零值一律拒絕。
func actorFromContext(c *gin.Context) domain.Actor {
	role, _ := c.Value(CtxRole).(domain.Role)
	return domain.Actor{
		UserID:   c.GetInt64(CtxUserID),
		TenantID: c.GetString(CtxTenantID),
		Role:     role,
	}
}
