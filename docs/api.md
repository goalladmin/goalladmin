# 接口清单

路径前缀 `/api/<portal>/v1`，v0.1 只有 `platform` 端。所有响应都是信封 `{code, data, msg, key?, params?, requestId}`：`code = 0` 成功；业务性失败（登录失败、参数校验、资源冲突）返回 HTTP 200 加非零 `code`；协议层问题用真实状态码（见文末）。`msg` 按 `Accept-Language` 选择语言（支持 11 种，规则见规范 §9.6），没有这个头时用简体中文，不支持的语言用英文；前端每个请求都带当前界面语言。字段错误 `data.fields` 为 `[{field, message, key?, params?}]`，业务自定的失败说明在信封的 `key`、`params` 里：前端有 `err.<key>` 的翻译时代入 `params` 显示，没有时显示英文的 `message` / `msg`。请求头 `X-Request-Id` 符合 `^[A-Za-z0-9-]{8,64}$` 时沿用，否则由服务端生成，响应头和响应体里都带。

需要登录的接口带 `Authorization: Bearer <accessToken>`。访问令牌 15 分钟有效，过期后用 `/auth/refresh`（靠 HttpOnly Cookie：release 模式 `__Host-ga_rt_<portal>`，debug 模式 `ga_rt_<portal>`，D-058）换新的；`@ga/shell` 的请求客户端已经把这一套做完了。

分页接口的参数：`page`（从 1 开始，上限 5000，D-055）、`pageSize`（默认 20，上限 200）、`keyword`、`sortBy`、`sortOrder`（`asc` / `desc`；`sortBy` 只接受各接口列出的字段）；响应 `data` 为 `{list, total, page, pageSize}`。

## 公开接口（不需要登录）

每个端都有这三条，其余接口一律需要登录。新增 Public 路由必须同步登记到这里（有测试校验）。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/auth/captcha` | 图形验证码：`{captchaId, image}`。同一来源 IP 每分钟最多 30 张、同时最多画 8 张，超出回 429 `4029`（D-055） |
| POST | `/auth/login` | 要求 `X-GA-Client: web`、Origin 白名单、`Content-Type: application/json`，缺一回 403 `2001`，不建会话、不写 Cookie（D-051）；入参 `username`（先去首尾空白、转小写再处理，大小写变体是同一个账号、共用一份限流配额）、`password`、`captchaId?`、`captchaCode?`；出参 `accessToken`、`tokenType`、`expiresIn`、`mustChangePwd`、`pwdExpired`（必须改密的原因是密码过期）、`sessionId`（这个令牌所属的会话，D-048）；同时下发 HttpOnly 刷新 Cookie；端配置了 `captchaAlways` 时每次都要带验证码；限流回 429 `4029`，登录防护的计数表满时没有记录的新来源也回 429（D-056）；服务端同时核对密码的请求太多时也回 429、不算一次失败（D-058）；被限流、被锁定的请求不写登录日志，只记安全事件（D-058） |
| POST | `/auth/refresh` | 靠 Cookie；要求 `X-GA-Client: web` 与 Origin 白名单；出参同登录；并发冲突返回 409 `1005`（客户端隔一小段时间再试）；可带 `X-GA-Session: <sessionId>` 声明要续的会话，Cookie 不是这个会话时回 401 `1004`（翻译键 `auth.sessionSwitched`），不轮换、不改 Cookie（D-048、D-050）。查账号时数据库临时出错回 503 `5003`，这时还没轮换，同一个凭证稍后照样能用（D-050）。凭证和会话的当前值、上一个值都对不上时回 401 `1004`，会话不吊销、Cookie 不改，记安全事件 `refresh_mismatch`（D-049）；上一个值在宽限期外重放才吊销整个会话（`refresh_reuse`）。刷新失败的任何情况都不改 Cookie。失败时服务端确定会话已经结束（不存在、已吊销、已过期，或这次因重放、账号停用被吊销）的，401 带翻译键 `auth.sessionEnded`，前端据此才把没确认的退出算作确认（D-052）；带了多个同名的刷新 Cookie 时回 401 并记安全事件 `refresh_cookie_dup`（D-058） |

## 认证接口（登录即可）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/auth/logout` | 吊销当前会话；要求 `X-GA-Client: web` 与 Origin 白名单；不动刷新 Cookie（服务端只在登录和刷新成功时写它、从不删除，D-049：退出的响应迟到时浏览器里的 Cookie 可能已经是别人新登录的） |
| GET | `/auth/me` | `{user, locked, perms, menus, pwdPolicy}`；锁屏时 `locked` 为真，`perms`、`menus` 为空；`menus` 是按权限裁剪后的菜单树，已叠加菜单管理里的调整，节点带 `titles`（后台改过的显示名，按语言；前端优先用它，没有当前语言时翻译 `titleKey`），分组节点的 `path` 为空；`user.mustChangePwd` 为真时除 `/auth/*` 外一律 403 `2002`（首次登录、被重置或密码过期，过期时 `user.pwdExpired` 为真）；`pwdPolicy` 为 `{minLength, requireUpper, requireLower, requireSymbol, maxAgeDays}`；`user.avatar` 是空、`preset:<名字>` 或 `upload:<键>`（D-040） |
| PUT | `/auth/password` | 入参 `oldPassword`、`newPassword`；同一会话 15 分钟内最多核对 5 次旧密码（成功清零），之后回 429 `4029`、记安全事件 `pwd_change_throttled`（D-055）；成功后其他会话失效。本次会话已被吊销（例如管理员刚重置了密码）回 401（D-045、D-046）；验证旧密码之后密码已被换掉（例如同一会话的另一个改密请求先完成）按旧密码不对回 3001（D-047） |
| POST | `/auth/lock` | 锁屏（D-027）：锁定当前会话。之后除 `/auth/me`、`/auth/unlock`、`/auth/logout`（以及不需要登录的 `/auth/refresh`）外的接口一律 HTTP 423、`1006`；刷新换来的新令牌照样锁着；操作日志 `auth.lock` |
| POST | `/auth/unlock` | 入参 `password`（当前账号的密码）；输错回 `3001`，字段错误键 `password.unlockIncorrect`、参数 `left`（剩余次数）；连续输错 5 次吊销会话（`unlock_failed`）并回 401；输错同样计入登录防护，登录防护的计数表满、放不下这次核对时回 429 `4029`，不占解锁次数（D-057）；同时核对密码的请求太多时同样回 429、不占次数（D-058）；操作日志 `auth.unlock`（密码打码） |
| GET | `/dicts` | 读取字典：`codes=a,b`（1–50 个，逗号分隔）；返回 `{编码: {code, name, valueType, items}}`，只含属于当前端或所有端共用、且启用中的字典，看不到的编码直接略过。`items` 为树形 `[{value, label, color?, extra?, status, children?}]`：`value` 按字典的 `valueType` 输出为字符串或数字；`label`、`name` 按 `Accept-Language` 选语言；停用的项也返回（`status: 0`），用于显示历史数据，下拉只该列启用的项 |

## 系统管理（platform 端，前缀 `/api/platform/v1/system`）

守卫为 `Require`，括号内是所需权限码；标 **敏感** 的权限码只有超管能授予。

按部门的数据权限（D-039）：用户的列表、详情、下拉，以及修改、启停、分配角色、会话的查看和下线，都只对操作人在对应权限码上的数据范围内的人生效；范围外的人一律当作不存在（404）。新建用户和改部门时，新部门必须在范围内，否则 403、字段 `system.user.deptOutOfScope`；非超管改自己的部门 403、字段 `system.user.ownDept`；非超管改部门上级要用户资源的每个权限码都是全部范围，否则 403、字段 `system.dept.moveNeedsAll`；部门负责人只能选"查看用户"范围内的人（范围外的和不存在的一样回 `system.dept.leader`）。新建、修改、分配角色成功后，目标不在"查看用户"范围内时不回显资料（修改回 `data: null`，新建只回 `id` 和 `username`）。非超管建用户时带 `roleIds` 要有 `system:user:assign-role`。非超管把停用的角色重新启用，按分配角色的规则检查（`PUT /roles/:id`）。

| 方法 | 路径 | 权限码 | 说明 |
|---|---|---|---|
| GET | `/users` | `system:user:list` | 分页；`keyword` 匹配账号、显示名、邮箱；`status` 过滤；`deptId` 按部门过滤，默认包括下级部门（`withChildren=0` 只看这一个部门）；`sortBy` 取 createdAt / username / sort / lastLoginAt。每行带 `deptId`、`deptName`、`posts: [{id, code, name}]`（D-033）和 `bio`（个人简介，D-038） |
| GET | `/users/:id` | `system:user:list` | 范围外的人 404（D-039） |
| POST | `/users` | `system:user:create` **敏感** | 入参 `username`、`password?`、`displayName?`、`email?`、`phone?`、`roleIds?`、`deptId?`、`postIds?`、`sort?`、`remark?`；部门、岗位必须存在且启用（D-033）；不传密码则生成，响应里 `initialPassword` 只返回这一次；新账号下次登录必须改密 |
| PUT | `/users/:id` | `system:user:update` | 资料（含 `bio` 个人简介）和部门、岗位，不含密码、状态、角色、头像（传 `avatar` 不改，D-040）。非超管改超管账号回 403，字段 `system.user.superProtected`（D-035）。`deptId`、`postIds` 不传表示不改，`postIds: []` 清空；新选的部门、岗位必须存在且启用，原来就有的停用了也能保留；岗位最多 20 个、整体替换（D-033） |
| POST | `/users/:id/status` | `system:user:status` | `{status: 0|1}`；停用会吊销全部会话；不能停用自己或最后一个超管。非超管对超管账号（改资料、启停）一律 403，字段 `system.user.superProtected`（D-035）；非超管重新启用停用的账号时，账号当前启用的角色要都是他能分配的（含敏感权限码、含自己没有的权限码或范围更宽的回 403，D-058；错误只给通用键 `rbac.user.enableNotAssignable`，不列具体角色和权限码，D-059） |
| DELETE | `/users/:id/avatar` | `system:user:update` | 管理员清除别人的头像（D-040，只能清除、不能替别人换）：受数据范围约束（范围外 404），非超管动超管账号 403、字段 `system.user.superProtected`。记操作日志 `user.avatar-clear` |
| POST | `/users/:id/reset-password` | **只有超管**（`RequireSuper`，D-035） | 生成新密码（只返回这一次），吊销全部会话。目标是超管（包括自己）时回 403，字段 `system.user.resetSuper`：超管的密码只能用 `server admin reset-password` 重置 |
| PUT | `/users/:id/roles` | `system:user:assign-role` **敏感** | `{roleIds}` 整体替换；只有超管能分配 super 角色 |
| GET | `/options/users` | 登录即可 | `[{id, displayName}]`，最多 500；只含"查看用户"数据范围内的人，没有 `system:user:list` 的人只看得到自己（D-039） |
| GET | `/options/depts` | `system:user:list` | 用户表单选部门（D-033）：全部部门 `[{id, parentId, name, status}]`，停用的前端置灰 |
| GET | `/options/posts` | `system:user:list` | 用户表单选岗位（D-033）：全部岗位 `[{id, code, name, status}]` |
| GET | `/depts` | `system:dept:list` | 全部部门的扁平列表（前端拼成树），按排序值：`[{id, parentId, name, leaderUserId, leaderName, leaderHidden, phone, email, status, sort, remark, userCount, createdAt, updatedAt}]`；`userCount` 是直属用户数。负责人不在调用者"查看用户"（`system:user:list`）的范围内时 `leaderUserId` 为 0、`leaderName` 为空、`leaderHidden` 为 true（D-055） |
| POST | `/depts` | `system:dept:create` | `{parentId?, name, leaderUserId?, phone?, email?, status?, sort?, remark?}`；名称 1–64 字、不含控制字符和不可见字符，同一上级下不能重名（`4001`，`system.dept.nameTaken`）；上级必须存在；最多 10 层；新选的负责人必须是启用的用户（原来的负责人停用了，编辑时可以保留） |
| PUT | `/depts/:id` | `system:dept:update` | 同上，整体替换，只是不给 `leaderUserId` 时保持原来的负责人、给 0 才清空（D-055）；明确给出原负责人时他也要在调用者"查看用户"的范围内，否则和范围外的 ID 一样回 `system.dept.leader`（D-058）；新建、修改的回显和列表一样遮蔽负责人，超管身份按写完时的库认定（D-056）；可以换上级，但不能挪到自己或自己的下级下面（`system.dept.cycle`），挪完不能超过 10 层（`system.dept.depth`） |
| DELETE | `/depts/:id` | `system:dept:delete` | 有下级部门（`system.dept.hasChildren`）或还有用户（`system.dept.hasUsers`）时拒绝，`4001` |
| GET | `/posts` | `system:post:list` | 分页；`keyword` 匹配编码和名称；`status` 过滤；每行带 `userCount` |
| POST | `/posts` | `system:post:create` | `{code, name, status?, sort?, remark?}`；`code` 小写字母开头，只含小写字母、数字、连字符，最长 64，唯一（`system.post.codeTaken`） |
| PUT | `/posts/:id` | `system:post:update` | `{name, status?, sort?, remark?}`；编码不可改（传了也忽略） |
| DELETE | `/posts/:id` | `system:post:delete` | 还有用户在用时拒绝（`4001`，`system.post.inUse`） |
| GET | `/roles` | `system:role:list` | 只有本端的角色；下面按 `:id` 操作的接口对别的端的角色一律 404（规范 §6.7） |
| POST | `/roles` | `system:role:create` | `{code, name, status?, sort?, remark?}`；编码创建后不可改 |
| PUT | `/roles/:id` | `system:role:update` | `{name, status?, sort?, remark?}`；super 角色不能停用，非超管不能改 super 角色（403，字段 `rbac.role.superOnlyEdit`，D-035） |
| DELETE | `/roles/:id` | `system:role:delete` | 仍被用户引用时拒绝；super 角色不能删 |
| GET | `/roles/:id/perms` | `system:role:list` | 权限码数组，只含已注册的（代码里删掉的权限码不返回，D-035） |
| PUT | `/roles/:id/perms` | `system:role:grant` **敏感** | `{codes, dataScopes?}`：`codes` 整体替换；`dataScopes` 是 `{资源编码: self / dept / dept_tree / all}`，不传或不含某个资源时该资源的范围不变（D-039）。两者在一个事务里保存。非超管不能授出敏感权限码或自己没有的权限码，也不能让角色的范围宽于自己在同一权限码上的范围（字段 `rbac.dataScope.wider`）；不认识的资源或范围回 3001 |
| GET | `/roles/:id/data-scopes` | `system:role:list` | `{资源编码: 范围}`，没设置的是默认值；超管角色全部是 `all`（D-039） |
| GET | `/data-resources` | `system:role:list` | `[{code, name, perms, default, scopes}]`：本端声明的数据资源、受约束的权限码、默认范围和可选范围（D-039） |
| GET | `/perms/tree` | `system:role:list` | `[{group, perms:[{code, name, sensitive}]}]` |
| GET | `/sessions` | `system:session:list` | 分页；`userId` 过滤；只含数据范围内的用户的会话（D-039）。每行带 `superAccount`：这是超管账号的会话（D-035） |
| POST | `/sessions/:sid/revoke` | `system:session:revoke` | 只能吊销本端的会话，别的端的 `sid`、数据范围外的用户的会话一律 404；重复吊销返回成功。非超管吊销超管的会话回 403，字段 `system.session.superProtected`（D-035） |
| GET | `/operation-logs` | `system:oplog:list` | 分页，时间倒序；过滤 `userId`、`username`（前缀）、`action`、`path`（前缀）、`method`、`ip`、`sessionId`、`failed`（1 只看失败）、`from` / `to`（RFC 3339）；每行带 `sessionId`（D-032） |
| GET | `/login-logs` | `system:loginlog:list` | 分页，时间倒序；过滤 `userId`、`username`（前缀）、`ip`、`sessionId`、`success`（0/1）、`from` / `to`；成功的行带 `sessionId`（D-032） |
| GET | `/error-logs` | `system:errorlog:list`（敏感） | 错误日志（D-032）：服务端故障（5xx、panic）按指纹合并，一类一行。分页，按最近一次出现倒序；只含本端的和不属于任何端的错误（没登录的请求按路由模板归端，D-050）；过滤 `kind`（`panic` / `error`）、`route`（前缀）、`from` / `to`（按最近一次出现）。每行 `{id, fingerprint, kind, portal, method, route, code, httpStatus, message, count, firstAt, lastAt, lastRequestId, lastUserId, lastUsername, lastSessionId, lastIp}`；`message` 已去掉引号里的值、凭据和连接串里的账号密码，最长 1 KB；**不含调用栈** |
| GET | `/error-logs/:id` | `system:errorlog:detail`（敏感） | 同上一行，另带 `stack`（只有 panic 有；只留文件名和行号的最后三级路径，最长 8 KB）；不存在或属于别的端回 404 |
| GET | `/security-events` | `system:secevent:list`（敏感） | 安全事件（D-032）：本端的攻击迹象，同一来源（类型、用户、会话、IP、方法、路径、说明）在同一分钟内合并成一行。分页，按第一次出现倒序；过滤 `kind`、`level`（只看不低于这个级别的：1 提示、2 警告、3 严重）、`userId`、`username`（前缀）、`ip`、`sessionId`、`from` / `to`（按第一次出现），包括不属于任何端的事件。每行 `{id, portal, kind, level, userId, username, sessionId, ip, userAgent, method, path, detail, requestId, count, firstAt, lastAt}`；`kind` 取值：`forbidden`（越权被拒，`detail` 是权限码；业务层拒绝的为空）、`cross_portal`、`token_invalid`（签名或格式不对，过期不算）、`token_mismatch`（严重：签名有效但会话不存在或和用户对不上）、`session_revoked`（提示：已吊销会话的令牌仍在使用）、`refresh_reuse`（严重：上一个刷新凭证在宽限期外重放，会话已吊销）、`refresh_mismatch`（警告：刷新凭证和会话的当前值、上一个值都对不上，会话未吊销，D-049）、`bad_origin`（`detail` 为 `refresh` / `logout`）、`login_locked`、`login_rate_limited`、`unlock_exhausted`、`pwd_change_throttled`（改密核对旧密码次数用完，D-055）、`refresh_cookie_dup`（刷新时带了多个同名的刷新 Cookie，D-058）、`cli`（命令行建管理员、清理策略），以及模块通过 `Deps.Audit.RecordSecurity` 自己记录的类型 |
| GET | `/audit/timeline` | `system:audit:timeline`（敏感） | 调查时间线（D-032）：`userId`、`ip`、`sessionId` **恰好给一个**（否则 `3001`，字段 `subject`），把本端的登录日志、操作日志、安全事件合成一条按时间倒序的线；`limit` 1–200（默认 100），`cursor` 是上一页返回的 `next`（格式不对回 `3001`，字段 `cursor`）。按（时间，类型，ID）倒序，同一毫秒的记录翻页时不漏不重。返回 `{items: [{type, id, at, userId, username, sessionId, ip, userAgent, requestId, …}], more, next}`；`type` 为 `login`（另带 `success`、`reason`）、`operation`（另带 `action`、`method`、`path`、`httpStatus`、`code`、`error`，不带请求体和查询串）或 `security`（另带 `kind`、`level`、`detail`、`method`、`path`、`count`、`lastAt`，`at` 是第一次出现）；`more` 为 true 时把 `next` 作为下一页的 `cursor`。不属于任何端的安全事件（如命令行清理策略）也包括在内。错误日志不进时间线 |
| GET | `/security-policy` | `system:security:view`（敏感） | 安全设置（D-034）：操作者所在端生效的登录防护和密码策略，只读；修改只能在配置文件里改、重启生效（D-024），没有写接口。返回 `{portal, items: [{group, key, configKey, kind, value, default, min?, max?, source}]}`：`group` 为 `captcha` / `password` / `rate` / `lock` / `expiry`；`kind` 为 `bool`（`value`、`default` 是布尔值，没有范围，只能打开）、`times`（次数）、`perMinute`（每分钟次数）、`seconds`（时长，单位秒）、`days`（天，0 表示不过期）、`chars`（字符数）；`min` / `max` 是底线允许的范围；`source` 为 `config`（配置文件写了）、`code`（代码注册端时声明）、`default`（框架默认值）；`configKey` 形如 `portals.platform.login.lockAfterFailures`。不含 JWT 密钥等其他配置 |
| GET | `/dicts` | `system:dict:list` | 分页；`keyword` 匹配编码和名称；过滤 `portal`、`source`（`code` 代码声明 / `admin` 后台新建）；`sortBy` 取 sort / code / updatedAt |
| GET | `/dicts/:id` | `system:dict:list` | 字典与全部项（树形），项带 `locked`（代码声明）、`overridden`（显示被后台改过）、`labelI18n` |
| POST | `/dicts` | `system:dict:create` | `{code, name, nameI18n?, portal?, valueType?, status?, sort?, remark?}`；`code` 小写字母开头、只含小写字母数字下划线，**不能含点**（带点的编码留给代码声明）；`portal` 为端代号或 `*`（默认 `*`，所有端共用）；`valueType` 为 `string`（默认）或 `int`，创建后编码和值类型不可改 |
| PUT | `/dicts/:id` | `system:dict:update` | `{name, nameI18n?, portal?, status?, sort?, remark?}`；代码声明的字典返回 `4101` |
| DELETE | `/dicts/:id` | `system:dict:delete` | 连同全部项删除；代码声明的字典返回 `4101` |
| POST | `/dicts/:id/items` | `system:dict:update` | `{value, label, labelI18n?, color?, extra?, parentId?, status?, sort?, remark?}`；只能往后台字典里加项（代码字典返回 `4101`）；一本字典最多 1000 项（`dict.tooManyItems`）、5 层（`dict.tooDeep`），`3001`（D-055）；`value` 在字典内唯一（不区分大小写），`int` 字典须是规范的十进制整数；`color` 为 primary / success / warning / danger / info 或 `#RRGGBB`；`labelI18n` 形如 `{"en-US": "..."}`；父项创建后不可改 |
| PUT | `/dicts/:id/items/:itemId` | `system:dict:update` | 同上（不含 `parentId`）；代码声明的项不能改 `value`（`4101`），改显示字段后标记为 `overridden`，之后代码里的文字变了也不覆盖；代码字典里已不在声明中的遗留项也不能改 `value`，不能改成启用（`4101`、`dict.legacyItem`，D-058） |
| DELETE | `/dicts/:id/items/:itemId` | `system:dict:update` | 代码声明的项返回 `4101`；有子项时拒绝 |
| POST | `/dicts/:id/items/:itemId/reset` | `system:dict:update` | 代码声明的项恢复成代码里的显示文字、颜色、扩展值和排序 |
| GET | `/options/portals` | 登录即可 | 已注册的端代号数组，供"所属端"下拉 |
| GET | `/dashboard` | `system:dashboard:view` | 数据中心（D-027、D-030）：`days`（1–90，默认 30）、`tz`（浏览器时区偏移，分钟，东区为正，±840 以内）；返回 `{days, users: {total, enabled, new}, newUsers, sessions, logins: {success, failed}, operations, reasons: [{reason, count}], topActions: [{action, count}], topUsers: [{userId, username, count}], hours: {logins, operations}}`，`days` 是连续的本地日期、最后一天是今天，`newUsers`、`logins`、`operations` 与之一一对应（没有记录的日子为 0）；`topUsers` 是期间操作最多的人，只列操作人在用户资源上"查看用户"范围内的人（D-044）；`hours` 是按本地钟点（下标 0–23）的成功登录和操作次数 |
| GET | `/monitor/security` | `system:monitor:view`（敏感） | 监控中心·安全（D-030）：最近 24 小时，按端在进程内缓存 `monitor.securityCache`（默认 10 秒，D-031）。`{hours（24 个 UTC 整点）, success, failed（与 hours 对齐）, sessions, lockedSessions, operations, failedIps: [{ip, count}], recent: [{username, success, reason, ip, createdAt}]}`；`recent` 是最近 15 次登录，不含 User-Agent 和请求 ID |
| GET | `/monitor/server` | `system:monitor:view`（敏感） | 监控中心·服务器（D-030）：进程自身的只读指标，在进程内缓存 `monitor.serverCache`（默认 2 秒，D-031）。配置 `monitor.server: false` 时只回 `{enabled: false}`。开启时返回 `{enabled: true, now, startedAt, uptime, runtime: {goVersion, os, arch, version, numCpu, gomaxprocs, goroutines}, cpu: {supported, percent}, memory: {sys, heapAlloc, heapInuse, stackInuse, heapObjects, numGc, pauseTotalMs, lastGc}, db: {ok, latencyMs, version, maxOpen, open, inUse, idle, waitCount, waitMs}, requests: {minutes: [{at, count, clientErrors, serverErrors, avgMs, p95Ms, maxMs, cpu, heapInuse, goroutines}], routes: [{method, route, count, serverErrors, avgMs, p95Ms}]}}`；`minutes` 固定 60 项、最后一项是当前分钟，`routes` 是这 60 分钟里请求最多的 10 个路由模板；健康检查不计入；`goVersion`、`db.version` 只到大版本（如 `go1.26`、`8.0`、`10.11 MariaDB`） |
| GET | `/workspace` | `system:workspace:view` | 工作台（D-030）：**只返回调用者本人的数据**，不接受指定用户的参数。`tz` 同上（只用来算"今天"）。`{roles, sessions: [{current, ip, userAgent, createdAt, lastSeenAt}], opsToday, ops30d, recent: [{action, method, path, code, createdAt}], logins: [{success, reason, ip, createdAt}], lastLogin, topActions}`；`lastLogin` 是本次会话之前的最近一次成功登录，没有时为 `null`；会话不返回会话 ID |
| GET | `/profile` | 登录即可 | 个人中心（D-038）：**只返回调用者本人**的资料，不接受指定用户的参数。`{id, username, displayName, email, phone, avatar, bio, super, roles: [{id, code, name, isSuper}], deptName, posts: [{id, code, name}], pwdChangedAt, lastLoginAt, lastLoginIp, createdAt, sessions}`；`sessions` 是本人当前有效的会话数（含本次）；不含管理员的备注、排序和状态 |
| PUT | `/profile` | 登录即可 | 本人改资料：`{displayName, email?, phone?, bio?}`，字段白名单严格解析，**带别的字段（用户名、状态、角色、部门、岗位、头像、排序、备注、密码……）整体回 `3002`**；显示名 1–64 字、不含控制字符和不可见字符（`system.org.nameChars` / `system.org.nameLength`）；简介最多 255 字、允许换行（`common.maxLength`）；邮箱要合法。改完 `/auth/me` 立即是新显示名。记操作日志 `profile.update` |
| POST | `/profile/revoke-other-sessions` | 登录即可 | 本人除当前会话外的全部会话下线（原因 `logout`），当前会话保留；不读请求体。返回 `{revoked}`。记操作日志 `profile.revoke-others` |
| POST | `/avatar` | 登录即可 | 本人上传头像（D-040）：请求体就是图片本身，`Content-Type` 必须是 `image/jpeg` 或 `image/png`（否则 3001、字段 `system.avatar.type`，不记操作日志）；按内容判断格式，只收 JPEG 和 PNG；超过 1 MB 回 413，宽高超过 2048 或渐进式 JPEG 扫描段超过 40 个不解码直接拒绝；同时最多处理 2 个上传，其余排队。服务器居中裁成正方形、透明处铺白底，重新编码成 256×256 和 64×64 的 JPEG 存进数据库（原文件的 EXIF、注释和夹带内容都不保留）。返回 `{avatar: "upload:<键>"}`，旧键失效。错误键 `system.avatar.empty` / `size` / `type` / `dimensions`。记操作日志 `profile.avatar`（请求体记成 `<image/png, N bytes>`） |
| PUT | `/avatar` | 登录即可 | 本人选内置头像：`{preset}`（严格解析），名字不在内置表里回 3001、字段 `system.avatar.preset`；返回 `{avatar: "preset:<名字>"}`；原来上传的图删掉。记 `profile.avatar` |
| DELETE | `/avatar` | 登录即可 | 本人恢复默认（显示账号首字母），返回 `{avatar: ""}`。记 `profile.avatar` |
| GET | `/avatars/:key` | 登录即可 | 按随机键读上传的头像：`{image: "data:image/jpeg;base64,..."}`，`size=64` 取小图；键不对或已失效 404；`Cache-Control: no-store`。键只出现在看得到这个人的地方（本人的 `/auth/me` 和个人中心、数据范围内的用户列表） |
| GET | `/menus` | `system:menu:list` | 本端全部菜单节点的扁平列表（不按权限裁剪）：`[{name, kind, parent, sort, path, component, perm, titleKey, titles, icon, hidden, codeHidden, customized, need, default?}]`；`kind` 为 `dir`（代码目录）/ `page`（代码页面）/ `group`（后台分组）；代码菜单带 `default: {parent, sort, icon}`（代码原值）和 `customized`（有后台调整）；`need` 是看到它要具备的全部权限码（自己的加代码祖先的） |
| PUT | `/menus/:name` | `system:menu:update`（敏感） | 整体替换这个菜单的显示调整（没给的字段按空值处理），只收 `{titles?, icon?, hidden?, sort?}`，**带其他字段（如 `path`、`component`、`perm`）回 `3002`**（D-025）。`titles` 形如 `{"zh-CN": "...", "en-US": "..."}`，每个最多 32 字、不能含控制字符和不可见的格式字符，代码菜单留空的语言沿用翻译键，分组必须有 `zh-CN`；`icon` 为图标组件名（`^[A-Z][A-Za-z0-9]{0,63}$`），代码菜单留空沿用代码；代码里隐藏的菜单不能取消隐藏（`4101`）；`sort` 是同级排序值（0–1000000，越小越靠前），不传表示不改，只改同级顺序、上级不变，改回代码里的值时不算调整 |
| POST | `/menus/:name/reset` | `system:menu:update`（敏感） | 代码菜单恢复成代码声明的样子（显示名、图标、隐藏、位置）；恢复后会成环或超过层数时拒绝（`3001`） |
| PUT | `/menu-layout` | `system:menu:update`（敏感） | 拖动后保存位置：`{items: [{name, parent, sort}]}`，必须包含本端**全部**节点（与当前集合不一致回 `4001`，请刷新）；上级必须是本端的目录或分组（菜单在代码里的上级总是可以），带权限码的目录（及它的代码子孙目录）只能放它在代码里本来的子项，分组也不能放进这样的目录；不能成环，最多 4 层（代码声明得更深时以代码为准）；整体校验，任何一项不合格都不写 |
| POST | `/menu-groups` | `system:menu:update`（敏感） | 新建分组 `{titles, icon?, hidden?, parent?, sort?}`，返回服务端生成的 `{name}`（`@g-` 加 12 位十六进制）；分组没有路径、页面和权限码，内有可见子项时才显示；每端最多 200 个 |
| DELETE | `/menu-groups/:name` | `system:menu:update`（敏感） | 删除分组：里面的代码菜单回到代码声明的位置，里面的分组挪到被删分组的上级（结果同样做成环和层数校验）；代码菜单返回 `4101` |

以上六个审计查看接口（操作日志、登录日志、错误日志列表与详情、安全事件、调查时间线）每次调用都会记一条操作日志，动作名分别是 `audit.view.oplog`、`audit.view.loginlog`、`audit.view.errorlog`、`audit.view.errorlog-detail`、`audit.view.secevent`、`audit.view.timeline`，带脱敏后的查询条件（D-032）。所有日志都没有修改和删除接口。

操作日志只对挂了 `oplog.Record` 的路由记录（规范 §10），本模块记录的动作名：`user.create`、`user.update`、`user.status`、`user.reset-password`、`user.assign-role`、`role.create`、`role.update`、`role.delete`、`role.grant`、`session.revoke`、`dept.create`、`dept.update`、`dept.delete`、`post.create`、`post.update`、`post.delete`、`dict.create`、`dict.update`、`dict.delete`、`dict.item-create`、`dict.item-update`、`dict.item-delete`、`dict.item-reset`、`menu.update`、`menu.reset`、`menu.layout`、`menu.group-create`、`menu.group-delete`、`profile.update`、`profile.revoke-others`、`profile.avatar`、`user.avatar-clear`，以及查看审计数据的 `audit.view.*`（见上）；框架为每个端的 `PUT /auth/password`、`POST /auth/lock`、`POST /auth/unlock` 记录 `auth.password`、`auth.lock`、`auth.unlock`。请求体最多保留 4 KB，键名命中 `password|passwd|secret|token|key|credential|sign|authorization`（不区分大小写）的值写成 `***`，查询串同样处理；不记响应体。所有日志都只增不改，没有更新和删除接口；每写一条同时以 `AUDIT` 级别写一行到日志输出（D-032）。

## Raw 路由（不属于任何端）

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/healthz` | 存活探针 |
| GET | `/readyz` | 就绪探针（检查数据库） |

## 错误码

见 `server/core/httpx/codes.go`。业务性的框架错误码：4001 资源冲突、4002 不能停用或降级最后一个超管、4101 由代码声明的内容不能在后台修改或删除。协议层：401 未认证（1004）、403 无权限（2001）/ 需改密（2002）、404（4004）、405（4005）、409 刷新冲突（1005）、423 会话已锁屏（1006）、413（4013）、429（4029）、500（5000）、503（5003：依赖不可用，包括请求处理超过 `server.handlerTimeout`，D-037）。业务性失败返回 200 加非零 `code`。
