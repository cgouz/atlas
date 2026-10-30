#!/usr/bin/env bash
# Sets up and runs the whole Atlas stack inside a Kubernetes GPU pod (no Docker,
# no systemd): PostgreSQL, llama-server (Qwen3-8B on the GPU) and Atlas.
#
# Go, the model tools, logs and pid files live under $WORKSPACE (default
# /workspace), which survives a pod restart.
#
#   scripts/pod.sh setup            one time: install Postgres, Go, build llama.cpp, download the model
#   scripts/pod.sh start            start Postgres, llama-server and Atlas (again after every pod restart)
#   scripts/pod.sh check            end-to-end checks: database, LLM answer, Atlas API
#   scripts/pod.sh crawl stat.uz    crawl one site and show what was stored
#   scripts/pod.sh status | logs [postgres|llama|atlas] | stop
#
# PostgreSQL, two ways (PG_MODE):
#   apt        (default) apt-get install postgresql + "service postgresql start", run as root.
#              Lives in the pod's own filesystem: after a pod restart, start installs it
#              again and the database is empty (crawl again).
#   workspace  PostgreSQL 17 installed with micromamba into $WORKSPACE/pg, data in
#              $WORKSPACE/pgdata: survives pod restarts, works without root.
#
# Settings (environment variables):
#   PG_MODE=apt              apt or workspace (see above)
#   WORKSPACE=/workspace     where tools, logs (and in workspace mode the database) go
#   PG_PORT=5432             Postgres port (on 127.0.0.1)
#   LLAMA_PORT=8080          llama-server port (on 127.0.0.1); Atlas is pointed at it automatically
#   DB_PASSWORD=atlas        password of the atlas database user
#   SKIP_LLAMA=1             do not build/start llama-server (use LLM_BASE_URL from the env file or environment)
#   HF_TOKEN=...             only if the model download needs it
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKSPACE="${WORKSPACE:-/workspace}"
RUN="$WORKSPACE/atlas-run"           # pids and logs
PG_MODE="${PG_MODE:-apt}"
PG="$WORKSPACE/pg"                   # workspace mode: Postgres install (micromamba env)
PGDATA="$WORKSPACE/pgdata"           # workspace mode: database files
TOOLS="$WORKSPACE/tools"             # micromamba env with the Hugging Face CLI
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
LLAMA_PORT="${LLAMA_PORT:-8080}"
LLAMA_URL="http://127.0.0.1:$LLAMA_PORT"
# Atlas talks to our llama-server; with SKIP_LLAMA the caller's LLM_BASE_URL (or the env file) is used.
[ -z "${SKIP_LLAMA:-}" ] && export LLM_BASE_URL="$LLAMA_URL/v1"
ATLAS_URL="http://127.0.0.1:8090"

export PATH="$GO_DIR/bin:$PG/bin:$TOOLS/bin:/usr/local/cuda/bin:$PATH"
mkdir -p "$RUN"
case $PG_MODE in apt | workspace) ;; *) echo "PG_MODE must be apt or workspace" >&2; exit 1 ;; esac

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

# port_busy PORT: something already listens on 127.0.0.1:PORT.
port_busy() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

# start_bg NAME CMD...: runs CMD in its own process group so stop can end it with its children.
start_bg() {
	local name=$1; shift
	if running "$name"; then ok "$name already running (pid $(cat "$RUN/$name.pid"))"; return; fi
	# The child writes its own pid before exec: setsid may fork, so $! is not always the process.
	rm -f "$RUN/$name.pid"
	(cd "$ROOT" && setsid nohup sh -c 'echo $$ >"$0"; exec "$@"' "$RUN/$name.pid" "$@" >"$RUN/$name.log" 2>&1 &)
	for _ in $(seq 50); do [ -s "$RUN/$name.pid" ] && break; sleep 0.1; done
	ok "$name started (pid $(cat "$RUN/$name.pid"), log $RUN/$name.log)"
}

stop_bg() {
	local name=$1 pid
	if running "$name"; then
		pid=$(cat "$RUN/$name.pid")
		kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
		for _ in $(seq 30); do running "$name" || break; sleep 1; done
		if running "$name"; then
			kill -KILL -- "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
			sleep 1
		fi
		running "$name" && fail "$name (pid $pid) does not stop"
		ok "$name stopped"
	fi
	rm -f "$RUN/$name.pid"
}

# ---------------------------------------------------------------- setup

setup_tools() {
	say "Checking tools"
	have curl || fail "curl is missing (apt-get install -y curl)"
	have tar || fail "tar is missing (apt-get install -y tar)"
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

	# micromamba: the Hugging Face CLI (for the model) and, in workspace mode, PostgreSQL.
	local need_tools="" need_pg=""
	[ -z "${SKIP_LLAMA:-}" ] && [ ! -x "$TOOLS/bin/hf" ] && [ ! -x "$TOOLS/bin/huggingface-cli" ] && need_tools=1
	[ "$PG_MODE" = workspace ] && [ ! -x "$PG/bin/postgres" ] && need_pg=1
	if [ -n "$need_tools$need_pg" ] && [ ! -x "$MAMBA" ]; then
		have bzip2 || fail "bzip2 is missing (apt-get install -y bzip2)"
		say "Installing micromamba into $WORKSPACE/bin"
		mkdir -p "$WORKSPACE/bin"
		curl -fsSL https://micro.mamba.pm/api/micromamba/linux-64/latest | tar -xj -C "$WORKSPACE" bin/micromamba
	fi
	if [ -n "$need_tools" ]; then
		say "Installing the Hugging Face CLI into $TOOLS"
		"$MAMBA" create -y -q -r "$WORKSPACE/mamba" -p "$TOOLS" -c conda-forge huggingface_hub
	fi
	if [ -n "$need_pg" ]; then
		say "Installing PostgreSQL 17 into $PG"
		"$MAMBA" create -y -q -r "$WORKSPACE/mamba" -p "$PG" -c conda-forge "postgresql=17"
	fi
}

# ---------------------------------------------------------------- PostgreSQL
# pg_setup / pg_start / pg_stop / pg_running / pg_log pick the PG_MODE implementation.

pg_setup()   { "pg_${PG_MODE}_setup"; }
pg_start()   { "pg_${PG_MODE}_start"; }
pg_stop()    { "pg_${PG_MODE}_stop"; }
pg_running() { "pg_${PG_MODE}_running"; }
pg_log()     { "pg_${PG_MODE}_log"; }

# --- apt mode: the distribution's PostgreSQL, started with "service".

pg_apt_installed() { compgen -G "/usr/lib/postgresql/*/bin/postgres" >/dev/null; }

pg_apt_setup() { pg_apt_start; }

pg_apt_start() {
	[ "$(id -u)" = 0 ] || fail "PG_MODE=apt needs root (apt-get, service); use PG_MODE=workspace"
	if ! pg_apt_installed; then
		say "Installing PostgreSQL with apt-get (lost on pod restart; start installs it again)"
		apt-get update -qq
		DEBIAN_FRONTEND=noninteractive apt-get install -y -qq postgresql >/dev/null
		# Same port as Atlas expects (the cluster is created on install with port 5432).
		sed -i "s/^#\?port = [0-9]*/port = $PG_PORT/" /etc/postgresql/*/main/postgresql.conf
	fi
	if pg_apt_running; then ok "postgres already running"; else
		service postgresql start >/dev/null
		for _ in $(seq 30); do pg_apt_running && break; sleep 1; done
		pg_apt_running || fail "postgres did not start; see: scripts/pod.sh logs postgres"
		ok "postgres started on 127.0.0.1:$PG_PORT ($(su postgres -c "psql -p $PG_PORT -tAc 'SHOW server_version'"))"
	fi
	# User and database, created once; the password is set every time so it follows DB_PASSWORD.
	# UTF8 + C locale explicitly: an image without locales would otherwise give SQL_ASCII,
	# which mangles Uzbek, Russian and Japanese text.
	local pw=${DB_PASSWORD//\'/\'\'}
	su postgres -c "psql -p $PG_PORT -v ON_ERROR_STOP=1 -q" <<SQL
DO \$\$ BEGIN
	IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'atlas') THEN CREATE ROLE atlas LOGIN; END IF;
END \$\$;
ALTER ROLE atlas PASSWORD '$pw';
SELECT 'CREATE DATABASE atlas OWNER atlas ENCODING ''UTF8'' LC_COLLATE ''C'' LC_CTYPE ''C'' TEMPLATE template0'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'atlas')\gexec
SQL
	ok "database atlas ready (user atlas, $(su postgres -c "psql -p $PG_PORT -tAc \"SELECT pg_encoding_to_char(encoding) FROM pg_database WHERE datname = 'atlas'\""))"
}

pg_apt_stop()    { pg_apt_running && service postgresql stop >/dev/null && ok "postgres stopped" || true; }
pg_apt_running() { pg_apt_installed && pg_isready -q -h 127.0.0.1 -p "$PG_PORT"; }
pg_apt_log()     { tail -n 100 -f /var/log/postgresql/*.log; }

# --- workspace mode: PostgreSQL 17 from micromamba, data in $WORKSPACE/pgdata.

pg_workspace_setup() {
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
	pg_workspace_start
	as_pg "createdb -h /tmp -p $PG_PORT -U atlas atlas"
	ok "database atlas created (user atlas)"
}

pg_workspace_start() {
	[ -f "$PGDATA/PG_VERSION" ] || fail "not set up yet: run scripts/pod.sh setup"
	if pg_workspace_running; then ok "postgres already running"; return; fi
	touch "$RUN/postgres.log"
	[ "$(id -u)" = 0 ] && chown pg "$RUN/postgres.log" # postgres runs as pg and writes its own log
	# mmap: pods often have only 64 MB of /dev/shm.
	as_pg "pg_ctl -D '$PGDATA' -l '$RUN/postgres.log' -w \
		-o \"-c listen_addresses=127.0.0.1 -c port=$PG_PORT -c unix_socket_directories=/tmp -c dynamic_shared_memory_type=mmap\" start" >/dev/null
	ok "postgres started on 127.0.0.1:$PG_PORT (log $RUN/postgres.log)"
}

pg_workspace_stop() {
	pg_workspace_running && as_pg "pg_ctl -D '$PGDATA' -m fast stop" >/dev/null && ok "postgres stopped" || true
}
pg_workspace_running() { [ -f "$PGDATA/PG_VERSION" ] && as_pg "pg_ctl -D '$PGDATA' status" >/dev/null 2>&1; }
pg_workspace_log()     { tail -n 100 -f "$RUN/postgres.log"; }

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

start_all() {
	say "Starting PostgreSQL ($PG_MODE mode)"
	pg_start

	if [ -z "${SKIP_LLAMA:-}" ]; then
		say "Starting llama-server"
		[ -x "$LLAMA_SERVER" ] && [ -f "$MODEL" ] || fail "llama-server or the model is missing: run scripts/pod.sh setup"
		if running llama; then
			ok "llama already running (pid $(cat "$RUN/llama.pid"))"
		elif curl -sf "$LLAMA_URL/health" >/dev/null && curl -sf "$LLAMA_URL/v1/models" | grep -q qwen3-8b-q8_0; then
			ok "a llama-server with qwen3-8b-q8_0 already answers on port $LLAMA_PORT (started outside this script); using it"
		elif port_busy "$LLAMA_PORT"; then
			fail "port $LLAMA_PORT is taken by another program; pick a free one: LLAMA_PORT=8081 scripts/pod.sh start"
		else
			start_bg llama make llama-run LLAMA_PORT="$LLAMA_PORT"
		fi
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
	pg_stop
}

status() {
	pg_running && ok "postgres running ($PG_MODE mode)" || echo "  -  postgres stopped ($PG_MODE mode)"
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
	if [ "$name" = postgres ]; then pg_log; else tail -n 100 -f "$RUN/$name.log"; fi
}

case "${1:-}" in
setup)  setup_tools; say "PostgreSQL ($PG_MODE mode)"; pg_setup; setup_llama; echo; ok "setup done. Next: scripts/pod.sh start" ;;
start)  start_all ;;
stop)   stop_all ;;
status) status ;;
check)  check ;;
crawl)  shift; crawl "$@" ;;
logs)   shift; logs "$@" ;;
*)      sed -n '2,20p' "$0"; exit 1 ;;
esac
