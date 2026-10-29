package dto

import "github.com/cgouz/atlas/src/main/modules/organization/model"

type OrganizationResponse struct {
	ID                 int64   `json:"id"`
	CountryID          int64   `json:"country_id"`
	OrganizationTypeID int64   `json:"organization_type_id"`
	ParentID           *int64  `json:"parent_id"`
	Name               string  `json:"name"`
	OfficialName       *string `json:"official_name"`
	Description        *string `json:"description"`
	Status             string  `json:"status"`
}

// UpdateOrganizationRequest is the body of PATCH /v1/organizations/{id}. Send
// only the fields to change; a missing field keeps its current value.
type UpdateOrganizationRequest struct {
	Name         *string `json:"name" example:"Tax Committee"`                                        // short name, 1-300 characters, unique within the country
	OfficialName *string `json:"official_name" example:"Tax Committee under the Ministry of Economy"` // full official name, up to 500 characters; "" clears it
	Description  *string `json:"description" example:"Collects taxes and enforces tax law."`          // free text; "" clears it
	Status       *string `json:"status" enums:"active,inactive" example:"active"`                     // active or inactive
}

func (r UpdateOrganizationRequest) ToModel() model.Update {
	return model.Update{Name: r.Name, OfficialName: r.OfficialName, Description: r.Description, Status: r.Status}
}

func FromModel(o model.Organization) OrganizationResponse {
	return OrganizationResponse{
		ID: o.ID, CountryID: o.CountryID, OrganizationTypeID: o.OrganizationTypeID, ParentID: o.ParentID,
		Name: o.Name, OfficialName: o.OfficialName, Description: o.Description, Status: o.Status,
	}
}
