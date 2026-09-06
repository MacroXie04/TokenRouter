package commerce

// TopUpRequest is a balance top-up request.
type TopUpRequest struct {
	Amount        int    `json:"amount" binding:"required"`
	PaymentMethod string `json:"payment_method"`
}
