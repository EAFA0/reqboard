package service

func BuildVisibilityFilter(scope string) string {
	return "scope=" + scope + "&exclude=not_ready"
}

func LegacyVisibilityScope(channel string) string {
	return "legacy-channel=" + channel
}

func QueryOrders(customerID string) []string {
	return []string{customerID}
}
