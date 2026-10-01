<p align="center">
  <img src="docs/assets/logo-vt-alpha.png" width="220" alt="GoAllAdmin logo">
</p>

<p align="center">
  <strong>多端认证。权限写在代码里。一个业务碰不坏的内核。</strong><br>
  Go + Vue 3 的后台脚手架：小而完整，能安全上线。
</p>

<p align="center">
  <img src="docs/assets/badge-go.svg" height="24" alt="Go 1.26 或更高">
  <img src="docs/assets/badge-vue.svg" height="24" alt="Vue 3.5">
  <img src="docs/assets/badge-element-plus.svg" height="24" alt="Element Plus 2.14">
  <img src="docs/assets/badge-mysql.svg" height="24" alt="MySQL 8.0">
  <a href="LICENSE"><img src="docs/assets/badge-license.svg" height="24" alt="许可证：Apache-2.0"></a>
  <a href="CHANGELOG.md"><img src="docs/assets/badge-version.svg" height="24" alt="版本：v0.1.0"></a>
</p>

<p align="center">
  <strong>简体中文</strong> · <a href="README.en.md">English</a>
</p>

<p align="center">
  <a href="https://goalladmin.com">官网</a> ·
  <a href="#为-ai-开发时代而生">定位</a> ·
  <a href="#快速开始">快速开始</a> ·
  <a href="#为什么是-goalladmin">为什么</a> ·
  <a href="#文档">文档</a> ·
  <a href="docs/spec.md#12-安全基线">安全</a>
</p>

---

GoAllAdmin 是一个基于 Go（Gin、GORM、Casbin）和 Vue 3（Vite、Element Plus）的后台脚手架。它把每个后台都需要、又最容易做错的几件事做扎实——多端认证、代码里声明的权限与菜单、可审计的操作、业务代码碰不到的框架内核——其余的留给你。

## 为 AI 开发时代而生

GoAllAdmin 不追求成为一个"大而全"的后台管理框架。我们更希望把它做成一套简洁、安全、清晰、易扩展的 Go 后台开发基础设施。

有开发经验的人都知道：框架内置的功能越多、依赖越复杂，要维护的代码就越多，潜在的攻击面、配置错误和安全风险也会随之增加。到了 AI 写代码的时代，这笔账更清楚了——业务功能让 AI 按你的需求生成，成本很低；真正难、也真正要紧的，是一个 AI 写错了也破坏不了的内核。所以 GoAllAdmin 把认证、授权、审计放在内核里，业务代码在物理上碰不到它；把权限、菜单、配置写在代码里，AI 看得见、改得动，进 git、可评审；每条安全规则都有"不该发生的事确实不会发生"的反向测试兜底。想要的功能按需写成自己的模块，不要的从来不在你的程序里。

Keep the core small. Build what you need. 这就是 GoAllAdmin。

## 为什么是 GoAllAdmin？

| 特性 | 你得到什么 |
|---|---|
| 多端原生 | 一个工程里可以有多个"端"（平台后台、代理商后台、商户后台……），各有独立的用户来源、登录入口、签名密钥、菜单和权限集。v0.1 内置并启用 `platform` 端。 |
| 权限和菜单在代码里 | 模块用 Go 代码声明权限码和菜单树，进 git、可评审、可测试；数据库只存"哪个角色被授予了哪些权限码"。菜单管理只能改显示名、图标、顺序和分组，路径、页面和权限码后台改不了；没有 API 管理页。字典同理：代码里声明的值后台改不了，后台只调显示文字和颜色。 |
| 认证做扎实 | 15 分钟访问令牌加 HttpOnly 轮换刷新凭证，重放检测；验证码按需出现、限流、锁定，阈值只能在配置文件里、在底线之内调整，后台只读展示，偷到登录会话也关不掉；强制改密；停用、改密、吊销立即生效。 |
| 授权有边界 | 路由守卫（`Public` / `AuthOnly` / `Require`，以及只给超管的 `RequireSuper`）；超级管理员与敏感权限规则；非超管授不出、也借不到自己没有的权限，也碰不了超管账号；重置密码只有超管能做，超管自己的密码只能在服务器命令行重置；最后一个可用超管在并发下也不会丢。 |
| 按部门的数据权限 | 权限码管"能不能做"，数据范围管"能对哪些人做"：仅本人、本部门、本部门及下级、全部四档，按角色逐个资源设置，只收窄、不放宽；业务模块用 `DataFilter` 给自己的查询套上同一套范围。 |
| 可审计 | 操作日志按路由显式开启，密码、密钥类字段脱敏；登录日志记录每一次尝试；越权被拒、伪造令牌、凭证重放记为安全事件，服务端故障按类归并进错误日志；调查时间线按用户、IP 或会话还原经过；日志不能删改，同时写一份到日志输出；会话可查可吊销。 |
| 开箱即有的后台 | 数据中心、安全监控、工作台；用户、角色与授权、部门、岗位、菜单管理；运维中心（操作日志、登录日志、错误日志、安全事件、调查时间线、会话管理）；系统设置（字典、只读的安全设置）；个人中心（资料、头像、改密、下线其他设备）；服务端锁屏、全局搜索、偏好设置、暗色模式。 |
| 业务碰不到内核 | 后端 `core/internal` 由 Go 编译器隔离，前端 `@ga/shell` 用 `exports` 限制深层引用，CI 检查依赖方向；升级框架靠 `git merge upstream`。 |
| 反向测试 | 每条安全规则都有"不该发生的事确实不会发生"的测试，连真实 MySQL 跑；前端 Vitest 加 Playwright 冒烟。 |
| 11 种界面语言 | English、简体中文、繁體中文、日本語、한국어、Bahasa Melayu、தமிழ்、বাংলা、Русский、Français、Deutsch；语言选择带国旗，接口错误说明也跟着界面语言走，加一种语言只需加文案文件。 |
| 拿来就能跑 | `make` 三条命令或原始的 `go run` / `npm run` 都行；Docker Compose 一键起整套；新增模块指南一步步走完"从表到页面"。 |

信任边界、输入限制和启动校验见[安全基线](docs/spec.md#12-安全基线)。

## 快速开始

需要 Go 1.26 或更高（建议装官方仍在维护的最新版）、Docker（本地 MySQL）、Node 22 或更高；前端用 pnpm 10+ 或 npm 10+ 都行。

### 本地跑起来

```bash
git clone https://github.com/goalladmin/goalladmin.git && cd goalladmin

make test-db-up      # 1. 起本地 MySQL（127.0.0.1:3307，含开发库 goalladmin；数据不持久）
make migrate         # 2. 建表
make admin           # 3. 创建超级管理员 admin，随机密码只打印这一次
make run             # 4. 后端 http://127.0.0.1:8080（debug 模式）

# 另开一个终端
make web-install     # 5. 前端依赖
make web-dev         # 6. 平台端 http://localhost:5173（/api 反代到后端）
```

浏览器打开 http://localhost:5173，用第 3 步的账号密码登录，按提示改一次密码，就能看到权限管理、运维中心、系统设置等页面。

### 不用 make

`make` 只是省打字：每个目标就是下面这几条命令加几个环境变量。

```bash
# 后端（server/ 目录）。表由迁移文件建：debug 模式默认 migrate.auto=true，
# admin create 和 serve 启动时都会自动把表建齐，所以全新的库上两条命令就够。
cd server
export GA_DB_HOST=127.0.0.1 GA_DB_PORT=3307 GA_DB_NAME=goalladmin GA_DB_USER=ga GA_DB_PASSWORD=ga_test_pw
go run . admin create -username admin   # 等于 go run main.go admin create -username admin
go run .                                # 启动服务；不带子命令等于 serve

# 用自己装的 MySQL：先建一个空库和账号，把上面的 GA_DB_* 改成你的，
# 或者复制 config/config.example.yaml 为 config.yaml 填进去。

# 前端（pnpm 和 npm 都可以；pnpm-lock.yaml 是可复现安装的依据，CI 和 Docker 镜像用它）
cd web
pnpm install          # 或 npm install
pnpm dev              # 或 npm run dev
```

debug 模式没设 `GA_JWT_SECRET_PLATFORM` 会自动生成临时密钥；release 模式没有真密钥拒绝启动。

### Docker Compose

MySQL + 后端 + nginx 托管的前端：

```bash
cp deploy/.env.example deploy/.env      # 填密码和 JWT 密钥（openssl rand -base64 48）
docker compose -f deploy/docker-compose.yml up -d
docker compose -f deploy/docker-compose.yml exec server /server admin create -username admin
```

浏览器打开 http://localhost:8000。前端和 `/api` 必须同源（刷新凭证是 HttpOnly Cookie），`deploy/nginx.conf` 就是这样配的；后端的 8080 只在容器网络内可见。

镜像默认 release 模式，刷新 Cookie 带 `Secure`（名字是 `__Host-ga_rt_<端>`）：正式部署要在前面配 HTTPS，并把 https 地址填进 `deploy/.env` 的 `GA_SERVER_ALLOWED_ORIGINS`。只在本机试用、浏览器不接受 http 下的 Secure Cookie 时，可以在 `deploy/.env` 里临时把 `GA_SERVER_MODE` 改成 `debug`。

## 写业务模块

业务代码只放在 `server/modules/<模块>/` 和 `web/apps/<端>/`，框架目录不改。模块用 Go 声明自己的权限码和菜单，在 `main.go` 里注册：

```go
func (m *module) Perms() []rbac.Perm {
	return []rbac.Perm{
		{Code: "order:order:list", Name: "perm.order.order.list", Portal: PortalCode, Group: "order.order"},
		{Code: "order:order:create", Name: "perm.order.order.create", Portal: PortalCode, Group: "order.order"},
	}
}

func (m *module) Routes(r *app.Router) {
	g := r.Portal(PortalCode).Group("/order")
	g.GET("/orders", rbac.Require("order:order:list"), m.h.list)                                   // 每条路由都写明守卫
	g.POST("/orders", rbac.Require("order:order:create"), m.h.create, oplog.Record("order.create")) // 写操作记日志
}
```

```go
// server/main.go —— 唯一的装配点，顺序即初始化顺序
func modules() []app.Module {
	return []app.Module{
		system.Module(), // 必须在前：它注册 platform 端，其他模块把路由挂在这个端下
		order.Module(),  // 二次开发的模块手写在这里
	}
}
```

[新增模块指南](docs/guides/new-module.md)用一个订单模块做例子，从建表、迁移、模型、repo、service、handler、权限码、菜单，到前端页面、文案和测试，一步一步走完整条路。

## 目录

```text
server/                 Go 后端
  main.go               唯一的装配点：读配置 → app.New → 显式注册模块 → Run
  core/                 框架内核。带 [公开] 标记的包是业务模块能 import 的 API 面，其余在 core/internal/
  modules/system/       用户、角色与授权、部门、岗位、会话、个人中心、数据中心、工作台、安全监控、运维中心（操作日志、登录日志、错误日志、安全事件、调查时间线）、系统设置（字典、只读的安全设置）；也注册 platform 端
  modules/<名>/         二次开发的模块（你自己的代码），在 main.go 里登记
  cmd/dev/              make dev：源码一改就自动重新编译、重启
  migrations/core/      框架自己的 SQL 迁移
web/                    pnpm 工作区
  packages/shell/       @ga/shell：请求、认证、权限、路由、布局、内置页面、国际化
  apps/platform/        平台端：每个端一个 Vite 应用，入口只有一行 createPortalApp；二次开发模块的页面放在 src/views/<名>/ 下
deploy/                 Dockerfile、docker-compose、nginx 示例、本地 MySQL 初始化脚本
docs/                   技术规范、约定、接口清单、决策记录、指南
```

后端子命令：`server`（不带子命令等于 `serve`）、`server migrate up|status`、`server admin create -username NAME`、`server admin reset-password -username NAME`（超管忘了密码时用，后台不能重置超管的密码）、`server rbac prune`、`server healthcheck`。`make ci` 跑全部检查：gofmt、vet、golangci-lint、依赖方向、门禁自测、tidy、第三方许可证文件、连 MySQL 的后端测试、前端 lint / typecheck / 单元测试 / 构建；`make e2e` 起真实后端跑 Playwright 冒烟。

## 配置

配置来自 `server/config/config.yaml`（可选，不进 git）加环境变量，环境变量优先。本地开发把数据库密码、JWT 密钥、端口都写在 `config.yaml` 里就行；容器和生产环境建议机密走环境变量：`GA_DB_PASSWORD`、`GA_JWT_SECRET_<端代号大写>`。全部的键见 `server/config/config.example.yaml` 顶部的清单。

release 模式会做启动校验：每个端的 JWT 密钥至少 32 字节且不是示例值、数据库密码非空、`allowedOrigins` 非空、每个 HTTP 超时都大于 0，否则拒绝启动。release 模式的刷新 Cookie 带 `Secure`，生产环境要经 HTTPS 访问。部署在 nginx 后面时把 nginx 的地址填进 `trustedProxies`，登录限流和日志里的 IP 都依赖它。

### 日志与审计

后端只把日志写到标准输出，不写文件，也不在后台网页里读日志文件：

- 单机查看：`docker compose -f deploy/docker-compose.yml logs -f server`，或 systemd 下的 `journalctl -u <服务名>`。
- 集中保存和检索：把 `log.format` 设为 `json`（`GA_LOG_FORMAT=json`），用 Vector、Fluent Bit 这类采集器送到 Loki、Elasticsearch / OpenSearch 或云厂商的日志服务。每行都带 `request_id`，后台错误日志里的请求 ID 可以直接拿来搜。
- 登录、操作和安全事件每一条都会以 `AUDIT` 级别另写一行（`audit.login`、`audit.operation`、`audit.security`），不受 `log.level` 影响。把它们存到数据库之外，是数据库被删改时唯一可信的记录。

数据库里的审计日志没有删除和修改接口，也不会自动清理。更严格的部署可以给应用单独的数据库账号：`ga_login_log`、`ga_operation_log` 只授予 `INSERT`、`SELECT`，`ga_error_log`、`ga_security_event` 授予 `INSERT`、`SELECT`、`UPDATE`（合并计数要用），都不给 `DELETE`；建表和迁移用另一个账号执行（`migrate.auto: false`）。

## 文档

| 文档 | 内容 |
|---|---|
| [技术规范](docs/spec.md) | 范围、目录与依赖方向、认证、授权、表、接口、安全基线、测试、扩展与升级。代码注释里的"规范 §x.y"指它 |
| [接口清单](docs/api.md) | 每条路由的守卫和入出参；公开路由清单有测试保证与代码一致 |
| [约定](docs/conventions.md) | 数据表列顺序、前端页面与文案键的约定 |
| [决策记录](docs/decisions.md) | 每一处偏离规范或默认做法的原因 |
| [新增模块指南](docs/guides/new-module.md) | 从表到页面，一步一步 |
| [变更记录](CHANGELOG.md) | 版本与升级注意 |
| [AI 协作者规则](CLAUDE.md) | AI 辅助开发时的工作规则 |

## 路线图

v0.1 刻意只做一个端、单实例、MySQL。下一步按优先级：第二个端与主体（Org）数据隔离、Redis 缓存与多实例、TOTP 二次验证、文件上传、OpenAPI。明确不做的东西见 [`docs/spec.md`](docs/spec.md) §2。

## 参与规则

提交的代码必须是自己写的；确需引入第三方代码时，只接受 MIT / Apache-2.0 / BSD / ISC 这类宽松许可证的，并在文件头保留原出处和版权声明。认证、授权相关的改动必须带测试；`docs/spec.md` §13.2 列出的反向测试不能删、不能弱化。偏离规范或默认做法时先在 `docs/decisions.md` 记一条，提交前 `make ci` 全绿。

## 作者与维护

GoAllAdmin 由新加坡籍华人 Danny Xu 与 Sean Xiang 共同开发，并由 STARDATA INTERNATIONAL PTE. LTD. 与两位作者共同维护。

## 许可

GoAllAdmin 以 [Apache License 2.0](LICENSE) 发布，见 `LICENSE` 与 `NOTICE`。前端构建产物、后端二进制和 Docker 镜像都随附自动生成的第三方许可证清单 `third-party-licenses.txt`（`make build` 放在 `server/bin/`，镜像里在 `/licenses/`）。

Copyright (c) 2026 STARDATA INTERNATIONAL PTE. LTD.
