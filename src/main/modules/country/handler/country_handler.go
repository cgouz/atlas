package handler

import (
	"log/slog"
	"net/http"

	"github.com/cgouz/atlas/src/main/infrastructure"
	"github.com/cgouz/atlas/src/main/modules/country/dto"
	"github.com/cgouz/atlas/src/main/modules/country/model"
	"github.com/cgouz/atlas/src/main/modules/country/service"
)

type Handler struct {
	svc *service.Service
	log *slog.Logger
}

func New(svc *service.Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/countries", h.list)
	mux.HandleFunc("GET /v1/countries/{id}", h.get)
	mux.HandleFunc("PATCH /v1/countries/{id}", h.update)
}

// list supports ?region=, ?is_active=, ?locale=uz|ru|kaa, ?cursor=, ?limit=.
//
// @Summary      List countries
// @Description  Returns countries ordered by id. With ?locale= the name is translated when a translation exists.
// @Tags         countries
// @Produce      json
// @Param        region     query  string  false  "Only countries in this region, e.g. Asia"
// @Param        is_active  query  bool    false  "Only active (true) or inactive (false) countries"
// @Param        locale     query  string  false  "Translate names into this locale"  Enums(uz, ru, kaa)
// @Param        cursor     query  int     false  "next_cursor from the previous page"
// @Param        limit      query  int     false  "Page size (default 300, max 1000)"
// @Success      200  {object}  infrastructure.ListResponse[dto.CountryResponse]
// @Failure      400  {object}  map[string]string  "Invalid query parameter"
// @Router       /v1/countries [get]
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	page, err := infrastructure.ParsePage(r, 300, 1000)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	active, err := infrastructure.QueryBool(r, "is_active")
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	items, err := h.svc.List(r.Context(), model.ListFilter{
		Region:   infrastructure.QueryString(r, "region"),
		IsActive: active,
		Locale:   infrastructure.QueryString(r, "locale"),
		Cursor:   page.Cursor,
		Limit:    page.Limit,
	})
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	out := make([]dto.CountryResponse, len(items))
	for i, c := range items {
		out[i] = dto.FromModel(c)
	}
	infrastructure.WriteJSON(w, http.StatusOK,
		infrastructure.NewListResponse(out, page.Limit, func(c dto.CountryResponse) int64 { return c.ID }))
}

// get returns one country with all its translated names.
//
// @Summary      Get a country
// @Description  Returns one country; translations maps locale to the translated name.
// @Tags         countries
// @Produce      json
// @Param        id   path      int  true  "Country id"
// @Success      200  {object}  dto.CountryResponse
// @Failure      400  {object}  map[string]string  "id is not a positive integer"
// @Failure      404  {object}  map[string]string  "Country not found"
// @Router       /v1/countries/{id} [get]
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := infrastructure.PathID(r)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	c, err := h.svc.Get(r.Context(), id)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	h.writeWithTranslations(w, r, c)
}

// update changes a country's name and/or whether it is crawled.
//
// @Summary      Update a country
// @Description  Changes the English name and/or is_active. Send only the fields to change.
// @Description  Deactivating a country stops new crawls of its sites (batches and the scheduler skip it);
// @Description  crawls already running finish normally.
// @Tags         countries
// @Accept       json
// @Produce      json
// @Param        id    path      int                       true  "Country id"
// @Param        body  body      dto.UpdateCountryRequest  true  "Fields to change"
// @Success      200   {object}  dto.CountryResponse       "The updated country"
// @Failure      400   {object}  map[string]string         "Bad id, bad body, empty name, or nothing to update"
// @Failure      404   {object}  map[string]string         "Country not found"
// @Router       /v1/countries/{id} [patch]
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := infrastructure.PathID(r)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	var req dto.UpdateCountryRequest
	if err := infrastructure.DecodeJSON(w, r, &req); err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	c, err := h.svc.Update(r.Context(), id, req.ToModel())
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	h.writeWithTranslations(w, r, c)
}

// writeWithTranslations writes the country as GET /v1/countries/{id} does, with all its translated names.
func (h *Handler) writeWithTranslations(w http.ResponseWriter, r *http.Request, c model.Country) {
	translations, err := h.svc.Translations(r.Context(), c.ID)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	resp := dto.FromModel(c)
	if len(translations) > 0 {
		resp.Translations = map[string]string{}
		for _, t := range translations {
			resp.Translations[t.Locale] = t.Name
		}
	}
	infrastructure.WriteJSON(w, http.StatusOK, resp)
}
