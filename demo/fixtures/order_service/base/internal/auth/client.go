package auth

func SyncAuthPermission(orderID string) string {
	return "auth:" + orderID
}
