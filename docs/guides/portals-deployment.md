# 平台、代理商和商户三端部署

三端共用一个业务数据库，各自运行独立后端、使用独立域名和 JWT 密钥。浏览器始终在本端域名下访问页面和 `/api/<端>/v1`；不用跨域请求或跨域 Cookie。

## 构建与入口

| 端 | Go 入口 | 后端构建参数 | 前端产物 | 默认域名 |
|---|---|---|---|---|
| 平台 | `server/main.go` | `PORTAL=platform`（默认） | `web/apps/platform/dist` | `platform.localhost` |
| 代理商 | `server/cmd/agent` | `PORTAL=agent` | `web/apps/agent/dist` | `agent.localhost` |
| 商户 | `server/cmd/merchant` | `PORTAL=merchant` | `web/apps/merchant/dist` | `merchant.localhost` |

三个后端镜像中的可执行文件统一叫 `/server`，实际编译的程序由构建参数决定。不能通过运行时环境变量把平台镜像变成代理商镜像。各镜像附带该程序的第三方许可证；`make build` 在 `server/bin` 生成三个本机二进制，旁边的许可证覆盖三个程序的依赖并集。

```sh
docker build -f deploy/Dockerfile.server --build-arg PORTAL=platform -t goalladmin-platform:local .
docker build -f deploy/Dockerfile.server --build-arg PORTAL=agent -t goalladmin-agent:local .
docker build -f deploy/Dockerfile.server --build-arg PORTAL=merchant -t goalladmin-merchant:local .
docker build -f deploy/Dockerfile.portals.web -t goalladmin-portals-web:local .
```

前端镜像分别构建三个应用，把静态文件放到各自目录，保留每个应用的 `third-party-licenses.txt` 和 `version.json`。原 `Dockerfile.web` 继续只构建平台端。

## 启动示例

`deploy/docker-compose.portals.yml` 独立使用，不与单实例或双实例 Compose 叠加。它的项目名和数据库卷独立；已有部署的数据不会自动复制过来。

```sh
cp deploy/.env.example deploy/.env
# 编辑文件：填写两个数据库密码、三个独立的 JWT 密钥。
# 每项分别执行 openssl rand -base64 48 生成，不共用密钥。
# 只在本机试用时可设 GA_SERVER_MODE=debug。
docker compose --env-file deploy/.env -f deploy/docker-compose.portals.yml config --quiet
docker compose --env-file deploy/.env -f deploy/docker-compose.portals.yml up -d --build
docker compose --env-file deploy/.env -f deploy/docker-compose.portals.yml ps -a
docker compose --env-file deploy/.env -f deploy/docker-compose.portals.yml exec platform /server admin create -username admin
```

默认访问三个域名的 8000 端口，如 `http://agent.localhost:8000`。若本地系统没有把这些名字解析到回环地址，在 hosts 文件中把三个名字映射到 `127.0.0.1`。同时运行旧示例时，改 `GA_PORTALS_WEB_PORT` 避免端口冲突；本示例的来源白名单默认随该端口变化。

只有平台程序能执行迁移。`migrate` 先等 MySQL 健康，完成后显示 `Exited (0)`；三个常驻后端随后启动，nginx 等待它们首次健康。代理商/商户启动时只核对表结构，落后会拒绝启动。不要给它们加入自动迁移逻辑。

未启用 Redis 时，CLI 创建管理员属于进程外修改，运行中的平台服务会在授权缓存到期后读入角色，通常最多等待 15 秒。刚创建就登录可能暂时没有管理权限，稍后刷新页面即可。

先登录平台创建代理商和商户，保存页面上仅显示一次的初始密码。代理商/商户登录还需要主体编号；首次登录必须改密。代理商/商户程序没有 `admin create`，不能在那里创建平台超级管理员。账号管理方式见接口清单的主体管理部分。

## 域名、来源和密钥

`GA_PLATFORM_HOST`、`GA_AGENT_HOST`、`GA_MERCHANT_HOST` 只填小写域名，不带协议、端口、路径或通配符，并且互不相同。nginx 启动前验证它们，再把域名填入模板；其他 nginx 变量不参与环境替换。运行时模板机制来自[官方 nginx 镜像](https://hub.docker.com/_/nginx)。

生产分别设置 `GA_PLATFORM_ORIGIN`、`GA_AGENT_ORIGIN`、`GA_MERCHANT_ORIGIN` 为浏览器实际访问的 HTTPS 来源，例如 `https://agent.example.com`。每个后端只允许本端来源；原单实例用的 `GA_SERVER_ALLOWED_ORIGINS` 不控制这个三端示例。

每个后端只收到本端的 `GA_JWT_SECRET_<端>`。密钥必须持久保存，至少 32 字节，同端增加副本时共用本端密钥。不要把 JWT 密钥写进前端、Dockerfile 或构建参数。release 模式保持开启，刷新 Cookie 使用本端名称、`Secure` 和 `__Host-` 约束。三个域名必须都由可信方控制，不和不受信任的网站共用可写 Cookie 的主域。

nginx 每个域名只放行本端 API 前缀，其他端的 API 返回 404；后端还会独立检查端和令牌。未配置的 Host 返回 404。每个域名的 SPA 回退、静态资源、版本文件、许可证也只来自本端目录。

## 网络和 Redis

示例只发布 nginx 端口，后端和 MySQL 留在 Docker 网络。nginx 固定 `172.29.0.10`，三个后端只信任这个地址，并由 nginx 覆盖客户端转发头。网段冲突时同时改子网、固定地址、后端信任地址。前面还有 TLS 终结代理或 CDN 时，按实际可信链配置 nginx 的真实 IP 恢复；不能直接信任用户提供的转发头。

Redis 默认关闭：三个不同程序各一个实例可以独立运行。同一端运行多个实例时必须启用。要启用时，设置三个容器均可访问的 `GA_REDIS_ADDR`、认证、TLS、库号和前缀；Compose 会把它们传给全部三个后端。容器内的 `127.0.0.1` 指自己，不是宿主机。生产使用私有端点；不要把 Redis 公开到互联网。

三个端及其全部副本连接同一业务库、同一 Redis 前缀。缓存通知、计数、验证码的正常/降级行为，以及 TLS 私有 CA、ACL 和主从切换边界见[多实例部署指南](multi-instance.md)。Redis 计数故障允许降级，本地验证码仍依赖生成实例。

每个后端默认最多 20 个数据库连接、5 个空闲连接，可用 `GA_DB_MAX_OPEN_CONNS`/`GA_DB_MAX_IDLE_CONNS` 调整。计算数据库容量时累加三个程序及所有副本，再给迁移、管理和其他客户端预留余量。密码计算与主体并发预算也按实例计算。

会话历史由各后端自动清理（D-081），只处理本程序已注册的端。过期或吊销超过 30 天的会话才会删除，登录等日志继续保留。启动 1 分钟后首次检查；有积压或错误时隔 1 分钟继续，无积压时隔 1 小时检查。每端每轮最多删除 1000 行，两条删除共用 5 秒超时；多个副本可同时运行，不依赖 Redis。日志中的 `session history cleaned` 和 `session history cleanup failed` 分别记录删除数量和清理失败。

## 升级与验收

新部署默认 MySQL 8.4 LTS。已有 8.0 数据卷先用 `GA_MYSQL_IMAGE` 固定原镜像，再按[运维指南](operations.md#升级与迁移)独立安排数据库引擎升级；以下命令只说明应用迁移与更新，不能替代引擎升级检查。

先备份数据库，确认新增业务表的迁移已被平台程序注册。对不确定是否兼容旧程序的迁移，使用停机升级：

会话清理需要平台先执行 `00018_session_cleanup_indexes.sql`；该迁移只添加两个查询索引，不清理数据。大表建立索引需要时间和额外空间，应安排维护窗口，待迁移成功后再启动三个后端。

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.portals.yml stop web platform agent merchant
docker compose --env-file deploy/.env -f deploy/docker-compose.portals.yml build
docker compose --env-file deploy/.env -f deploy/docker-compose.portals.yml run --rm --no-deps migrate
# 上一条成功后再继续。
docker compose --env-file deploy/.env -f deploy/docker-compose.portals.yml up -d
```

迁移失败时保持后端停止，修正后重试，不删除数据库卷。回退应用前确认表结构兼容旧版本；框架不自动执行向下迁移。

发布前逐端验证：域名和页面对应；本端 API 可用、其他端 API 404；HTTPS 下登录、刷新、登出正常；主体主账号首次改密和员工权限正常；真实来源 IP 正确；三个后端的健康状态、`/readyz` 与日志正常。`/healthz` 是存活检查，`/readyz` 核对数据库，都不能替代真实登录验收。

这是三个端各一个实例的单机示例，nginx、MySQL 和宿主机仍是单点。同端横向扩展时，在对应 upstream 增加同端实例并配置亲和，不能把三个不同端混到一个 upstream。应用构建、代理定向测试与完整浏览器/生产环境验收应分别记录。


## 自助入驻

默认关闭。两个开关可以分别设置：`GA_ONBOARDING_AGENT_ENABLED` 控制代理商申请，`GA_ONBOARDING_MERCHANT_ENABLED` 控制商户申请和代理商创建商户邀请。关闭代理商申请不会单独关闭商户邀请。开启商户入驻时必须设置 `GA_ONBOARDING_MERCHANT_ORIGIN` 为商户前端来源，release 只接受 HTTPS。邀请链接使用该来源加 `/register`；本示例按根路径部署。

三端统一从 `deploy/.env` 读取这组设置，修改后需要重新创建三个后端容器。仅执行 `docker compose restart` 会保留容器原来的环境变量，不能应用 `.env` 中的新开关值。已完成数据库迁移的部署，可在维护窗口修改配置后执行：

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.portals.yml stop web
docker compose --env-file deploy/.env -f deploy/docker-compose.portals.yml up -d --no-deps --force-recreate --wait platform agent merchant
# 确认上一条成功，三个后端健康后恢复入口。
docker compose --env-file deploy/.env -f deploy/docker-compose.portals.yml up -d --no-deps --force-recreate web
```

分别访问代理商域名下的 `/api/agent/v1/onboarding/config` 和商户域名下的 `/api/merchant/v1/onboarding/config`，核对 `data.enabled`。代理商响应中的 `data.invitationsEnabled` 对应商户入驻开关。关闭后申请接口返回 404，即使申请人还打开着原来的表单，也不能继续提交；关闭商户入驻还会停止创建新邀请。

登录页按服务端开关显示申请入口。申请人提交主体名称、联系人、电话、主账号名及验证码，保存申请编号。平台在“入驻申请”中核实联系方式后通过或驳回；通过时才创建主体和主账号，初始密码只向审核人显示一次，由平台交付，首次登录必须改密。关掉开关后仍可审核已有申请、查看和撤销已有邀请；已开通账号仍可正常登录和使用。

代理商主账号在“商户邀请”生成链接，7 天有效且只能提交一份申请。未使用的链接可以撤销；过期和撤销不影响已提交的申请。没有邀请的商户直属平台，申请页面不能自行指定代理商。邀请不保存在浏览器存储；打开链接后凭证从地址栏移除，刷新该页须重新打开原邀请链接。


### 容器日志轮转

Compose 默认使用 `json-file`，每个容器最多保留 3 个、每个 10m 的标准输出日志文件（D-092）。这不是数据库审计表的保留策略，也不是整个主机磁盘上限；需要长期留存时，应接入外部日志采集。修改现有环境时需重建容器才生效，保留数据卷，不能只执行 restart。可用 `docker inspect --format '{{json .HostConfig.LogConfig}}' <容器>` 核对实际配置。
