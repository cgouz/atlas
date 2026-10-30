# Running Atlas on a GPU server (NVIDIA B200)

This guide takes a fresh server from `git clone` to a running crawl:
Qwen3-8B on llama.cpp on the GPU, PostgreSQL in Docker, and the Atlas API on port 8090.

```text
                      ┌───────────────────── GPU server ─────────────────────┐
 official websites ◄──┤ Atlas (Go, :8090) ──► llama-server (:8080) ──► B200  │
                      │        │              Qwen3-8B Q8_0, 16 slots        │
                      │        ▼                                             │
                      │ PostgreSQL 17 (:5432, Docker)                        │
                      └──────────────────────────────────────────────────────┘
```

Every command below is run from the repository root unless it says otherwise.

---

## 1. Requirements

| What | Version | Check |
|---|---|---|
| NVIDIA driver with the B200 visible | any recent driver | `nvidia-smi` |
| CUDA toolkit | **12.8 or newer** (needed for Blackwell, `sm_100`) | `nvcc --version` |
| CMake and a C++ compiler | CMake ≥ 3.24, GCC ≥ 11 | `cmake --version`, `g++ --version` |
| Go | **1.27.3** or newer (see `go.mod`) | `go version` |
| Docker | any | `docker ps` |
| Hugging Face CLI | any | `huggingface-cli --version` (`pip install -U "huggingface_hub[cli]"`) |
| Disk | ~10 GB for the model, ~1 GB for the llama.cpp build, plus the database | `df -h .` |

If `nvcc` is not found but CUDA is installed, add it to the path:

```sh
export PATH=/usr/local/cuda/bin:$PATH
```

## 2. Clone

llama.cpp is a git submodule, so clone with `--recurse-submodules`:

```sh
git clone --recurse-submodules git@github.com:cgouz/atlas.git
cd atlas
```

If you already cloned without it:

```sh
git submodule update --init --recursive
```

## 3. Build llama-server

```sh
make llama-build
```

This builds `src/third_party/llama.cpp/build/bin/llama-server` with CUDA for the B200
(`CMAKE_CUDA_ARCHITECTURES=100`). It takes several minutes. When building on the GPU server itself,
`make llama-build CUDA_ARCH=native` also works.

Check:

```sh
src/third_party/llama.cpp/build/bin/llama-server --version
```

## 4. Download the model

The model file is not in git (`models/**` is ignored); only the empty folder is.

```sh
huggingface-cli download Qwen/Qwen3-8B-GGUF Qwen3-8B-Q8_0.gguf --local-dir models/qwen3-8b
ls -lh models/qwen3-8b/Qwen3-8B-Q8_0.gguf      # about 8.7 GB
```

To use another file, change all three together:
`LLAMA_MODEL` / `LLAMA_ALIAS` in the `Makefile` (or on the `make` command line), and
`LLM_MODEL_PATH` / `LLM_MODEL` in `src/resources/env/llamacpp.env`.

## 5. Start llama-server

```sh
make llama-run
```

It serves on `127.0.0.1:8080` with every layer on the GPU, flash attention, 16 parallel slots,
a shared KV cache of 524 288 tokens (32 768 per request, ~77 GB) and thinking turned off.
See the comments above `llama-run` in the `Makefile` for every flag.

Run it in `tmux` (or as a service, see [section 10](#10-run-as-services)) so it keeps running after you log out.

Check, from a second terminal:

```sh
curl -s localhost:8080/health          # {"status":"ok"} once the model is loaded
curl -s localhost:8080/v1/models       # lists "qwen3-8b-q8_0"
nvidia-smi                             # llama-server uses ~90 GB of GPU memory
```

Tuning, if needed:

| Want | Run |
|---|---|
| More parallel pages | `make llama-run LLAMA_SLOTS=32` **and** set `LLM_WORKERS=32` in `llamacpp.env` |
| Less GPU memory | `make llama-run LLAMA_SLOT_CTX=16384` |
| Reach it from another machine | `make llama-run LLAMA_HOST=0.0.0.0` (it has no authentication: firewall it) |

## 6. Start PostgreSQL

```sh
docker run -d --name atlas-postgres --restart unless-stopped \
  -e POSTGRES_DB=atlas -e POSTGRES_USER=atlas -e POSTGRES_PASSWORD=atlas \
  -p 127.0.0.1:5432:5432 \
  -v atlas-pgdata:/var/lib/postgresql/data \
  postgres:17-alpine

docker exec atlas-postgres pg_isready -U atlas -d atlas
```

The user, password, database and port match `src/resources/env/llamacpp.env`.
**Change the password** for anything beyond a test server, in both places.

If port 5432 is already taken, publish another one (`-p 127.0.0.1:5434:5432`) and set `DB_PORT=5434`
in `llamacpp.env`.

## 7. Start Atlas

```sh
make run-llamacpp
```

On start it:

1. applies the migrations in `src/resources/migrations/`,
2. loads the seed data (countries, organizations, categories, sites; only Uzbekistan because `SEED_COUNTRIES=UZ`),
3. loads the prompt and the output schema from `src/resources/prompts/`,
4. serves the API on `0.0.0.0:8090`.

You should see:

```text
level=INFO msg="migrations done" applied=9
level=INFO msg="seed applied" countries=250 ... sites=10
level=INFO msg="atlas starting" env=local llm_runtime=llamacpp llm_model=qwen3-8b-q8_0 scheduler=false ...
level=INFO msg="http server listening" addr=0.0.0.0:8090
```

Check:

```sh
curl -s localhost:8090/health                 # {"llm_runtime":"llamacpp","status":"ok"}
curl -s "localhost:8090/v1/sites?limit=3"
```

Swagger UI: `http://<server>:8090/swagger/index.html`

> **The API has no authentication** and listens on all interfaces (`HTTP_HOST=0.0.0.0`).
> Firewall port 8090, or set `HTTP_HOST=127.0.0.1` in `llamacpp.env` and use an SSH tunnel:
> `ssh -L 8090:localhost:8090 <server>`.

## 8. Run a crawl

The scheduler is **off** in `llamacpp.env` (`SCHEDULER_ENABLED=false`), so nothing is crawled until you ask.

**One site** (start small, e.g. `stat.uz`):

```sh
curl -s "localhost:8090/v1/sites?limit=50" | python3 -m json.tool | grep -E '"(id|domain)"'
curl -s -X POST localhost:8090/v1/sites/<id>/crawl            # 202, returns the crawl run
curl -s localhost:8090/v1/crawl-runs/<run id>                 # counters grow while status = running
```

**Every site of every active country** (one batch at a time):

```sh
curl -s -X POST localhost:8090/v1/crawl-batches                              # all active countries
curl -s -X POST localhost:8090/v1/crawl-batches -d '{"country_ids":[242]}'   # only Uzbekistan
curl -s localhost:8090/v1/crawl-batches/<id>                                 # progress per status
curl -s -X POST localhost:8090/v1/crawl-batches/<id>/cancel                  # stop it
```

**Results:**

```sh
curl -s "localhost:8090/v1/crawling-data?limit=5"          # list, newest first
curl -s localhost:8090/v1/crawling-data/<id>               # full content + llm_output
curl -s "localhost:8090/v1/pages?site_id=<id>&limit=20"    # what happened to each URL
```

**Automatic recrawls:** set `SCHEDULER_ENABLED=true` in `llamacpp.env` and restart Atlas. Each site is then
crawled every 6 hours (`crawl_interval_sec` of the site), a failed one after 30 minutes. After the first crawl
only new or changed pages go to the LLM.

Limits per site come from `src/resources/settings/crawler.json`: 500 pages per run, link depth 3,
2 requests per second.

## 9. Watch it work

| What | Where |
|---|---|
| Atlas log | the terminal of `make run-llamacpp` (or `journalctl -u atlas -f`) |
| Crawl progress | `GET /v1/crawl-runs?status=running`, `GET /v1/crawl-batches/<id>` |
| LLM throughput | `curl -s localhost:8080/metrics` (Prometheus), `GET localhost:8080/slots` |
| GPU | `watch -n 2 nvidia-smi` |
| Database | `docker exec -it atlas-postgres psql -U atlas -d atlas` |

Useful SQL:

```sql
SELECT status, count(*) FROM pages GROUP BY status;
SELECT c.code, count(*) FROM crawling_data d JOIN categories c ON c.id = d.category_id GROUP BY c.code ORDER BY 2 DESC;
SELECT id, status, pages_found, pages_new, pages_failed, records_created FROM crawl_runs ORDER BY id DESC LIMIT 10;
```

## 10. Run as services

For a server that should survive reboots, build Atlas once and run both processes under systemd.
Replace `/opt/atlas` with the clone path and `atlas` with the user that owns it.

```sh
go build -o bin/atlas ./src/main
```

`/etc/systemd/system/llama-server.service`:

```ini
[Unit]
Description=llama-server (Qwen3-8B for Atlas)
After=network.target

[Service]
User=atlas
WorkingDirectory=/opt/atlas
ExecStart=/usr/bin/make llama-run
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

`/etc/systemd/system/atlas.service`:

```ini
[Unit]
Description=Atlas API and crawler
After=network.target docker.service llama-server.service
Wants=llama-server.service

[Service]
User=atlas
WorkingDirectory=/opt/atlas
ExecStart=/opt/atlas/bin/atlas --env-file src/resources/env/llamacpp.env
Restart=on-failure
# Lets a running crawl close its bookkeeping on stop.
TimeoutStopSec=60

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now llama-server atlas
journalctl -u atlas -f
```

Atlas does not wait for llama-server to load the model; crawls started before it is ready fail their pages
and are retried on the next crawl. Start crawls after `curl localhost:8080/health` says ok.

## 11. Update to a new version

```sh
git pull --recurse-submodules
make llama-build                 # only if the llama.cpp submodule changed
go build -o bin/atlas ./src/main # if you run the built binary
sudo systemctl restart atlas     # or restart make run-llamacpp
```

New migrations are applied on start.
**Migrations that were edited after they were applied are not re-run.** If a release changed an existing
migration file (for example `009_create_crawling_data.sql`), either recreate the database (section 13) or
apply the difference by hand; the release notes say which.

## 12. Changing behaviour without a rebuild

`RESOURCES_DIR=src/resources` is set in `llamacpp.env`, so these files are read from disk on every start.
Edit, then restart Atlas:

| File | Controls |
|---|---|
| `src/resources/prompts/normalize_system.txt` | the instructions sent to the model |
| `src/resources/prompts/normalize_output.schema.json` | the JSON the model must return (must match the Go struct; Atlas refuses to start otherwise) |
| `src/resources/settings/taxonomy.json` | allowed record types, the unknown category code |
| `src/resources/settings/crawler.json` | pages per run, depth, request rate, skipped file types |
| `src/resources/seed/*.json` | countries, organizations, categories, sites |
| `src/resources/env/llamacpp.env` | ports, database, workers, scheduler, LLM limits |

## 13. Known issues and troubleshooting

| Symptom | Cause and fix |
|---|---|
| `make llama-build`: `nvcc not found` / `No CUDA toolset found` | CUDA is not on the path: `export PATH=/usr/local/cuda/bin:$PATH`, or install CUDA ≥ 12.8. |
| llama-server: `CUDA error: no kernel image is available` | Built for the wrong GPU: `rm -rf src/third_party/llama.cpp/build && make llama-build CUDA_ARCH=native`. |
| llama-server: `out of memory` | Lower `LLAMA_SLOT_CTX` or `LLAMA_SLOTS`. |
| Atlas: `connect: connection refused` on 5432 | PostgreSQL is not running or `DB_PORT` is wrong (section 6). |
| Atlas: `migration 0xx_...: ...` | The database has an older schema. On a test server, recreate it: `docker rm -f atlas-postgres && docker volume rm atlas-pgdata`, then section 6 again. |
| Pages fail with `llm status ...` or `connection refused` on 8080 | llama-server is not running or still loading the model. |
| Many pages fail with `llm output was cut off` | Pages are long: raise `LLM_MAX_TOKENS` in `llamacpp.env` (Atlas already retries without `content`). |
| A site gives almost no pages | It builds its content with JavaScript or blocks crawlers in `robots.txt`; Atlas reads only server-rendered HTML. |
| **Edits made with `PATCH /v1/countries`, `/organizations` or `/sites` are undone after a restart** | With `SEED_ON_START=true` the seed files are written back over existing rows on every start (and a site whose domain was changed is created again under its old domain). Until this is fixed, set `SEED_ON_START=false` after the first start, or make the same change in `src/resources/seed/*.json`. |
| An `inactive` organization's sites are still crawled | Organization status is informational for now; set the sites to `inactive` (`PATCH /v1/sites/<id>`). |

## 14. Stop everything

```sh
# Ctrl+C in the terminals of make run-llamacpp and make llama-run, or:
sudo systemctl stop atlas llama-server
docker stop atlas-postgres          # data stays in the atlas-pgdata volume
```
