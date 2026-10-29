package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cgouz/atlas/src/main/infrastructure"
	"github.com/cgouz/atlas/src/main/modules/country/model"
	"github.com/cgouz/atlas/src/main/modules/country/repo"
)

// maxNameLen is the size of countries.name (VARCHAR(150)).
const maxNameLen = 150

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

// Update trims and checks the new values, then stores them. At least one field must be set.
func (s *Service) Update(ctx context.Context, id int64, u model.Update) (model.Country, error) {
	if u.Name == nil && u.IsActive == nil {
		return model.Country{}, fmt.Errorf("%w: nothing to update: send name and/or is_active", infrastructure.ErrInvalidInput)
	}
	if u.Name != nil {
		name := strings.TrimSpace(*u.Name)
		if name == "" || utf8.RuneCountInString(name) > maxNameLen {
			return model.Country{}, fmt.Errorf("%w: name must be 1-%d characters", infrastructure.ErrInvalidInput, maxNameLen)
		}
		u.Name = &name
	}
	return s.repo.Update(ctx, id, u)
}

func (s *Service) Translations(ctx context.Context, id int64) ([]model.Translation, error) {
	return s.repo.Translations(ctx, id)
}
