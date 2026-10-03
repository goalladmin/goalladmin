# 接口清单

路径前缀 `/api/<portal>/v1`：平台程序是 `platform` 端，代理商、商户程序是 `agent`、`merchant` 端（D-061）。所有响应都是信封 `{code, data, msg, key?, params?, requestId}`：`code = 0` 成功；业务性失败（登录失败、参数校验、资源冲突）返回 HTTP 200 加非零 `code`；协议层问题用真实状态码（见文末）。`msg` 按 `Accept-Language` 选择语言（支持 11 种，规则见规范 §9.6），没有这个头时用简体中文，不支持的语言用英文；前端每个请求都带当前界面语言。字段错误 `data.fields` 为 `[{field, message, key?, params?}]`，业务自定的失败说明在信封的 `key`、`params` 里：前端有 `err.<key>` 的翻译时代入 `params` 显示，没有时显示英文的 `message` / `msg`。请求头 `X-Request-Id` 符合 `^[A-Za-z0-9-]{8,64}$` 时沿用，否则由服务端生成，响应头和响应体里都带。

带 JSON 请求体的接口要求 `Content-Type: application/json`（或 `+json` 结尾的类型）；别的类型不解析请求体，回 400 / `3002`（D-095）。

需要登录的接口带 `Authorization: Bearer <accessToken>`。访问令牌 15 分钟有效，过期后用 `/auth/refresh`（靠 HttpOnly Cookie：release 模式 `__Host-ga_rt_<portal>`，debug 模式 `ga_rt_<portal>`，D-058）换新的；`@ga/shell` 的请求客户端已经把这一套做完了。

内置本人操作和管理写事务在锁住会话行后复核有效性与锁屏状态（D-106）；锁屏先完成时，等待中的写入返回 423 / `1006`，不会继续提交。先取得会话锁的写事务结束后，锁屏才会成功返回。业务模块写操作需使用同样的授权事务入口，读取接口仍遵守 `LiveAuth` 与状态缓存的原有边界。

日志类接口的时间参数 `from`、`to` 用 RFC 3339；格式不对、或换算成 UTC 后年份超出 1–9999 的，当作没传（D-099）。

分页接口的参数：`page`（从 1 开始，上限 5000，D-055）、`pageSize`（默认 20，上限 200）、`keyword`、`sortBy`、`sortOrder`（`asc` / `desc`；`sortBy` 只接受各接口列出的字段）；响应 `data` 为 `{list, total, page, pageSize}`。

## 公开接口（不需要登录）

每个端都有以下三条认证公开接口；代理商、商户端另有下述入驻公开接口，其余接口一律需要登录。新增 Public 路由必须同步登记到这里（有测试校验）。

配置 Redis 后，同一前缀、同一端的实例共用登录防护、验证码生成次数、本人改密次数和主体后台密码操作次数（D-076），并共享一次性验证码答案（D-077）。Redis 不可用时按本实例影子计数继续服务，恢复保留共享状态；故障期间新生成的验证码只在生成实例可用，已有共享验证码暂时无法校验时需重新获取。HTTP 路径、参数和响应字段不变，`captchaId` 按不透明字符串使用。部署示例和独立进程验证见 D-078、D-079 及规范 §11.3。

| 端 | 方法 | 路径 | 说明 |
|---|---|---|---|
| platform,agent,merchant | GET | `/auth/captcha` | 图形验证码：`{captchaId, image}`；要求 `X-GA-Client: web`，来源规则见 D-112。5 分钟有效，校验时不论对错均作废一次，跨端不可使用（D-077）；同一来源 IP 每分钟最多 30 张、同时最多画 8 张，超出回 429 `4029`（D-055） |
| platform,agent,merchant | POST | `/auth/login` | 要求 `X-GA-Client: web`、Origin 白名单、`Content-Type: application/json`，缺一回 403 `2001`，不建会话、不写 Cookie（D-051）；入参 `username`（先去首尾空白、转小写再处理，大小写变体是同一个账号、共用一份限流配额）、`password`、`captchaId?`、`captchaCode?`，主体端（代理商、商户）另有必填的 `org`（主体编号，先去首尾空白、转大写；缺了回 `3001`、字段 `org`；编号不存在、主体停用和账号不存在一样回 `1001`，D-061）；出参 `accessToken`、`tokenType`、`expiresIn`、`mustChangePwd`、`pwdExpired`（必须改密的原因是密码过期）、`sessionId`（这个令牌所属的会话，D-048）；同时下发 HttpOnly 刷新 Cookie；端配置了 `captchaAlways` 时每次都要带验证码；同一个账号一分钟内的登录请求（所有来源合计）超过上限后也要带验证码（回 `1002`、`captchaRequired: true`，不是 429；没通过验证码的请求不占这个次数，D-103）；来源 IP 的次数超限回 429 `4029`，登录防护的计数表满时没有记录的新来源也回 429（D-056）；服务端同时做密码计算的请求太多时先等位置（最多 2 秒），等不到回 429、不算一次失败（D-058、D-071）；被限流、被锁定的请求不写登录日志，只记安全事件（D-058）；来源在 IP 黑名单或端白名单之外回 403 `2003`，命中主体黑名单或在账号、主体的 IP 白名单之外按账号或密码错误回 `1001`、登录日志原因 `ip_denied`（D-062） |
| platform,agent,merchant | POST | `/auth/refresh` | 靠 Cookie；要求 `X-GA-Client: web` 与 Origin 白名单；出参同登录；并发冲突返回 409 `1005`（客户端隔一小段时间再试）；可带 `X-GA-Session: <sessionId>` 声明要续的会话，Cookie 不是这个会话时回 401 `1004`（翻译键 `auth.sessionSwitched`），不轮换、不改 Cookie（D-048、D-050）。查账号时数据库临时出错回 503 `5003`，这时还没轮换，同一个凭证稍后照样能用（D-050）。凭证和会话的当前值、上一个值都对不上、家族密钥也对不上时回 401 `1004`，会话不吊销、Cookie 不改，记安全事件 `refresh_mismatch`（D-049），这种请求不查账号、不判 IP 白名单（D-097）；上一个值在宽限期外重放，或者家族密钥对得上而凭证已经不是当前值和上一个值（被轮换掉不止一次，D-104），吊销整个会话（`refresh_reuse`）。Cookie 的值是 `<sid>.<family>.<secret>`，对客户端不透明。刷新失败的任何情况都不改 Cookie。失败时服务端确定会话已经结束（不存在、已吊销、已过期，或这次因重放、账号停用被吊销）的，401 带翻译键 `auth.sessionEnded`，前端据此才把没确认的退出算作确认（D-052）；带了多个同名的刷新 Cookie 时回 401 并记安全事件 `refresh_cookie_dup`（D-058） |

代理商、商户端另有（D-080）：

| 端 | 方法 | 路径 | 说明 |
|---|---|---|---|
| agent,merchant | GET | `/onboarding/config` | 返回 `{enabled, invitationsEnabled}`，默认 false。后者仅在代理商端且商户入驻开启时为 true。平台端没有此路由 |
| agent,merchant | POST | `/onboarding/applications` | 关闭时 404；要求 X-GA-Client、JSON、Origin 白名单、本端一次性验证码。入参严格限定 `name`、`contactName`、`contactPhone`、`ownerUsername`、`invitationToken?`、`captchaId`、`captchaCode`；不接受主体 ID。每来源网段每小时最多 5 次尝试。成功只回 `{reference}`，不创建登录账号。商户无邀请时直属平台，有邀请时归属邀请代理商；无效/过期/已用邀请拒绝。没有匿名查询和领取密码接口 |

## 认证接口（登录即可）

会话下线的完成边界（D-089）：三个端的退出、下线其他设备和管理下线操作成功返回后，被下线会话先前获准的管理写事务均已提交或回滚。下线会等待这些事务结束，等待失败不算成功；已提交的业务变更不会撤回。

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/auth/logout` | 吊销当前会话；要求 `X-GA-Client: web` 与 Origin 白名单；不动刷新 Cookie（服务端只在登录和刷新成功时写它、从不删除，D-049：退出的响应迟到时浏览器里的 Cookie 可能已经是别人新登录的） |
| GET | `/auth/me` | `{user, locked, perms, menus, pwdPolicy}`；锁屏时 `locked` 为真，`perms`、`menus` 为空；`menus` 是按权限裁剪后的菜单树，已叠加菜单管理里的调整，节点带 `titles`（后台改过的显示名，按语言；前端优先用它，没有当前语言时翻译 `titleKey`），分组节点的 `path` 为空；`user.mustChangePwd` 为真时除 `/auth/*` 外一律 403 `2002`（首次登录、被重置或密码过期，过期时 `user.pwdExpired` 为真）；`pwdPolicy` 为 `{minLength, requireUpper, requireLower, requireSymbol, maxAgeDays}`；`user.avatar` 是空、`preset:<名字>` 或 `upload:<键>`（D-040）；主体端另有 `org: {code, name}`（当前账号所属的主体），`user.super` 表示本主体的主账号（D-061） |
| PUT | `/auth/password` | 入参 `oldPassword`、`newPassword`；同一会话 15 分钟内最多核对 5 次旧密码（成功清零），之后回 429 `4029`、记安全事件 `pwd_change_throttled`（D-055）；同一个账号 15 分钟内最多成功改密 5 次（没改成的不占；被要求改密的那一次不挡也不计），之后同样回 429、记同一种安全事件（D-071）；改密最多占一半的密码计算位置，满了直接回 429、不等、不占次数，主体端一个主体同时最多 2 个改密在途（D-071）；成功后其他会话失效。本次会话已被吊销（例如管理员刚重置了密码）回 401（D-045、D-046）；验证旧密码之后密码已被换掉（例如同一会话的另一个改密请求先完成）按旧密码不对回 3001（D-047） |
| POST | `/auth/lock` | 锁屏（D-027）：锁定当前会话。之后除 `/auth/me`、`/auth/unlock`、`/auth/logout`（以及不需要登录的 `/auth/refresh`）外的接口一律 HTTP 423、`1006`；刷新换来的新令牌照样锁着；操作日志 `auth.lock` |
| POST | `/auth/unlock` | 入参 `password`（当前账号的密码）；输错回 `3001`，字段错误键 `password.unlockIncorrect`、参数 `left`（剩余次数）；连续输错 5 次吊销会话（`unlock_failed`）并回 401；输错同样计入登录防护，登录防护的计数表满、放不下这次核对时回 429 `4029`，不占解锁次数（D-057）；同时做密码计算的请求太多时和登录一样先等位置（最多 2 秒），等不到回 429、不占次数（D-058、D-071）；主体端一个主体同时最多 2 个解锁在途，超出直接回 429、不占次数（D-071）；操作日志 `auth.unlock`（密码打码） |
| GET | `/dicts` | 读取字典：`codes=a,b`（1–50 个，逗号分隔）；返回 `{编码: {code, name, valueType, items}}`，只含属于当前端或所有端共用、且启用中的字典，看不到的编码直接略过。`items` 为树形 `[{value, label, color?, extra?, status, children?}]`：`value` 按字典的 `valueType` 输出为字符串或数字；`label`、`name` 按 `Accept-Language` 选语言；停用的项也返回（`status: 0`），用于显示历史数据，下拉只该列启用的项 |

## 系统管理（platform 端，前缀 `/api/platform/v1/system`）

守卫为 `Require`，括号内是所需权限码；标 **敏感** 的权限码只有超管能授予。

按部门的数据权限（D-039）：用户的列表、详情、下拉，以及修改、启停、分配角色、会话的查看和下线，都只对操作人在对应权限码上的数据范围内的人生效；范围外的人一律当作不存在（404）。新建用户和改部门时，新部门必须在范围内，否则 403、字段 `system.user.deptOutOfScope`；非超管改自己的部门 403、字段 `system.user.ownDept`；非超管改部门上级要用户资源的每个权限码都是全部范围，否则 403、字段 `system.dept.moveNeedsAll`；部门负责人只能选"查看用户"范围内的人（范围外的和不存在的一样回 `system.dept.leader`）。新建、修改、分配角色成功后，目标不在"查看用户"范围内时不回显资料（修改回 `data: null`，新建只回 `id` 和 `username`）。非超管建用户时带 `roleIds` 要有 `system:user:assign-role`。非超管把停用的角色重新启用，按分配角色的规则检查（`PUT /roles/:id`）。

| 方法 | 路径 | 权限码 | 说明 |
|---|---|---|---|
| GET | `/users` | `system:user:list` | 分页；`keyword` 匹配账号、显示名、邮箱；`status` 过滤；`deptId` 按部门过滤，默认包括下级部门（`withChildren=0` 只看这一个部门）；`sortBy` 取 createdAt / username / sort / lastLoginAt。每行带 `deptId`、`deptName`、`posts: [{id, code, name}]`（D-033）和 `bio`（个人简介，D-038） |
| GET | `/users/:id` | `system:user:list` | 范围外的人 404（D-039） |
| POST | `/users` | `system:user:create` **敏感** | 入参 `username`、`password?`、`displayName?`、`email?`、`phone?`、`roleIds?`、`deptId?`、`postIds?`、`sort?`、`remark?`；部门、岗位必须存在且启用（D-033）；不传密码则生成，响应里 `initialPassword` 只返回这一次；新账号下次登录必须改密。生成密码哈希和登录核对密码占同一个并发上限（D-068），服务端同时在做的密码计算太多时回 429 `4029`、什么都不写，稍后重试 |
| PUT | `/users/:id` | `system:user:update` | 资料（含 `bio` 个人简介）和部门、岗位，不含密码、状态、角色、头像（传 `avatar` 不改，D-040）。非超管改超管账号回 403，字段 `system.user.superProtected`（D-035）。`deptId`、`postIds` 不传表示不改，`postIds: []` 清空；新选的部门、岗位必须存在且启用，原来就有的停用了也能保留；岗位最多 20 个、整体替换（D-033） |
| POST | `/users/:id/status` | `system:user:status` | `{status: 0|1}`；停用会吊销全部会话；不能停用自己或最后一个超管。非超管对超管账号（改资料、启停）一律 403，字段 `system.user.superProtected`（D-035）；非超管重新启用停用的账号时，账号身上的角色（停用中的也算，D-100）要都是他能分配的（含敏感权限码、含自己没有的权限码或范围更宽的回 403，D-058；错误只给通用键 `rbac.user.enableNotAssignable`，不列具体角色和权限码，D-059） |
| DELETE | `/users/:id/avatar` | `system:user:update` | 管理员清除别人的头像（D-040，只能清除、不能替别人换）：受数据范围约束（范围外 404），非超管动超管账号 403、字段 `system.user.superProtected`。记操作日志 `user.avatar-clear` |
| GET / PUT | `/users/:id/ip-allow` | `system:user:ip`（敏感） | 平台账号的 IP 白名单（D-062）：GET 要目标在"查看用户"范围内，PUT 要在"修改用户"范围内（范围外 404），非超管改超管账号 403、字段 `system.user.superProtected`。PUT 入参 `{items: [{cidr, remark?}]}` 整份替换，最多 100 条，空表示不限制；`cidr` 是单个地址或网段（规范化后返回）；改自己的账号时名单必须包含当前地址，否则字段 `ipacl.selfLockout`。返回 `{items: [{id, cidr, remark, createdAt, …}], yourIp}`。记操作日志 `user.ip` |
| POST | `/users/:id/reset-password` | **只有超管**（`RequireSuper`，D-035） | 生成新密码（只返回这一次），吊销全部会话。目标是超管（包括自己）时回 403，字段 `system.user.resetSuper`：超管的密码只能用 `server admin reset-password` 重置。生成密码哈希和登录核对密码占同一个并发上限（D-068），服务端同时在做的密码计算太多时回 429 `4029`、什么都不写，稍后重试 |
| PUT | `/users/:id/roles` | `system:user:assign-role` **敏感** | `{roleIds}` 整体替换；只有超管能分配 super 角色；非超管新加的角色不能带敏感权限码、自己没有的权限码、比自己宽的数据范围，否则 403、字段 `roleIds`：有"查看角色"权限（`system:role:list`）的人看到具体原因（`rbac.role.holdsSensitive`、`rbac.role.holdsNotOwned`、`rbac.role.widerScope`，带权限码或资源和范围），没有的只看到 `rbac.role.notAssignable` 和角色 ID，原因记在服务端日志里（D-069） |
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
| POST | `/roles` | `system:role:create` | `{code, name, status?, sort?, remark?}`；编码创建后不可改；名称不含控制字符和不可见的格式字符（零宽连接符、零宽非连接符除外；`3001`，`org.textChars`，D-099，主体端的角色相同） |
| PUT | `/roles/:id` | `system:role:update` | `{name, status?, sort?, remark?}`；省略 status 保留原值（D-110）；super 角色不能停用，非超管不能改 super 角色（403，字段 `rbac.role.superOnlyEdit`，D-035）；非超管改角色的状态（启用或停用）要过和分配角色一样的检查，被拒时的回显也一样（D-039、D-069、D-100）；只改名字、排序、备注不受限 |
| DELETE | `/roles/:id` | `system:role:delete` | 仍被用户引用时拒绝；super 角色不能删 |
| GET | `/roles/:id/perms` | `system:role:list` | 权限码数组，只含已注册的（代码里删掉的权限码不返回，D-035） |
| PUT | `/roles/:id/perms` | `system:role:grant` **敏感** | `{codes, dataScopes?}`：`codes` 整体替换；`dataScopes` 是 `{资源编码: self / dept / dept_tree / all}`，不传或不含某个资源时该资源的范围不变（D-039）。两者在一个事务里保存。非超管不能授出敏感权限码或自己没有的权限码，也不能让角色的范围宽于自己在同一权限码上的范围（字段 `rbac.dataScope.wider`）；不认识的资源或范围回 3001 |
| GET | `/roles/:id/data-scopes` | `system:role:list` | `{资源编码: 范围}`，没设置的是默认值；超管角色全部是 `all`（D-039） |
| GET | `/data-resources` | `system:role:list` | `[{code, name, perms, default, scopes}]`：本端声明的数据资源、受约束的权限码、默认范围和可选范围（D-039） |
| GET | `/perms/tree` | `system:role:list` | `[{group, perms:[{code, name, sensitive}]}]` |
| GET | `/sessions` | `system:session:list` | 分页；`userId` 过滤；只含数据范围内的用户的会话（D-039）。每行带 `superAccount`：这是超管账号的会话（D-035） |
| POST | `/sessions/:sid/revoke` | `system:session:revoke` | 只能吊销本端的会话，别的端的 `sid`、数据范围外的用户的会话一律 404；`sid` 只认 32 位小写十六进制，别的写法同样 404（D-097，代理商、商户管理和主体端的下线接口相同）；重复吊销返回成功。非超管吊销超管的会话回 403，字段 `system.session.superProtected`（D-035） |
| GET | `/operation-logs` | `system:oplog:list` | 分页，时间倒序；过滤 `userId`、`username`（前缀）、`action`、`path`（前缀）、`method`、`ip`、`sessionId`、`failed`（1 只看失败）、`from` / `to`（RFC 3339）；每行带 `sessionId`（D-032） |
| GET | `/login-logs` | `system:loginlog:list` | 分页，时间倒序；过滤 `userId`、`username`（前缀）、`ip`、`sessionId`、`success`（0/1）、`from` / `to`；成功的行带 `sessionId`（D-032） |
| GET | `/error-logs` | `system:errorlog:list`（敏感） | 错误日志（D-032）：服务端故障（5xx、panic）按指纹合并，一类一行。分页，按最近一次出现倒序；`portal` 选端：不传或 `platform` 是平台端的和不属于任何端的错误（没登录的请求按路由模板归端，D-050），`agent`、`merchant` 只看代理商端、商户端的（D-066），别的值回 `3001`、字段 `portal`；过滤 `kind`（`panic` / `error`）、`route`（前缀）、`from` / `to`（按最近一次出现）。每行 `{id, fingerprint, kind, portal, method, route, code, httpStatus, message, count, firstAt, lastAt, lastRequestId, lastUserId, lastUsername, lastSessionId, lastIp}`；`message` 已去掉引号里的值、凭据和连接串里的账号密码，最长 1 KB；**不含调用栈** |
| GET | `/error-logs/:id` | `system:errorlog:detail`（敏感） | 同上一行，另带 `stack`（只有 panic 有；只留文件名和行号的最后三级路径，最长 8 KB）；不存在或属于运维中心看不到的端（平台、代理商、商户之外）回 404 |
| GET | `/security-events` | `system:secevent:list`（敏感） | 安全事件（D-032）：攻击迹象，`portal` 选端（同错误日志，D-066），同一合并身份（类型、用户、会话、IP〔IPv6 /64〕、方法类别、路由模板或固定未匹配值、说明）在同一分钟内合并成一行；匿名和已认证事件使用独立的新键预算（D-114）。分页，按第一次出现倒序；过滤 `kind`、`level`（只看不低于这个级别的：1 提示、2 警告、3 严重）、`userId`、`username`（前缀）、`ip`、`sessionId`、`from` / `to`（按第一次出现）；看平台端时包括不属于任何端的事件，看代理商端、商户端时不包括。每行 `{id, portal, kind, level, userId, username, sessionId, ip, userAgent, method, path, detail, requestId, count, firstAt, lastAt}`；`kind` 取值：`forbidden`（越权被拒，`detail` 是权限码；业务层拒绝的为空）、`cross_portal`、`token_invalid`（签名或格式不对，过期不算）、`token_mismatch`（严重：签名有效但会话不存在或和用户对不上）、`session_revoked`（提示：已吊销会话的令牌仍在使用）、`refresh_reuse`（严重：上一个凭证在宽限期外重放，或家族密钥正确且凭证落到两代之外；会话已吊销）、`refresh_mismatch`（警告：不能证明持有本会话，包括当前 secret 正确但家族密钥缺失/错误；会话未吊销，D-049、D-104）、`bad_origin`（`detail` 为 `login` / `refresh` / `logout`）、`login_locked`、`login_rate_limited`、`unlock_exhausted`、`pwd_change_throttled`（改密核对旧密码次数用完，D-055）、`refresh_cookie_dup`（刷新时带了多个同名的刷新 Cookie，D-058）、`cli`（命令行建管理员、清理策略），以及模块通过 `Deps.Audit.RecordSecurity` 自己记录的类型 |
| GET | `/audit/timeline` | `system:audit:timeline`（敏感） | 调查时间线（D-032）：`userId`、`ip`、`sessionId` **恰好给一个**（否则 `3001`，字段 `subject`），把 `portal` 选的那个端（同错误日志，D-066；用户 ID 只在一个端里有意义）的登录日志、操作日志、安全事件合成一条按时间倒序的线；`limit` 1–200（默认 100），`cursor` 是上一页返回的 `next`（格式不对回 `3001`，字段 `cursor`）。按（时间，类型，ID）倒序，同一毫秒的记录翻页时不漏不重。返回 `{items: [{type, id, at, userId, username, sessionId, ip, userAgent, requestId, …}], more, next}`；`type` 为 `login`（另带 `success`、`reason`）、`operation`（另带 `action`、`method`、`path`、`httpStatus`、`code`、`error`，不带请求体和查询串）或 `security`（另带 `kind`、`level`、`detail`、`method`、`path`、`count`、`lastAt`，`at` 是第一次出现）；`more` 为 true 时把 `next` 作为下一页的 `cursor`。不属于任何端的安全事件（如命令行清理策略）也包括在内。错误日志不进时间线 |
| GET | `/security-policy` | `system:security:view`（敏感） | 安全设置（D-034）：操作者所在端生效的登录防护和密码策略，只读；修改只能在配置文件里改、重启生效（D-024），没有写接口。返回 `{portal, items: [{group, key, configKey, kind, value, default, min?, max?, source}]}`：`group` 为 `captcha` / `password` / `rate` / `lock` / `expiry`；`kind` 为 `bool`（`value`、`default` 是布尔值，没有范围，只能打开）、`times`（次数）、`perMinute`（每分钟次数）、`seconds`（时长，单位秒）、`days`（天，0 表示不过期）、`chars`（字符数）；`min` / `max` 是底线允许的范围；`source` 为 `config`（配置文件写了）、`code`（代码注册端时声明）、`default`（框架默认值）；`configKey` 形如 `portals.platform.login.lockAfterFailures`。不含 JWT 密钥等其他配置 |
| GET | `/ip-rules/deny` | `system:ip:list` | IP 黑名单（D-062），对平台、代理商、商户三个程序都生效。分页，新加的在前；`keyword` 按网段前缀匹配，`includeExpired=1` 连已过期的一起。每行 `{id, cidr, expiresAt, remark, createdBy, createdAt, updatedAt}` |
| POST | `/ip-rules/deny` | `system:ip:deny`（敏感） | 加一条黑名单：`{cidr, expiresIn?, remark?}`（字段白名单严格解析），`expiresIn` 是分钟，0 或不传为永久，最多一年；同一网段再加一次是更新有效期和备注。网段不能宽于 IPv4 /8、IPv6 /16（`ipacl.denyTooBroad`），不能包含操作者当前的地址（`ipacl.selfLockout`），生效中的最多 10000 条（`ipacl.denyFull`；只数全局名单，主体自己的黑名单不占这个额度，D-102）。记操作日志 `ip.deny` |
| DELETE | `/ip-rules/deny/:id` | `system:ip:deny`（敏感） | 删一条黑名单；不是黑名单或不存在的 404。记操作日志 `ip.unblock` |
| GET / PUT | `/ip-rules/allow` | `system:ip:list` / `system:ip:allow`（敏感） | 平台端 IP 白名单（D-062）：平台的每个接口（含登录、验证码）只放行名单里的来源，空表示不限制。PUT 入参 `{items: [{cidr, remark?}]}` 整份替换，最多 100 条，名单不为空时必须包含操作者当前的地址（`ipacl.selfLockout`）。返回 `{items, yourIp}`。记操作日志 `ip.allow`。配错把自己挡在外面时，在服务器上用 `server ip clear-allow -portal platform` 清空 |
| GET | `/dicts` | `system:dict:list` | 分页；`keyword` 匹配编码和名称；过滤 `portal`、`source`（`code` 代码声明 / `admin` 后台新建）；`sortBy` 取 sort / code / updatedAt |
| GET | `/dicts/:id` | `system:dict:list` | 字典与全部项（树形），项带 `locked`（代码声明）、`overridden`（显示被后台改过）、`labelI18n` |
| POST | `/dicts` | `system:dict:create` | `{code, name, nameI18n?, portal?, valueType?, status?, sort?, remark?}`；`code` 小写字母开头、只含小写字母数字下划线，**不能含点**（带点的编码留给代码声明）；`portal` 为端代号或 `*`（默认 `*`，所有端共用）；`valueType` 为 `string`（默认）或 `int`，创建后编码和值类型不可改；`name`、`nameI18n` 的文字不含控制字符和不可见的格式字符（`3001`，`org.textChars`，D-099；项的 `label`、`labelI18n` 相同） |
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

操作日志只对挂了 `oplog.Record` 的路由记录（规范 §10），本模块记录的动作名：`user.create`、`user.update`、`user.status`、`user.reset-password`、`user.assign-role`、`role.create`、`role.update`、`role.delete`、`role.grant`、`session.revoke`、`dept.create`、`dept.update`、`dept.delete`、`post.create`、`post.update`、`post.delete`、`dict.create`、`dict.update`、`dict.delete`、`dict.item-create`、`dict.item-update`、`dict.item-delete`、`dict.item-reset`、`menu.update`、`menu.reset`、`menu.layout`、`menu.group-create`、`menu.group-delete`、`profile.update`、`profile.revoke-others`、`profile.avatar`、`user.avatar-clear`，以及查看审计数据的 `audit.view.*`（见上）；框架为每个端的 `PUT /auth/password`、`POST /auth/lock`、`POST /auth/unlock` 记录 `auth.password`、`auth.lock`、`auth.unlock`。请求体最多保留 4 KB，键名命中 `password|passwd|secret|token|key|credential|sign|authorization`（不区分大小写）的值写成 `***`，查询串同样处理；单个字符串值超过 512 字节只留开头并标 `…(N bytes)`，整段被截断时结尾标 `…(truncated, N bytes)`（D-095）；不记响应体。所有日志都只增不改，没有更新和删除接口；每写一条同时以 `AUDIT` 级别写一行到日志输出（D-032）。

## 代理商管理、商户管理（platform 端，前缀 `/api/platform/v1/agent`、`/api/platform/v1/merchant`）

平台开主体、管主账号（D-065）。两个模块的接口一一对应，下表以商户为例：代理商的把 `/merchant/merchants` 换成 `/agent/agents`、权限码 `partner:merchant:*` 换成 `partner:agent:*`、操作日志 `merchant.*` 换成 `agent.*`；标"仅商户"的代理商没有。写接口都在锁里重新认定操作人（D-047）。代理商、商户自己的子账号和角色在它们自己的程序里管（见下一节），平台这边只读。

| 方法 | 路径 | 权限码 | 说明 |
|---|---|---|---|
| GET | `/merchant/merchants` | `partner:merchant:list` | 分页；`keyword` 匹配编号、名称、联系人、联系电话；`status` 过滤；`agentId` 只看这个代理商名下的（`0` 只看直属平台的，仅商户）；`sortBy` 取 createdAt / code / name / sort。每行 `{id, code, name, contactName, contactPhone, agentId, agentCode, agentName, ownerUserId, ownerUsername, status, sort, remark, createdAt, updatedAt, createdBy, updatedBy}`（代理商没有 `agent*` 三项） |
| GET | `/merchant/merchants/:id` | `partner:merchant:list` | 不存在 404 |
| POST | `/merchant/merchants` | `partner:merchant:create` | 入参 `name`、`contactName?`、`contactPhone?`、`agentId?`（仅商户，0 或不传为直属平台；代理商必须存在且启用，否则字段 `org.agent`、`org.agentDisabled`）、`sort?`、`remark?`、`ownerUsername`（主账号的登录名，规则同平台用户名）、`ownerDisplayName?`（不填用登录名）。编号自动生成（代理商 `A`、商户 `M` 加 8 位随机数字），创建后不可改。返回 `{merchant, ownerId, initialPassword}`（代理商是 `agent`）：主账号的初始密码**只在这里出现一次**，首次登录必须改密。记操作日志 `merchant.create`（不记响应体）。算初始密码的哈希和登录核对密码占同一个并发上限（D-068），满了回 429 `4029`、什么都不写 |
| PUT | `/merchant/merchants/:id` | `partner:merchant:update` | 整份替换 `{name, contactName, contactPhone, sort, remark}`；编号、主账号、状态、归属各有单独的接口。记操作日志 `merchant.update` |
| POST | `/merchant/merchants/:id/status` | `partner:merchant:status` | `{status}`（1 启用、0 停用）。停用时在同一个事务里吊销这个商户的全部会话，它的账号此后都按停用处理；代理商停用不影响名下商户。记操作日志 `merchant.status` |
| POST | `/merchant/merchants/:id/reset-owner-password` | `partner:merchant:owner`（敏感） | 主账号的密码换成随机密码，返回 `{initialPassword}`（只这一次），下次登录必须改密；主账号的全部会话吊销。记操作日志 `merchant.reset-owner-password`。并发上限同开商户：满了回 429 `4029`、密码不换 |
| PUT | `/merchant/merchants/:id/owner` | `partner:merchant:owner`（敏感） | 更换主账号 `{userId}`：只能换成这个商户里启用的账号（别的主体的账号、不存在的回字段 `org.ownerNotInOrg`，停用的 `org.ownerDisabled`）；原来的主账号留下，不再是主体内的超管；同一个事务里吊销它的全部会话、清空它的角色（D-101）。新主账号的会话和角色不动。记操作日志 `merchant.change-owner` |
| PUT | `/merchant/merchants/:id/agent` | `partner:merchant:transfer`（敏感） | 仅商户：改归属代理商 `{agentId}`，0 表示改为直属平台；代理商必须存在且启用。原代理商下一次请求就看不到这个商户。记操作日志 `merchant.transfer` |
| GET | `/merchant/merchants/:id/accounts` | `partner:merchant:list` | 这个商户的账号（只读）：分页，`keyword` 匹配登录名、显示名，`status` 过滤，`sortBy` 取 createdAt / username / lastLoginAt；每行 `{id, orgId, username, displayName, email, phone, avatar, owner, mustChangePwd, lastLoginAt, lastLoginIp, status, sort, remark, createdAt}`（`sort`、`remark` 是主体自己写的） |
| GET | `/merchant/merchants/:id/sessions` | `partner:merchant:list` | 这个商户的有效会话：分页，每行 `{sid, userId, username, ip, userAgent, locked, createdAt, lastSeenAt, expiresAt}` |
| POST | `/merchant/merchants/:id/sessions/:sid/revoke` | `partner:merchant:status` | 让这个商户的一个会话下线；不属于这个商户的会话 404。记操作日志 `merchant.revoke-session` |
| GET / DELETE | `/merchant/merchants/:id/ip-allow` | `partner:merchant:list` / `partner:merchant:owner`（敏感） | 商户的 IP 白名单（D-062，商户的主账号在商户端自己设）：GET 返回 `{items}`；DELETE 清空（不再限制），返回 `{removed}`，用在商户把自己挡在外面的时候。记操作日志 `merchant.ip-clear` |
| GET / DELETE | `/merchant/merchants/:id/ip-deny` | `partner:merchant:list` / `partner:merchant:owner`（敏感） | 商户自己设的 IP 黑名单（D-090、D-102，商户的主账号在商户端设）：GET 分页返回这个商户的黑名单，含已过期的记录（`page`、`pageSize`、`keyword` 按网段前缀）；DELETE 整体清空（含已过期的记录），返回 `{removed}`，用在名单把接手的人挡在外面的时候。全局黑名单、白名单、别的主体不受影响。记操作日志 `merchant.ip-deny-clear` |
| GET | `/merchant/options/agents` | `partner:merchant:list` | 仅商户：选代理商用，`keyword` 匹配编号、名称，最多 50 条 `[{id, code, name, status}]`（停用的也列出，新建和改归属时不能选） |
| GET | `/merchant/login-logs` | `partner:merchant:log` | 商户端的登录日志：分页，`orgCode`（商户编号，精确；不存在时结果为空）、`username`、`ip`、`sessionId`、`success`、`from`、`to`；每行同系统管理的登录日志，另有 `orgName`。记操作日志 `audit.view.merchant-loginlog` |
| GET | `/merchant/operation-logs` | `partner:merchant:log` | 商户端的操作日志：分页，`orgCode`、`username`、`action`、`path`、`method`、`ip`、`sessionId`、`failed`、`from`、`to`；每行同系统管理的操作日志，另有 `orgCode`、`orgName`（操作人所属的商户）。记操作日志 `audit.view.merchant-oplog` |

代理商管理的路径：`/agent/agents`、`/agent/agents/:id`、`/agent/agents/:id/status`、`/agent/agents/:id/reset-owner-password`、`/agent/agents/:id/owner`、`/agent/agents/:id/accounts`、`/agent/agents/:id/sessions`、`/agent/agents/:id/sessions/:sid/revoke`、`/agent/agents/:id/ip-allow`、`/agent/agents/:id/ip-deny`、`/agent/login-logs`、`/agent/operation-logs`。

## 代理商端、商户端自己的后台（agent、merchant 端，前缀 `/api/agent/v1`、`/api/merchant/v1`）

代理商、商户程序里各自的后台（D-067），由内核的端后台套件（`core/orgportal`）提供，两个端一模一样，下表的路径相对端的前缀。权限码写成 `<端>:` 加表里的后两段（`merchant:account:list`、`agent:account:list`……）；标"主账号"的只有本主体的主账号能调（`RequireSuper`，主账号就是主体内的超管，D-061）。

- 主体只取登录会话里的主体，从不读请求：主体端已登录的接口，查询串、请求体（JSON 任意一层的键，不论 Content-Type 写的是什么都按 JSON 查一遍；表单和 multipart 的字段名）里出现 `orgId`、`agentId`、`merchantId`（不分大小写，忽略 `_`、`-`、`.` 和方括号，复数 `…Ids` 也算）一律回 `3002`，翻译键 `scope.orgParam`、参数 `param`（D-067 第 5 条）。这条对 `/auth/*`、`/dicts` 和业务模块的接口同样生效；公开接口不查。
- 别的主体的账号、角色、会话一律当作不存在（404）。会话列表同时核对会话行与账号行的主体归属。写接口都在锁里重新认定操作人（锁主体行，D-063）；随后按账号、会话顺序锁住操作人，直到事务结束（D-089）。
- 下表的接口和代理商端的 `/merchants` 认证时按库核对（D-073）：会话、账号、主体的状态和"是不是主账号"每个请求读库，不用状态缓存。平台停用主体、更换主账号、重置主账号密码、让会话下线之后，这些接口在下一个请求就按新状态处理（401，或者不再是主账号的 403）；`/auth/*` 和业务模块的接口仍走缓存；配了 Redis 时收到提交后的失效通知就清缓存（D-075），通知漏收或 Redis 不可用时最长 15 秒后跟上。
- 员工对主账号改资料、启停、分配角色、让会话下线一律 403（`org.account.ownerProtected`、`org.session.ownerProtected`）；主体内没有人能重置主账号的密码（`org.account.resetOwner`，主账号自己用 `/auth/password` 改，忘了由平台重置）；不能停用自己（`org.account.disableSelf`）。
- 建子账号、重置员工密码要算密码哈希（D-068）：这两条接口合起来，一个主体同时最多 2 个在算、每分钟最多 30 次，另外和本程序的登录、改密共用一组密码计算的位置（这两条接口属于最多占一半的那一类，D-071）；超出任何一项都回 429 `4029`、什么都不写，稍后重试。字段不合法、登录名已被占用、目标不在本主体、目标是主账号的请求在这之前就被拒绝，不占次数；被程序的并发上限挡回来的也不占。

| 方法 | 路径 | 权限码 | 说明 |
|---|---|---|---|
| GET | `/org/dashboard` | `<端>:dashboard:view` | 数据中心（D-090）：`days` 默认 30，范围 1–90；`tz` 为东区正值的时区分钟偏移，范围 ±840。结构与平台 `/system/dashboard` 相同，但全部统计只含本端、本主体；活跃账号排名有 `account:list` 权限时为本主体，否则仅本人。读取使用同一个数据库快照 |
| GET | `/org/ip-deny` | 主账号 | 分页列出本主体黑名单，`keyword` 按 IP/CIDR 前缀匹配，`includeExpired=1` 包含已过期规则；结果 `{list, total, page, pageSize}` |
| POST | `/org/ip-deny` | 主账号 | `{cidr, expiresIn, remark?}`，有效期按分钟，0 永久、最多 525600；同一网段更新到期时间和备注。最多 100 条生效规则，含过期记录总计最多 1000 条（满后可续期或删除已有记录），不能封禁当前 IP；返回规则，记 `org.ip-deny` |
| DELETE | `/org/ip-deny/:id` | 主账号 | 删除本主体黑名单规则；其他主体/端或非主体黑名单记录均为 404。记 `org.ip-unblock` |
| GET | `/org/overview` | 登录即可 | 概览：`{org: {id, code, name, contactName, contactPhone, status, createdAt}, owner, counts}`；`owner` 表示调用者是主账号；`counts` 只含调用者有查看权限的几项：`accounts`（账号总数，包含主账号和子账号；`account:list`）、`sessions`（`session:list`）、`roles`（`role:list`），代理商端另有 `merchants`（`agent:merchant:list`）。平台写的备注、排序不给主体看 |
| GET | `/org/ip-allow` | 主账号 | 本主体的 IP 白名单（D-062）：`{items, yourIp}` |
| PUT | `/org/ip-allow` | 主账号 | 整份替换 `{items: [{cidr, remark?}]}`，空表示不限制；名单对主账号自己也生效，不为空时必须包含主账号当前的地址（否则回 `3001`）；每份最多 100 条。把自己挡在外面了由平台在"商户管理""代理商管理"里清空。记操作日志 `org.ip-allow` |
| GET | `/org/accounts` | `account:list` | 子账号：分页，`keyword` 匹配登录名、显示名，`status` 过滤，`sortBy` 取 createdAt / username / lastLoginAt；每行 `{id, orgId, username, displayName, email, phone, avatar, owner, mustChangePwd, lastLoginAt, lastLoginIp, status, sort, remark, createdAt, roles: [{id, code, name, status}]}` |
| GET | `/org/accounts/:id` | `account:list` | 不在本主体 404 |
| POST | `/org/accounts` | `account:create`（敏感） | 入参 `username`（主体内唯一，规则同平台用户名；重复回 `4001` `org.usernameTaken`）、`password?`（不传则生成）、`displayName?`、`email?`、`phone?`、`roleIds?`、`sort?`、`remark?`；下次登录必须改密。带 `roleIds` 时非主账号要有 `account:assign-role`，角色规则同分配角色。返回 `{account, initialPassword}`（生成的密码**只在这里出现一次**，自己给了密码时为空）。记操作日志 `org.account.create`。受密码计算的限制（见上面的规则），超出回 429 `4029`、账号不建 |
| PUT | `/org/accounts/:id` | `account:update` | 整份替换 `{displayName, email, phone, sort, remark}`。记操作日志 `org.account.update` |
| POST | `/org/accounts/:id/status` | `account:status` | `{status}`；停用时同一个事务吊销它的全部会话；重新启用时非主账号只能启用角色都是自己能分配的账号（D-058；停用中的角色也算，D-100）；主账号在主体内停不了。记操作日志 `org.account.status` |
| POST | `/org/accounts/:id/reset-password` | 主账号 | 员工的密码换成随机密码，返回 `{initialPassword}`（只这一次），下次登录必须改，全部会话吊销；主账号自己的回 403。记操作日志 `org.account.reset-password`。受密码计算的限制，超出回 429 `4029`、密码不换 |
| PUT | `/org/accounts/:id/roles` | `account:assign-role`（敏感） | 整体替换 `{roleIds}`：别的主体的角色按不存在回 `3001`（`rbac.role.notFound`）；非主账号分配不了带敏感权限码或自己没有的权限码的角色（D-063）。记操作日志 `org.account.assign-role`。被拒时有 `role:list` 的员工看到具体原因，没有的只看到 `rbac.role.notAssignable` 和角色 ID（D-069） |
| GET | `/org/roles` | `role:list` | 本主体的角色 `[{id, code, name, status, sort, remark, …}]` |
| POST | `/org/roles` | `role:create` | `{code, name, status?, sort?, remark?}`，编码在主体内唯一。记操作日志 `org.role.create` |
| PUT | `/org/roles/:id` | `role:update` | 同上。记操作日志 `org.role.update` |
| DELETE | `/org/roles/:id` | `role:delete` | 还分给了账号的角色删不了（`rbac.role.inUse`）。记操作日志 `org.role.delete` |
| GET | `/org/roles/:id/perms` | `role:list` | 角色的权限码 |
| PUT | `/org/roles/:id/perms` | `role:grant`（敏感） | 整体替换 `{codes}`：敏感权限码只有主账号能授，非主账号授不出自己没有的（D-063）。主体端没有数据范围。记操作日志 `org.role.grant` |
| GET | `/org/perms/tree` | `role:list` | 本端的权限码分组树 |
| GET | `/org/sessions` | `session:list` | 本主体的有效会话：分页，每行 `{sid, userId, username, ip, userAgent, locked, createdAt, lastSeenAt, expiresAt, owner, current}`（`owner` 是主账号的会话，`current` 是调用者这一次的会话） |
| POST | `/org/sessions/:sid/revoke` | `session:revoke` | 让本主体的一个会话下线；别的主体、别的端的会话 404。记操作日志 `org.session.revoke` |
| GET | `/org/login-logs` | `loginlog:list` | 本主体的登录日志（含输错账号名的失败登录）：分页，`userId`、`username`、`ip`、`sessionId`、`success`、`from`、`to`。记操作日志 `audit.view.org-loginlog` |
| GET | `/org/operation-logs` | `oplog:list` | 本主体账号的操作日志：分页，`userId`、`username`、`action`、`path`、`method`、`ip`、`sessionId`、`failed`、`from`、`to`。调用者不是主账号时，`org.ip-allow`、`org.ip-deny` 两种操作的 `body` 是 `***`（名单只有主账号能看，D-097）。记操作日志 `audit.view.org-oplog` |
| GET | `/org/profile` | 登录即可 | 个人中心：本人的账号资料加 `roles`、`orgCode`、`orgName`、`sessions`（当前有效的会话数）；不含管理者写在账号上的 `remark`、`sort`、`status`（D-099） |
| PUT | `/org/profile` | 登录即可 | 本人改 `{displayName, email, phone}`。记操作日志 `org.profile.update` |
| PUT | `/org/profile/avatar` | 登录即可 | 本人选内置头像 `{preset}`（名字同平台端），空字符串表示清除；这一版不能上传。返回 `{avatar}`。记操作日志 `org.profile.avatar` |
| POST | `/org/profile/revoke-other-sessions` | 登录即可 | 本人除当前会话外的会话全部下线，返回 `{revoked}`。记操作日志 `org.profile.revoke-other-sessions` |
| GET | `/merchants` | `agent:merchant:list` | 仅代理商端：名下商户（只读，D-065 第 6 条）。分页，`keyword` 匹配编号、名称、联系人、联系电话，`status` 过滤，`sortBy` 取 createdAt / code / name；每行 `{id, code, name, contactName, contactPhone, status, createdAt}`。商户改了归属，原代理商下一次请求就看不到 |
| GET | `/merchants/:id` | `agent:merchant:list` | 仅代理商端：不在名下 404。代理商端没有写商户数据的接口 |

IP 输入（D-091）：黑白名单的 `cidr` 同时接受单个 IP、CIDR 和完整四段的 IPv4 连续后缀通配，例如 `124.55.12.*`、`124.55.*.*`、`124.*.*.*`。返回和去重使用等价 CIDR；不接受 `**` 或 `124.*.12.*`。黑名单最宽仍为 IPv4 `/8`，并保留自锁保护。

主体安全设置（D-090）：黑白名单分别为独立页面，仅主账号可见。主体黑名单在登录、刷新和已认证请求中先于主体白名单生效；不影响平台、其他主体及没有可信主体身份的匿名验证码/入驻请求。规则复用现有 IP 变更通知与最长 5 秒核对。

## Raw 路由（不属于任何端）

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/healthz` | 存活探针 |
| GET | `/readyz` | 就绪探针：数据库检查成功返回 200，失败返回 503 / `5003`；每实例缓存结果 1 秒，并发请求共用最多 2 秒的检查（D-084），不检查 Redis。状态变化需等缓存过期及下一次检查完成 |

## 错误码

见 `server/core/httpx/codes.go`。业务性的框架错误码：4001 资源冲突、4002 不能停用或降级最后一个超管、4101 由代码声明的内容不能在后台修改或删除。协议层：401 未认证（1004）、403 无权限（2001）/ 需改密（2002）/ 当前 IP 不允许访问（2003：IP 黑名单、端白名单、已登录后主体或账号白名单，D-062）、404（4004）、405（4005）、409 刷新冲突（1005）、423 会话已锁屏（1006）、413（4013）、429（4029）、500（5000）、503（5003：依赖不可用，包括请求处理超过 `server.handlerTimeout`，D-037）。业务性失败返回 200 加非零 `code`。


## 入驻审核与邀请（D-080）

| 端 | 方法 | 路径 | 守卫与说明 |
|---|---|---|---|
| platform | GET | `/onboarding/applications` | `onboarding:application:list`，LiveAuth；分页，`state` 为 pending/approved/rejected，keyword 匹配编号/名称。返回申请资料、agentId、reviewState、reviewNote、orgCode、createdAt、reviewedAt、reviewedBy，始终没有密码 |
| platform | POST | `/onboarding/applications/:id/review` | `onboarding:application:review`（敏感），WithActor；`{decision: approve 或 reject, note}`，驳回必须说明。只允许待审转为通过/驳回一次；通过时同事务创建主体和主账号，返回 `{org, ownerId, password}`；初始密码只显示一次，首次登录强制改密；驳回返回 null |
| agent | GET | `/onboarding/invitations` | LiveAuth + RequireSuper，只返回本代理商的邀请；分页，行含 id、agentId、expiresAt、usedAt、revokedAt、createdAt、createdBy，无凭证或摘要 |
| agent | POST | `/onboarding/invitations` | RequireSuper + WithActor；商户入驻开启时可用；同主体每天最多 20 次创建尝试，返回 `{invitation, url}`，链接只显示一次、7 天有效、只能提交一份申请 |
| agent | DELETE | `/onboarding/invitations/:id` | RequireSuper + WithActor，其他代理商或不存在的 ID 404；已提交的申请不受撤销影响；已经撤销过的再撤销返回成功、不改第一次的撤销时间（D-099） |

审核与邀请写入记录 `onboarding.review`、`onboarding.invite`、`onboarding.revoke-invite` 操作日志；公开申请不记录请求体，邀请原文只存摘要。关闭入驻不影响处理已有申请。平台核实申请联系人后交付初始密码；密码响应丢失使用既有主账号重置流程。

## 请求与管理边界

JSON 请求字段按接口声明校验：未知结构体字段、各层重复键、同一结构体字段的大小写别名重复、尾随文档及超过 64 层嵌套均回 `3002`（D-108）。动态 map 键区分大小写；请求大小仍受统一 BodyLimit 约束。

账号部门调整保持用户资源各权限码的范围边界；新建、角色分配与部门变更还会核对目标角色在目标部门的实际覆盖集合（D-109）。超出边界回 403，事务不保存；同范围内调整和超管管理仍允许。

三个内置端创建角色省略 status 默认启用；更新角色省略 status 保留锁内的原状态（D-110）。显式 0/1 才表示停用/启用，角色显示信息的更新不触发授权发布等待。

验证码 GET 需要 `X-GA-Client: web`，拒绝跨站 Fetch Metadata；提供 Origin 时须在白名单中，同源 GET 可以不带 Origin。刷新访问数据库前按来源每分钟最多 120 次（IPv6 /64），超限 429 且不修改 Cookie；本端不存在的会话直接返回结束（D-112）。

代理商简要选项仅按编号和名称搜索，最多 50 条，联系人/电话不参与匹配（D-113）。共享 IP 名单的更新使用独立 2 秒时限，失败最早 1 秒后重试并保留较新失效通知。

操作查询记录标注部分解析和截断；安全事件按稳定路由、方法类别、注册端、IPv6 /64 合并，匿名与已认证事件分别使用有界新键预算（D-114）。详情保留截断后的原始路径/地址。

每端每主体最多 200 个角色（含停用与内置角色），每账号的角色分配请求最多 20 项；平台岗位请求最多 20 项（按原始数组计数），权限授予请求最多 1024 项；超限回参数校验错误（D-115）。现有超量数据不自动删除。授权判定在重载竞争时最多等待 3 秒，失败回 503。

每进程后台写入口最多 32 个，本人按账号最多 2 个、管理按主体最多 2 个；角色写入口每主体每分钟最多 120 次。头像每进程最多 2 个、每账号 1 个，超额在读体前拒绝。繁忙均回 429，位置结束后恢复（D-116）。

移除角色/权限、收窄角色范围同样要求具备原角色管理资格；保留原有角色不视为新增。没有关联权限码的资源也不能显式保存超出本人范围的设置，拒绝时事务不保存（D-117）。

Redis 调用方取消或期限到达时，验证码存储不生成本地回退答案；共享服务真正故障时的降级规则保持不变（D-118）。

平台相对部门角色的新增权限/范围扩大、角色启停及账号启用，核对受影响账号的实际部门集合；超出操作人的对应集合回 403，事务不保存。角色撤权检查全部持有人，账号角色移除检查该目标，原样保留不被当作新增授权（D-120）。

主体确认操作绑定确认前的端、主体和目标；关闭、主体切换或页面停用后不提交失去归属的确认结果。创建/重置密码的明文结果及手填初始密码在关闭或页面停用时清空，迟到结果不再展示（D-121）。

名单重载的时限包含等待其他重载的时间；等待超时仍保持原有名单与退避语义（D-123）。每日邀请额度在管理事务内认定当前主账号后占用，失效身份及管理写准入拒绝不占额度（D-125）。
