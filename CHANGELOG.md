# Changelog / 变更记录

给使用者看的变更记录，只在发布版本时更新；格式遵循 Keep a Changelog，版本号遵循语义化版本。每个版本先中文、后英文，内容一致，以中文为准。
v0.x 期间允许破坏性变更，但必须写进对应版本的"升级注意"。补丁版本（0.1.1、0.1.2…）收日常改动，次版本（0.2.0）留给较大的变动。

User-facing changelog, updated only when a version is released; the format follows Keep a Changelog and versions follow Semantic Versioning. Each version is written in Chinese first, then English; the two say the same thing and the Chinese text is authoritative.
Breaking changes are allowed during v0.x but must be listed under that version's "Upgrade notes". Patch versions (0.1.1, 0.1.2, …) collect day-to-day changes; minor versions (0.2.0) are reserved for larger ones.

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
- pnpm 与 npm 都可用；锁文件以 `pnpm-lock.yaml` 为准。

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
- Works with both pnpm and npm; `pnpm-lock.yaml` is the source of truth.

### Deployment and documentation

- Docker Compose (MySQL, backend, nginx-served frontend), an nginx example with CSP and other security headers, backend and frontend images (release mode by default); binaries, images and frontend builds ship with a third-party license list.
- `make dev`: rebuild and restart the backend automatically when the source changes.
- `docs/spec.md` technical spec, `docs/conventions.md` conventions, `docs/api.md` API reference, `docs/decisions.md` decision log, `docs/guides/new-module.md` new-module guide.

### Tests

- The 114 reverse tests listed in `docs/spec.md` §13.2, run against a real MySQL; Vitest on the frontend; Playwright smoke tests. `make ci` runs every check and `make e2e` runs the smoke tests.

Upgrade notes: none (first release).
