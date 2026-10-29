package handler

import (
	"log/slog"
	"net/http"

	"github.com/cgouz/atlas/src/main/infrastructure"
	"github.com/cgouz/atlas/src/main/modules/organization/dto"
	"github.com/cgouz/atlas/src/main/modules/organization/model"
	"github.com/cgouz/atlas/src/main/modules/organization/service"
)

type Handler struct {
	svc *service.Service
	log *slog.Logger
}

func New(svc *service.Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations", h.list)
	mux.HandleFunc("GET /v1/organizations/{id}", h.get)
	mux.HandleFunc("PATCH /v1/organizations/{id}", h.update)
}

// update changes an organization's names, description and/or status.
//
// @Summary      Update an organization
// @Description  Changes name, official_name, description and/or status. Send only the fields to change;
// @Description  an empty string clears official_name or description.
// @Tags         organizations
// @Accept       json
// @Produce      json
// @Param        id    path      int                            true  "Organization id"
// @Param        body  body      dto.UpdateOrganizationRequest  true  "Fields to change"
// @Success      200   {object}  dto.OrganizationResponse       "The updated organization"
// @Failure      400   {object}  map[string]string              "Bad id, bad body, invalid value, or nothing to update"
// @Failure      404   {object}  map[string]string              "Organization not found"
// @Failure      409   {object}  map[string]string              "Another organization of the same country already has this name"
// @Router       /v1/organizations/{id} [patch]
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := infrastructure.PathID(r)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	var req dto.UpdateOrganizationRequest
	if err := infrastructure.DecodeJSON(w, r, &req); err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	o, err := h.svc.Update(r.Context(), id, req.ToModel())
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	infrastructure.WriteJSON(w, http.StatusOK, dto.FromModel(o))
}

// list supports ?country_id=, ?organization_type_id=, ?parent_id=, ?status=, ?cursor=, ?limit=.
//
// @Summary      List organizations
// @Description  Returns government organizations (ministries, agencies, ...) ordered by id.
// @Tags         organizations
// @Produce      json
// @Param        country_id            query  int     false  "Only organizations of this country"
// @Param        organization_type_id  query  int     false  "Only organizations of this type"
// @Param        parent_id             query  int     false  "Only child organizations of this parent"
// @Param        status                query  string  false  "Only organizations with this status, e.g. active"
// @Param        cursor                query  int     false  "next_cursor from the previous page"
// @Param        limit                 query  int     false  "Page size (default 100, max 1000)"
// @Success      200  {object}  infrastructure.ListResponse[dto.OrganizationResponse]
// @Failure      400  {object}  map[string]string  "Invalid query parameter"
// @Router       /v1/organizations [get]
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	items, err := h.svc.List(r.Context(), f)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	out := make([]dto.OrganizationResponse, len(items))
	for i, o := range items {
		out[i] = dto.FromModel(o)
	}
	infrastructure.WriteJSON(w, http.StatusOK,
		infrastructure.NewListResponse(out, f.Limit, func(o dto.OrganizationResponse) int64 { return o.ID }))
}

func parseFilter(r *http.Request) (model.ListFilter, error) {
	page, err := infrastructure.ParsePage(r, 100, 1000)
	if err != nil {
		return model.ListFilter{}, err
	}
	f := model.ListFilter{Status: infrastructure.QueryString(r, "status"), Cursor: page.Cursor, Limit: page.Limit}
	for key, dst := range map[string]**int64{
		"country_id": &f.CountryID, "organization_type_id": &f.OrganizationTypeID, "parent_id": &f.ParentID,
	} {
		if *dst, err = infrastructure.QueryInt64(r, key); err != nil {
			return f, err
		}
	}
	return f, nil
}

// get returns one organization.
//
// @Summary      Get an organization
// @Tags         organizations
// @Produce      json
// @Param        id   path      int  true  "Organization id"
// @Success      200  {object}  dto.OrganizationResponse
// @Failure      400  {object}  map[string]string  "id is not a positive integer"
// @Failure      404  {object}  map[string]string  "Organization not found"
// @Router       /v1/organizations/{id} [get]
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := infrastructure.PathID(r)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	o, err := h.svc.Get(r.Context(), id)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	infrastructure.WriteJSON(w, http.StatusOK, dto.FromModel(o))
}
