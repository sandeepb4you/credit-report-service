package service

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// couponCodePattern is what a code may contain. Deliberately narrow: codes get
// typed by hand off a screenshot or a message, so no lower case (everything is
// upper-cased on write) and nothing that renders ambiguously.
var couponCodePattern = regexp.MustCompile(`^[A-Z0-9_-]+$`)

// CouponService owns coupon issuance and the arithmetic of applying one.
//
// Pricing is never accepted from the client. The only thing a caller sends is
// a code; the discount and the payable amount are both derived here from the
// catalog price and the stored percentage.
type CouponService struct {
	coupons *repository.CouponRepo
	orders  *repository.OrderRepo
}

func NewCouponService(coupons *repository.CouponRepo, orders *repository.OrderRepo) *CouponService {
	return &CouponService{coupons: coupons, orders: orders}
}

// NewCouponInput is the validated shape of a create request.
type NewCouponInput struct {
	Code            string
	DiscountPercent float64
	ProductCode     *string
	MaxRedemptions  *int
	PerAccountLimit *int
	ValidFrom       *time.Time
	ValidUntil      *time.Time
}

// Quote is what the payment screen renders when a code is applied. Every
// figure in it is computed server-side.
type Quote struct {
	Code            string  `json:"code"`
	ProductCode     string  `json:"productCode"`
	Currency        string  `json:"currency"`
	OriginalAmount  float64 `json:"originalAmount"`
	DiscountPercent float64 `json:"discountPercent"`
	DiscountAmount  float64 `json:"discountAmount"`
	PayableAmount   float64 `json:"payableAmount"`
}

// Create issues a coupon owned by the calling account.
func (s *CouponService) Create(
	ctx context.Context, creatorID int64, in NewCouponInput,
) (*models.Coupon, error) {
	code, err := normalizeCouponCode(in.Code)
	if err != nil {
		return nil, err
	}

	details := map[string]string{}
	if in.DiscountPercent <= 0 || in.DiscountPercent > 100 {
		details["discountPercent"] = "discountPercent must be greater than 0 and at most 100"
	}
	if in.MaxRedemptions != nil && *in.MaxRedemptions <= 0 {
		details["maxRedemptions"] = "maxRedemptions must be positive when set"
	}
	perAccount := 1
	if in.PerAccountLimit != nil {
		perAccount = *in.PerAccountLimit
		if perAccount <= 0 {
			details["perAccountLimit"] = "perAccountLimit must be positive"
		}
	}
	from := time.Now().UTC()
	if in.ValidFrom != nil {
		from = in.ValidFrom.UTC()
	}
	if in.ValidUntil != nil && !in.ValidUntil.After(from) {
		details["validUntil"] = "validUntil must be after validFrom"
	}
	if len(details) > 0 {
		return nil, apperr.NewValidationWith("Validation failed", details)
	}

	// A coupon scoped to a product that does not exist would silently never
	// apply, so reject it at creation rather than at checkout.
	var productCode *string
	if in.ProductCode != nil {
		if pc := strings.TrimSpace(*in.ProductCode); pc != "" {
			if _, err := s.orders.FindProduct(ctx, pc); err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return nil, apperr.NewValidationWith("Validation failed",
						map[string]string{"productCode": "unknown product"})
				}
				return nil, err
			}
			productCode = &pc
		}
	}

	c := &models.Coupon{
		Code:            code,
		CreatedBy:       creatorID,
		DiscountPercent: in.DiscountPercent,
		ProductCode:     productCode,
		MaxRedemptions:  in.MaxRedemptions,
		PerAccountLimit: perAccount,
		ValidFrom:       from,
		ValidUntil:      in.ValidUntil,
	}
	if err := s.coupons.Create(ctx, c); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return nil, apperr.NewConflict("That coupon code is already taken")
		}
		return nil, err
	}
	slog.Info("coupon created",
		"code", c.Code, "created_by", creatorID, "percent", c.DiscountPercent)
	return c, nil
}

// List returns the caller's coupons, or every coupon when they hold
// models.PermCouponAdmin.
func (s *CouponService) List(ctx context.Context, accountID int64, role string) ([]models.Coupon, error) {
	if models.HasPermission(role, models.PermCouponAdmin) {
		return s.coupons.ListAll(ctx)
	}
	return s.coupons.ListByCreator(ctx, accountID)
}

// Revoke stops future use of a coupon. Agents may only revoke their own;
// holders of coupon:admin may revoke any. Already-placed orders keep their
// discount — revoking is not retroactive.
func (s *CouponService) Revoke(ctx context.Context, code string, accountID int64, role string) error {
	normalized, err := normalizeCouponCode(code)
	if err != nil {
		return err
	}
	// 0 means "any creator" in the repo query.
	var scopeTo int64
	if !models.HasPermission(role, models.PermCouponAdmin) {
		scopeTo = accountID
	}
	ok, err := s.coupons.Revoke(ctx, normalized, scopeTo)
	if err != nil {
		return err
	}
	if !ok {
		// Someone else's coupon reads as missing rather than forbidden, so the
		// endpoint cannot be used to discover which codes exist.
		return apperr.NewNotFound("No such active coupon")
	}
	slog.Info("coupon revoked", "code", normalized, "by_account", accountID)
	return nil
}

// Quote validates a code against a product and returns the resulting price.
// This is what the payment screen calls when a customer applies a code; it
// reserves nothing, so the answer is advisory until the order is created.
func (s *CouponService) Quote(ctx context.Context, code, productCode string) (*Quote, error) {
	normalized, err := normalizeCouponCode(code)
	if err != nil {
		return nil, err
	}
	product, err := s.orders.FindProduct(ctx, strings.TrimSpace(productCode))
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"productCode": "unknown product"})
	}
	if err != nil {
		return nil, err
	}

	c, err := s.coupons.FindByCode(ctx, normalized)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, couponRejected()
	}
	if err != nil {
		return nil, err
	}
	if !c.UsableAt(time.Now().UTC()) || c.Exhausted() || !c.AppliesTo(product.Code) {
		return nil, couponRejected()
	}

	discount, payable := c.ApplyTo(product.Amount)
	if payable < models.MinChargeableAmount {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"couponCode": "this coupon would reduce the total below the minimum payable amount"})
	}
	return &Quote{
		Code:            c.Code,
		ProductCode:     product.Code,
		Currency:        product.Currency,
		OriginalAmount:  product.Amount,
		DiscountPercent: c.DiscountPercent,
		DiscountAmount:  discount,
		PayableAmount:   payable,
	}, nil
}

// ClaimForOrder reserves a redemption inside an in-flight order transaction and
// returns the coupon plus the discount it is worth on this amount.
//
// Ordering matters: the conditional UPDATE in Claim both reserves the slot and
// locks the coupon row, so the per-account count that follows cannot race
// another checkout of the same code. Doing the count first would be a
// check-then-act bug.
func (s *CouponService) ClaimForOrder(
	ctx context.Context, tx pgx.Tx, code string, accountID int64, product *models.Product,
) (c *models.Coupon, discount, payable float64, err error) {
	normalized, err := normalizeCouponCode(code)
	if err != nil {
		return nil, 0, 0, err
	}

	c, err = s.coupons.Claim(ctx, tx, normalized)
	if errors.Is(err, repository.ErrNotFound) {
		// Unknown, revoked, out of window, or exhausted — all one answer.
		return nil, 0, 0, couponRejected()
	}
	if err != nil {
		return nil, 0, 0, err
	}
	if !c.AppliesTo(product.Code) {
		return nil, 0, 0, apperr.NewValidationWith("Validation failed",
			map[string]string{"couponCode": "this coupon does not apply to that product"})
	}

	used, err := s.coupons.CountAccountRedemptions(ctx, tx, c.ID, accountID)
	if err != nil {
		return nil, 0, 0, err
	}
	if used >= c.PerAccountLimit {
		return nil, 0, 0, apperr.NewConflict("You have already used this coupon")
	}

	discount, payable = c.ApplyTo(product.Amount)
	if payable < models.MinChargeableAmount {
		return nil, 0, 0, apperr.NewValidationWith("Validation failed",
			map[string]string{"couponCode": "this coupon would reduce the total below the minimum payable amount"})
	}
	return c, discount, payable, nil
}

// RecordRedemption writes the redemption row for a created order.
func (s *CouponService) RecordRedemption(
	ctx context.Context, tx pgx.Tx, couponID, accountID int64, orderUID string, discount float64,
) error {
	return s.coupons.RecordRedemption(ctx, tx, &models.CouponRedemption{
		CouponID:       couponID,
		AccountID:      accountID,
		OrderUID:       orderUID,
		DiscountAmount: discount,
	})
}

// Release hands a redemption back after an order that will never be paid.
// Best-effort by design: it runs on failure paths, where returning an error
// would mask the original failure the caller is already reporting.
func (s *CouponService) Release(ctx context.Context, orderUID string) {
	released, err := s.coupons.ReleaseByOrder(ctx, orderUID)
	if err != nil {
		slog.Error("failed to release coupon redemption", "order_uid", orderUID, "error", err)
		return
	}
	if released {
		slog.Info("coupon redemption released", "order_uid", orderUID)
	}
}

// ---- helpers -------------------------------------------------------------

// couponRejected is the single answer for every reason a code cannot be used.
// Distinguishing "no such code" from "expired" from "used up" would let a
// stranger enumerate which codes exist and how healthy they are.
func couponRejected() error {
	return apperr.NewValidationWith("Validation failed",
		map[string]string{"couponCode": "this coupon code is not valid"})
}

func normalizeCouponCode(code string) (string, error) {
	c := strings.ToUpper(strings.TrimSpace(code))
	if len(c) < models.CouponCodeMinLen || len(c) > models.CouponCodeMaxLen {
		return "", apperr.NewValidationWith("Validation failed",
			map[string]string{"code": "code must be between 3 and 32 characters"})
	}
	if !couponCodePattern.MatchString(c) {
		return "", apperr.NewValidationWith("Validation failed",
			map[string]string{"code": "code may only contain letters, digits, hyphen and underscore"})
	}
	return c, nil
}
