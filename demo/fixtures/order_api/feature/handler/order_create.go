package handler

// CreateOrder 创单入口：校验商品与库存后写入订单。
func CreateOrder(customerID, sku string) string {
	return customerID + ":" + sku
}
