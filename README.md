# atlas

```
 THREE WAYS TO START
 ┌──────────────────────┐  ┌──────────────────────────┐  ┌───────────────────────────────┐
 │ Scheduler (every 60s)│  │ POST /v1/sites/{id}/crawl│  │ POST /v1/crawl-batches        │
 │ sites with next_crawl│  │ one site, returns 202    │  │ all active sites of the active│
 │ _at in the past      │  │                          │  │ countries, 4 sites per country│
 └──────────┬───────────┘  └────────────┬─────────────┘  └──────────────┬────────────────┘
            └───────────────────────────┼───────────────────────────────┘
                                        ▼
 ┌────────────────────────────────────────────────────────────────────────────────────────┐
 │ RUNNER   max 8 sites at once (CRAWL_MAX_SITES), never the same site twice              │
 │          → inserts a crawl_runs row (status = running)                                 │
 │          → loads the country, the organization and the active categories (taxonomy)   │
 └──────────────────────────────────────┬─────────────────────────────────────────────────┘
                                        ▼
 ┌─ CRAWLER (common/crawler) ─────────────────────────────────────────────────────────────┐
 │  robots.txt ──► sitemaps (max 20, newest first) ──► queue of URLs (breadth-first)      │
 │                                                     max 500 pages, link depth 3        │
 │  URL filter: same domain or www.domain · allowed by robots · no .pdf/.jpg/.zip…        │
 │              · tracking parameters (utm_*…) removed                                    │
 │  4 fetch workers · 2 requests/s per site (lower if robots.txt sets Crawl-delay)        │
 │  fetch: 30s timeout · ≤5 redirects · 2 retries · ≤5 MB · HTML/XHTML/text/JSON only     │
 └──────────────────────────────────────┬─────────────────────────────────────────────────┘
                                        ▼   for every URL fetched
 ┌─ PAGE HANDLER (runner.handlePage) ─────────────────────────────────────────────────────┐
 │  fetch error / skipped ───────────────────────────────► page = failed / skipped  ✗     │
 │  EXTRACT: title, headings, main text, language, links, noindex/nofollow                │
 │  links ──► back into the crawler queue (unless nofollow)                               │
 │  pages: insert or update (url_hash, http_status, last_seen_at)                          │
 │  FILTER: <200 chars, <30 words, or noindex ───────────► page = skipped           ✗     │
 │  SHA-256(text) vs pages.content_hash:                                                  │
 │     same  ────────────────────────────────────────────► page = unchanged  (no LLM) ✗   │
 │     new / changed ──► LLM queue                                                        │
 └──────────────────────────────────────┬─────────────────────────────────────────────────┘
                                        ▼
 ┌─ LLM WORKERS (LLM_WORKERS, shared by all runs) ────────────────────────────────────────┐
 │  text cut to 12000 chars ──► prompt + JSON schema (allowed codes only)                 │
 │        ──► llama-server / vLLM (Qwen3-8B, thinking off)                                │
 │  VALIDATE: category exists · subcategory belongs to it · record_type allowed           │
 │  invalid / error ──► retry (×2; if the reply was cut off: retry without content) ──►   │
 │                      still bad ───────────────────────► page = failed            ✗     │
 └──────────────────────────────────────┬─────────────────────────────────────────────────┘
                                        ▼
 ┌─ STORE (one transaction) ──────────────────────────────────────────────────────────────┐
 │  crawling_data: insert, or update this page's row (one row per page)                   │
 │     country/org/site/page come from the DB, never from the LLM                         │
 │  pages: content_hash = new hash, status = new / changed                                │
 └──────────────────────────────────────┬─────────────────────────────────────────────────┘
                                        ▼
 ┌─ FINISH RUN ───────────────────────────────────────────────────────────────────────────┐
 │  crawl_runs: counters + status                                                         │
 │     completed (no failures) · partial (some pages failed) · failed · cancelled         │
 │  sites: success ──► next_crawl_at = now + 6h (crawl_interval_sec)                      │
 │         failure ──► next_crawl_at = now + 30 min (SCHEDULER_FAILURE_RETRY_SEC)         │
 └────────────────────────────────────────────────────────────────────────────────────────┘

```