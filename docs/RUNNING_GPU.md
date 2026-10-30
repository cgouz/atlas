# Running Atlas on a GPU server (NVIDIA B200)

This guide takes a fresh server from `git clone` to a running crawl:
Qwen3-8B on llama.cpp on the GPU, PostgreSQL (no Docker), and the Atlas API on port 8090.

```text
                      ┌───────────────────── GPU server ─────────────────────┐
 official websites ◄──┤ Atlas (Go, :8090) ──► llama-server (:8080) ──► B200  │
                      │        │              Qwen3-8B Q8_0, 16 slots        │
                      │        ▼                                             │
                      │ PostgreSQL (:5432, apt or /workspace)                │
                      └──────────────────────────────────────────────────────┘
```

Every command below is run from the repository root unless it says otherwise.

---

## Quick start in a Kubernetes pod

A pod usually has no Docker and no systemd, and only `/workspace` survives a restart.
[`scripts/pod.sh`](../scripts/pod.sh) does everything for that case: it installs PostgreSQL (no Docker),
Go and the Hugging Face CLI, builds llama.cpp, downloads the model and starts all three processes in the background.

PostgreSQL comes in two ways, chosen with `PG_MODE`:

| `PG_MODE` | How | After a pod restart |
|---|---|---|
| `apt` (default) | `apt-get install postgresql` and `service postgresql start`, as root | `scripts/pod.sh start` installs it again; **the database is empty** (crawl again) |
| `workspace` | PostgreSQL 17 via micromamba in `/workspace/pg`, data in `/workspace/pgdata`, also without root | everything is kept |

For a test run `apt` is enough; to keep crawled data across restarts, use `PG_MODE=workspace` for every command
(`export PG_MODE=workspace`).

```sh
git clone --recurse-submodules git@github.com:cgouz/atlas.git && cd atlas

scripts/pod.sh setup          # once: Postgres, Go, llama.cpp build, model download
scripts/pod.sh start          # Postgres + llama-server + Atlas; run again after every pod restart
scripts/pod.sh check          # database, a real answer from the GPU, every API list endpoint
scripts/pod.sh crawl stat.uz  # crawl one site and show what was stored
```

Also: `scripts/pod.sh status`, `scripts/pod.sh logs atlas|llama|postgres`, `scripts/pod.sh stop`.
Logs and pid files live in `/workspace/atlas-run`. Settings are at the top of the script
(`PG_MODE`, `WORKSPACE`, `PG_PORT`, `LLAMA_PORT`, `DB_PASSWORD`, `SKIP_LLAMA`, `HF_TOKEN`).
If port 8080 is taken in the pod, run with `LLAMA_PORT=8081`; Atlas is pointed at it automatically.

To reach Swagger from your laptop: `kubectl port-forward <pod> 8090:8090`, then open
`http://localhost:8090/swagger/index.html`.

The script still needs from the pod image: an NVIDIA driver and CUDA toolkit ≥ 12.8 (`nvcc`), `cmake`, `g++`,
`git`, `curl`, `tar`, `bzip2` and `python3`. It stops with a clear message when one is missing.

The rest of this guide explains each step by hand; section 10 (systemd services) only applies to a normal server.

---

## 1. Requirements

| What | Version | Check |
|---|---|---|
| NVIDIA driver with the B200 visible | any recent driver | `nvidia-smi` |
| CUDA toolkit | **12.8 or newer** (needed for Blackwell, `sm_100`) | `nvcc --version` |
| CMake and a C++ compiler | CMake ≥ 3.24, GCC ≥ 11 | `cmake --version`, `g++ --version` |
| Go | **1.27.3** or newer (see `go.mod`) | `go version` |
| Root (for the default apt PostgreSQL) | or use `PG_MODE=workspace` | `id -u` prints `0` |
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
| Reach it from another machine | `LLAMA_API_KEY=<key> make llama-run LLAMA_HOST=0.0.0.0` (see below) |

### llama-server on another machine, with an API key

When the GPU server only runs the model and Atlas runs elsewhere, protect llama-server with a key.
llama-server reads it from `LLAMA_API_KEY`; then every endpoint except `/health` requires
`Authorization: Bearer <key>`. Atlas sends exactly that header when `LLM_API_KEY` is set.

```sh
# a long random key, generated once
openssl rand -hex 32

# GPU server: serve on all interfaces, key required
LLAMA_API_KEY=<key> make llama-run LLAMA_HOST=0.0.0.0

# Atlas server: point Atlas at it with the same key (real environment variables override llamacpp.env)
LLM_BASE_URL=http://<gpu-host>:8080/v1 LLM_API_KEY=<key> make run-llamacpp

# or, in a pod, with the script (it checks the key before starting Atlas, and check/crawl send it)
LLM_URL=http://<gpu-host>:8080 LLAMA_API_KEY=<key> scripts/pod.sh start
LLM_URL=http://<gpu-host>:8080 LLAMA_API_KEY=<key> scripts/pod.sh check
```

Check from the Atlas side:

```sh
curl -s http://<gpu-host>:8080/health                                            # public: {"status":"ok"}
curl -s -H "Authorization: Bearer <key>" http://<gpu-host>:8080/v1/models         # lists qwen3-8b-q8_0
curl -s -o /dev/null -w '%{http_code}\n' http://<gpu-host>:8080/v1/models         # 401 without the key
```

The key travels in plain HTTP. Keep the traffic inside the cluster or a private network (or put llama-server
behind a TLS proxy), and do not commit the key: pass it as an environment variable, not in `llamacpp.env`.

## 6. Start PostgreSQL

No Docker needed. The script does it in one step: it installs PostgreSQL if it is missing, starts it and creates
the `atlas` user and database (UTF8):

```sh
scripts/pod.sh postgres                       # apt-get + "service postgresql start" (needs root)
PG_MODE=workspace scripts/pod.sh postgres     # or: PostgreSQL 17 in /workspace, kept across pod restarts, no root
```

Running it again is safe: it only starts what is not running and creates what is missing.

By hand, the same as the default mode (as root, Debian/Ubuntu):

```sh
apt-get update && apt-get install -y postgresql
service postgresql start
su postgres -c "psql -c \"CREATE USER atlas WITH PASSWORD 'atlas';\" \
  -c \"CREATE DATABASE atlas OWNER atlas ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0;\""

PGPASSWORD=atlas psql -h 127.0.0.1 -U atlas -d atlas -c 'SELECT 1'     # check the login
```

The user, password, database and port match `src/resources/env/llamacpp.env`.
**Change the password** for anything beyond a test server, in both places (`DB_PASSWORD=... scripts/pod.sh postgres`
sets it).

If port 5432 is already taken, use another one: `PG_PORT=5434 scripts/pod.sh postgres` and `DB_PORT=5434` in
`llamacpp.env`.

In apt mode the database lives in the pod's own filesystem (`/var/lib/postgresql`): after a pod restart it is
gone and `scripts/pod.sh postgres` (or `start`) installs an empty one. Use `PG_MODE=workspace` to keep it.

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
| Database | `PGPASSWORD=atlas psql -h 127.0.0.1 -U atlas -d atlas` |

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
After=network.target postgresql.service llama-server.service
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
| Atlas: `migration 0xx_...: ...` | The database has an older schema. On a test server, recreate it: `su postgres -c "psql -c 'DROP DATABASE atlas'"` (workspace mode: `scripts/pod.sh stop && rm -rf /workspace/pgdata`), then `scripts/pod.sh postgres` again. |
| Pages fail with `llm status ...` or `connection refused` on 8080 | llama-server is not running or still loading the model. |
| Many pages fail with `llm output was cut off` | Pages are long: raise `LLM_MAX_TOKENS` in `llamacpp.env` (Atlas already retries without `content`). |
| A site gives almost no pages | It builds its content with JavaScript or blocks crawlers in `robots.txt`; Atlas reads only server-rendered HTML. |
| **Edits made with `PATCH /v1/countries`, `/organizations` or `/sites` are undone after a restart** | With `SEED_ON_START=true` the seed files are written back over existing rows on every start (and a site whose domain was changed is created again under its old domain). Until this is fixed, set `SEED_ON_START=false` after the first start, or make the same change in `src/resources/seed/*.json`. |
| An `inactive` organization's sites are still crawled | Organization status is informational for now; set the sites to `inactive` (`PATCH /v1/sites/<id>`). |

## 14. Stop everything

```sh
# Ctrl+C in the terminals of make run-llamacpp and make llama-run, or:
sudo systemctl stop atlas llama-server
scripts/pod.sh stop                 # Atlas, llama-server and PostgreSQL
```
