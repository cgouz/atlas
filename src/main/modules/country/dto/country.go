package dto

import "github.com/cgouz/atlas/src/main/modules/country/model"

type CountryResponse struct {
	ID           int64             `json:"id"`
	ISO2         string            `json:"iso2"`
	ISO3         string            `json:"iso3"`
	Name         string            `json:"name"`
	Soato        *string           `json:"soato"`
	Region       *string           `json:"region"`
	Subregion    *string           `json:"subregion"`
	IsActive     bool              `json:"is_active"`
	Translations map[string]string `json:"translations,omitempty"`
}

// UpdateCountryRequest is the body of PATCH /v1/countries/{id}. Send only the
// fields to change; a missing field keeps its current value.
type UpdateCountryRequest struct {
	Name     *string `json:"name" example:"Uzbekistan"` // English name, 1-150 characters
	IsActive *bool   `json:"is_active" example:"true"`  // only active countries are crawled
}

func (r UpdateCountryRequest) ToModel() model.Update {
	return model.Update{Name: r.Name, IsActive: r.IsActive}
}

func FromModel(c model.Country) CountryResponse {
	return CountryResponse{
		ID: c.ID, ISO2: c.ISO2, ISO3: c.ISO3, Name: c.Name, Soato: c.Soato,
		Region: c.Region, Subregion: c.Subregion, IsActive: c.IsActive,
	}
}
