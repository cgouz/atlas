package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/cgouz/atlas/src/main/infrastructure"
	"github.com/cgouz/atlas/src/main/modules/crawling_data/model"
)

type Repo struct {
	db infrastructure.DBTX
}

func New(db infrastructure.DBTX) *Repo {
	return &Repo{db: db}
}

// Upsert stores the crawling data for a page (one row per page). It returns the
// row id and whether a new row was created rather than an existing one updated.
func (r *Repo) Upsert(ctx context.Context, d model.CrawlingData) (int64, bool, error) {
	var id int64
	var created bool
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO
			crawling_data (
				country_id,
				organization_id,
				site_id,
				page_id,
				category_id,
				subcategory_id,
				record_type,
				title,
				language_code,
				published_at,
				content,
				summary,
				confidence,
				content_hash
			)
			VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
			)
		ON CONFLICT
			(page_id)
		DO UPDATE SET
			country_id = EXCLUDED.country_id,
			organization_id = EXCLUDED.organization_id,
			site_id = EXCLUDED.site_id,
			category_id = EXCLUDED.category_id,
			subcategory_id = EXCLUDED.subcategory_id,
			record_type = EXCLUDED.record_type,
			title = EXCLUDED.title,
			language_code = EXCLUDED.language_code,
			published_at = EXCLUDED.published_at,
			content = EXCLUDED.content,
			summary = EXCLUDED.summary,
			confidence = EXCLUDED.confidence,
			content_hash = EXCLUDED.content_hash,
			updated_at = NOW()
		RETURNING
			id,
			(xmax = 0)`,
		d.CountryID, d.OrganizationID, d.SiteID, d.PageID, d.CategoryID, d.SubcategoryID,
		d.RecordType, d.Title, d.LanguageCode, d.PublishedAt, d.Content, d.Summary,
		d.Confidence, d.ContentHash,
	).Scan(&id, &created)
	if err != nil {
		return 0, false, fmt.Errorf("upsert crawling data for page %d: %w", d.PageID, err)
	}
	return id, created, nil
}

func scan(row interface{ Scan(...any) error }) (model.CrawlingData, error) {
	var d model.CrawlingData
	err := row.Scan(&d.ID, &d.CountryID, &d.OrganizationID, &d.SiteID, &d.PageID, &d.CategoryID,
		&d.SubcategoryID, &d.RecordType, &d.Title, &d.LanguageCode, &d.PublishedAt, &d.Content,
		&d.Summary, &d.Confidence, &d.ContentHash, &d.CreatedAt, &d.UpdatedAt, &d.SourceURL)
	return d, err
}

// List returns crawling data newest first (cursor = continue below this id).
func (r *Repo) List(ctx context.Context, f model.ListFilter) ([]model.CrawlingData, error) {
	var w infrastructure.Where
	if f.Cursor > 0 {
		w.Add("d.id < ?", f.Cursor)
	}
	if f.CountryID != nil {
		w.Add("d.country_id = ?", *f.CountryID)
	}
	if f.OrganizationID != nil {
		w.Add("d.organization_id = ?", *f.OrganizationID)
	}
	if f.SiteID != nil {
		w.Add("d.site_id = ?", *f.SiteID)
	}
	if f.CategoryID != nil {
		w.Add("d.category_id = ?", *f.CategoryID)
	}
	if f.SubcategoryID != nil {
		w.Add("d.subcategory_id = ?", *f.SubcategoryID)
	}
	if f.RecordType != nil {
		w.Add("d.record_type = ?", *f.RecordType)
	}
	if f.Language != nil {
		w.Add("d.language_code = ?", *f.Language)
	}
	if f.PublishedFrom != nil {
		w.Add("d.published_at >= ?", *f.PublishedFrom)
	}
	if f.PublishedTo != nil {
		w.Add("d.published_at < ?", *f.PublishedTo)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			d.id,
			d.country_id,
			d.organization_id,
			d.site_id,
			d.page_id,
			d.category_id,
			d.subcategory_id,
			d.record_type,
			d.title,
			d.language_code,
			d.published_at,
			d.content,
			d.summary,
			d.confidence,
			d.content_hash,
			d.created_at,
			d.updated_at,
			p.url
		FROM
			crawling_data d
			JOIN pages p ON p.id = d.page_id`+w.SQL()+` ORDER BY d.id DESC LIMIT `+w.Arg(f.Limit+1),
		w.Args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list crawling data: %w", err)
	}
	defer rows.Close()
	var out []model.CrawlingData
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Repo) Get(ctx context.Context, id int64) (model.CrawlingData, error) {
	d, err := scan(
		r.db.QueryRowContext(ctx, `
		SELECT
			d.id,
			d.country_id,
			d.organization_id,
			d.site_id,
			d.page_id,
			d.category_id,
			d.subcategory_id,
			d.record_type,
			d.title,
			d.language_code,
			d.published_at,
			d.content,
			d.summary,
			d.confidence,
			d.content_hash,
			d.created_at,
			d.updated_at,
			p.url
		FROM
			crawling_data d
			JOIN pages p ON p.id = d.page_id
		WHERE
			d.id = $1`,
			id,
		),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return d, fmt.Errorf("crawling data %d: %w", id, infrastructure.ErrNotFound)
	}
	return d, err
}
