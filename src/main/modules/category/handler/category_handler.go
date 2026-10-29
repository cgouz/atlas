package handler

import (
	"log/slog"
	"net/http"

	"github.com/cgouz/atlas/src/main/infrastructure"
	"github.com/cgouz/atlas/src/main/modules/category/dto"
	"github.com/cgouz/atlas/src/main/modules/category/model"
	"github.com/cgouz/atlas/src/main/modules/category/service"
)

type Handler struct {
	svc *service.Service
	log *slog.Logger
}

func New(svc *service.Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/categories", h.list)
	mux.HandleFunc("GET /v1/categories/{id}", h.get)
}

// list supports ?parent_id=, ?is_active=, ?top_level=true and ?tree=true
// (categories with their subcategories nested).
//
// @Summary      List categories
// @Description  Returns the taxonomy the LLM classifies pages into. Categories have subcategories
// @Description  (parent_id). With ?tree=true top-level categories are returned with their subcategories nested.
// @Description  Not paginated.
// @Tags         categories
// @Produce      json
// @Param        parent_id  query  int   false  "Only subcategories of this category"
// @Param        is_active  query  bool  false  "Only active (true) or inactive (false) categories"
// @Param        top_level  query  bool  false  "Only top-level categories (no parent)"
// @Param        tree       query  bool  false  "Nest subcategories under their category (ignores parent_id and top_level)"
// @Success      200  {object}  infrastructure.ListResponse[dto.CategoryResponse]
// @Failure      400  {object}  map[string]string  "Invalid query parameter"
// @Router       /v1/categories [get]
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	
	parentID, err := infrastructure.QueryInt64(r, "parent_id")
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	
	active, err := infrastructure.QueryBool(r, "is_active")
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	
	topLevel, err := infrastructure.QueryBool(r, "top_level")
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	
	tree, err := infrastructure.QueryBool(r, "tree")
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}
	
	f := model.ListFilter{ParentID: parentID, IsActive: active, TopLevel: topLevel != nil && *topLevel}
	if tree != nil && *tree {
		f = model.ListFilter{IsActive: active}
	}
	
	items, err := h.svc.List(r.Context(), f)
	if err != nil {
		infrastructure.WriteError(w, h.log, err)
		return
	}

	var out []dto.CategoryResponse
	if tree != nil && *tree {
		out = dto.Tree(items)
	} else {
		for _, c := range items {
			out = append(out, dto.FromModel(c))
		}
	}
	
	if out == nil {
		out = []dto.CategoryResponse{}
	}
	
	infrastructure.WriteJSON(w, http.StatusOK, infrastructure.ListResponse[dto.CategoryResponse]{Items: out})
}

// get returns one category or subcategory.
//
// @Summary      Get a category
// @Tags         categories
// @Produce      json
// @Param        id   path      int  true  "Category id"
// @Success      200  {object}  dto.CategoryResponse
// @Failure      400  {object}  map[string]string  "id is not a positive integer"
// @Failure      404  {object}  map[string]string  "Category not found"
// @Router       /v1/categories/{id} [get]
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
	infrastructure.WriteJSON(w, http.StatusOK, dto.FromModel(c))
}
