package handler

import "order_api/service"

func SearchOrders(customerID string) []string {
	return service.QueryOrders(customerID)
}

func GetOrder(orderID string) string {
	return "order-" + orderID
}
