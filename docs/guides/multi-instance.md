# 多实例部署

同一个程序运行多个实例时，用同一个 MySQL 保存业务、账号和会话，用同一个 Redis 传递缓存失效通知、共享计数和验证码。缓存数据仍在各实例内存中。设计见规范 §11.3、D-074 至 D-078。

## 先运行平台端双实例示例

`deploy/docker-compose.multi.yml` 是独立示例：一个 nginx、两个相同的平台后端、一个 MySQL、一个 Redis。它与原单实例 Compose 分开使用，不要用两个 `-f` 合并。示例使用独立项目名 `goalladmin-multi` 和数据卷，不会自动迁移原单实例数据。

```sh
cp deploy/.env.example deploy/.env
# 编辑 deploy/.env，分别填写 GA_DB_ROOT_PASSWORD、GA_DB_PASSWORD、
# GA_JWT_SECRET_PLATFORM，并取消 GA_REDIS_PASSWORD 的注释、填写随机密码。
# 各项分别生成，例如 openssl rand -base64 48。
docker compose --env-file deploy/.env -f deploy/docker-compose.multi.yml config --quiet
docker compose --env-file deploy/.env -f deploy/docker-compose.multi.yml up -d --build
docker compose --env-file deploy/.env -f deploy/docker-compose.multi.yml ps -a
docker compose --env-file deploy/.env -f deploy/docker-compose.multi.yml exec server-a /server admin create -username admin
```

`migrate` 先等待数据库健康，再执行一次 `migrate up`；退出码为 0 后，`server-a`、`server-b` 启动并校验表结构，常驻实例不自动迁移。`migrate` 显示 `Exited (0)` 是正常状态。nginx 等待两个后端首次健康后启动。Redis 使用 `service_started`，短暂不可用不阻止应用走本地降级。依赖条件的含义见 [Compose 启动顺序](https://docs.docker.com/compose/how-tos/startup-order/)。

浏览器默认访问 `http://localhost:8000`。正式环境在入口提供 HTTPS，把实际浏览器来源填入 `GA_SERVER_ALLOWED_ORIGINS`；前端与 `/api` 必须同源。仅本机试用时可设 `GA_SERVER_MODE=debug`；正式环境保留 `release`，刷新 Cookie 带 `Secure`。更改端口时同时更改允许来源。单实例与此示例同时运行时必须使用不同的宿主机端口。

只映射 nginx 的 80 端口；MySQL、Redis、后端端口留在容器网络。nginx 地址固定为 `172.31.0.10`，后端只信任它。如果该网段与现有网络冲突，应同时修改子网、nginx 地址和 `GA_SERVER_TRUSTED_PROXIES`。

本例只部署平台端。代理商和商户是不同程序、不同前端；不能把它们放进平台端 upstream。它们各自横向扩展时也要有自己的 upstream、域名和 JWT 密钥。三个端使用同一业务库和 Redis 前缀，端之间的隔离由框架处理；三端镜像和域名的 Compose 示例见[三端部署指南](portals-deployment.md)。

## 所有实例必须一致的配置

| 配置 | 要求 |
|---|---|
| 程序和模块 | 同一端使用同一版本、同一模块注册和权限声明 |
| MySQL | 同一业务库；每个实例有独立连接池，规划总连接数时累加所有实例 |
| JWT | 同一端所有实例使用同一个持久密钥；不同端使用不同密钥，不能依赖 debug 临时密钥 |
| Redis | 同一端点、数据库编号和 `keyPrefix`；不同部署的前缀互不为前缀，避免通道与键权限重叠 |
| 策略与时钟 | 登录防护、窗口、验证码、密码策略等一致；主机同步时间，Lua 使用应用传入的时间 |
| 浏览器来源 | 同一端使用一致的 `allowedOrigins`；信任代理只填实际入口地址 |

示例中的 Redis 使用单独密码、128 MB 上限、`noeviction`，关闭 RDB/AOF，容器重建后短期状态会丢失。容量数字只适合起步验证，应按实例数、并发和活跃记录量调整。内存满时报错并触发应用降级；不要用静默淘汰有效记录来换取表面成功。Redis 密码只保护连接认证，同一 Redis 中的不同数据库编号不是权限隔离。

生产中改用私有 Redis 端点时，两个实例一起设置 `GA_REDIS_ADDR`、`GA_REDIS_USERNAME`（如使用 ACL）、`GA_REDIS_PASSWORD`、`GA_REDIS_DB`、`GA_REDIS_KEY_PREFIX`、`GA_REDIS_TLS=true`。应移除本地 `redis` 服务及其启动依赖；这些值要加入两个实例共用的 environment，不能只写进 `.env` 而不传入容器。

TLS 使用系统根证书。私有 CA 需要加入主机或容器信任库；distroless 镜像不能在启动时执行安装脚本，应构建派生镜像，将包含原有可信根和私有 CA 的完整证书包放在 `/etc/ssl/certs/ca-certificates.crt`。保持服务器名称验证，不关闭证书校验。

## nginx 亲和、重试与健康检查

`deploy/nginx.multi.conf` 默认 `ip_hash`：同一来源通常落在同一实例，Redis 不可用时仍有机会在原实例验证本地验证码。NAT 后的大量客户端可能集中到同一实例，来源改变、实例退出或重新加入时亲和会改变；此时重新获取验证码。若使用更上层的会话亲和，也要保持可信代理链。

nginx 是直接入口时覆盖 `X-Forwarded-For` 为 `$remote_addr`。前面再加 CDN 或负载均衡时，先用 `set_real_ip_from` 只信任该层，再恢复真实客户端地址；不要直接信任客户端传来的转发头，否则亲和、限流和日志都会失真。

upstream 使用 Docker DNS `127.0.0.11` 动态解析，缓存 10 秒，容器换 IP 后不需要固定后端地址。部署到宿主机或其他编排平台时替换 resolver 与 upstream 地址。本配置依赖开源 nginx 1.27.3 之后的动态解析能力，项目镜像使用 1.30；参见 [nginx upstream](https://nginx.org/en/docs/http/ngx_http_upstream_module.html#server)。

容器的健康状态与 nginx 重新发现实例是两件事。停止或重建后的实例即使已经显示 `healthy`，也可能尚未重新进入 nginx 的可用后端列表；此时立即停止另一个实例，仍可能短暂返回 502/504。逐个维护时，先确认恢复实例已通过入口接收请求，再操作下一台，可用响应中的 `requestId` 对照该实例的访问日志。单次入口探针成功只证明选中的实例可用，不能据此认定两台都已恢复；也不要把 10 秒 DNS 缓存时间当成端到端恢复保证。升级仍采用下文的停机流程。

连接失败或超时会被动标记后端暂时不可用，后续可发往另一实例。`proxy_next_upstream` 不启用 `non_idempotent`，已经发出的登录、刷新等非幂等请求不切实例重试；请求失败后由客户端重新获取状态，不假设操作未发生。参见 [nginx 代理重试](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_next_upstream)。

`/healthz` 表示进程存活，`/readyz` 检查数据库。Redis 故障时就绪检查仍可成功，这是允许本地降级的设计。nginx 的探针只证明当前选中的一个实例；同时检查 Compose 中两个后端的健康状态。不要把 `/readyz` 当成 Redis 健康信号或整体集群就绪证明。

`/readyz` 在每个实例内缓存成功或失败结果 1 秒，并发探针共用一次最多 2 秒的数据库检查（D-084）。缓存从检查完成起算，故障和恢复需等缓存到期并完成下一次检查才能反映；无请求时不轮询。请求取消不影响其他探针，HTTP 响应仍是 no-store，各自保留请求 ID 和语言。

## 故障时会怎样

| 情况 | 行为 |
|---|---|
| Redis 正常 | 同端跨实例验证码只消费一次；共享登录失败/锁定与操作计数；提交后的失效通知使远端缓存重读 |
| Redis 不可用 | 缓存按原 TTL 工作；计数使用各实例本地影子，允许合计限制暂时放宽；不新增数据库计数查询 |
| 已发出的共享验证码遇 Redis 故障 | 校验失败，需要重新获取；不改走可重复消费的本地副本 |
| 故障期间新验证码 | 只在生成实例可用；恢复后仍属于该实例，直到消费或过期 |
| Redis 恢复 | 重新订阅并清缓存；保留 Redis 现存计数，不合并故障期间的本地历史 |
| 一个后端退出 | 其余实例仍能使用共享会话与共享状态；退出实例的本地验证码及本地计数丢失，进行中的请求可能失败 |

仍按实例计算的包括：密码哈希并发预算、按主体的并发上限、验证码绘制并发、数据库连接池，以及服务器监控页上的 CPU、内存、请求统计。这些值不能当成所有实例之和。缓存通知在 Redis 断线时可能丢失，普通缓存最多按原 15 秒窗口兜底；标记 `LiveAuth()` 的接口继续按库核对。

这份单机示例的 nginx、MySQL、Redis 和宿主机都仍是单点。生产高可用需要另外部署冗余入口、数据库与 Redis。当前客户端只连接一个稳定地址，不实现 Sentinel 发现或 Redis Cluster 拓扑路由；不能把若干节点地址直接拼给 `GA_REDIS_ADDR`。

Redis 复制与故障转移可能丢失已确认的写入；已消费的验证码记录也可能在旧副本上重新出现，所以“一次性”保证针对正常工作的同一 Redis 主节点，不承诺跨故障转移严格一次。若部署要求切换后旧挑战全部失效，应暂停入口、统一切换所有实例到新的前缀并重建进程，再恢复流量；不要让新旧前缀同时接请求。这个操作同时重置计数，符合接受计数失效的约定。复制语义见 [Redis replication](https://redis.io/docs/latest/operate/oss_and_stack/management/replication/)。

## Redis 版本与权限

客户端固定在 `server/go.mod` 的 go-redis v9.22.0；服务器最低 6.0。最低版本兼容性由本项目测试保证，客户端上游主要覆盖较新版本。部署示例选择 8.2，最低版本测试使用 6.0.20；这不表示每个中间版本、云代理或故障转移方案都已经验收。生产选受维护版本，并在实际端点上做发布前验证。

应用使用 `HELLO`/认证、按需 `SELECT`、`PING`、`INFO server`、`SET`、`EVAL`、`HGET`、`HSET`、`HDEL`、`HLEN`、`HINCRBY`、`ZADD`、`ZRANGE`、`ZRANGEBYSCORE`、`ZREM`、`PEXPIRE`、`SUBSCRIBE` 和 `PUBLISH`。`INFO server` 被禁止时会警告无法读取版本，部署者需自行确认服务器版本；其他必需权限仍会检查。限制可访问键为实际 `keyPrefix` 下的键；支持通道 ACL 的版本还应限制为 `<keyPrefix>invalidate`。不要只允许 EVAL 而禁止 Lua 内部使用的命令。Redis 6.0 与后续版本的通道 ACL 能力不同，按实际版本配置；参见 [Redis ACL](https://redis.io/docs/latest/operate/oss_and_stack/management/security/acl/)。

启动和恢复会探测实际命令权限。地址暂时不可达可以降级；认证错误、缺少必需权限等明确配置错误会拒绝启动。根据 `redis connected`、`redis unavailable`、`redis available again` 日志观察状态，避免仅凭进程存活判断 Redis 已接通。

## 升级与验证

新部署默认 MySQL 8.4 LTS。已有 8.0 数据卷先用 `GA_MYSQL_IMAGE` 固定原镜像，再按[运维指南](operations.md#升级与迁移)独立安排数据库引擎升级；以下流程针对应用与表结构更新。

升级前备份 MySQL，确认迁移是否兼容旧程序。本示例提供停机升级流程，不假定每次迁移都支持滚动升级：

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.multi.yml stop web server-a server-b
docker compose --env-file deploy/.env -f deploy/docker-compose.multi.yml build
docker compose --env-file deploy/.env -f deploy/docker-compose.multi.yml run --rm --no-deps migrate
# 上一条成功后再启动；迁移器会跳过已经完成的版本。
docker compose --env-file deploy/.env -f deploy/docker-compose.multi.yml up -d
```

迁移失败时保持应用停止，处理错误后再继续。不要删数据卷来解决迁移错误。回退应用前确认数据库结构与旧程序兼容，不能自动向下迁移。

专用测试数据库和 Redis 就绪后，在仓库根运行：

```sh
make test-multi-instance
```

该目标只运行 §13.2 第 167 条定向测试，默认连接测试端口 3307/6380。它创建独立临时数据库与 Redis 前缀，启动两个独立 Go 测试子进程，通过真实 HTTP 验证双向验证码与一次性消费、B 撤权后 A 拒绝、分散失败合计锁定、A 退出后 B 继续登录。测试用户源为各子进程独立的固定夹具，会话、授权和共享状态使用真实数据库及 Redis；答案仅通过测试进程管道取得。测试结束关闭子进程并清理临时状态。

这项定向测试不启动浏览器或生产镜像。发布前还应在实际 Compose/负载均衡和 Redis 端点上验证 HTTPS、Cookie、真实来源地址、故障切换、容量与数据备份，并运行项目的完整检查和浏览器测试。


### 容器日志轮转

Compose 默认使用 `json-file`，每个容器最多保留 3 个、每个 10m 的标准输出日志文件（D-092）。这不是数据库审计表的保留策略，也不是整个主机磁盘上限；需要长期留存时，应接入外部日志采集。修改现有环境时需重建容器才生效，保留数据卷，不能只执行 restart。可用 `docker inspect --format '{{json .HostConfig.LogConfig}}' <容器>` 核对实际配置。
