package relay

import (
	"errors"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/pagination"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/relay/tasks"
	"net/http"
	"strconv"
)

type relayQuotaReservationResolutionRequest struct {
	Resolution string `json:"resolution"`
}

// ListManualReviewRelayQuotaReservations lists the root-only accounting holds
// that automatic recovery could not safely finish.
func ListManualReviewRelayQuotaReservations(c *gin.Context) {
	page := pagination.FromContext(c)
	filter := billingsvc.RelayQuotaReservationReviewFilter{
		Page:          page.Page,
		PageSize:      page.PageSize,
		Operation:     c.Query("operation"),
		ReservationID: c.Query("reservation_id"),
	}
	if rawUserID, present := c.GetQuery("user_id"); present {
		userID, err := strconv.Atoi(rawUserID)
		if err != nil || userID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid review query"})
			return
		}
		filter.UserID = userID
	}
	items, total, err := billingsvc.ListManualReviewRelayQuotaReservations(filter)
	if err != nil {
		if errors.Is(err, billingsvc.ErrRelayQuotaReviewQueryInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid review query"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to list relay quota reviews"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"page": page.Page, "page_size": page.PageSize, "total": total, "items": items,
		},
	})
}

// GetManualReviewRelayQuotaReservation returns a single whitelisted,
// secret-free accounting snapshot. Raw reconciliation errors are never sent.
func GetManualReviewRelayQuotaReservation(c *gin.Context) {
	item, err := billingsvc.GetManualReviewRelayQuotaReservation(c.Param("reservation_id"))
	if err != nil {
		switch {
		case errors.Is(err, billingsvc.ErrRelayQuotaReviewQueryInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid reservation id"})
		case errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound):
			c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "review record not found"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to load relay quota review"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": item})
}

// RetryManualReviewRelayQuotaReservation either requeues the immutable
// accounting operation already stored on the ledger row or, for an already
// settled video task, reopens only its provider-specific authenticated poll.
// Neither path accepts replacement economic or provider fields.
func RetryManualReviewRelayQuotaReservation(c *gin.Context) {
	reservationID := c.Param("reservation_id")
	operatorUserID := requestctx.GetUserId(c)
	var result any
	videoResult, err := tasks.RetryVideoTaskManualReview(reservationID, operatorUserID)
	if err == nil {
		result = videoResult
	} else if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
		result, err = tasks.RetryKlingTaskManualReview(reservationID, operatorUserID)
		if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
			result, err = tasks.RetryDoubaoTaskManualReview(reservationID, operatorUserID)
			if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
				result, err = tasks.RetryMidjourneyTaskManualReview(reservationID, operatorUserID)
				if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
					result, err = tasks.RetrySunoTaskManualReview(reservationID, operatorUserID)
					if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
						result, err = tasks.RetryViduTaskManualReview(reservationID, operatorUserID)
						if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
							result, err = tasks.RetryHailuoTaskManualReview(reservationID, operatorUserID)
							if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
								result, err = tasks.RetryAliWanTaskManualReview(reservationID, operatorUserID)
								if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
									result, err = tasks.RetryGeminiVeoTaskManualReview(reservationID, operatorUserID)
									if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
										result, err = billingsvc.RetryManualReviewRelayQuotaReservation(reservationID, operatorUserID)
									}
								}
							}
						}
					}
				}
			}
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, billingsvc.ErrRelayQuotaReviewQueryInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid reservation id"})
		case errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound):
			c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "review record not found"})
		case errors.Is(err, billingsvc.ErrRelayQuotaReviewInvalidState),
			errors.Is(err, billingsvc.ErrRelayQuotaReviewUnsafeRetry),
			errors.Is(err, billingsvc.ErrRelayQuotaReservationBusy):
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "reservation cannot be safely retried in its current state"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to retry relay quota review"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": result})
}

// ResolveManualReviewRelayQuotaReservation applies an explicit root decision
// only to an exact unresolved provider/dispatched tuple. Amounts, user/token
// coordinates, and the provider channel are never accepted from the request.
func ResolveManualReviewRelayQuotaReservation(c *gin.Context) {
	var request relayQuotaReservationResolutionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid resolution request"})
		return
	}
	reservationID := c.Param("reservation_id")
	operatorUserID := requestctx.GetUserId(c)
	var result any
	klingResult, err := tasks.ResolveKlingRelayQuotaReservationReview(
		reservationID, operatorUserID, request.Resolution,
	)
	if err == nil {
		result = klingResult
	} else if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
		result, err = tasks.ResolveDoubaoRelayQuotaReservationReview(
			reservationID, operatorUserID, request.Resolution,
		)
		if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
			result, err = tasks.ResolveViduRelayQuotaReservationReview(
				reservationID, operatorUserID, request.Resolution,
			)
			if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
				result, err = tasks.ResolveHailuoRelayQuotaReservationReview(
					reservationID, operatorUserID, request.Resolution,
				)
				if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
					result, err = tasks.ResolveAliWanRelayQuotaReservationReview(
						reservationID, operatorUserID, request.Resolution,
					)
					if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
						result, err = tasks.ResolveGeminiVeoRelayQuotaReservationReview(
							reservationID, operatorUserID, request.Resolution,
						)
						if errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) {
							result, err = tasks.ResolveJimengRelayQuotaReservationReview(
								reservationID, operatorUserID, request.Resolution,
							)
						}
					}
				}
			}
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, billingsvc.ErrRelayQuotaReviewQueryInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid resolution request"})
		case errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound):
			c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "review record not found"})
		case errors.Is(err, billingsvc.ErrRelayQuotaReviewInvalidState),
			errors.Is(err, billingsvc.ErrRelayQuotaReviewUnsafeRetry),
			errors.Is(err, billingsvc.ErrRelayQuotaReservationBusy),
			errors.Is(err, billingsvc.ErrRelayQuotaOperation),
			errors.Is(err, billingsvc.ErrRelayQuotaManualReview):
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "reservation cannot be safely resolved in its current state"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to resolve relay quota review"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": result})
}
