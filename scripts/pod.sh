#!/usr/bin/env bash
# Sets up and runs the whole Atlas stack inside a Kubernetes GPU pod (no Docker,
# no systemd): PostgreSQL, llama-server (Qwen3-8B on the GPU) and Atlas.
#
# Everything that must survive a pod restart lives under $WORKSPACE
# (default /workspace): the Postgres install and data, Go, logs and pid files.
#
#   scripts/pod.sh setup            one time: install Postgres, Go, build llama.cpp, download the model
#   scripts/pod.sh start            start Postgres, llama-server and Atlas (again after every pod restart)
#   scripts/pod.sh check            end-to-end checks: database, LLM answer, Atlas API
#   scripts/pod.sh crawl stat.uz    crawl one site and show what was stored
#   scripts/pod.sh status | logs [postgres|llama|atlas] | stop
#
# Settings (environment variables):
#   WORKSPACE=/workspace     where tools, data and logs go
#   PG_PORT=5432             Postgres port (on 127.0.0.1)
#   DB_PASSWORD=atlas        password of the atlas database user
#   SKIP_LLAMA=1             do not build/start llama-server (use LLM_BASE_URL from the env file or environment)
#   HF_TOKEN=...             only if the model download needs it
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKSPACE="${WORKSPACE:-/workspace}"
RUN="$WORKSPACE/atlas-run"           # pids and logs
PG="$WORKSPACE/pg"                   # Postgres install (micromamba env)
PGDATA="$WORKSPACE/pgdata"
MAMBA="$WORKSPACE/bin/micromamba"
GO_DIR="$WORKSPACE/go"
GO_VERSION="1.27.3"
PG_PORT="${PG_PORT:-5432}"
PG_USER_UID=1001                     # fixed uid so $PGDATA ownership is valid after a pod restart
export DB_PASSWORD="${DB_PASSWORD:-atlas}"
export DB_PORT="$PG_PORT"
ENV_FILE="$ROOT/src/resources/env/llamacpp.env"
MODEL="$ROOT/models/qwen3-8b/Qwen3-8B-Q8_0.gguf"
LLAMA_SERVER="$ROOT/src/third_party/llama.cpp/build/bin/llama-server"
LLAMA_URL="http://127.0.0.1:8080"
ATLAS_URL="http://127.0.0.1:8090"

export PATH="$GO_DIR/bin:$PG/bin:/usr/local/cuda/bin:$PATH"
mkdir -p "$RUN"

say()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m ok\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mfail\033[0m %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# Postgres refuses to run as root; pods often are root, so it runs as user "pg".
as_pg() {
	if [ "$(id -u)" = 0 ]; then
		id pg >/dev/null 2>&1 || useradd -u "$PG_USER_UID" -m -s /bin/sh pg
		su pg -s /bin/sh -c "PATH='$PG/bin:\$PATH' $*"
	else
		sh -c "$*"
	fi
}

# wait_for URL SECONDS NAME: polls until the URL answers 200.
wait_for() {
	local url=$1 secs=$2 name=$3
	for _ in $(seq "$secs"); do
		curl -sf -o /dev/null "$url" && { ok "$name is up ($url)"; return 0; }
		sleep 1
	done
	fail "$name did not come up within ${secs}s; see: scripts/pod.sh logs ${name%% *}"
}

running() { [ -f "$RUN/$1.pid" ] && kill -0 "$(cat "$RUN/$1.pid")" 2>/dev/null; }

# start_bg NAME CMD...: runs CMD in its own process group so stop can end it with its children.
start_bg() {
	local name=$1; shift
	if running "$name"; then ok "$name already running (pid $(cat "$RUN/$name.pid"))"; return; fi
	(cd "$ROOT" && setsid nohup "$@" >"$RUN/$name.log" 2>&1 & echo $! >"$RUN/$name.pid")
	ok "$name started (pid $(cat "$RUN/$name.pid"), log $RUN/$name.log)"
}

stop_bg() {
	local name=$1
	if running "$name"; then
		kill -TERM -- "-$(cat "$RUN/$name.pid")" 2>/dev/null || true
		for _ in $(seq 30); do running "$name" || break; sleep 1; done
		ok "$name stopped"
	fi
	rm -f "$RUN/$name.pid"
}

# ---------------------------------------------------------------- setup

setup_tools() {
	say "Checking tools"
	have curl || fail "curl is missing (apt-get install -y curl)"
	have tar && have bzip2 || fail "tar/bzip2 are missing (apt-get install -y tar bzip2)"
	if [ -z "${SKIP_LLAMA:-}" ]; then
		have nvidia-smi || fail "nvidia-smi not found: is this a GPU pod?"
		nvidia-smi --query-gpu=name,memory.total --format=csv,noheader | sed 's/^/     GPU: /'
		have nvcc || fail "nvcc not found: install the CUDA toolkit >= 12.8 or add it to PATH"
		have cmake || fail "cmake is missing (apt-get install -y cmake, or pip install cmake)"
		have g++ || fail "g++ is missing (apt-get install -y build-essential)"
		have git || fail "git is missing"
	fi

	if ! have go; then
		say "Installing Go $GO_VERSION into $GO_DIR"
		curl -fsSL "https://go.dev/dl/go$GO_VERSION.linux-amd64.tar.gz" | tar -xz -C "$WORKSPACE"
	fi
	ok "$(go version)"

	if [ ! -x "$MAMBA" ]; then
		say "Installing micromamba into $WORKSPACE/bin"
		mkdir -p "$WORKSPACE/bin"
		curl -fsSL https://micro.mamba.pm/api/micromamba/linux-64/latest | tar -xj -C "$WORKSPACE" bin/micromamba
	fi
	if [ ! -x "$PG/bin/postgres" ]; then
		say "Installing PostgreSQL 17 and the Hugging Face CLI into $PG"
		"$MAMBA" create -y -q -r "$WORKSPACE/mamba" -p "$PG" -c conda-forge "postgresql=17" huggingface_hub
	fi
	ok "$("$PG/bin/postgres" --version)"
}

setup_postgres() {
	if [ -f "$PGDATA/PG_VERSION" ]; then ok "database cluster exists ($PGDATA)"; return; fi
	say "Creating the database cluster in $PGDATA"
	mkdir -p "$PGDATA"
	[ "$(id -u)" = 0 ] && { as_pg true; chown pg "$PGDATA"; }
	local pw="$WORKSPACE/.pgpw"
	printf '%s\n' "$DB_PASSWORD" >"$pw" && chmod 644 "$pw"
	# UTF8 + C locale explicitly: minimal images have no locales, and the default
	# would be SQL_ASCII, which mangles Uzbek, Russian and Japanese text.
	as_pg "initdb -D '$PGDATA' -U atlas --pwfile='$pw' --auth-local=trust --auth-host=scram-sha-256 -E UTF8 --locale=C >/dev/null"
	rm -f "$pw"
	start_postgres
	as_pg "createdb -h /tmp -p $PG_PORT -U atlas atlas"
	ok "database atlas created (user atlas)"
}

setup_llama() {
	[ -n "${SKIP_LLAMA:-}" ] && return
	say "Fetching the llama.cpp submodule"
	git -C "$ROOT" submodule update --init --recursive
	if [ -x "$LLAMA_SERVER" ]; then ok "llama-server already built"; else
		say "Building llama-server with CUDA (several minutes)"
		make -C "$ROOT" llama-build
	fi
	if [ -f "$MODEL" ]; then ok "model present ($(du -h "$MODEL" | cut -f1))"; return; fi
	say "Downloading Qwen3-8B Q8_0 (~8.7 GB)"
	if have hf; then hf download Qwen/Qwen3-8B-GGUF Qwen3-8B-Q8_0.gguf --local-dir "$(dirname "$MODEL")"
	else huggingface-cli download Qwen/Qwen3-8B-GGUF Qwen3-8B-Q8_0.gguf --local-dir "$(dirname "$MODEL")"; fi
	[ -f "$MODEL" ] || fail "model download failed"
	ok "model downloaded"
}

# ---------------------------------------------------------------- start / stop

start_postgres() {
	if as_pg "pg_ctl -D '$PGDATA' status" >/dev/null 2>&1; then ok "postgres already running"; return; fi
	touch "$RUN/postgres.log"
	[ "$(id -u)" = 0 ] && chown pg "$RUN/postgres.log" # postgres runs as pg and writes its own log
	# mmap: pods often have only 64 MB of /dev/shm.
	as_pg "pg_ctl -D '$PGDATA' -l '$RUN/postgres.log' -w \
		-o \"-c listen_addresses=127.0.0.1 -c port=$PG_PORT -c unix_socket_directories=/tmp -c dynamic_shared_memory_type=mmap\" start" >/dev/null
	ok "postgres started on 127.0.0.1:$PG_PORT (log $RUN/postgres.log)"
}

start_all() {
	[ -f "$PGDATA/PG_VERSION" ] || fail "not set up yet: run scripts/pod.sh setup"
	say "Starting PostgreSQL"
	start_postgres

	if [ -z "${SKIP_LLAMA:-}" ]; then
		say "Starting llama-server"
		[ -x "$LLAMA_SERVER" ] && [ -f "$MODEL" ] || fail "llama-server or the model is missing: run scripts/pod.sh setup"
		start_bg llama make llama-run
		wait_for "$LLAMA_URL/health" 600 "llama (model loading)"
	fi

	say "Building and starting Atlas"
	(cd "$ROOT" && go build -o bin/atlas ./src/main)
	start_bg atlas "$ROOT/bin/atlas" --env-file "$ENV_FILE"
	wait_for "$ATLAS_URL/health" 120 "atlas"
	echo
	ok "Swagger: $ATLAS_URL/swagger/index.html  (inside the pod; use kubectl port-forward <pod> 8090:8090)"
}

stop_all() {
	say "Stopping"
	stop_bg atlas
	stop_bg llama
	if as_pg "pg_ctl -D '$PGDATA' status" >/dev/null 2>&1; then
		as_pg "pg_ctl -D '$PGDATA' -m fast stop" >/dev/null && ok "postgres stopped"
	fi
}

status() {
	as_pg "pg_ctl -D '$PGDATA' status" >/dev/null 2>&1 && ok "postgres running" || echo "  -  postgres stopped"
	for n in llama atlas; do running "$n" && ok "$n running (pid $(cat "$RUN/$n.pid"))" || echo "  -  $n stopped"; done
}

# ---------------------------------------------------------------- checks

check() {
	say "Database"
	PGPASSWORD="$DB_PASSWORD" psql -h 127.0.0.1 -p "$PG_PORT" -U atlas -d atlas -tAc "SELECT 1" >/dev/null || fail "cannot log in as atlas"
	ok "login as atlas works"
	local tables
	tables=$(PGPASSWORD="$DB_PASSWORD" psql -h 127.0.0.1 -p "$PG_PORT" -U atlas -d atlas -tAc \
		"SELECT string_agg(version::text, ',' ORDER BY version) FROM schema_migrations")
	ok "migrations applied: $tables"

	if [ -z "${SKIP_LLAMA:-}" ]; then
		say "LLM (a real answer from the GPU)"
		curl -sf "$LLAMA_URL/health" >/dev/null || fail "llama-server is not up"
		local reply
		reply=$(curl -sf "$LLAMA_URL/v1/chat/completions" -H 'Content-Type: application/json' -d '{
			"model": "qwen3-8b-q8_0", "temperature": 0, "max_tokens": 20,
			"chat_template_kwargs": {"enable_thinking": false},
			"messages": [{"role": "user", "content": "Reply with exactly: OK"}]}' |
			python3 -c 'import json,sys; d=json.load(sys.stdin); t=d.get("timings", {}).get("predicted_per_second"); print(d["choices"][0]["message"]["content"].strip(), "|", "%d tokens/s" % t if t else "")')
		ok "model says: $reply"
	fi

	say "Atlas API"
	curl -sf "$ATLAS_URL/health" | grep -q '"ok"' || fail "atlas /health is not ok"
	ok "/health ok"
	curl -sf -o /dev/null "$ATLAS_URL/swagger/doc.json" && ok "/swagger ok"
	local n
	for path in countries organizations categories sites; do
		n=$(curl -sf "$ATLAS_URL/v1/$path?limit=1000" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["items"]))')
		ok "/v1/$path: $n items"
	done
	echo
	ok "everything works. Next: scripts/pod.sh crawl <domain>   (e.g. stat.uz)"
}

crawl() {
	local domain=${1:?usage: scripts/pod.sh crawl <domain>, e.g. stat.uz}
	local id
	id=$(curl -sf "$ATLAS_URL/v1/sites?limit=1000" | python3 -c '
import json, sys
for s in json.load(sys.stdin)["items"]:
    if s["domain"] == sys.argv[1]:
        print(s["id"])' "$domain")
	[ -n "$id" ] || fail "no site with domain $domain (see $ATLAS_URL/v1/sites)"

	say "Crawling $domain (site $id)"
	local run
	run=$(curl -sf -X POST "$ATLAS_URL/v1/sites/$id/crawl" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])') ||
		fail "crawl not started (already running, or too many crawls)"
	ok "crawl run $run started; following it (Ctrl+C stops watching, not the crawl)"

	local summary='import json,sys; r=json.load(sys.stdin); print(r["status"], "| found", r["pages_found"], "new", r["pages_new"], "changed", r["pages_changed"], "unchanged", r["pages_unchanged"], "skipped", r["pages_skipped"], "failed", r["pages_failed"], "| stored", r["records_created"], "updated", r["records_updated"])'
	while :; do
		line=$(curl -sf "$ATLAS_URL/v1/crawl-runs/$run" | python3 -c "$summary")
		printf '\r     %s   ' "$line"
		case $line in running*) sleep 5 ;; *) echo; break ;; esac
	done

	say "Latest crawling data of $domain"
	curl -sf "$ATLAS_URL/v1/crawling-data?site_id=$id&limit=5" | python3 -c '
import json, sys
items = json.load(sys.stdin)["items"]
if not items:
    print("     (nothing stored: check the failed counter and scripts/pod.sh logs atlas)")
for d in items:
    print("     #%-5s %-12s %-60s conf %s" % (d["id"], d["record_type"], (d["title"] or "")[:60], d["confidence"]))
    print("            %s" % d["source_url"])'
}

logs() {
	local name=${1:-atlas}
	tail -n 100 -f "$RUN/$name.log"
}

case "${1:-}" in
setup)  setup_tools; setup_postgres; setup_llama; echo; ok "setup done. Next: scripts/pod.sh start" ;;
start)  start_all ;;
stop)   stop_all ;;
status) status ;;
check)  check ;;
crawl)  shift; crawl "$@" ;;
logs)   shift; logs "$@" ;;
*)      sed -n '2,20p' "$0"; exit 1 ;;
esac
