package service

import (
	"context"
	"strings"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// AdminOrdersService backs the console's purchases list: every order across
// every account, with the totals an operator reads it for.
type AdminOrdersService struct {
	orders *repository.OrderRepo
}

func NewAdminOrdersService(orders *repository.OrderRepo) *AdminOrdersService {
	return &AdminOrdersService{orders: orders}
}

// OrderQuery is one page of the purchases list.
type OrderQuery struct {
	From time.Time
	To   time.Time
	// Status is a group — paid, pending or failed — or "" for every order.
	Status string
	// Mode is "live", "test" or "" for both. Spelled as the console says it
	// rather than as the column stores it ("production" / "sandbox").
	Mode        string
	ProductCode string
	Search      string
	Sort        string
	Desc        bool
	Limit       int
	Offset      int
}

// List returns a page of orders placed inside the window, with the summary of
// everything the filters matched.
func (s *AdminOrdersService) List(ctx context.Context, q OrderQuery) (*models.AdminOrderPage, error) {
	// The same window rules as the user list, so "last 30 days" on the two
	// screens covers the same days and an omitted bound is unbounded.
	from, to, err := resolveAccountWindow(q.From, q.To)
	if err != nil {
		return nil, err
	}
	var end *time.Time
	if to != nil {
		e := to.AddDate(0, 0, 1)
		end = &e
	}

	details := map[string]string{}

	var statuses []string
	if g := strings.ToLower(strings.TrimSpace(q.Status)); g != "" {
		set, ok := models.OrderStatusGroups[g]
		if !ok {
			// A typo'd status that renders as "no purchases" would read as a
			// revenue problem, so it is refused rather than matched to nothing.
			details["status"] = "expected one of paid, pending, failed"
		}
		statuses = set
	}

	var mode *string
	switch strings.ToLower(strings.TrimSpace(q.Mode)) {
	case "":
	case "live":
		m := models.PaymentModeProduction
		mode = &m
	case "test":
		m := models.PaymentModeSandbox
		mode = &m
	default:
		details["mode"] = "expected live or test"
	}

	sort, ok := models.ParseOrderSort(q.Sort)
	if !ok {
		details["sort"] = "expected one of " + strings.Join(models.SortableOrderColumns(), ", ")
	}
	if len(details) > 0 {
		return nil, apperr.NewValidationWith("Validation failed", details)
	}

	var product *string
	if pc := strings.ToUpper(strings.TrimSpace(q.ProductCode)); pc != "" {
		product = &pc
	}

	limit := q.Limit
	if limit <= 0 {
		limit = accountsDefaultPage
	}
	if limit > accountsMaxPage {
		limit = accountsMaxPage
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}

	rows, summary, err := s.orders.ListOrders(ctx, repository.OrderListFilter{
		From:        from,
		To:          end,
		Statuses:    statuses,
		PaymentMode: mode,
		ProductCode: product,
		Search:      trimmedPtr(q.Search),
		OrderBy:     sort.SQL(q.Desc),
		Limit:       limit,
		Offset:      offset,
	})
	if err != nil {
		return nil, err
	}
	return &models.AdminOrderPage{
		Items:   rows,
		Total:   summary.Orders,
		Limit:   limit,
		Offset:  offset,
		Summary: summary,
	}, nil
}
