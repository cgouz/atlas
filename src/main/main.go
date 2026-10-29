// Command atlas crawls official government websites, normalizes their text
// with an LLM into a fixed taxonomy and stores the crawling data in PostgreSQL.
//
// main only wires dependencies and starts the application; the logic lives
// in the modules.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	httpSwagger "github.com/swaggo/http-swagger/v2"

	"github.com/cgouz/atlas/src/main/app"
	crawlerservice "github.com/cgouz/atlas/src/main/common/crawler"
	extractorservice "github.com/cgouz/atlas/src/main/common/extractor"
	_ "github.com/cgouz/atlas/src/main/docs" // registers the generated Swagger spec
	"github.com/cgouz/atlas/src/main/infrastructure"
	categoryhandler "github.com/cgouz/atlas/src/main/modules/category/handler"
	categoryrepo "github.com/cgouz/atlas/src/main/modules/category/repo"
	categoryservice "github.com/cgouz/atlas/src/main/modules/category/service"
	countryhandler "github.com/cgouz/atlas/src/main/modules/country/handler"
	countryrepo "github.com/cgouz/atlas/src/main/modules/country/repo"
	countryservice "github.com/cgouz/atlas/src/main/modules/country/service"
	crawlhandler "github.com/cgouz/atlas/src/main/modules/crawl/handler"
	crawlrepo "github.com/cgouz/atlas/src/main/modules/crawl/repo"
	crawlservice "github.com/cgouz/atlas/src/main/modules/crawl/service"
	crawlingdatahandler "github.com/cgouz/atlas/src/main/modules/crawling_data/handler"
	crawlingdatarepo "github.com/cgouz/atlas/src/main/modules/crawling_data/repo"
	crawlingdataservice "github.com/cgouz/atlas/src/main/modules/crawling_data/service"
	llmservice "github.com/cgouz/atlas/src/main/modules/llm/service"
	organizationhandler "github.com/cgouz/atlas/src/main/modules/organization/handler"
	organizationrepo "github.com/cgouz/atlas/src/main/modules/organization/repo"
	organizationservice "github.com/cgouz/atlas/src/main/modules/organization/service"
	orgtypehandler "github.com/cgouz/atlas/src/main/modules/organization_type/handler"
	orgtyperepo "github.com/cgouz/atlas/src/main/modules/organization_type/repo"
	orgtypeservice "github.com/cgouz/atlas/src/main/modules/organization_type/service"
	pagehandler "github.com/cgouz/atlas/src/main/modules/page/handler"
	pagerepo "github.com/cgouz/atlas/src/main/modules/page/repo"
	pageservice "github.com/cgouz/atlas/src/main/modules/page/service"
	sitehandler "github.com/cgouz/atlas/src/main/modules/site/handler"
	siterepo "github.com/cgouz/atlas/src/main/modules/site/repo"
	siteservice "github.com/cgouz/atlas/src/main/modules/site/service"
	"github.com/cgouz/atlas/src/resources"
)

// @title           Atlas API
// @version         1.0
// @description     Atlas crawls official government websites, turns every page into structured
// @description     crawling data with an LLM (category, record type, title, summary, ...) and serves it here.
// @description
// @description     Reference data (countries, organizations, categories, sites) is read-only and comes from
// @description     the seed files. Crawls can be started for one site or as a batch over whole countries.
// @description
// @description     List endpoints use cursor paging: pass the returned next_cursor as ?cursor= to get the
// @description     next page; next_cursor is null on the last page.
// @BasePath        /
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "atlas:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := infrastructure.LoadConfig(os.Args[1:])
	if err != nil {
		return err
	}
	log := infrastructure.NewLogger(cfg.AppEnv, cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	resources, err := resourcesFS(cfg.ResourcesDir)
	if err != nil {
		return err
	}
	settings, err := app.LoadSettings(resources)
	if err != nil {
		return err
	}
	llmClient, err := llmservice.NewClient(cfg.LLM.Runtime, llmservice.ClientConfig{
		BaseURL: cfg.LLM.BaseURL, Model: cfg.LLM.Model, APIKey: cfg.LLM.APIKey,
		Timeout: cfg.LLM.Timeout, MaxTokens: cfg.LLM.MaxTokens, SystemPrompt: settings.SystemPrompt,
	})
	if err != nil {
		return err
	}

	db, err := infrastructure.OpenPostgres(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := prepareDatabase(ctx, cfg, db, resources, log); err != nil {
		return err
	}

	// Repositories and services.
	countries := countryservice.New(countryrepo.New(db))
	orgTypes := orgtypeservice.New(orgtyperepo.New(db))
	organizations := organizationservice.New(organizationrepo.New(db))
	categories := categoryservice.New(categoryrepo.New(db))
	sites := siteservice.New(siterepo.New(db), cfg.Scheduler.FailureRetry)
	pages := pageservice.New(pagerepo.New(db))
	crawlingData := crawlingdataservice.New(crawlingdatarepo.New(db))

	// Crawl pipeline.
	crawler := crawlerservice.New(settings.Crawler, crawlerservice.Options{
		Workers: cfg.Crawler.Workers, Timeout: cfg.Crawler.Timeout,
		MaxBodyBytes: cfg.Crawler.MaxBodyBytes, UserAgent: cfg.Crawler.UserAgent,
	}, log)
	normalizer := llmservice.NewNormalizer(llmClient, settings.OutputSchema, cfg.LLM.Workers, cfg.LLM.MaxRetries, cfg.LLM.MaxInputChars, log)
	runner := crawlservice.NewRunner(ctx, crawlservice.RunnerDeps{
		DB: db, Sites: sites, Countries: countries, Organizations: organizations, Categories: categories,
		Crawler: crawler, Normalizer: normalizer, TaxonomySettings: settings.Taxonomy,
		Filter:     extractorservice.Filter{MinChars: settings.Crawler.MinTextChars, MinWords: settings.Crawler.MinTextWords},
		LLMWorkers: cfg.LLM.Workers, MaxSites: cfg.Crawl.MaxSites, Log: log,
	})
	crawls := crawlservice.New(crawlrepo.New(db), sites, runner)
	batches := crawlservice.NewBatchService(ctx, crawlrepo.NewBatchRepo(db), sites, runner, cfg.Crawl.MaxSitesPerCountry, log)

	// HTTP API.
	mux := http.NewServeMux()
	app.RegisterHealth(mux, db, llmClient.Runtime())
	mux.Handle("GET /swagger/", httpSwagger.Handler(httpSwagger.URL("/swagger/doc.json")))
	countryhandler.New(countries, log).Register(mux)
	orgtypehandler.New(orgTypes, log).Register(mux)
	organizationhandler.New(organizations, log).Register(mux)
	categoryhandler.New(categories, log).Register(mux)
	sitehandler.New(sites, log).Register(mux)
	pagehandler.New(pages, log).Register(mux)
	crawlhandler.New(crawls, batches, log).Register(mux)
	crawlingdatahandler.New(crawlingData, log).Register(mux)

	log.Info("atlas starting", "env", cfg.AppEnv, "llm_runtime", llmClient.Runtime(), "llm_model", llmClient.Model(),
		"scheduler", cfg.Scheduler.Enabled, "crawl_max_sites", cfg.Crawl.MaxSites,
		"crawl_max_sites_per_country", cfg.Crawl.MaxSitesPerCountry)

	var wg sync.WaitGroup
	if cfg.Scheduler.Enabled {
		scheduler := app.NewScheduler(sites, runner, cfg.Scheduler.Interval, cfg.Scheduler.MaxSites, log)
		wg.Go(func() { scheduler.Run(ctx) })
	}
	serverErr := app.RunServer(ctx, cfg.HTTPAddr(), app.WithLogging(app.WithRecovery(mux, log), log), log)
	stop() // the server can also stop on its own (e.g. port in use); stop everything else too

	// Order matters: the server is down (no new batch can start), then the
	// scheduler, then a running batch, then the runs started outside a batch.
	wg.Wait()
	batches.Wait()
	runner.Wait()
	log.Info("atlas stopped")
	return serverErr
}

func prepareDatabase(ctx context.Context, cfg infrastructure.Config, db *sql.DB, resources fs.FS, log *slog.Logger) error {
	if cfg.MigrateOnStart {
		migrations, err := fs.Sub(resources, "migrations")
		if err != nil {
			return err
		}
		n, err := infrastructure.Migrate(ctx, db, migrations, log)
		if err != nil {
			return err
		}
		log.Info("migrations done", "applied", n)
	}
	if cfg.SeedOnStart {
		data, err := app.LoadSeed(resources)
		if err != nil {
			return err
		}
		if err := data.KeepCountries(cfg.SeedCountries); err != nil {
			return err
		}
		if err := app.ApplySeed(ctx, db, data, log); err != nil {
			return err
		}
	}
	if n, err := crawlrepo.New(db).FailInterrupted(ctx); err != nil {
		return err
	} else if n > 0 {
		log.Warn("closed crawl runs interrupted by a previous stop", "count", n)
	}
	if n, err := crawlrepo.NewBatchRepo(db).FailInterrupted(ctx); err != nil {
		return err
	} else if n > 0 {
		log.Warn("closed crawl batches interrupted by a previous stop", "count", n)
	}
	return nil
}

// resourcesFS returns the resources folder on disk when dir is set,
// otherwise the copy embedded in the binary.
func resourcesFS(dir string) (fs.FS, error) {
	if dir != "" {
		if _, err := os.Stat(dir); err != nil {
			return nil, fmt.Errorf("RESOURCES_DIR: %w", err)
		}
		return os.DirFS(dir), nil
	}
	return resources.FS, nil
}
