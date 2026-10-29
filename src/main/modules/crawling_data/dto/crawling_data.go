package dto

import (
	"encoding/hex"
	"time"

	"github.com/cgouz/atlas/src/main/modules/crawling_data/model"
)

// CrawlingDataSummary is a list item; the full content is only returned by GET /v1/crawling-data/{id}.
type CrawlingDataSummary struct {
	ID             int64      `json:"id"`
	CountryID      int64      `json:"country_id"`
	OrganizationID int64      `json:"organization_id"`
	SiteID         int64      `json:"site_id"`
	PageID         int64      `json:"page_id"`
	SourceURL      string     `json:"source_url"`
	CategoryID     int64      `json:"category_id"`
	SubcategoryID  *int64     `json:"subcategory_id"`
	RecordType     string     `json:"record_type"`
	Title          *string    `json:"title"`
	LanguageCode   *string    `json:"language_code"`
	PublishedAt    *time.Time `json:"published_at"`
	Summary        *string    `json:"summary"`
	Confidence     *float64   `json:"confidence"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      *time.Time `json:"updated_at"`
}

type CrawlingDataResponse struct {
	CrawlingDataSummary
	Content     string `json:"content"`
	ContentHash string `json:"content_hash"`
}

func SummaryFromModel(d model.CrawlingData) CrawlingDataSummary {
	return CrawlingDataSummary{
		ID: d.ID, CountryID: d.CountryID, OrganizationID: d.OrganizationID, SiteID: d.SiteID, PageID: d.PageID,
		SourceURL: d.SourceURL, CategoryID: d.CategoryID, SubcategoryID: d.SubcategoryID, RecordType: d.RecordType,
		Title: d.Title, LanguageCode: d.LanguageCode, PublishedAt: d.PublishedAt, Summary: d.Summary,
		Confidence: d.Confidence, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

func FromModel(d model.CrawlingData) CrawlingDataResponse {
	return CrawlingDataResponse{
		CrawlingDataSummary: SummaryFromModel(d),
		Content:             d.Content,
		ContentHash:         hex.EncodeToString(d.ContentHash),
	}
}
