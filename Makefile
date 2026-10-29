SWAG := $(shell go env GOPATH)/bin/swag

# llama.cpp, tuned for one NVIDIA B200 (Blackwell, compute capability 10.0, 180 GB).
LLAMA_DIR    := src/third_party/llama.cpp
LLAMA_SERVER := $(LLAMA_DIR)/build/bin/llama-server
# B200 = sm_100; "native" also works when building on the GPU server itself.
CUDA_ARCH ?= 100
# Must match LLM_MODEL_PATH and LLM_MODEL in llamacpp.env.
LLAMA_MODEL ?= models/qwen3-8b/Qwen3-8B-Q8_0.gguf
LLAMA_ALIAS ?= qwen3-8b-q8_0
# Must match LLM_BASE_URL in llamacpp.env.
LLAMA_HOST ?= 127.0.0.1
LLAMA_PORT ?= 8080
# Parallel requests; keep equal to LLM_WORKERS in llamacpp.env.
LLAMA_SLOTS ?= 16
# Max tokens per request (Qwen3-8B native context).
LLAMA_SLOT_CTX ?= 32768
# One KV cache shared by all slots: 16 x 32768 tokens ~ 77 GB in f16 for Qwen3-8B.
LLAMA_CTX ?= $(shell echo $$(( $(LLAMA_SLOTS) * $(LLAMA_SLOT_CTX) )))

.PHONY: swagger run run-llamacpp test llama-build llama-run

# Regenerate src/main/docs from the @Summary/@Router comments on the handlers.
swagger:
	$(SWAG) init -g main.go -d src/main -o src/main/docs --outputTypes go,json,yaml

run:
	go run ./src/main --env-file src/resources/env/local.env

# Run Atlas against the llama-server started by `make llama-run`.
run-llamacpp:
	go run ./src/main --env-file src/resources/env/llamacpp.env

test:
	go test ./src/...

# Build llama-server with CUDA (needs CUDA >= 12.8 for Blackwell).
llama-build:
	cmake -S $(LLAMA_DIR) -B $(LLAMA_DIR)/build \
		-DCMAKE_BUILD_TYPE=Release \
		-DGGML_CUDA=ON \
		-DCMAKE_CUDA_ARCHITECTURES=$(CUDA_ARCH)
	cmake --build $(LLAMA_DIR)/build --config Release -j --target llama-server

# Serve the model on the GPU:
#   -ngl 999            every layer on the GPU
#   -fa on              flash attention
#   -np / -c / -kvu     LLAMA_SLOTS parallel slots sharing one KV cache of LLAMA_CTX tokens,
#   --kv-unified-per-slot  each request capped at LLAMA_SLOT_CTX tokens
#   -b / -ub            large batches for fast prompt processing
#   --cache-reuse 256   reuse the shared prompt prefix (system prompt + taxonomy) across requests
#   --jinja             chat template; Atlas sends enable_thinking=false, --reasoning-budget 0 enforces it
#   --metrics           Prometheus metrics on /metrics
llama-run:
	@test -f $(LLAMA_SERVER) || { echo "llama-server not built: run make llama-build"; exit 1; }
	@test -f $(LLAMA_MODEL) || { echo "model not found: $(LLAMA_MODEL)"; exit 1; }
	$(LLAMA_SERVER) \
		-m $(LLAMA_MODEL) \
		--alias $(LLAMA_ALIAS) \
		--host $(LLAMA_HOST) --port $(LLAMA_PORT) \
		-ngl 999 \
		-fa on \
		-np $(LLAMA_SLOTS) \
		-c $(LLAMA_CTX) \
		-kvu --kv-unified-per-slot $(LLAMA_SLOT_CTX) \
		-b 4096 -ub 1024 \
		--cache-reuse 256 \
		--jinja --reasoning-budget 0 \
		--metrics \
		--no-webui
