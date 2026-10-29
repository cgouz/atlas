package handler

import (
	"log/slog"
	"net/http"

	"github.com/cgouz/atlas/src/main/infrastructure"
	"github.com/cgouz/atlas/src/main/modules/site/dto"
	"github.com/cgouz/atlas/src/main/modules/site/model"
	"github.com/cgouz/atlas/src/main/modules/site/service"
)

type Handler struct {
	svc *service.Service
	log *slog.Logger
}

func New(svc *service.Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// Register adds the site endpoints; POST /v1/sites/{id}/crawl lives in the crawl module.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/sites", h.list)
	mux.HandleFunc("GET /v1/sites/{id}", h.get)
	mux.HandleFunc("PATCH /v1/sites/{id}", h.update)
}

// update changes a site's domain, start URL and/or status.
//
// @Summary      Update a site
// @Description  Changes domain, base_url and/or status. Send only the fields to change.
// @Description  base_url must be on the domain (domain or www.domain); when changing the domain, send a
// @Description  matching base_url in the same request. status inactive stops new crawls of the site; a crawl
// @Description  already running finishes with the old settings. Crawl history and pages are kept.
// @Tags         sites
// @Accept       json
// @Produce      json
// @Param        id    path      int                    true  "Site id"
// @Param        body  body      dto.UpdateSiteRequest  true  "Fields to change"
// @Success      200   {object}  dto.SiteResponse       "The updated site"
// @Failure      400   {object}  map[string]string      "Bad id, bad body, invalid domain/base_url/status, or nothing to update"
// @Failure      404   {object}  map[string]string      "Site not found"
// @Failure      409   {object}  map[string]string      "Another site already has this domain"
// @Router       /v1/sites/{id} [patch]
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := infrastructure.PathID(r)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	var req dto.UpdateSiteRequest
	if err := infrastructure.DecodeJSON(w, r, &req); err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	s, err := h.svc.Update(r.Context(), id, req.ToModel())
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	infrastructure.WriteJSON(w, http.StatusOK, dto.FromModel(s))
}

// list supports ?country_id=, ?organization_id=, ?status=, ?cursor=, ?limit=.
//
// @Summary      List sites
// @Description  Returns the websites Atlas crawls, with their crawl schedule and last result.
// @Tags         sites
// @Produce      json
// @Param        country_id       query  int     false  "Only sites of this country"
// @Param        organization_id  query  int     false  "Only sites of this organization"
// @Param        status           query  string  false  "Only sites with this status, e.g. active"
// @Param        cursor           query  int     false  "next_cursor from the previous page"
// @Param        limit            query  int     false  "Page size (default 100, max 1000)"
// @Success      200  {object}  infrastructure.ListResponse[dto.SiteResponse]
// @Failure      400  {object}  map[string]string  "Invalid query parameter"
// @Router       /v1/sites [get]
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	page, err := infrastructure.ParsePage(r, 100, 1000)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	f := model.ListFilter{Status: infrastructure.QueryString(r, "status"), Cursor: page.Cursor, Limit: page.Limit}
	if f.CountryID, err = infrastructure.QueryInt64(r, "country_id"); err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	if f.OrganizationID, err = infrastructure.QueryInt64(r, "organization_id"); err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	items, err := h.svc.List(r.Context(), f)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	out := make([]dto.SiteResponse, len(items))
	for i, s := range items {
		out[i] = dto.FromModel(s)
	}
	infrastructure.WriteJSON(w, http.StatusOK,
		infrastructure.NewListResponse(out, page.Limit, func(s dto.SiteResponse) int64 { return s.ID }))
}

// get returns one site.
//
// @Summary      Get a site
// @Tags         sites
// @Produce      json
// @Param        id   path      int  true  "Site id"
// @Success      200  {object}  dto.SiteResponse
// @Failure      400  {object}  map[string]string  "id is not a positive integer"
// @Failure      404  {object}  map[string]string  "Site not found"
// @Router       /v1/sites/{id} [get]
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := infrastructure.PathID(r)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	s, err := h.svc.Get(r.Context(), id)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	infrastructure.WriteJSON(w, http.StatusOK, dto.FromModel(s))
}
