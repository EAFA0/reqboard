package service

func BuildVisibilityFilter(scope string) string {
	return "scope=" + scope
}

func LegacyVisibilityScope(channel string) string {
	return "channel=" + channel
}

func QueryOrders(customerID string) []string {
	return []string{customerID}
}
