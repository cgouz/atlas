package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cgouz/atlas/src/main/infrastructure"
	"github.com/cgouz/atlas/src/main/modules/organization/model"
	"github.com/cgouz/atlas/src/main/modules/organization/repo"
)

// Column sizes of the organizations table.
const (
	maxNameLen         = 300
	maxOfficialNameLen = 500
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

// Update trims and checks the new values, then stores them. At least one field must be set.
func (s *Service) Update(ctx context.Context, id int64, u model.Update) (model.Organization, error) {
	if u.Name == nil && u.OfficialName == nil && u.Description == nil && u.Status == nil {
		return model.Organization{}, fmt.Errorf("%w: nothing to update: send name, official_name, description and/or status",
			infrastructure.ErrInvalidInput)
	}
	if u.Name != nil {
		name := strings.TrimSpace(*u.Name)
		if name == "" || utf8.RuneCountInString(name) > maxNameLen {
			return model.Organization{}, fmt.Errorf("%w: name must be 1-%d characters", infrastructure.ErrInvalidInput, maxNameLen)
		}
		u.Name = &name
	}
	if u.OfficialName != nil {
		official := strings.TrimSpace(*u.OfficialName)
		if utf8.RuneCountInString(official) > maxOfficialNameLen {
			return model.Organization{}, fmt.Errorf("%w: official_name must be at most %d characters",
				infrastructure.ErrInvalidInput, maxOfficialNameLen)
		}
		u.OfficialName = &official
	}
	if u.Description != nil {
		description := strings.TrimSpace(*u.Description)
		u.Description = &description
	}
	if u.Status != nil {
		status := strings.ToLower(strings.TrimSpace(*u.Status))
		if status != model.StatusActive && status != model.StatusInactive {
			return model.Organization{}, fmt.Errorf("%w: status must be %q or %q",
				infrastructure.ErrInvalidInput, model.StatusActive, model.StatusInactive)
		}
		u.Status = &status
	}
	return s.repo.Update(ctx, id, u)
}
