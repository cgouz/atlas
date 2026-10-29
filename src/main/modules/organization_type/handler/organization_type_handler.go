package handler

import (
	"log/slog"
	"net/http"

	"github.com/cgouz/atlas/src/main/infrastructure"
	"github.com/cgouz/atlas/src/main/modules/organization_type/dto"
	"github.com/cgouz/atlas/src/main/modules/organization_type/service"
)

type Handler struct {
	svc *service.Service
	log *slog.Logger
}

func New(svc *service.Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organization-types", h.list)
}

// list returns every organization type; the table is small, so there is no paging.
//
// @Summary      List organization types
// @Description  Returns all organization types (ministry, agency, parliament, ...). Not paginated.
// @Tags         organization-types
// @Produce      json
// @Success      200  {object}  infrastructure.ListResponse[dto.OrganizationTypeResponse]
// @Router       /v1/organization-types [get]
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.List(r.Context())
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	out := make([]dto.OrganizationTypeResponse, len(items))
	for i, t := range items {
		out[i] = dto.FromModel(t)
	}
	infrastructure.WriteJSON(w, http.StatusOK, infrastructure.ListResponse[dto.OrganizationTypeResponse]{Items: out})
}
