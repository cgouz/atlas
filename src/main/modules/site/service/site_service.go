package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/cgouz/atlas/src/main/common/crawler"
	"github.com/cgouz/atlas/src/main/infrastructure"
	"github.com/cgouz/atlas/src/main/modules/site/model"
	"github.com/cgouz/atlas/src/main/modules/site/repo"
)

// maxErrorLen keeps last_error readable; full errors are in the logs.
const maxErrorLen = 2000

// maxDomainLen is the size of sites.domain (VARCHAR(255)).
const maxDomainLen = 255

type Service struct {
	repo *repo.Repo
	now  func() time.Time
	// failureRetry is how long a failed site waits before the scheduler picks
	// it again (never longer than the site's own crawl interval).
	failureRetry time.Duration
}

func New(r *repo.Repo, failureRetry time.Duration) *Service {
	return &Service{repo: r, now: time.Now, failureRetry: failureRetry}
}

func (s *Service) List(ctx context.Context, f model.ListFilter) ([]model.Site, error) {
	return s.repo.List(ctx, f)
}

func (s *Service) Get(ctx context.Context, id int64) (model.Site, error) {
	return s.repo.Get(ctx, id)
}

// Update checks and stores a new domain, base URL and/or status. The base
// URL must stay on the site's domain (the one being set, or the current one),
// because the crawler never leaves it.
func (s *Service) Update(ctx context.Context, id int64, u model.Update) (model.Site, error) {
	if u.Domain == nil && u.BaseURL == nil && u.Status == nil {
		return model.Site{}, fmt.Errorf("%w: nothing to update: send domain, base_url and/or status", infrastructure.ErrInvalidInput)
	}
	if u.Status != nil {
		status := strings.ToLower(strings.TrimSpace(*u.Status))
		if status != model.StatusActive && status != model.StatusInactive {
			return model.Site{}, fmt.Errorf("%w: status must be %q or %q",
				infrastructure.ErrInvalidInput, model.StatusActive, model.StatusInactive)
		}
		u.Status = &status
	}
	if u.Domain != nil || u.BaseURL != nil {
		current, err := s.repo.Get(ctx, id)
		if err != nil {
			return model.Site{}, err
		}
		domain, baseURL := current.Domain, current.BaseURL
		if u.Domain != nil {
			if domain, err = NormalizeDomain(*u.Domain); err != nil {
				return model.Site{}, err
			}
			u.Domain = &domain
		}
		if u.BaseURL != nil {
			baseURL = strings.TrimSpace(*u.BaseURL)
			u.BaseURL = &baseURL
		}
		if err := CheckBaseURL(baseURL, domain); err != nil {
			return model.Site{}, err
		}
	}
	return s.repo.Update(ctx, id, u)
}

// NormalizeDomain lower-cases a host name and drops a trailing dot and a
// leading "www." (the crawler treats domain and www.domain as the same site).
// Internationalized domains must be given in punycode (xn--...).
func NormalizeDomain(raw string) (string, error) {
	d := strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), "."), "www.")
	invalid := fmt.Errorf("%w: domain must be a host name like gov.uz, without scheme or path", infrastructure.ErrInvalidInput)
	if d == "" || len(d) > maxDomainLen || !strings.Contains(d, ".") {
		return "", invalid
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", invalid
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", invalid
			}
		}
	}
	return d, nil
}

// CheckBaseURL requires an absolute http(s) URL whose host is domain or www.domain.
func CheckBaseURL(baseURL, domain string) error {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: base_url must be an absolute http or https URL", infrastructure.ErrInvalidInput)
	}
	if !crawler.NewScope(domain).Allows(u) {
		return fmt.Errorf("%w: base_url host %q is not on domain %q (send base_url together with a new domain)",
			infrastructure.ErrInvalidInput, u.Hostname(), domain)
	}
	return nil
}

func (s *Service) Due(ctx context.Context, limit int) ([]model.Site, error) {
	return s.repo.Due(ctx, s.now(), limit)
}

// Crawlable returns every active official site of an active country,
// ignoring next_crawl_at. countryIDs narrows it to those countries (empty = all).
func (s *Service) Crawlable(ctx context.Context, countryIDs []int64) ([]model.Site, error) {
	return s.repo.Crawlable(ctx, countryIDs)
}

// RecordSuccess sets last_crawled_at = last_success_at = at and schedules the
// next crawl one interval later.
func (s *Service) RecordSuccess(ctx context.Context, site model.Site, at time.Time) error {
	return s.repo.MarkSuccess(ctx, site.ID, at, NextCrawlAt(at, site.CrawlInterval()))
}

// RecordFailure stores the error and schedules a retry, so one broken site is
// neither hammered every scheduler tick nor dropped.
func (s *Service) RecordFailure(ctx context.Context, site model.Site, at time.Time, cause error) error {
	msg := cause.Error()
	if len(msg) > maxErrorLen {
		msg = msg[:maxErrorLen]
	}
	return s.repo.MarkFailure(ctx, site.ID, at, RetryAt(at, site.CrawlInterval(), s.failureRetry), msg)
}

// NextCrawlAt is the next scheduled crawl after a successful one.
func NextCrawlAt(at time.Time, interval time.Duration) time.Time {
	return at.Add(interval)
}

// RetryAt is the next attempt after a failure: the retry delay, capped by the
// site's normal crawl interval.
func RetryAt(at time.Time, interval, retry time.Duration) time.Time {
	if retry <= 0 || retry > interval {
		retry = interval
	}
	return at.Add(retry)
}
