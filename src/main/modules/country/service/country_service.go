package service

import (
	"context"

	"github.com/cgouz/atlas/src/main/modules/country/model"
	"github.com/cgouz/atlas/src/main/modules/country/repo"
)

type Service struct {
	repo *repo.Repo
}

func New(r *repo.Repo) *Service {
	return &Service{repo: r}
}

func (s *Service) List(ctx context.Context, f model.ListFilter) ([]model.Country, error) {
	return s.repo.List(ctx, f)
}

func (s *Service) Get(ctx context.Context, id int64) (model.Country, error) {
	return s.repo.Get(ctx, id)
}

func (s *Service) Translations(ctx context.Context, id int64) ([]model.Translation, error) {
	return s.repo.Translations(ctx, id)
}
