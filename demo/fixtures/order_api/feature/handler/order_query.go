package handler

import "order_api/service"

// SearchOrders 对客查询入口：线下单待支付前不可见，过滤条件由可见性规则统一生成。
func SearchOrders(customerID string) []string {
	filter := service.BuildVisibilityFilter("customer")
	return service.QueryOrders(customerID + "|" + filter)
}

func GetOrder(orderID string) string {
	return "order-" + orderID
}
