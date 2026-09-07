package logger

import (
	"context"
	"os"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// TraceIDKey 是用來在 context 中傳遞 TraceID 的鍵值
type contextKey string

const TraceIDKey contextKey = "trace_id"

var (
	globalLogger *zap.Logger
	once         sync.Once
)

// InitLogger 初始化全域 Zap logger
func InitLogger() {
	once.Do(func() {
		encoderConfig := zap.NewProductionEncoderConfig()
		encoderConfig.TimeKey = "timestamp"
		encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

		// 使用 JSON 編碼器，方便整合到 ELK 或其他 Log 系統
		core := zapcore.NewCore(
			zapcore.NewJSONEncoder(encoderConfig),
			zapcore.AddSync(os.Stdout),
			zap.InfoLevel,
		)

		globalLogger = zap.New(core, zap.AddCaller())
		zap.ReplaceGlobals(globalLogger)
	})
}

// GetLogger 回傳全域 logger 實例
func GetLogger() *zap.Logger {
	if globalLogger == nil {
		InitLogger()
	}
	return globalLogger
}

// WithTraceID 從 context 取出 trace_id 並建立帶有該欄位的 logger
func WithTraceID(ctx context.Context) *zap.Logger {
	log := GetLogger()
	if ctx != nil {
		if traceID, ok := ctx.Value(TraceIDKey).(string); ok {
			return log.With(zap.String("trace_id", traceID))
		}
	}
	return log
}
