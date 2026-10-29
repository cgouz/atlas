package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/cgouz/atlas/src/main/common/crawler"
	"github.com/cgouz/atlas/src/main/common/extractor"
	"github.com/cgouz/atlas/src/main/infrastructure"
	categorymodel "github.com/cgouz/atlas/src/main/modules/category/model"
	countrymodel "github.com/cgouz/atlas/src/main/modules/country/model"
	"github.com/cgouz/atlas/src/main/modules/crawl/model"
	crawlrepo "github.com/cgouz/atlas/src/main/modules/crawl/repo"
	crawlingdatamodel "github.com/cgouz/atlas/src/main/modules/crawling_data/model"
	crawlingdatarepo "github.com/cgouz/atlas/src/main/modules/crawling_data/repo"
	llmmodel "github.com/cgouz/atlas/src/main/modules/llm/model"
	llmservice "github.com/cgouz/atlas/src/main/modules/llm/service"
	organizationmodel "github.com/cgouz/atlas/src/main/modules/organization/model"
	pagemodel "github.com/cgouz/atlas/src/main/modules/page/model"
	pagerepo "github.com/cgouz/atlas/src/main/modules/page/repo"
	pageservice "github.com/cgouz/atlas/src/main/modules/page/service"
	sitemodel "github.com/cgouz/atlas/src/main/modules/site/model"
)

var (
	// ErrAlreadyRunning: the site is being crawled right now.
	ErrAlreadyRunning = errors.New("site is already being crawled")
	// ErrBusy: the maximum number of concurrent site crawls is reached.
	ErrBusy = errors.New("maximum number of concurrent crawls reached")
)

// Lookups the runner needs from other modules.
type (
	SiteTracker interface {
		RecordSuccess(ctx context.Context, site sitemodel.Site, at time.Time) error
		RecordFailure(ctx context.Context, site sitemodel.Site, at time.Time, cause error) error
	}
	CountryGetter interface {
		Get(ctx context.Context, id int64) (countrymodel.Country, error)
	}
	OrganizationGetter interface {
		Get(ctx context.Context, id int64) (organizationmodel.Organization, error)
	}
	TaxonomySource interface {
		Active(ctx context.Context) ([]categorymodel.Category, error)
	}
)

type RunnerDeps struct {
	DB               *sql.DB
	Sites            SiteTracker
	Countries        CountryGetter
	Organizations    OrganizationGetter
	Categories       TaxonomySource
	Crawler          *crawler.Crawler
	Normalizer       *llmservice.Normalizer
	TaxonomySettings llmmodel.TaxonomySettings
	Filter           extractor.Filter
	LLMWorkers       int
	MaxSites         int
	Log              *slog.Logger
}

// Runner executes crawl runs in the background: at most MaxSites at once and
// never two for the same site. Its context is the application lifetime;
// cancelling it stops all runs.
type Runner struct {
	deps    RunnerDeps
	runs    *crawlrepo.Repo
	pages   *pagerepo.Repo
	ctx     context.Context
	sem     chan struct{}
	mu      sync.Mutex
	running map[int64]bool
	wg      sync.WaitGroup
}

func NewRunner(ctx context.Context, deps RunnerDeps) *Runner {
	return &Runner{
		deps:    deps,
		runs:    crawlrepo.New(deps.DB),
		pages:   pagerepo.New(deps.DB),
		ctx:     ctx,
		sem:     make(chan struct{}, deps.MaxSites),
		running: map[int64]bool{},
	}
}

// Start creates the crawl run row and crawls the site in the background.
// It returns ErrAlreadyRunning or ErrBusy instead of waiting.
func (r *Runner) Start(site sitemodel.Site) (model.CrawlRun, error) {
	release, err := r.acquire(r.ctx, site.ID, false)
	if err != nil {
		return model.CrawlRun{}, err
	}
	run, err := r.runs.Create(r.ctx, site.ID, nil, time.Now())
	if err != nil {
		release()
		return run, err
	}
	r.wg.Go(func() {
		defer release()
		r.execute(r.ctx, site, run)
	})
	return run, nil
}

// Run crawls the site in the calling goroutine and returns the finished run.
// Unlike Start it waits for a free crawl slot (until ctx ends), and ctx also
// cancels the crawl itself. batchID links the run to a crawl batch.
func (r *Runner) Run(ctx context.Context, site sitemodel.Site, batchID *int64) (model.CrawlRun, error) {
	release, err := r.acquire(ctx, site.ID, true)
	if err != nil {
		return model.CrawlRun{}, err
	}
	defer release()
	run, err := r.runs.Create(ctx, site.ID, batchID, time.Now())
	if err != nil {
		return run, err
	}
	return r.execute(ctx, site, run), nil
}

// acquire marks the site as running and takes one of the MaxSites crawl
// slots. With wait it queues for a slot until ctx ends; without, a full
// runner is ErrBusy. The site stays marked while it waits, so nothing else
// starts it meanwhile. release frees both.
func (r *Runner) acquire(ctx context.Context, siteID int64, wait bool) (release func(), err error) {
	r.mu.Lock()
	if r.running[siteID] {
		r.mu.Unlock()
		return nil, ErrAlreadyRunning
	}
	r.running[siteID] = true
	r.mu.Unlock()

	unmark := func() {
		r.mu.Lock()
		delete(r.running, siteID)
		r.mu.Unlock()
	}
	if wait {
		took := false
		select {
		case r.sem <- struct{}{}:
			took = true
		case <-ctx.Done():
		}
		if err := ctx.Err(); err != nil { // select picks at random when both are ready
			if took {
				<-r.sem
			}
			unmark()
			return nil, err
		}
	} else {
		select {
		case r.sem <- struct{}{}:
		default:
			unmark()
			return nil, ErrBusy
		}
	}
	return func() {
		unmark()
		<-r.sem
	}, nil
}

// Wait blocks until every started run has finished.
func (r *Runner) Wait() { r.wg.Wait() }

// pageJob is a new or changed page waiting for the LLM.
type pageJob struct {
	page    pagemodel.Page
	change  pageservice.Change
	url     string
	content extractor.Content
	hash    []byte
}

// run holds what one crawl run needs while it is executing.
type run struct {
	site     sitemodel.Site
	runID    int64
	country  countrymodel.Country
	org      organizationmodel.Organization
	taxonomy *llmmodel.Taxonomy
	counters model.LiveCounters
	jobs     chan pageJob
}

// execute crawls one site and closes its run row; it never panics. Panics
// are handled in three layers: a page (handlePageSafe, and the crawler's own
// worker guard) fails only that page, an LLM job (processJob) fails only that
// page, and anything else in the crawl (crawlSite) fails the run. The guard
// here covers the bookkeeping, so a run row can never stay 'running'.
func (r *Runner) execute(ctx context.Context, site sitemodel.Site, crawlRun model.CrawlRun) model.CrawlRun {
	log := r.deps.Log.With("site_id", site.ID, "crawl_run_id", crawlRun.ID, "domain", site.Domain)
	result := crawlRun
	closed := false
	err := infrastructure.Protect(func() { result = r.executeRun(ctx, site, crawlRun, log, &closed) })
	if err == nil {
		return result
	}
	infrastructure.LogPanic(log, "crawl run panicked", err)
	if !closed {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		now, msg := time.Now(), err.Error()
		ferr := infrastructure.Protect(func() {
			if err := r.runs.Finish(finishCtx, crawlRun.ID, model.StatusFailed, now, result.Counters, &msg); err != nil {
				log.Error("could not finish crawl run", "error", err)
			}
		})
		if ferr != nil {
			infrastructure.LogPanic(log, "closing a panicked crawl run panicked too", ferr)
		}
		result.Status, result.FinishedAt, result.ErrorMessage = model.StatusFailed, &now, &msg
	}
	return result
}

func (r *Runner) executeRun(ctx context.Context, site sitemodel.Site, crawlRun model.CrawlRun, log *slog.Logger, closed *bool) model.CrawlRun {
	start := time.Now()
	log.Info("crawl started")

	rn, crawlErr := r.crawlSite(ctx, site, crawlRun.ID, log)
	var panicErr *infrastructure.PanicError
	if errors.As(crawlErr, &panicErr) {
		infrastructure.LogPanic(log, "crawl panicked, run failed", crawlErr)
	}

	var counters model.Counters
	if rn != nil {
		counters = rn.counters.Snapshot()
	}
	status := model.StatusCompleted
	switch {
	case ctx.Err() != nil:
		status = model.StatusCancelled
	case crawlErr != nil:
		status = model.StatusFailed
	case counters.PagesFailed > 0:
		status = model.StatusPartial
	}

	// Bookkeeping must survive shutdown, so it does not use the cancelled context.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	finished := time.Now()
	var errMsg *string
	if crawlErr != nil && status != model.StatusCancelled {
		msg := crawlErr.Error()
		errMsg = &msg
	}
	err := r.runs.Finish(finishCtx, crawlRun.ID, status, finished, counters, errMsg)
	if err != nil {
		log.Error("could not finish crawl run", "error", err)
	}
	*closed = true // even when Finish failed: a second attempt would fail the same way
	result := crawlRun
	result.Status, result.FinishedAt, result.Counters, result.ErrorMessage = status, &finished, counters, errMsg
	switch status {
	case model.StatusFailed:
		err = r.deps.Sites.RecordFailure(finishCtx, site, finished, crawlErr)
	case model.StatusCompleted, model.StatusPartial:
		err = r.deps.Sites.RecordSuccess(finishCtx, site, finished)
	default:
		err = nil // cancelled: leave the schedule as is, the site is picked up after restart
	}
	if err != nil {
		log.Error("could not update site crawl state", "error", err)
	}

	log.Info("crawl finished", "status", status, "duration", time.Since(start),
		"pages_found", counters.PagesFound, "pages_new", counters.PagesNew, "pages_changed", counters.PagesChanged,
		"pages_unchanged", counters.PagesUnchanged, "pages_skipped", counters.PagesSkipped,
		"pages_failed", counters.PagesFailed, "records_created", counters.RecordsCreated,
		"records_updated", counters.RecordsUpdated, "error", crawlErr)
	return result
}

// crawlSite runs prepare → crawl → LLM workers. A panic in this goroutine
// (prepare, robots.txt, sitemaps) becomes the run's error; panics in the
// crawl and LLM workers are handled where they happen and fail one page.
func (r *Runner) crawlSite(ctx context.Context, site sitemodel.Site, runID int64, log *slog.Logger) (rn *run, err error) {
	defer infrastructure.RecoverAsError(&err)
	if rn, err = r.prepare(ctx, site, runID); err != nil {
		return rn, err
	}
	var workers sync.WaitGroup
	for range r.deps.LLMWorkers {
		workers.Go(func() {
			for job := range rn.jobs {
				r.processJob(ctx, rn, job, log)
			}
		})
	}
	// Deferred so it also runs after a panic, and before RecoverAsError
	// (defers run last-in first-out): every LLM worker has exited on return.
	defer func() {
		close(rn.jobs)
		workers.Wait()
	}()
	_, err = r.deps.Crawler.Crawl(ctx, crawler.Target{Domain: site.Domain, BaseURL: site.BaseURL},
		func(ctx context.Context, res *crawler.FetchResult) []string {
			return r.handlePageSafe(ctx, rn, res, log)
		})
	return rn, err
}

// handlePageSafe is handlePage behind a recover: a page that makes the
// extractor (or anything else) panic counts as failed and gives no links.
func (r *Runner) handlePageSafe(ctx context.Context, rn *run, res *crawler.FetchResult, log *slog.Logger) (links []string) {
	if err := infrastructure.Protect(func() { links = r.handlePage(ctx, rn, res, log) }); err != nil {
		rn.counters.PagesFailed.Add(1)
		infrastructure.LogPanic(log, "page handler panicked, page failed", err, "url", res.URL)
		return nil
	}
	return links
}

// processJob normalizes and stores one page. A panic fails only that page and
// the worker keeps reading the queue; a dead worker could block the crawl,
// which waits for room in the queue.
func (r *Runner) processJob(ctx context.Context, rn *run, job pageJob, log *slog.Logger) {
	if err := infrastructure.Protect(func() { r.normalizeAndStore(ctx, rn, job, log) }); err != nil {
		rn.counters.PagesFailed.Add(1)
		infrastructure.LogPanic(log, "llm worker panicked, page failed", err, "page_id", job.page.ID, "url", job.url)
		// guarded too: whatever broke may break this call as well, and the worker must survive
		if err := infrastructure.Protect(func() { r.pages.SetStatus(ctx, job.page.ID, pagemodel.StatusFailed) }); err != nil {
			infrastructure.LogPanic(log, "could not mark page failed", err, "page_id", job.page.ID)
		}
	}
}

func (r *Runner) prepare(ctx context.Context, site sitemodel.Site, runID int64) (*run, error) {
	country, err := r.deps.Countries.Get(ctx, site.CountryID)
	if err != nil {
		return nil, err
	}
	org, err := r.deps.Organizations.Get(ctx, site.OrganizationID)
	if err != nil {
		return nil, err
	}
	cats, err := r.deps.Categories.Active(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]llmmodel.Entry, len(cats))
	for i, c := range cats {
		entries[i] = llmmodel.Entry{ID: c.ID, ParentID: c.ParentID, Code: c.Code, Name: c.Name}
		if c.Description != nil {
			entries[i].Description = *c.Description
		}
	}
	taxonomy, err := llmmodel.NewTaxonomy(entries, r.deps.TaxonomySettings)
	if err != nil {
		return nil, err
	}
	return &run{
		site: site, runID: runID, country: country, org: org, taxonomy: taxonomy,
		jobs: make(chan pageJob, r.deps.LLMWorkers*2),
	}, nil
}

// handlePage runs fetch result → extract → filter → hash → dedup. Only new
// or changed text is queued for the LLM; everything else stops here.
func (r *Runner) handlePage(ctx context.Context, rn *run, res *crawler.FetchResult, log *slog.Logger) []string {
	rn.counters.PagesFound.Add(1)
	log = log.With("url", res.URL)

	if res.SkipReason != "" {
		rn.counters.PagesSkipped.Add(1)
		log.Debug("page skipped", "reason", res.SkipReason)
		return nil
	}

	obs := pagemodel.Observation{SiteID: rn.site.ID, URL: res.URL, URLHash: crawler.HashURL(res.URL), At: time.Now()}
	if res.StatusCode != 0 {
		status := res.StatusCode
		obs.HTTPStatus = &status
	}

	if res.Err != nil {
		rn.counters.PagesFailed.Add(1)
		log.Debug("page fetch failed", "http_status", res.StatusCode, "error", res.Err)
		if res.StatusCode != 0 {
			if p, _, err := r.pages.Observe(ctx, obs); err == nil {
				r.pages.SetStatus(ctx, p.ID, pagemodel.StatusFailed)
			}
		}
		return nil
	}

	pageURL, _ := url.Parse(res.FinalURL)
	content := extractor.Extract(res.Body, res.ContentType, pageURL)
	var links []string
	if !content.NoFollow {
		links = content.Links
	}
	if content.Language != "" {
		obs.LanguageCode = &content.Language
	}

	page, _, err := r.pages.Observe(ctx, obs)
	if err != nil {
		rn.counters.PagesFailed.Add(1)
		log.Error("could not store page", "error", err)
		return links
	}

	if ok, reason := r.deps.Filter.Check(content); !ok {
		rn.counters.PagesSkipped.Add(1)
		r.pages.SetStatus(ctx, page.ID, pagemodel.StatusSkipped)
		log.Debug("page text skipped", "page_id", page.ID, "reason", reason)
		return links
	}

	hash := infrastructure.SHA256(content.Text)
	change := pageservice.Classify(page.ContentHash, hash)
	switch change {
	case pageservice.ChangeUnchanged:
		rn.counters.PagesUnchanged.Add(1)
		r.pages.SetStatus(ctx, page.ID, pagemodel.StatusUnchanged)
		return links
	case pageservice.ChangeNew:
		rn.counters.PagesNew.Add(1)
	case pageservice.ChangeChanged:
		rn.counters.PagesChanged.Add(1)
	}

	select {
	case rn.jobs <- pageJob{page: page, change: change, url: res.URL, content: content, hash: hash}:
	case <-ctx.Done():
	}
	return links
}

// normalizeAndStore sends one page to the LLM and stores the validated crawling
// data and the new page content hash in one transaction.
func (r *Runner) normalizeAndStore(ctx context.Context, rn *run, job pageJob, log *slog.Logger) {
	log = log.With("page_id", job.page.ID, "url", job.url)
	result, err := r.deps.Normalizer.Normalize(ctx, llmmodel.NormalizeInput{
		Country:      llmmodel.NamedRef{ID: rn.country.ID, Name: rn.country.Name},
		Organization: llmmodel.NamedRef{ID: rn.org.ID, Name: rn.org.Name},
		SourceURL:    job.url,
		PageTitle:    job.content.Title,
		Content:      job.content.Text,
	}, rn.taxonomy)
	if err != nil {
		if ctx.Err() == nil {
			rn.counters.PagesFailed.Add(1)
			r.pages.SetStatus(ctx, job.page.ID, pagemodel.StatusFailed)
			log.Warn("page not normalized", "error", err)
		}
		return
	}

	data := buildCrawlingData(rn, job, result)
	var created bool
	err = infrastructure.InTx(ctx, r.deps.DB, func(tx *sql.Tx) error {
		var err error
		if _, created, err = crawlingdatarepo.New(tx).Upsert(ctx, data); err != nil {
			return err
		}
		return pagerepo.New(tx).MarkProcessed(ctx, job.page.ID, job.hash, job.change.Status())
	})
	if err != nil {
		if ctx.Err() == nil {
			rn.counters.PagesFailed.Add(1)
			log.Error("could not store crawling data", "error", err)
		}
		return
	}
	if created {
		rn.counters.RecordsCreated.Add(1)
	} else {
		rn.counters.RecordsUpdated.Add(1)
	}
}

func buildCrawlingData(rn *run, job pageJob, res llmmodel.Result) crawlingdatamodel.CrawlingData {
	content := res.Content
	if content == "" {
		content = job.content.Text // the source text is still the best content we have
	}
	language := res.Language
	if language == "" {
		language = job.content.Language
	}
	confidence := res.Confidence
	return crawlingdatamodel.CrawlingData{
		CountryID:      rn.site.CountryID,
		OrganizationID: rn.site.OrganizationID,
		SiteID:         rn.site.ID,
		PageID:         job.page.ID,
		CategoryID:     res.CategoryID,
		SubcategoryID:  res.SubcategoryID,
		RecordType:     res.RecordType,
		Title:          nonEmpty(res.Title),
		LanguageCode:   nonEmpty(language),
		PublishedAt:    res.PublishedAt,
		Content:        content,
		Summary:        nonEmpty(res.Summary),
		Confidence:     &confidence,
		ContentHash:    job.hash,
	}
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
