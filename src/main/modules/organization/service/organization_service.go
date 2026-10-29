package service

import (
	"context"

	"github.com/cgouz/atlas/src/main/modules/organization/model"
	"github.com/cgouz/atlas/src/main/modules/organization/repo"
)

type Service struct {
	repo *repo.Repo
}

func New(r *repo.Repo) *Service {
	return &Service{repo: r}
}

func (s *Service) List(ctx context.Context, f model.ListFilter) ([]model.Organization, error) {
	return s.repo.List(ctx, f)
}

func (s *Service) Get(ctx context.Context, id int64) (model.Organization, error) {
	return s.repo.Get(ctx, id)
}
