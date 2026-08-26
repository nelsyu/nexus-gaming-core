package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// HttpRequestsTotal 計算 HTTP 請求總數
	HttpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP 請求的總數量",
		},
		[]string{"method", "path", "status"},
	)

	// HttpRequestDuration 紀錄 HTTP 請求的回應延遲時間
	HttpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP 請求的延遲時間分佈 (秒)",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)
)

func init() {
	// 將指標註冊到 Prometheus 預設的 registry
	prometheus.MustRegister(HttpRequestsTotal)
	prometheus.MustRegister(HttpRequestDuration)
}
