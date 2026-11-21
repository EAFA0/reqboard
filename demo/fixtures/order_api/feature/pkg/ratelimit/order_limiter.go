package ratelimit

// NewOrderRateLimiter 是 PRD 未要求的越界实现，用于演示 Orphan Diff 拦截。
func NewOrderRateLimiter(limit int) int {
	return limit * 2
}
