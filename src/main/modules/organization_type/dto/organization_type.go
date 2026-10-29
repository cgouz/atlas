package dto

import "github.com/cgouz/atlas/src/main/modules/organization_type/model"

type OrganizationTypeResponse struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

func FromModel(t model.OrganizationType) OrganizationTypeResponse {
	return OrganizationTypeResponse{ID: t.ID, Code: t.Code, Name: t.Name}
}
