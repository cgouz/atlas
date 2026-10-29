package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/cgouz/atlas/src/main/infrastructure"
	"github.com/cgouz/atlas/src/main/modules/crawl/dto"
	"github.com/cgouz/atlas/src/main/modules/crawl/model"
	"github.com/cgouz/atlas/src/main/modules/crawl/service"
)

type Handler struct {
	svc     *service.Service
	batches *service.BatchService
	log     *slog.Logger
}

func New(svc *service.Service, batches *service.BatchService, log *slog.Logger) *Handler {
	return &Handler{svc: svc, batches: batches, log: log}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/sites/{id}/crawl", h.trigger)
	mux.HandleFunc("GET /v1/crawl-runs", h.list)
	mux.HandleFunc("GET /v1/crawl-runs/{id}", h.get)
	mux.HandleFunc("POST /v1/crawl-batches", h.startBatch)
	mux.HandleFunc("GET /v1/crawl-batches", h.listBatches)
	mux.HandleFunc("GET /v1/crawl-batches/{id}", h.getBatch)
	mux.HandleFunc("POST /v1/crawl-batches/{id}/cancel", h.cancelBatch)
}

// trigger starts a crawl in the background and returns 202 with the new run;
// follow it with GET /v1/crawl-runs/{id}.
//
// @Summary      Crawl a site now
// @Description  Starts a crawl of one site in the background and returns the new crawl run right away.
// @Description  Poll GET /v1/crawl-runs/{id} to follow its progress.
// @Tags         crawl-runs
// @Produce      json
// @Param        id   path      int  true  "Site id"
// @Success      202  {object}  dto.CrawlRunResponse  "Crawl started"
// @Failure      400  {object}  map[string]string     "id is not a positive integer"
// @Failure      404  {object}  map[string]string     "Site not found"
// @Failure      409  {object}  map[string]string     "This site is already being crawled"
// @Failure      503  {object}  map[string]string     "Too many crawls running; try again later"
// @Router       /v1/sites/{id}/crawl [post]
func (h *Handler) trigger(w http.ResponseWriter, r *http.Request) {
	id, err := infrastructure.PathID(r)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	run, err := h.svc.TriggerSite(r.Context(), id)
	switch {
	case errors.Is(err, service.ErrAlreadyRunning):
		infrastructure.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, service.ErrBusy):
		infrastructure.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	case err != nil:
		infrastructure.WriteError(w, h.log, err)
	default:
		infrastructure.WriteJSON(w, http.StatusAccepted, dto.FromModel(run))
	}
}

// list supports ?site_id=, ?batch_id=, ?status=, ?cursor=, ?limit= (newest first).
//
// @Summary      List crawl runs
// @Description  Returns crawl runs newest first. A run is one crawl of one site, with page and data counters.
// @Tags         crawl-runs
// @Produce      json
// @Param        site_id   query  int     false  "Only runs of this site"
// @Param        batch_id  query  int     false  "Only runs started by this batch"
// @Param        status    query  string  false  "Only runs with this status"  Enums(running, completed, partial, failed, cancelled)
// @Param        cursor    query  int     false  "next_cursor from the previous page"
// @Param        limit     query  int     false  "Page size (default 50, max 500)"
// @Success      200  {object}  infrastructure.ListResponse[dto.CrawlRunResponse]
// @Failure      400  {object}  map[string]string  "Invalid query parameter"
// @Router       /v1/crawl-runs [get]
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	page, err := infrastructure.ParsePage(r, 50, 500)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	f := model.ListFilter{Status: infrastructure.QueryString(r, "status"), Cursor: page.Cursor, Limit: page.Limit}
	if f.SiteID, err = infrastructure.QueryInt64(r, "site_id"); err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	if f.BatchID, err = infrastructure.QueryInt64(r, "batch_id"); err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	items, err := h.svc.List(r.Context(), f)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	out := make([]dto.CrawlRunResponse, len(items))
	for i, run := range items {
		out[i] = dto.FromModel(run)
	}
	infrastructure.WriteJSON(w, http.StatusOK,
		infrastructure.NewListResponse(out, page.Limit, func(r dto.CrawlRunResponse) int64 { return r.ID }))
}

// get returns one crawl run.
//
// @Summary      Get a crawl run
// @Description  Returns one crawl run. While status is running the counters keep growing.
// @Tags         crawl-runs
// @Produce      json
// @Param        id   path      int  true  "Crawl run id"
// @Success      200  {object}  dto.CrawlRunResponse
// @Failure      400  {object}  map[string]string  "id is not a positive integer"
// @Failure      404  {object}  map[string]string  "Crawl run not found"
// @Router       /v1/crawl-runs/{id} [get]
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := infrastructure.PathID(r)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	run, err := h.svc.Get(r.Context(), id)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	infrastructure.WriteJSON(w, http.StatusOK, dto.FromModel(run))
}

// startBatch crawls every active official site of the active countries (or of
// the body's country_ids) in the background and returns 202 with the batch;
// follow it with GET /v1/crawl-batches/{id}. 409 while another batch runs.
//
// @Summary      Start a crawl batch
// @Description  Crawls every active official site of the chosen countries in the background
// @Description  (all active countries when the body is empty or country_ids is empty).
// @Description  Only one batch runs at a time. Poll GET /v1/crawl-batches/{id} to follow it.
// @Tags         crawl-batches
// @Accept       json
// @Produce      json
// @Param        body  body      dto.StartBatchRequest  false  "Countries to crawl (optional)"
// @Success      202   {object}  dto.BatchResponse      "Batch started"
// @Failure      400   {object}  map[string]string      "Invalid body, or no active sites in the selected countries"
// @Failure      409   {object}  map[string]string      "Another batch is already running"
// @Router       /v1/crawl-batches [post]
func (h *Handler) startBatch(w http.ResponseWriter, r *http.Request) {
	var req dto.StartBatchRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) { // an empty body means all countries
		infrastructure.WriteError(w, h.log, fmt.Errorf("%w: body: %v", infrastructure.ErrInvalidInput, err))
		return
	}
	for _, id := range req.CountryIDs {
		if id <= 0 {
			infrastructure.WriteError(w, h.log, fmt.Errorf("%w: country_ids must be positive integers", infrastructure.ErrInvalidInput))
			return
		}
	}
	batch, err := h.batches.Start(r.Context(), req.CountryIDs)
	switch {
	case errors.Is(err, service.ErrBatchRunning):
		infrastructure.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case err != nil:
		infrastructure.WriteError(w, h.log, err)
	default:
		infrastructure.WriteJSON(w, http.StatusAccepted, dto.BatchFromModel(batch))
	}
}

// listBatches supports ?status=, ?cursor=, ?limit= (newest first).
//
// @Summary      List crawl batches
// @Description  Returns crawl batches newest first.
// @Tags         crawl-batches
// @Produce      json
// @Param        status  query  string  false  "Only batches with this status, e.g. running"
// @Param        cursor  query  int     false  "next_cursor from the previous page"
// @Param        limit   query  int     false  "Page size (default 50, max 500)"
// @Success      200  {object}  infrastructure.ListResponse[dto.BatchResponse]
// @Failure      400  {object}  map[string]string  "Invalid query parameter"
// @Router       /v1/crawl-batches [get]
func (h *Handler) listBatches(w http.ResponseWriter, r *http.Request) {
	page, err := infrastructure.ParsePage(r, 50, 500)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	items, err := h.batches.List(r.Context(), model.BatchListFilter{
		Status: infrastructure.QueryString(r, "status"), Cursor: page.Cursor, Limit: page.Limit,
	})
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	out := make([]dto.BatchResponse, len(items))
	for i, b := range items {
		out[i] = dto.BatchFromModel(b)
	}
	infrastructure.WriteJSON(w, http.StatusOK,
		infrastructure.NewListResponse(out, page.Limit, func(b dto.BatchResponse) int64 { return b.ID }))
}

// getBatch returns the batch with its crawl runs counted per status.
//
// @Summary      Get a crawl batch
// @Description  Returns one batch with progress: runs counts its crawl runs per status and
// @Description  sites_pending is how many sites have not started yet.
// @Tags         crawl-batches
// @Produce      json
// @Param        id   path      int  true  "Crawl batch id"
// @Success      200  {object}  dto.BatchResponse
// @Failure      400  {object}  map[string]string  "id is not a positive integer"
// @Failure      404  {object}  map[string]string  "Crawl batch not found"
// @Router       /v1/crawl-batches/{id} [get]
func (h *Handler) getBatch(w http.ResponseWriter, r *http.Request) {
	id, err := infrastructure.PathID(r)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	batch, counts, err := h.batches.Get(r.Context(), id)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	infrastructure.WriteJSON(w, http.StatusOK, dto.BatchDetail(batch, counts))
}

// cancelBatch stops a running batch (202); 409 when it has already finished.
//
// @Summary      Cancel a crawl batch
// @Description  Stops a running batch: sites not started yet are not crawled, running crawls are cancelled.
// @Tags         crawl-batches
// @Produce      json
// @Param        id   path      int  true  "Crawl batch id"
// @Success      202  {object}  dto.BatchResponse  "Cancel requested"
// @Failure      400  {object}  map[string]string  "id is not a positive integer"
// @Failure      404  {object}  map[string]string  "Crawl batch not found"
// @Failure      409  {object}  map[string]string  "The batch is not running anymore"
// @Router       /v1/crawl-batches/{id}/cancel [post]
func (h *Handler) cancelBatch(w http.ResponseWriter, r *http.Request) {
	id, err := infrastructure.PathID(r)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	batch, err := h.batches.Cancel(r.Context(), id)
	switch {
	case errors.Is(err, service.ErrBatchNotRunning):
		infrastructure.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case err != nil:
		infrastructure.WriteError(w, h.log, err)
	default:
		infrastructure.WriteJSON(w, http.StatusAccepted, dto.BatchFromModel(batch))
	}
}
