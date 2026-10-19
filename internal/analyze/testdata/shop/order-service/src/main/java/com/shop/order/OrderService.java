package com.shop.order;

public class OrderService {
    static final int MAX_DISCOUNT_PERCENT = 30;

    @PreAuthorize("hasRole('MANAGER')")
    public void approve(Order order) {
        for (int i = 0; i < 3; i++) {
            retry();
        }
        if (order.getTotal() > 10000) {
            throw new BusinessRuleException("orders over 10000 need approval");
        }
        switch (order.getTier()) {
            case "GOLD":
                applyDiscount(order, 20);
                break;
            default:
                break;
        }
    }
}
