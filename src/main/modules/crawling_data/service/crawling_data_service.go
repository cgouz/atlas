package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/cgouz/atlas/src/main/infrastructure"
	"github.com/cgouz/atlas/src/main/modules/crawling_data/model"
	"github.com/cgouz/atlas/src/main/modules/crawling_data/repo"
)

type Service struct {
	repo *repo.Repo
}

func New(r *repo.Repo) *Service {
	return &Service{repo: r}
}

func (s *Service) List(ctx context.Context, f model.ListFilter) ([]model.CrawlingData, error) {
	if f.RecordType != nil {
		upper := strings.ToUpper(*f.RecordType)
		f.RecordType = &upper
	}
	if f.PublishedFrom != nil && f.PublishedTo != nil && !f.PublishedFrom.Before(*f.PublishedTo) {
		return nil, fmt.Errorf("%w: published_from must be before published_to", infrastructure.ErrInvalidInput)
	}
	return s.repo.List(ctx, f)
}

func (s *Service) Get(ctx context.Context, id int64) (model.CrawlingData, error) {
	return s.repo.Get(ctx, id)
}
