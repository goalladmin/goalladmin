# Changelog / 变更记录

给使用者看的变更记录，只在发布版本时更新；格式遵循 Keep a Changelog，版本号遵循语义化版本。每个版本先中文、后英文，内容一致，以中文为准。
v0.x 期间允许破坏性变更，但必须写进对应版本的"升级注意"。补丁版本（0.1.1、0.1.2…）收日常改动，次版本（0.2.0）留给较大的变动。

User-facing changelog, updated only when a version is released; the format follows Keep a Changelog and versions follow Semantic Versioning. Each version is written in Chinese first, then English; the two say the same thing and the Chinese text is authoritative.
Breaking changes are allowed during v0.x but must be listed under that version's "Upgrade notes". Patch versions (0.1.1, 0.1.2, …) collect day-to-day changes; minor versions (0.2.0) are reserved for larger ones.

## [0.2.0] - 2026-10-03

代理商端和商户端：三个独立的程序和前端共用一个数据库，主体之间数据隔离。另有 IP 黑白名单、可选的 Redis 与多实例、主体自助入驻、可选的密码哈希算法，以及三端和多实例的部署示例。

### 代理商端与商户端

- 三个程序：平台（`server`）、代理商（`server/cmd/agent`）、商户（`server/cmd/merchant`），共用一个 MySQL，各有接口前缀、密钥和前端应用；数据库迁移只由平台程序执行，另外两个程序启动时只核对。
- 主体（代理商、商户）与主体账号（`core/org`）：主体端按"编号 + 账号 + 密码"登录；每个主体有一个主账号；主体停用后它的账号一并不可用。
- 主体数据隔离（`core/scope`）：业务查询用 `scope.ByOrg` 套上主体条件，`scopetest` 模板检查模块的列表、详情、修改、删除都带隔离条件；主体只从会话来，请求里不接受指定主体的参数。
- 主体端自带的后台（`core/orgportal`）：数据中心、概览、子账号、角色与权限（角色按主体各自一套）、会话、登录日志、操作日志、个人中心、安全设置；代理商端另有只读的"名下商户"。
- 平台端新增代理商管理、商户管理：开通、启停、资料、主账号（重置密码、更换）、查看账号与会话并下线、按主体看登录日志和操作日志；商户可以挂在代理商名下。运维中心的错误日志、安全事件、调查时间线可以按端筛选。
- 主体自助入驻（默认关闭）：申请人在代理商端、商户端提交资料，平台审核通过后开通；代理商可以生成邀请链接，受邀的商户归到自己名下。

### 访问控制与认证

- IP 黑名单与白名单：平台维护对三个程序都生效的黑名单，可以给端、主体、单个账号设白名单；主体的主账号在自己的后台维护本主体的白名单和黑名单，平台可以查看和清空；命令行 `server ip`。录入支持 CIDR 和 `116.88.8.*`、`116.88.*.*`、`116.*.*.*` 这样的 IPv4 星号网段。
- 密码哈希默认 Argon2id，可按部署选 bcrypt 或 PBKDF2（HMAC-SHA256、HMAC-SHA512，给有 FIPS 140 要求的部署）；核对时哪种都认，账号登录成功后自动换成选定的算法。密码计算的并发上限可配置（`server.passwordParallel`），登录和解锁优先。
- 刷新凭证带会话级的家族密钥：同一份凭证被两方各自使用时，会话自动吊销。
- 登录防护：同一账号每分钟的登录请求超限后要求图形验证码；同一账号 15 分钟内成功改密的次数有上限。
- 路由分组可以声明"按库核对"（`PortalRouter.LiveAuth()`）：认证时不用状态缓存，每个请求读会话、账号和主体；主体端后台默认启用，业务模块可以选用。
- 授权：非超管启用、停用角色都按分配角色的规则检查；没有"查看角色"权限的人分配角色被拒时只看到笼统的原因（`rbac.Perm.RoleView`）；菜单可以声明"仅主账号可见"（`MenuNode.SuperOnly`）；角色数量和每个账号分配的角色数量有上限。
- JSON 接口只接受 `application/json` 类型的请求体，未声明的字段、重复的键和多余的内容会被拒绝；操作日志对过长的内容标明截断。
- 过期或吊销超过 30 天的会话由后台分批清理。

### Redis 与多实例（可选）

- `redis.addr` 留空时和 v0.1.0 一样，每个程序一个实例。配置之后同一个程序可以跑多个实例：实例之间、三个程序之间传递缓存失效通知，共用登录防护的次数与锁定、验证码答案和各类操作计数。
- Redis 不可用时退回本实例的内存继续工作，业务请求照常；恢复后重新共用。
- `/readyz` 就绪检查带短缓存，多个探针同时访问时合并成一次数据库检查。

### 前端

- 新增 `web/apps/agent`、`web/apps/merchant` 两个应用，和平台端共用 `@ga/shell`；主体端的内置页面、数据中心组件 `GaDataCenter`、入驻申请页都在壳里，端的入口多一项 `scoped: true`。
- 前端的依赖方向检查：应用只能引用自己目录里的文件和 `@ga/shell`（ESLint 规则 `ga/boundary`）。
- 构建编号和第三方许可证清单的 Vite 插件移到 `@ga/shell/vite`。
- 三个端的页面底部显示版权说明。

### 部署与文档

- 三端部署：三个后端镜像、三套前端的 nginx 镜像和按域名分端的 Compose（`deploy/docker-compose.portals.yml`）；多实例示例（`deploy/docker-compose.multi.yml`）。部署示例默认使用 MySQL 8.4 LTS，容器日志按大小轮转。
- 开发命令：`make run-agent`、`make run-merchant`、`make dev-agent`、`make dev-merchant`、`make web-dev-agent`、`make web-dev-merchant`、`make test-multi-instance`；`make test-db-up` 同时启动测试用的 MySQL 和 Redis。
- 文档：`docs/README.md` 文档导航；新增后台使用、运维、排障、三端部署、多实例、密码哈希六份指南；新增模块指南补充了主体端模块的写法。

### 测试

- `docs/spec.md` §13.2 的反向测试从 114 条增加到 203 条，连真实 MySQL 和 Redis 跑；Playwright 冒烟覆盖三个端。

### 升级注意

1. 数据库：新增迁移 `00013`–`00019`。先用新版平台程序执行 `server migrate up`，再启动各个端；代理商、商户程序不执行迁移。
2. 刷新 Cookie 的格式变了：各端一起换成新版本，不要让新旧版本的程序同时运行。升级后已登录的用户不用重新登录；回退到 v0.1.0 时，这期间登录或刷新过的用户要重新登录。
3. 自己写脚本调接口的，请求要带 `Content-Type: application/json`，请求体里不能有接口没有声明的字段。
4. 同一账号的登录请求过多时，响应由 429 改为要求验证码（`accountRatePerMinute` 的含义随之变化）。
5. 新密码默认用 Argon2id 生成：已有的 bcrypt 密码照常能登录，登录成功后自动更换。每个进行中的密码计算约占 19 MiB 内存，小内存的机器调小 `server.passwordParallel`，或把 `server.passwordHash` 设成 `bcrypt`。见 `docs/guides/password-hash.md`。
6. 启动校验：`database.maxOpenConns` 必须不小于 1；release 模式下 `server.trustedProxies` 不能是覆盖全部地址的网段。
7. 部署示例的 MySQL 默认镜像改为 8.4：已有 8.0 数据卷的部署，更新 Compose 之前先把 `GA_MYSQL_IMAGE` 固定为原来的镜像，数据库引擎的升级另行安排。见 `docs/guides/operations.md`。
8. 公开接口：`portal.OrgProvider` 改名为 `portal.DeptProvider`（`Org` 留给主体）；前端应用的构建插件改为从 `@ga/shell/vite` 引入，原来 `apps/platform/build/` 下的两个文件已删除。
9. 后端测试需要 Redis（`GA_TEST_REDIS`），`make test-db-up` 会一起启动。前端统一使用 pnpm。

---

Agent and merchant portals: three separate programs and frontends share one database, with data isolated between organisations. Also new: IP denylists and allowlists, optional Redis with multiple instances, self-service applications for organisations, a choice of password hashing algorithms, and deployment examples for three portals and for multiple instances.

### Agent and merchant portals

- Three programs: platform (`server`), agent (`server/cmd/agent`) and merchant (`server/cmd/merchant`). They share one MySQL database and each has its own API prefix, secret and frontend application; only the platform program runs database migrations, the other two only verify them at startup.
- Organisations (agents, merchants) and their accounts (`core/org`): organisation portals sign in with "code + username + password"; every organisation has one owner account; disabling an organisation disables its accounts with it.
- Organisation data isolation (`core/scope`): business queries apply the organisation condition with `scope.ByOrg`, and the `scopetest` template checks that a module's list, detail, update and delete all carry it; the organisation comes only from the session, and requests may not name one.
- A built-in back office for organisation portals (`core/orgportal`): data center, overview, employee accounts, roles and permissions (each organisation has its own roles), sessions, login log, operation log, profile and security settings; the agent portal also has a read-only "my merchants" page.
- The platform portal gains agent management and merchant management: open, enable and disable, edit details, manage the owner account (reset password, replace), view accounts and sessions and sign them out, and read login and operation logs per organisation; a merchant can belong to an agent. The operations centre's error log, security events and investigation timeline can be filtered by portal.
- Self-service applications (disabled by default): applicants submit their details on the agent or merchant portal and the platform approves them; an agent can create invitation links, and merchants who accept them belong to that agent.

### Access control and authentication

- IP denylists and allowlists: the platform maintains a denylist that applies to all three programs and can set allowlists for a portal, an organisation or a single account; an organisation's owner maintains that organisation's own allowlist and denylist in its back office, and the platform can view and clear them; the `server ip` command. Entries accept CIDR as well as IPv4 wildcard ranges such as `116.88.8.*`, `116.88.*.*` and `116.*.*.*`.
- Password hashing defaults to Argon2id, with bcrypt or PBKDF2 (HMAC-SHA256, HMAC-SHA512, for deployments with FIPS 140 requirements) selectable per deployment; every algorithm is accepted on verification, and an account moves to the selected one after a successful sign-in. The concurrency limit for password computation is configurable (`server.passwordParallel`), with sign-in and unlock taking priority.
- Refresh credentials carry a per-session family secret: when the same credential is used by two parties, the session is revoked automatically.
- Login protection: once an account exceeds its sign-in requests per minute, a captcha is required; successful password changes per account are limited within 15 minutes.
- A route group can declare live verification (`PortalRouter.LiveAuth()`): authentication skips the status cache and reads the session, account and organisation on every request; organisation back offices use it by default and business modules may opt in.
- Authorization: for non-super administrators, enabling and disabling a role follow the same rules as assigning it; someone without permission to view roles sees only a general reason when an assignment is refused (`rbac.Perm.RoleView`); a menu can be declared owner-only (`MenuNode.SuperOnly`); the number of roles and of roles per account is bounded.
- JSON endpoints accept only `application/json` request bodies and reject undeclared fields, duplicate keys and trailing content; the operation log marks content it has truncated.
- Sessions that expired or were revoked more than 30 days ago are removed in batches in the background.

### Redis and multiple instances (optional)

- With `redis.addr` empty, behaviour is the same as v0.1.0: one instance per program. Once configured, a program can run several instances: cache invalidation is passed between instances and between the three programs, and login-protection counts and lockouts, captcha answers and operation counters are shared.
- When Redis is unavailable, each instance falls back to its own memory and keeps serving requests; sharing resumes when Redis returns.
- The `/readyz` readiness check has a short cache, and concurrent probes are merged into one database check.

### Frontend

- New `web/apps/agent` and `web/apps/merchant` applications sharing `@ga/shell` with the platform portal; the organisation portals' built-in pages, the `GaDataCenter` component and the application page live in the shell, and a portal's entry file adds `scoped: true`.
- A dependency-direction check for the frontend: an application may import only its own files and `@ga/shell` (ESLint rule `ga/boundary`).
- The Vite plugins for the build id and the third-party licence list moved to `@ga/shell/vite`.
- A copyright line at the bottom of every page in all three portals.

### Deployment and documentation

- Three-portal deployment: three backend images, an nginx image serving the three frontends and a Compose file that separates portals by domain (`deploy/docker-compose.portals.yml`); a multi-instance example (`deploy/docker-compose.multi.yml`). The deployment examples default to MySQL 8.4 LTS and rotate container logs by size.
- Development commands: `make run-agent`, `make run-merchant`, `make dev-agent`, `make dev-merchant`, `make web-dev-agent`, `make web-dev-merchant`, `make test-multi-instance`; `make test-db-up` starts both the test MySQL and Redis.
- Documentation: `docs/README.md` as the documentation index; six new guides covering back-office usage, operations, troubleshooting, three-portal deployment, multiple instances and password hashing; the new-module guide now covers organisation-portal modules.

### Tests

- The reverse tests in `docs/spec.md` §13.2 grew from 114 to 203 and run against a real MySQL and Redis; the Playwright smoke tests cover all three portals.

### Upgrade notes

1. Database: new migrations `00013`–`00019`. Run `server migrate up` with the new platform program first, then start each portal; the agent and merchant programs do not run migrations.
2. The refresh cookie format changed: switch all portals to the new version together and do not run old and new programs side by side. Signed-in users do not need to sign in again after the upgrade; after a rollback to v0.1.0, users who signed in or refreshed in the meantime do.
3. Scripts that call the API must send `Content-Type: application/json` and may not include fields the endpoint does not declare.
4. When one account receives too many sign-in requests, the response is now a captcha requirement instead of 429 (the meaning of `accountRatePerMinute` changes accordingly).
5. New passwords are hashed with Argon2id by default: existing bcrypt passwords keep working and are replaced after a successful sign-in. Each password computation in progress uses about 19 MiB of memory; on small machines lower `server.passwordParallel` or set `server.passwordHash` to `bcrypt`. See `docs/guides/password-hash.md`.
6. Startup validation: `database.maxOpenConns` must be at least 1; in release mode `server.trustedProxies` may not be a range that covers every address.
7. The deployment examples now default to the MySQL 8.4 image: with an existing 8.0 data volume, pin `GA_MYSQL_IMAGE` to the image you are running before updating the Compose file, and plan the database engine upgrade separately. See `docs/guides/operations.md`.
8. Public API: `portal.OrgProvider` was renamed to `portal.DeptProvider` (`Org` now means organisation); frontend applications import the build plugins from `@ga/shell/vite`, and the two files under `apps/platform/build/` were removed.
9. Backend tests need Redis (`GA_TEST_REDIS`); `make test-db-up` starts it as well. The frontend uses pnpm only.

## [0.1.0] - 2026-10-01

第一个版本：一个能安全上线的后台脚手架——Go 后端内核、Vue 3 前端壳和平台端的整套管理页面。

### 后端内核

- 端（Portal）抽象与注册；内置并启用 `platform` 端，路由前缀 `/api/platform/v1`。
- 认证：账号密码登录；15 分钟访问令牌（JWT，HS256）加 HttpOnly 轮换刷新凭证，重放检测，刷新绑定会话；登录、刷新、登出都校验请求来源；登出、强制改密、修改密码；账号停用、改密、吊销即时生效。release 模式下刷新 Cookie 带 `__Host-` 前缀和 `Secure`。
- 登录防护：图形验证码按需出现，按 IP（IPv6 按 /64）和按账号限流，失败锁定，登录名归一化后计数；改密核对旧密码、验证码接口都有限流；计数有容量上限。阈值按端在配置文件里调整，不能低于底线，后台只读展示。
- 密码策略：最短长度、大小写与符号要求、有效期，按端配置；新账号和被重置的账号首次登录必须改密。
- 服务端锁屏：锁定期间除查看自己、解锁、登出外一律拒绝，连续输错 5 次吊销会话。
- 授权：权限码与菜单在代码里声明，角色与授权存库；四档路由守卫（`Public` / `AuthOnly` / `Require` / `RequireSuper`）；超级管理员与敏感权限规则；非超管授不出、借不到自己没有的权限，也不能对超管账号做写操作；重置密码只有超管能做，超管的密码只能在服务器命令行重置；最后一个可用超管在并发下也不会丢。
- 按部门的数据权限：仅本人、本部门、本部门及下级、全部四档，按"角色 × 数据资源"设置，只收窄、不放宽；业务模块用 `rbac.Service.DataFilter` 套用。
- 写操作的一致性：权限判断和写入在同一把锁里按已提交的状态做（`WithActor`、`WithSelf`）；授权数据以快照整体发布；账号停用、会话吊销、权限收回之后，在途的写请求作废。
- 菜单管理：结构归代码，后台只改显示名、图标、顺序、位置和隐藏，可以建分组。
- 字典：代码声明和后台维护共用一张表，代码声明的值后台改不了；读取接口按端和界面语言返回。
- 审计：操作日志按路由开启，请求体脱敏、截断；登录日志；安全事件（越权被拒、伪造令牌、凭证重放、请求来源不对等）；错误日志按类合并；调查时间线按用户、IP 或会话把记录串起来；查看审计数据本身也留痕。所有日志只读、不能删改，并以 `AUDIT` 级别同时写进日志输出。
- 监控：进程自身的请求量、耗时、CPU、内存、协程、数据库连接池等只读指标（可以关闭），以及安全态势统计。
- 多语言：11 种界面语言；接口错误说明按 `Accept-Language` 返回，字段错误带翻译键。
- 工程：内置 SQL 迁移器（迁移可重跑）；`server` 子命令（`serve`、`migrate`、`admin create`、`admin reset-password`、`rbac prune`、`healthcheck`）；统一响应信封与错误码；请求 ID、安全响应头、请求体上限、完整的 HTTP 超时和请求时限；数据库连接、读写超时和可选的启动等待；release 模式启动校验；SQL 日志不带绑定参数；依赖方向检查。

### 平台端页面

- 数据中心、安全监控、工作台。
- 权限管理：用户（部门、岗位、角色、启停，清除头像）、角色与授权（功能权限 + 数据权限）、部门、岗位、菜单管理。
- 运维中心：操作日志、登录日志、错误日志、安全事件、调查时间线、会话管理。
- 系统设置：字典管理、只读的安全设置。
- 个人中心：改资料、上传或选内置头像、改密、下线其他设备。

### 前端壳 `@ga/shell`

- 一行装配的端应用（`createPortalApp`）；请求客户端（Bearer、401 单飞刷新后重放、409 按间隔重试、跨标签页刷新锁、换了人之前的结果一律作废）；访问令牌只在内存。
- 菜单转动态路由与组件键白名单；`v-perm` / `hasPerm` 按钮权限；`useDict` / `GaDictTag`。
- 布局：侧边栏、顶栏、面包屑、标签页、全局搜索（⌘K / Ctrl+K）、偏好设置、暗色模式、全屏、锁屏、移动端；登录、强制改密、个人信息、403、404 页面；11 种语言与带国旗的语言选择；服务器换了新版本时提示刷新。
- 前端使用 pnpm，依赖版本以 `pnpm-lock.yaml` 为准。

### 部署与文档

- Docker Compose（MySQL + 后端 + nginx 托管的前端）、nginx 示例（CSP 等安全响应头）、后端与前端镜像（默认 release 模式）；二进制、镜像和前端产物都随附第三方许可证清单。
- `make dev`：后端源码一改就自动重新编译、重启。
- `docs/spec.md` 技术规范、`docs/conventions.md` 约定、`docs/api.md` 接口清单、`docs/decisions.md` 决策记录、`docs/guides/new-module.md` 新增模块指南。

### 测试

- `docs/spec.md` §13.2 列出的 114 条反向测试，连真实 MySQL 跑；前端 Vitest；Playwright 冒烟。`make ci` 一键跑全部检查，`make e2e` 跑冒烟。

升级注意：无（首个版本）。

---

First release: an admin scaffold that is safe to put in production — a Go backend core, a Vue 3 frontend shell and a complete set of management pages for the platform portal.

### Backend core

- Portal abstraction and registration; the `platform` portal is built in and enabled, with the route prefix `/api/platform/v1`.
- Authentication: username and password login; 15-minute access tokens (JWT, HS256) plus HttpOnly rotating refresh credentials with reuse detection, with refresh bound to the session; login, refresh and logout all check where the request comes from; logout, forced and voluntary password change; disabling an account, changing its password or revoking a session takes effect immediately. In release mode the refresh cookie carries the `__Host-` prefix and `Secure`.
- Login protection: captcha on demand, rate limiting per IP (per /64 for IPv6) and per account, lockout after repeated failures, usernames normalized before counting; checking the old password on a password change and the captcha endpoint are rate-limited too; the counters have a capacity limit. Thresholds are set per portal in the config file, cannot go below fixed minimums, and are shown read-only in the admin UI.
- Password policy: minimum length, upper-case, lower-case and symbol requirements and maximum age, per portal; new accounts and accounts whose password was reset must change it at first login.
- Server-side lock screen: while locked, everything except viewing oneself, unlocking and logging out is refused; five wrong passwords in a row revoke the session.
- Authorization: permission codes and menus declared in code, roles and grants stored in the database; four route guards (`Public` / `AuthOnly` / `Require` / `RequireSuper`); super-administrator and sensitive-permission rules; a non-super user can neither grant nor borrow permissions they do not hold, nor write to a super administrator's account; only a super administrator can reset passwords, and a super administrator's own password can only be reset on the server command line; the last usable super administrator survives concurrent requests.
- Department-based data scopes: self, own department, own department and below, or everything, set per role and per data resource; scopes only narrow a permission, never widen it; business modules apply them with `rbac.Service.DataFilter`.
- Consistent writes: permission checks and the writes that depend on them run under the same lock against committed state (`WithActor`, `WithSelf`); authorization data is published as a whole snapshot; in-flight writes are discarded once the account is disabled, the session revoked or the permission withdrawn.
- Menu management: the structure belongs to code; the admin UI only changes titles, icons, order, placement and visibility, and can create groups.
- Dictionaries: code-declared and admin-maintained dictionaries share one table, and values declared in code cannot be changed from the admin UI; the read endpoint returns what the portal may see in the interface language.
- Auditing: operation logging switched on per route, with request bodies masked and truncated; login logs; security events (denied access, forged tokens, replayed credentials, requests from the wrong origin and more); an error log that groups failures by kind; an investigation timeline that strings records together by user, IP or session; viewing audit data is itself recorded. All logs are read-only, cannot be edited or deleted, and are also written to the log output at the `AUDIT` level.
- Monitoring: read-only metrics of the process itself — request volume, latency, CPU, memory, goroutines, database connection pool and more (can be switched off) — plus security statistics.
- Internationalization: 11 interface languages; API error messages follow `Accept-Language`, and field errors carry translation keys.
- Engineering: built-in SQL migrator (migrations are re-runnable); `server` subcommands (`serve`, `migrate`, `admin create`, `admin reset-password`, `rbac prune`, `healthcheck`); uniform response envelope and error codes; request IDs, security headers, request-body limit, complete HTTP timeouts and a per-request deadline; database connect and read/write timeouts and an optional startup wait; release-mode startup validation; SQL logs without bound parameters; dependency-direction check.

### Platform portal pages

- Data center, security monitoring and workspace.
- Access control: users (department, positions, roles, enable/disable, clear avatar), roles and grants (functional permissions plus data scopes), departments, positions and menu management.
- Operations centre: operation logs, login logs, error log, security events, investigation timeline and sessions.
- Settings: dictionary management and read-only security settings.
- Profile center: edit details, upload or pick a built-in avatar, change password, sign out other devices.

### Frontend shell `@ga/shell`

- A portal app assembled with one call (`createPortalApp`); request client (Bearer, single-flight refresh and replay on 401, spaced retries on 409, a cross-tab refresh lock, and results from before a change of user are always discarded); the access token lives only in memory.
- Menus become dynamic routes through a component-key whitelist; `v-perm` / `hasPerm` button permissions; `useDict` / `GaDictTag`.
- Layout: sidebar, top bar, breadcrumbs, tabs, global search (⌘K / Ctrl+K), preferences, dark mode, full screen, lock screen and mobile; login, forced password change, profile, 403 and 404 pages; 11 languages with a flag-based language picker; a prompt to reload when the server has been updated.
- Uses pnpm for the frontend; `pnpm-lock.yaml` is the source of truth.

### Deployment and documentation

- Docker Compose (MySQL, backend, nginx-served frontend), an nginx example with CSP and other security headers, backend and frontend images (release mode by default); binaries, images and frontend builds ship with a third-party license list.
- `make dev`: rebuild and restart the backend automatically when the source changes.
- `docs/spec.md` technical spec, `docs/conventions.md` conventions, `docs/api.md` API reference, `docs/decisions.md` decision log, `docs/guides/new-module.md` new-module guide.

### Tests

- The 114 reverse tests listed in `docs/spec.md` §13.2, run against a real MySQL; Vitest on the frontend; Playwright smoke tests. `make ci` runs every check and `make e2e` runs the smoke tests.

Upgrade notes: none (first release).
