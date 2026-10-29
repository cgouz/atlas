package handler

import (
	"log/slog"
	"net/http"

	"github.com/cgouz/atlas/src/main/infrastructure"
	"github.com/cgouz/atlas/src/main/modules/crawling_data/dto"
	"github.com/cgouz/atlas/src/main/modules/crawling_data/model"
	"github.com/cgouz/atlas/src/main/modules/crawling_data/service"
)

type Handler struct {
	svc *service.Service
	log *slog.Logger
}

func New(svc *service.Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/crawling-data", h.list)
	mux.HandleFunc("GET /v1/crawling-data/{id}", h.get)
}

// list supports ?country_id=, ?organization_id=, ?site_id=, ?category_id=,
// ?subcategory_id=, ?record_type=, ?language=, ?published_from=,
// ?published_to=, ?cursor=, ?limit= (newest first).
//
// @Summary      List crawling data
// @Description  Returns the normalized content the LLM produced from crawled pages, newest first.
// @Description  One item per page. The list leaves out the full content; get one item by id for it.
// @Tags         crawling-data
// @Produce      json
// @Param        country_id      query  int     false  "Only data of this country"
// @Param        organization_id query  int     false  "Only data of this organization"
// @Param        site_id         query  int     false  "Only data of this site"
// @Param        category_id     query  int     false  "Only data in this top-level category"
// @Param        subcategory_id  query  int     false  "Only data in this subcategory"
// @Param        record_type     query  string  false  "Only this record type, e.g. NEWS, LAW, DECREE (case-insensitive)"
// @Param        language        query  string  false  "Only this ISO 639-1 language, e.g. uz, ru, en"
// @Param        published_from  query  string  false  "Published on or after (YYYY-MM-DD or RFC 3339)"
// @Param        published_to    query  string  false  "Published before (YYYY-MM-DD or RFC 3339)"
// @Param        cursor          query  int     false  "next_cursor from the previous page"
// @Param        limit           query  int     false  "Page size (default 50, max 500)"
// @Success      200  {object}  infrastructure.ListResponse[dto.CrawlingDataSummary]
// @Failure      400  {object}  map[string]string  "Invalid query parameter, or published_from is not before published_to"
// @Router       /v1/crawling-data [get]
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
	out := make([]dto.CrawlingDataSummary, len(items))
	for i, d := range items {
		out[i] = dto.SummaryFromModel(d)
	}
	infrastructure.WriteJSON(w, http.StatusOK,
		infrastructure.NewListResponse(out, f.Limit, func(s dto.CrawlingDataSummary) int64 { return s.ID }))
}

func parseFilter(r *http.Request) (model.ListFilter, error) {
	page, err := infrastructure.ParsePage(r, 50, 500)
	if err != nil {
		return model.ListFilter{}, err
	}
	f := model.ListFilter{
		RecordType: infrastructure.QueryString(r, "record_type"),
		Language:   infrastructure.QueryString(r, "language"),
		Cursor:     page.Cursor,
		Limit:      page.Limit,
	}
	for key, dst := range map[string]**int64{
		"country_id": &f.CountryID, "organization_id": &f.OrganizationID, "site_id": &f.SiteID,
		"category_id": &f.CategoryID, "subcategory_id": &f.SubcategoryID,
	} {
		if *dst, err = infrastructure.QueryInt64(r, key); err != nil {
			return f, err
		}
	}
	if f.PublishedFrom, err = infrastructure.QueryTime(r, "published_from"); err != nil {
		return f, err
	}
	if f.PublishedTo, err = infrastructure.QueryTime(r, "published_to"); err != nil {
		return f, err
	}
	return f, nil
}

// get returns one crawling data item with its full content.
//
// @Summary      Get crawling data
// @Description  Returns one item with the full cleaned content and the hash of the source text.
// @Tags         crawling-data
// @Produce      json
// @Param        id   path      int  true  "Crawling data id"
// @Success      200  {object}  dto.CrawlingDataResponse
// @Failure      400  {object}  map[string]string  "id is not a positive integer"
// @Failure      404  {object}  map[string]string  "Crawling data not found"
// @Router       /v1/crawling-data/{id} [get]
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := infrastructure.PathID(r)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	d, err := h.svc.Get(r.Context(), id)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	infrastructure.WriteJSON(w, http.StatusOK, dto.FromModel(d))
}
