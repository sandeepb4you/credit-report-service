package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// simulatorDisclaimer is the compliance line the S29 screen mandates: the
// projection is an estimate from the user's own file, not a guarantee.
const simulatorDisclaimer = "Estimates from your actual file, not generic averages. Real movement depends on lender reporting cycles."

// ScoreBuilderService curates bank offerings (admin) and resolves both the
// score-builder product recs and the what-if simulator projection from a user's
// latest credit report (Journey 05·C, S28–S29).
type ScoreBuilderService struct {
	offerings *repository.BankOfferingRepo
	analytics *repository.CreditAnalyticsRepo
}

func NewScoreBuilderService(offerings *repository.BankOfferingRepo, analytics *repository.CreditAnalyticsRepo) *ScoreBuilderService {
	return &ScoreBuilderService{offerings: offerings, analytics: analytics}
}

// ---- Admin: offering CRUD --------------------------------------------------

// OfferingInput is the validated create/update payload for a bank offering.
type OfferingInput struct {
	Name                string
	ProductType         string
	MinFDAmount         float64
	InterestRatePercent float64
	MinCreditScore      int
	MaxCreditScore      int
	EstimatedPointsMin  int
	EstimatedPointsMax  int
	ApplyURL            string
	RevenueNote         string
	Active              *bool // nil defaults to true on create
}

func (in *OfferingInput) validate() map[string]string {
	d := map[string]string{}
	if strings.TrimSpace(in.Name) == "" {
		d["name"] = "is required"
	}
	if !models.ValidOfferingType(in.ProductType) {
		d["productType"] = "must be one of FD_CARD, SECURED_LOAN"
	}
	if in.InterestRatePercent < 0 || in.InterestRatePercent > 100 {
		d["interestRatePercent"] = "must be between 0 and 100"
	}
	if in.MinFDAmount < 0 {
		d["minFdAmount"] = "must not be negative"
	}
	if in.MinCreditScore < 0 || in.MinCreditScore > 900 {
		d["minCreditScore"] = "must be between 0 and 900"
	}
	if in.MaxCreditScore < 0 || in.MaxCreditScore > 900 {
		d["maxCreditScore"] = "must be between 0 and 900"
	}
	if in.MaxCreditScore < in.MinCreditScore {
		d["maxCreditScore"] = "must be greater than or equal to minCreditScore"
	}
	if in.EstimatedPointsMin < 0 {
		d["estimatedPointsMin"] = "must not be negative"
	}
	if in.EstimatedPointsMax < in.EstimatedPointsMin {
		d["estimatedPointsMax"] = "must be greater than or equal to estimatedPointsMin"
	}
	if strings.TrimSpace(in.ApplyURL) == "" {
		d["applyUrl"] = "is required"
	}
	return d
}

// CreateOffering inserts a new bank offering.
func (s *ScoreBuilderService) CreateOffering(ctx context.Context, in OfferingInput) (*models.BankOffering, error) {
	if d := in.validate(); len(d) > 0 {
		return nil, apperr.NewValidationWith("Validation failed", d)
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	o := &models.BankOffering{
		Name: strings.TrimSpace(in.Name), ProductType: in.ProductType,
		MinFDAmount: in.MinFDAmount, InterestRatePercent: in.InterestRatePercent,
		MinCreditScore: in.MinCreditScore, MaxCreditScore: in.MaxCreditScore,
		EstimatedPointsMin: in.EstimatedPointsMin, EstimatedPointsMax: in.EstimatedPointsMax,
		ApplyURL: strings.TrimSpace(in.ApplyURL), RevenueNote: in.RevenueNote, Active: active,
	}
	if err := s.offerings.Create(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

// UpdateOffering replaces the mutable fields of an existing offering.
func (s *ScoreBuilderService) UpdateOffering(ctx context.Context, id int64, in OfferingInput) (*models.BankOffering, error) {
	if d := in.validate(); len(d) > 0 {
		return nil, apperr.NewValidationWith("Validation failed", d)
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	o := &models.BankOffering{
		ID: id, Name: strings.TrimSpace(in.Name), ProductType: in.ProductType,
		MinFDAmount: in.MinFDAmount, InterestRatePercent: in.InterestRatePercent,
		MinCreditScore: in.MinCreditScore, MaxCreditScore: in.MaxCreditScore,
		EstimatedPointsMin: in.EstimatedPointsMin, EstimatedPointsMax: in.EstimatedPointsMax,
		ApplyURL: strings.TrimSpace(in.ApplyURL), RevenueNote: in.RevenueNote, Active: active,
	}
	if err := s.offerings.Update(ctx, o); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewNotFound("Bank offering not found")
		}
		return nil, err
	}
	return o, nil
}

// DeleteOffering removes an offering by id.
func (s *ScoreBuilderService) DeleteOffering(ctx context.Context, id int64) error {
	if err := s.offerings.Delete(ctx, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperr.NewNotFound("Bank offering not found")
		}
		return err
	}
	return nil
}

// GetOffering returns a single offering by id.
func (s *ScoreBuilderService) GetOffering(ctx context.Context, id int64) (*models.BankOffering, error) {
	o, err := s.offerings.FindByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Bank offering not found")
	}
	if err != nil {
		return nil, err
	}
	return o, nil
}

// ListOfferings returns offerings, optionally filtered by product type and active.
func (s *ScoreBuilderService) ListOfferings(ctx context.Context, productType *string, active *bool) ([]models.BankOffering, error) {
	if productType != nil {
		pt := strings.ToUpper(strings.TrimSpace(*productType))
		if !models.ValidOfferingType(pt) {
			return nil, apperr.NewValidationWith("Validation failed",
				map[string]string{"productType": "must be one of FD_CARD, SECURED_LOAN"})
		}
		productType = &pt
	}
	return s.offerings.List(ctx, productType, active)
}

// ---- Insights enrichment (S28 product recs) --------------------------------

// OfferingsForScore returns the active FD-card offerings whose score band
// contains the given score, highest estimated impact first. Called by the
// analytics enrichment to build the bank-product strategies in the score
// builder toolkit. A score <= 0 (no score on file) yields nothing.
func (s *ScoreBuilderService) OfferingsForScore(ctx context.Context, score int) ([]models.BankOffering, error) {
	if score <= 0 {
		return nil, nil
	}
	return s.offerings.ListActiveForScore(ctx, models.OfferingTypeFDCard, score)
}

// ---- What-if simulator (S29) -----------------------------------------------

// Simulation is the response for GET /credit-analytics/score-simulator.
type Simulation struct {
	CurrentScore   int64       `json:"currentScore"`
	ProjectedScore int64       `json:"projectedScore"`
	Delta          int64       `json:"delta"` // projected - current (sum of selected action deltas)
	Timeframe      string      `json:"timeframe"`
	Actions        []SimAction `json:"actions"`
	Disclaimer     string      `json:"disclaimer"`
}

// SimAction is one toggle row in the what-if simulator (the .simchip model).
type SimAction struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Selected  bool   `json:"selected"`  // ✓ selected (counts toward projection) vs ○ not
	Delta     int    `json:"delta"`     // signed score impact when selected
	Direction string `json:"direction"` // "up" (positive) | "down" (negative)
	Kind      string `json:"kind"`      // "advice" | "product" | "event"
}

// Simulate projects the caller's score under a chosen set of actions. By
// default every positive action the report supports is selected and the
// negative ones are not; pass a `selected` set of action keys to override
// (only those are counted). When reportID is nil the latest successful report
// is used; otherwise that specific report, which must belong to the caller.
func (s *ScoreBuilderService) Simulate(ctx context.Context, accountID int64, reportID *int64, selected map[string]bool) (*Simulation, error) {
	insights, err := s.insightsFor(ctx, accountID, reportID)
	if err != nil {
		return nil, err
	}
	actions := buildSimActions(insights)
	// Default: all positive (delta > 0) actions selected. An explicit (even if
	// empty) `selected` set overrides — the client sent its toggles.
	useDefault := selected == nil
	projected := scoreOrZero(insights.CreditScore)
	for i := range actions {
		if useDefault {
			actions[i].Selected = actions[i].Delta > 0
		} else {
			actions[i].Selected = selected[actions[i].Key]
		}
	}
	return s.project(ctx, insights, actions, projected)
}

func (s *ScoreBuilderService) project(ctx context.Context, insights *ReportInsights, actions []SimAction, projected int64) (*Simulation, error) {
	var delta int64
	for _, a := range actions {
		if a.Selected {
			delta += int64(a.Delta)
		}
	}
	projected = clampScore(projected + delta)
	out := &Simulation{
		CurrentScore:   scoreOrZero(insights.CreditScore),
		ProjectedScore: projected,
		Delta:          delta,
		Timeframe:      "projected in ~6 months",
		Actions:        actions,
		Disclaimer:     simulatorDisclaimer,
	}
	// If an FD-card action is selected, enrich its label with the best offering
	// for the score so the simulator reflects a real product. Best-effort: when
	// the score-builder isn't wired (e.g. tests) the generic label stays.
	if s.offerings != nil && insights.CreditScore != nil && *insights.CreditScore > 0 {
		if offs, err := s.OfferingsForScore(ctx, int(*insights.CreditScore)); err == nil && len(offs) > 0 {
			out = attachOfferingLabel(out, offs[0])
		}
	}
	return out, nil
}

// insightsFor resolves the caller's report (latest or a specific owned id) and
// returns the parsed insights. Mirrors the loan-switch ownership rules: a
// missing or foreign report reads as not found.
func (s *ScoreBuilderService) insightsFor(ctx context.Context, accountID int64, reportID *int64) (*ReportInsights, error) {
	var (
		row *models.CreditAnalyticsRequest
		err error
	)
	if reportID != nil {
		row, err = s.analytics.FindByID(ctx, *reportID)
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewNotFound("Report not found")
		}
		if err != nil {
			return nil, err
		}
		if row.AccountID == nil || *row.AccountID != accountID {
			return nil, apperr.NewNotFound("Report not found")
		}
	} else {
		row, err = s.analytics.FindLatestByAccount(ctx, accountID)
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewNotFound("No credit report found")
		}
		if err != nil {
			return nil, err
		}
	}
	return insightsFromReportRow(row)
}

// buildSimActions assembles the toggle set from the report's actual signals:
// positive levers the file has room to improve on, plus two universal negative
// actions that show what new debt or closing an old account would cost.
func buildSimActions(ins *ReportInsights) []SimAction {
	var out []SimAction
	current := scoreOrZero(ins.CreditScore)

	if ins.ReportCard != nil {
		for _, f := range ins.ReportCard.Factors {
			if !gradeImprovable(f.Grade) {
				continue
			}
			lo, hi, ok := estimatedScoreGain(f.Name, f.Grade)
			if !ok {
				continue
			}
			switch f.Name {
			case "Credit utilisation":
				if ins.CardUtilizationPercent > 30 {
					out = append(out, simPos("pay_card_down",
						fmt.Sprintf("Pay card down to under 30%% (from %.0f%%)", ins.CardUtilizationPercent),
						lo, hi, "advice"))
				}
			case "Payment history":
				out = append(out, simPos("on_time_streak",
					"6 months of all payments on time", lo, hi, "advice"))
			case "Enquiries":
				if ins.EnquiryCount180Days > 0 {
					out = append(out, simPos("stop_applications",
						fmt.Sprintf("No new applications for 6 months (%d enquiries ageing off)", ins.EnquiryCount180Days),
						lo, hi, "advice"))
				}
			}
		}
	}

	// FD-secured card: a product lever available to anyone on a rebuild path.
	if current > 0 && current < 700 {
		out = append(out, simPos("open_fd_card",
			"Open an FD-secured credit card", 15, 25, "product"))
	}

	// Universal negative actions — shown unselected, to teach the mechanics.
	out = append(out,
		SimAction{Key: "take_personal_loan", Label: "Apply for a personal loan now",
			Delta: -20, Direction: "down", Kind: "event"},
		SimAction{Key: "close_oldest_card", Label: "Close oldest credit card",
			Delta: -15, Direction: "down", Kind: "event"},
	)

	// Stable order for the client: positive first (by impact desc), then negatives.
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Delta > 0) != (out[j].Delta > 0) {
			return out[i].Delta > 0 // positives first
		}
		return out[i].Delta > out[j].Delta // within a sign, bigger magnitude first
	})
	return out
}

// simPos builds a positive action with an up direction. The reported delta is
// the midpoint of the estimated range (the projection uses one point per action).
func simPos(key, label string, lo, hi int, kind string) SimAction {
	return SimAction{
		Key:       key,
		Label:     label,
		Delta:     (lo + hi) / 2,
		Direction: "up",
		Kind:      kind,
	}
}

// attachOfferingLabel rewrites the open_fd_card action's label to name the best
// offering so the simulator points at a real product, when one is configured.
func attachOfferingLabel(sim *Simulation, o models.BankOffering) *Simulation {
	for i, a := range sim.Actions {
		if a.Key == "open_fd_card" {
			sim.Actions[i].Label = fmt.Sprintf("Open the %s (FD-secured card)", o.Name)
		}
	}
	return sim
}

// ---- small helpers ---------------------------------------------------------

func scoreOrZero(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// clampScore keeps a projected score inside the Experian 300–900 band.
func clampScore(s int64) int64 {
	if s < 300 {
		return 300
	}
	if s > 900 {
		return 900
	}
	return s
}
