package http

import (
	"context"
	"strconv"
	"time"

	"github.com/bosstest/nexus-core/pkg/logger"
	"github.com/bosstest/nexus-core/pkg/metrics"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
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
