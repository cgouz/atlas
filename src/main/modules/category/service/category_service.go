package service

import (
	"context"

	"github.com/cgouz/atlas/src/main/modules/category/model"
	"github.com/cgouz/atlas/src/main/modules/category/repo"
)

type Service struct {
	repo *repo.Repo
}

func New(r *repo.Repo) *Service {
	return &Service{repo: r}
}

func (s *Service) List(ctx context.Context, f model.ListFilter) ([]model.Category, error) {
	return s.repo.List(ctx, f)
}

func (s *Service) Get(ctx context.Context, id int64) (model.Category, error) {
	return s.repo.Get(ctx, id)
}

func (s *Service) Active(ctx context.Context) ([]model.Category, error) {
	active := true
	return s.repo.List(ctx, model.ListFilter{IsActive: &active})
}
