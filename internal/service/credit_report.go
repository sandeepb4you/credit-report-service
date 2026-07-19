package service

import (
	"context"
	"errors"
	"strings"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// CreditReportService owns CRUD over credit_reports.
type CreditReportService struct {
	repo *repository.CreditReportRepo
}

func NewCreditReportService(repo *repository.CreditReportRepo) *CreditReportService {
	return &CreditReportService{repo: repo}
}

func (s *CreditReportService) List(ctx context.Context) ([]models.CreditReport, error) {
	return s.repo.FindAll(ctx)
}

func (s *CreditReportService) Get(ctx context.Context, id int64) (*models.CreditReport, error) {
	cr, err := s.repo.FindByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Credit report not found with id " + itoa(id))
	}
	return cr, err
}

func (s *CreditReportService) GetBySubject(ctx context.Context, subjectID string) (*models.CreditReport, error) {
	cr, err := s.repo.FindBySubjectID(ctx, subjectID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Credit report not found for subject " + subjectID)
	}
	return cr, err
}

func (s *CreditReportService) Create(ctx context.Context, subjectID string, score *int32, status *string) (*models.CreditReport, error) {
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return nil, apperr.NewValidation("subjectId is required")
	}
	cr := &models.CreditReport{
		SubjectID: subjectID,
		Score:     score,
		Status:    status,
	}
	if err := s.repo.Create(ctx, cr); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return nil, apperr.NewConflict("subjectId already exists")
		}
		return nil, err
	}
	return cr, nil
}

func (s *CreditReportService) Delete(ctx context.Context, id int64) error {
	err := s.repo.DeleteByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return apperr.NewNotFound("Credit report not found with id " + itoa(id))
	}
	return err
}

// itoa avoids importing strconv across files where it's the only use.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
