package precheck

import "order_service/internal/auth"

// PrecheckOrder 依赖 AuthService 权限结果；AuthService 方案未确认时走 mocked 链路。
func PrecheckOrder(orderID string) string {
	permission := auth.SyncAuthPermission(orderID)
	if permission == "" {
		return "denied"
	}
	return "precheck:" + orderID
}
