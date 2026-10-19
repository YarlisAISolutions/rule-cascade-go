package com.shop.order;

import jakarta.validation.constraints.Max;
import jakarta.validation.constraints.Min;

public class OrderRequest {
    @Min(1)
    @Max(10)
    private int quantity;

    @Min(18)
    private int customerAge;
}
