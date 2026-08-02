package handler

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	_ "credit-report-service/internal/models" // referenced by swag annotations (models.Coupon)
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// CouponHandler serves coupon issuance for agents and the quote endpoint the
// payment screen uses.
type CouponHandler struct {
	svc *service.CouponService
}

func NewCouponHandler(svc *service.CouponService) *CouponHandler {
	return &CouponHandler{svc: svc}
}

// ---- POST /api/coupons ----------------------------------------------------

type createCouponReq struct {
	Code            string  `json:"code"            example:"SAVE20"`
	DiscountPercent float64 `json:"discountPercent" example:"20"`
	ProductCode     *string `json:"productCode"     example:"CREDIT_ANALYSIS"`
	MaxRedemptions  *int    `json:"maxRedemptions"  example:"100"`
	PerAccountLimit *int    `json:"perAccountLimit" example:"1"`
	ValidFrom       *string `json:"validFrom"       example:"2026-08-01T00:00:00Z"`
	ValidUntil      *string `json:"validUntil"      example:"2026-12-31T23:59:59Z"`
}

// CreateCoupon godoc
//
// @Summary      Issue a coupon code
// @Description  Creates a percentage-discount coupon owned by the calling account. Requires the 'coupon:create' permission, held by agents and admins. Omit productCode to have it apply to every product; omit maxRedemptions for unlimited use. The discount is applied server-side at checkout — clients never send prices.
// @Tags         coupons
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      createCouponReq  true  "Coupon definition"
// @Success      201      {object}  models.Coupon
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403      {object}  apperr.ErrorBody  "Missing the 'coupon:create' permission"
// @Failure      409      {object}  apperr.ErrorBody  "That coupon code is already taken"
// @Router       /coupons [post]
func (h *CouponHandler) CreateCoupon(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	var req createCouponReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}

	var details map[string]string
	validFrom, err := parseOptionalTime(req.ValidFrom)
	if err != nil {
		details = setDetail(details, "validFrom", "validFrom must be an RFC3339 timestamp")
	}
	validUntil, err := parseOptionalTime(req.ValidUntil)
	if err != nil {
		details = setDetail(details, "validUntil", "validUntil must be an RFC3339 timestamp")
	}
	if len(details) > 0 {
		return apperr.NewValidationWith("Validation failed", details)
	}

	coupon, err := h.svc.Create(c.Context(), accountID, service.NewCouponInput{
		Code:            req.Code,
		DiscountPercent: req.DiscountPercent,
		ProductCode:     req.ProductCode,
		MaxRedemptions:  req.MaxRedemptions,
		PerAccountLimit: req.PerAccountLimit,
		ValidFrom:       validFrom,
		ValidUntil:      validUntil,
	})
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(coupon)
}

// ---- GET /api/coupons -----------------------------------------------------

// ListCoupons godoc
//
// @Summary      List coupons
// @Description  Returns the coupons the caller issued, newest first. Holders of 'coupon:admin' (admins) see every coupon instead.
// @Tags         coupons
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   models.Coupon
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403  {object}  apperr.ErrorBody  "Missing the 'coupon:manage' permission"
// @Router       /coupons [get]
func (h *CouponHandler) ListCoupons(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	role, _ := middleware.AccountRole(c)
	out, err := h.svc.List(c.Context(), accountID, role)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// ---- DELETE /api/coupons/:code --------------------------------------------

// RevokeCoupon godoc
//
// @Summary      Revoke a coupon
// @Description  Stops future use of a coupon. Agents may only revoke their own; a code belonging to someone else reads as 404 so the endpoint cannot be used to discover which codes exist. Orders already placed keep their discount — revoking is not retroactive.
// @Tags         coupons
// @Produce      json
// @Security     BearerAuth
// @Param        code  path      string  true  "Coupon code"
// @Success      200   {object}  map[string]string  "{\"message\": \"Coupon revoked\"}"
// @Failure      400   {object}  apperr.ErrorBody  "Malformed code"
// @Failure      401   {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403   {object}  apperr.ErrorBody  "Missing the 'coupon:manage' permission"
// @Failure      404   {object}  apperr.ErrorBody  "No such active coupon"
// @Router       /coupons/{code} [delete]
func (h *CouponHandler) RevokeCoupon(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	role, _ := middleware.AccountRole(c)
	if err := h.svc.Revoke(c.Context(), c.Params("code"), accountID, role); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"message": "Coupon revoked"})
}

// ---- GET /api/coupons/quote -----------------------------------------------

// QuoteCoupon godoc
//
// @Summary      Price a product with a coupon applied
// @Description  What the payment screen calls when a customer enters a code. Returns the list price, the discount, and the payable total, all computed server-side. Reserves nothing — the coupon is only consumed when the order is created, so a quote can go stale if the last redemption is taken in between. Any authenticated user may call it.
// @Tags         coupons
// @Produce      json
// @Security     BearerAuth
// @Param        code         query     string  true  "Coupon code"
// @Param        productCode  query     string  true  "Product being purchased"
// @Success      200  {object}  service.Quote
// @Failure      400  {object}  apperr.ErrorBody  "Invalid coupon or unknown product"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /coupons/quote [get]
func (h *CouponHandler) QuoteCoupon(c *fiber.Ctx) error {
	if _, ok := middleware.AccountID(c); !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	code := strings.TrimSpace(c.Query("code"))
	productCode := strings.TrimSpace(c.Query("productCode"))

	var details map[string]string
	if code == "" {
		details = setDetail(details, "code", "code is required")
	}
	if productCode == "" {
		details = setDetail(details, "productCode", "productCode is required")
	}
	if len(details) > 0 {
		return apperr.NewValidationWith("Validation failed", details)
	}

	quote, err := h.svc.Quote(c.Context(), code, productCode)
	if err != nil {
		return err
	}
	return c.JSON(quote)
}

// parseOptionalTime parses an optional RFC3339 timestamp. A nil or blank input
// is not an error — it means "unset".
func parseOptionalTime(v *string) (*time.Time, error) {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(*v))
	if err != nil {
		return nil, err
	}
	utc := t.UTC()
	return &utc, nil
}
