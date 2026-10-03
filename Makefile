# GoAllAdmin — 所有检查收敛到 `make ci`，CI 平台只负责调用它。
#
# 后端目标在 server/ 下执行；前端目标在 web/ 下执行。

SHELL := /bin/bash
.DEFAULT_GOAL := help

SERVER_DIR := server
WEB_DIR    := web
BIN        := $(SERVER_DIR)/bin/server

# 测试库连接串。`make test-db-up` 起的容器与此默认值一致。
export GA_TEST_DSN ?= ga:ga_test_pw@tcp(127.0.0.1:3307)/goalladmin_test?charset=utf8mb4&parseTime=true&loc=UTC&multiStatements=true

# 测试用 Redis 的地址（D-074）。`make test-db-up` 起的容器与此默认值一致；端口避开 6379，不和本机自己的 Redis 冲突。
# 需要 Redis 的测试在这个变量为空时跳过。
export GA_TEST_REDIS ?= 127.0.0.1:6380

# 本地开发默认连 `make test-db-up` 起的库（开发库 goalladmin 由 deploy/mysql-init 创建）。
# 已经设置的环境变量优先；有 server/config/config.yaml 时也会被读取。这个 JWT 密钥只在 debug 模式可用，release 模式会拒绝启动。
DEV_ENV := GA_DB_PORT=$${GA_DB_PORT:-3307} GA_DB_USER=$${GA_DB_USER:-ga} GA_DB_PASSWORD=$${GA_DB_PASSWORD:-ga_test_pw} GA_JWT_SECRET_PLATFORM=$${GA_JWT_SECRET_PLATFORM:-dev-only-jwt-secret-not-for-production-0123456789}
# 代理商、商户程序（D-061）：各自的端口和密钥；它们不执行迁移，先 make migrate（平台程序）再起
DEV_ENV_AGENT := $(DEV_ENV) GA_SERVER_ADDR=$${GA_SERVER_ADDR:-127.0.0.1:8081} GA_JWT_SECRET_AGENT=$${GA_JWT_SECRET_AGENT:-dev-only-agent-jwt-secret-not-for-production-01234}
DEV_ENV_MERCHANT := $(DEV_ENV) GA_SERVER_ADDR=$${GA_SERVER_ADDR:-127.0.0.1:8082} GA_JWT_SECRET_MERCHANT=$${GA_JWT_SECRET_MERCHANT:-dev-only-merchant-jwt-secret-not-for-production-0}
ADMIN ?= admin

## ---------- 后端 ----------

.PHONY: build
build: licenses ## 编译三个后端程序到 server/bin/（server 平台、agent 代理商、merchant 商户），旁边附第三方许可证清单
	cd $(SERVER_DIR) && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/server .
	cd $(SERVER_DIR) && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/agent ./cmd/agent
	cd $(SERVER_DIR) && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/merchant ./cmd/merchant

.PHONY: licenses
licenses: ## 生成 server/bin/third-party-licenses.txt（编译进二进制的 Go 模块和标准库的许可证原文）
	mkdir -p $(SERVER_DIR)/bin
	CGO_ENABLED=0 ./scripts/go-licenses.sh $(SERVER_DIR) $(SERVER_DIR)/bin/third-party-licenses.txt . ./cmd/agent ./cmd/merchant

.PHONY: licenses-check
licenses-check: ## 检查每个编译进二进制的 Go 模块都带许可证文件
	@tmp=$$(mktemp) && CGO_ENABLED=0 ./scripts/go-licenses.sh $(SERVER_DIR) $$tmp . ./cmd/agent ./cmd/merchant && rm -f $$tmp

.PHONY: run
run: ## 以 debug 模式运行后端（默认连本地开发库；读 server/config/config.yaml 如果存在）
	cd $(SERVER_DIR) && $(DEV_ENV) go run .

.PHONY: dev
dev: ## 开发模式：后端源码一改就自动重新编译、重启；前端另开 make web-dev，本来就热更新
	cd $(SERVER_DIR) && $(DEV_ENV) go run ./cmd/dev

.PHONY: run-agent
run-agent: ## 以 debug 模式运行代理商程序（:8081；不执行迁移，先 make migrate）
	cd $(SERVER_DIR) && $(DEV_ENV_AGENT) go run ./cmd/agent

.PHONY: run-merchant
run-merchant: ## 以 debug 模式运行商户程序（:8082；不执行迁移，先 make migrate）
	cd $(SERVER_DIR) && $(DEV_ENV_MERCHANT) go run ./cmd/merchant

.PHONY: dev-agent
dev-agent: ## 代理商程序的开发模式（源码一改就重新编译、重启）
	cd $(SERVER_DIR) && $(DEV_ENV_AGENT) GA_DEV_PKG=./cmd/agent go run ./cmd/dev

.PHONY: dev-merchant
dev-merchant: ## 商户程序的开发模式（源码一改就重新编译、重启）
	cd $(SERVER_DIR) && $(DEV_ENV_MERCHANT) GA_DEV_PKG=./cmd/merchant go run ./cmd/dev

.PHONY: migrate
migrate: ## 执行数据库迁移
	cd $(SERVER_DIR) && $(DEV_ENV) go run . migrate up

.PHONY: admin
admin: ## 创建超级管理员（ADMIN=用户名，默认 admin；随机密码只打印一次）
	cd $(SERVER_DIR) && $(DEV_ENV) go run . admin create -username $(ADMIN)

.PHONY: fmt
fmt: ## 格式化 Go 代码
	cd $(SERVER_DIR) && gofmt -w . && go run golang.org/x/tools/cmd/goimports@latest -w . 2>/dev/null || true

.PHONY: fmt-check
fmt-check: ## 检查 gofmt
	@cd $(SERVER_DIR) && out=$$(gofmt -l .) && if [ -n "$$out" ]; then echo "gofmt 需要处理以下文件:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## go vet
	cd $(SERVER_DIR) && go vet ./...

.PHONY: lint
lint: ## golangci-lint
	cd $(SERVER_DIR) && golangci-lint run ./...

.PHONY: depcheck
depcheck: ## 依赖方向检查（core 不依赖 modules，模块之间不互相依赖，业务不碰 core/internal）
	./scripts/depcheck.sh

.PHONY: gates-test
gates-test: ## 门禁脚本自测：go list、gofmt、grep 出错时门禁必须失败（D-055）
	./scripts/test-gates.sh

.PHONY: banned
banned: ## 命名检查（词表 .local/banned-words.txt，不存在则跳过）
	./scripts/banned-words.sh

.PHONY: test
test: ## 后端测试（需要 GA_TEST_DSN 指向可用的 MySQL 8、GA_TEST_REDIS 指向可用的 Redis；make test-db-up 会把两个都起好）
	cd $(SERVER_DIR) && go test -race -count=1 -coverprofile=coverage.out ./...

.PHONY: test-multi-instance
test-multi-instance: ## 两个独立进程的 HTTP/Redis 定向冒烟（专用 MySQL + Redis；不启动浏览器）
	@test -n "$$GA_TEST_DSN" -a -n "$$GA_TEST_REDIS" || (echo "需要 GA_TEST_DSN 和 GA_TEST_REDIS"; exit 1)
	cd $(SERVER_DIR) && go test -race -count=1 ./core/app -run '^TestMultiProcess_167_SharedState$$'

.PHONY: test-db-up
test-db-up: ## 用 Docker 起测试用 MySQL（127.0.0.1:3307）和 Redis（127.0.0.1:6380）
	docker compose -f deploy/docker-compose.test.yml up -d --wait

.PHONY: test-db-down
test-db-down: ## 停掉测试用 MySQL 和 Redis
	docker compose -f deploy/docker-compose.test.yml down -v

.PHONY: vuln
vuln: ## govulncheck
	cd $(SERVER_DIR) && go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: tidy-check
tidy-check: ## go.mod / go.sum 必须是 tidy 的
	@cd $(SERVER_DIR) && cp go.mod /tmp/ga.go.mod && cp go.sum /tmp/ga.go.sum && go mod tidy && \
		(diff -q go.mod /tmp/ga.go.mod >/dev/null && diff -q go.sum /tmp/ga.go.sum >/dev/null) || \
		(echo "go.mod/go.sum 不是 tidy 状态，请运行 go mod tidy"; exit 1)

## ---------- 前端 ----------

.PHONY: web-install
web-install: ## 安装前端依赖（锁文件必须一致）
	cd $(WEB_DIR) && pnpm install --frozen-lockfile

.PHONY: web-dev
web-dev: ## 平台端开发服务器 :5173（/api 反代到 GA_API_TARGET，默认 http://127.0.0.1:8080）
	cd $(WEB_DIR) && npm run dev

.PHONY: web-dev-agent
web-dev-agent: ## 代理商端开发服务器 :5174（/api 反代到 GA_API_TARGET，默认 make run-agent 的 :8081）
	cd $(WEB_DIR) && npm run dev:agent

.PHONY: web-dev-merchant
web-dev-merchant: ## 商户端开发服务器 :5175（/api 反代到 GA_API_TARGET，默认 make run-merchant 的 :8082）
	cd $(WEB_DIR) && npm run dev:merchant

.PHONY: web-ci
web-ci: ## 前端检查：install → lint → typecheck → test → build（pnpm 锁文件是可复现的依据）
	cd $(WEB_DIR) && pnpm install --frozen-lockfile && npm run lint && npm run typecheck && npm run test && npm run build

.PHONY: e2e
e2e: ## 前端冒烟测试：起真实后端 + Playwright（需要 GA_TEST_DSN 的 MySQL；浏览器先 cd web/apps/platform && npx playwright install chromium）
	./scripts/e2e.sh

## ---------- 总入口 ----------

.PHONY: ci
ci: fmt-check vet lint depcheck banned gates-test tidy-check licenses-check test web-ci ## 全部检查（govulncheck 需要网络，单独用 make vuln）
	@echo "CI OK"

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
