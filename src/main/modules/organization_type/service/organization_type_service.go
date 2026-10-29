package service

import (
	"context"

	"github.com/cgouz/atlas/src/main/modules/organization_type/model"
	"github.com/cgouz/atlas/src/main/modules/organization_type/repo"
)

type Service struct {
	repo *repo.Repo
}

func New(r *repo.Repo) *Service {
	return &Service{repo: r}
}

func (s *Service) List(ctx context.Context) ([]model.OrganizationType, error) {
	return s.repo.List(ctx)
}
