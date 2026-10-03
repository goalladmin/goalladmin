# GoAllAdmin 技术规范

> 适用范围：当前源码。已发布版本的功能以对应 Git 标签和 CHANGELOG 为准；v0.1.0 的发布范围为平台端、单实例、MySQL；v0.2.0 起包含代理商端、商户端、主体隔离、可选的 Redis 与多实例。本文是设计依据，代码注释里的"规范 §x.y"指的就是本文的章节。
> 实现与本文不一致的地方，以 `docs/decisions.md` 为准（那里逐条记录了偏离的原因），依赖的精确版本以 `server/go.mod` 和 `web/**/package.json` 为准。
> 表结构的列顺序约定在 `docs/conventions.md`，接口清单在 `docs/api.md`，新增业务模块的步骤在 `docs/guides/new-module.md`，密码哈希算法怎么选、怎么设置在 `docs/guides/password-hash.md`。
> 使用、部署、维护与排障的入口见[文档导航](README.md)。

---

## 0. 怎么用这份文档

- 改框架内核（`server/core/`、`web/packages/shell/`）前必读：§1 范围、§3 目录与依赖方向、§5 认证、§6 授权、§13 测试、§16 扩展与升级。只写业务模块的话，读 §3、§4、§6、§8、§9 和 `docs/guides/new-module.md` 就够。
- 文中的"必须""禁止"是硬约束。"建议"可以偏离，但要先在 `docs/decisions.md` 记一笔理由。
- 遇到文档没覆盖的取舍：先写进 `docs/decisions.md`，再写代码。
- §15 列的是还没有定案的事项。

---

## 1. 定位与范围

### 1.1 是什么

GoAllAdmin 是 Go + Vue 3 的后台脚手架。它和同类脚手架的区别有三点：

1. **多端原生。** 一个工程里可以有多个"端"（Portal）。当前提供平台、代理商、商户三个独立程序和前端，各有独立的用户来源、登录入口、签名密钥、菜单和权限集。一个端的令牌在另一个端必然被拒绝；主体端的账号与数据按主体隔离（§7）。
2. **权限和菜单在代码里声明。** 权限码、菜单树都由模块在代码里注册，进 git、可评审、可测试。数据库只存"哪个角色被授予了哪些权限码"，以及菜单在后台调过的显示方式和位置。菜单的路径、页面和权限码不能在后台改（菜单管理只改显示，§6.6、D-025），没有 API 管理页。
3. **小。** 提供后台认证、授权、审计及主体管理的基础集合，业务功能按需写成模块，见 §1.3。

### 1.2 形态：脚手架

使用方式是克隆、改名、加业务模块，上游更新靠 `git merge upstream`。为了让合并不痛，定两条纪律：

- `server/core/` 和 `web/packages/shell/` 属于框架，业务方不改。需要改时，先改 GoAllAdmin 仓库，再合并到业务仓库。
- 业务代码只放在 `server/modules/<业务名>/` 和 `web/apps/<端名>/`。

### 1.3 当前源码范围

**做：**

| 类别 | 内容 |
|---|---|
| 端 | Portal 抽象与注册机制；平台、代理商、商户三个独立程序与前端，独立签名密钥与登录入口 |
| 认证 | 账号密码登录、短时 Access JWT、可轮换的 Refresh 会话、登出、强制改密、改密 |
| 登录防护 | 图形验证码、按 IP 和按账号限流、失败锁定、登录日志 |
| 授权 | 权限码注册表、Casbin 判定、路由守卫（三档 + 超管专用，D-035）、超级管理员、角色管理、角色授权 |
| 菜单 | 代码声明的菜单树，按权限过滤后下发；按钮权限；菜单管理（只改显示名、图标、顺序、位置和隐藏，可建分组，§6.6，D-025） |
| 系统管理 | 用户管理、角色管理与授权、部门与岗位（D-033）、个人中心（本人改资料和头像、看安全状态、改密、下线其他设备，D-038、D-040） |
| 主体管理 | 平台开通、启停代理商和商户，管理主账号与商户归属；主体端的员工、角色、会话、日志和个人中心，代理商只读查看名下商户（D-061–D-073） |
| 入驻与邀请 | 默认关闭的代理商、商户申请，平台审核；代理商主账号邀请商户（D-080） |
| 主体数据中心与安全设置 | 主体范围统计和受权限限制的排名，主账号专属 IP 白名单与黑名单，IPv4 末尾连续星号网段输入（D-090–D-093） |
| 多实例 | 可选 Redis 连接、缓存失效通知、共用计数、一次性验证码；计数故障降级，平台双实例与三端部署示例（D-074–D-079，§11.3） |
| 数据权限（D-039） | 仅本人、本部门、本部门及下级、全部四种范围，按"角色 × 数据资源"设置，只收窄权限码、不放宽；业务模块用 `DataFilter` 套用同一套范围 |
| 运维中心（D-032） | 操作日志、登录日志、错误日志、安全事件、调查时间线、会话管理；日志只读，没有删除和修改接口 |
| 系统设置（D-034） | 字典管理；安全设置（只读展示生效的登录防护和密码策略、允许范围和对应的配置项，修改只能走配置文件） |
| 数据中心、工作台与安全监控（D-030、D-036） | 数据中心（用户、登录、操作统计）、安全监控（原"监控中心"，D-041 起在根目录、紧跟数据中心；安全情况 + 进程自身的请求量、CPU、内存、协程、数据库连接池等只读指标，最近 60 分钟）、工作台（本人数据） |
| 字典 | 模块在代码里声明字典、后台新建字典，一张表两种来源；读取接口、管理页面、前端 `useDict`（§4.5，D-023） |
| 前端壳 | 登录页、布局、动态路由、标签页、面包屑、暗色模式、权限指令、请求封装、11 种界面语言（带国旗的语言选择，§9.6，D-026） |
| 工程 | 配置与启动校验、数据库迁移、Makefile、CI、Docker Compose、健康检查 |

**不做（未经需求确认不得自行扩展）：**

参数管理、在后台编辑菜单结构（路径、页面、权限码）、API 管理页、代码生成器、表单生成器、文件上传与媒体库（头像的专用上传见 D-040）、定时任务管理、在线配置编辑（安全设置只读展示，D-034）、服务器监控的告警与历史存储（进程自身的只读指标见 D-030）、插件市场、MCP、通用多租户框架、PostgreSQL、OpenAPI 自动生成、工作流、消息队列、微服务。

后续候选：TOTP 二次验证、文件上传、OpenAPI；版本和范围由维护者确认。

主体隔离和多实例已按后续决策接入当前源码。Redis 是可选的，不配时每个程序一个实例；同一端的多个实例必须启用。部署、升级和故障边界见 §11.3 与相关指南，已发布能力仍以标签为准。

通用 CRUD 组件（`GaTable`、`GaForm` 之类）v0.1 也不做：系统管理页面直接用 Element Plus 写，同一种写法重复出现三次以后再提炼。

### 1.4 洁净室规则（法律要求，不是风格偏好）

本项目以 Apache-2.0 发布，进入仓库的代码必须能在这个许可证下分发。

- **禁止**阅读、复制、改写任何非宽松许可证项目（BSL、SSPL、AGPL、专有代码等）的文件来产出 GoAllAdmin 的代码；无论是人还是 AI 协作者，工作目录和上下文里都**不得**包含这类源码。
- 参考只限 MIT、Apache-2.0、BSD、ISC 这类宽松许可证的项目。如果借用了其中的实质性代码，在文件头保留出处和版权声明。
- GoAllAdmin 有自己的命名体系；CI 里有命名检查，防止与被参考项目的标识符重合。
- 沿用接口约定不算复制，例如 `{code, data, msg}` 的响应形状、`page/pageSize` 分页参数。

---

## 2. 技术栈与版本

选主流、许可证干净、AI 熟悉的库；大版本一经确定不在 v0.x 内变更。下表是选型；精确版本以 `go.mod`、`package.json` 为准，与本表的差异记录在 `docs/decisions.md`（D-001、D-009、D-013）。

| 层 | 选型 | 版本 | 说明 |
|---|---|---|---|
| 语言 | Go | 1.26+ | |
| HTTP | Gin | 1.12.x | 只负责路由、绑定、中间件 |
| ORM | GORM + MySQL 驱动 | 1.31.x / 1.6.x | |
| 数据库 | MySQL | 8.0+（开发用 8.4） | v0.1 只支持 MySQL |
| 迁移 | 内置 SQL 迁移器 | | 编号 SQL 文件，`embed` 进二进制；只向前，不引入第三方迁移库（D-001） |
| 授权 | casbin | v3 | 只做判定引擎；策略表 `ga_casbin_rule` 由框架自己读写（D-009） |
| JWT | golang-jwt/jwt | v5 | 只允许 HS256 |
| 密码 | golang.org/x/crypto/argon2、bcrypt；标准库 crypto/pbkdf2 | | 默认 Argon2id（19 MiB、2 轮），可选 bcrypt（cost 12）、PBKDF2-HMAC-SHA256（60 万次）、PBKDF2-HMAC-SHA512（22 万次）；核对哪种都认（D-070、D-072） |
| 配置 | spf13/viper | 1.21+ | 环境变量必须显式绑定，见 §11 |
| 日志 | 标准库 log/slog | | 不引入 zap |
| 验证码 | 自带（标准库绘制，D-029） | | 5 位数字，5 分钟、一次性；可选 Redis 共用答案（D-077） |
| 限流 | golang.org/x/time/rate | | 内存实现，留接口 |
| 缓存 | 自带内存 TTL 缓存，留接口 | | 缓存始终在进程内存里；配了 Redis 时用它在实例之间传失效通知（D-074，§11.3） |
| Redis（可选） | github.com/redis/go-redis/v9 | v9.22.0 | 多实例时用；服务器最低 6.0，自己装的推荐 8.2 及以上（D-074，§11.3） |
| 测试 | testing + testify | | 连真 MySQL（`GA_TEST_DSN`）、真 Redis（`GA_TEST_REDIS`），不用 SQLite，不 mock GORM |
| 前端 | Vue 3.5、Vite 8、TypeScript 5、Element Plus 2.x、Pinia 3、Vue Router 4、vue-i18n 11、Axios 1.x | | 全部锁精确版本 |
| 前端工程 | pnpm workspace、ESLint 10、Vitest、Playwright | | 必须提交 `pnpm-lock.yaml` |

新增任何第三方依赖前，先在 `docs/decisions.md` 记录名称、用途、许可证。只接受 MIT、Apache-2.0、BSD、ISC、MPL-2.0。构建产物（前端文件、后端二进制、Docker 镜像）都随附自动生成的第三方许可证清单，缺许可证文件的依赖会让构建或 `make ci` 失败（D-028）。

前端供应链：所有依赖写精确版本或提交锁文件后用 `pnpm install --frozen-lockfile` 安装；在 `pnpm-workspace.yaml` 里设置 `minimumReleaseAge`，不装发布不足 7 天的新版本。

---

## 3. 仓库与目录结构

### 3.1 总览

```text
goalladmin/
├── server/            # 后端：平台、代理商、商户三个程序（D-061）
├── web/               # 前端：pnpm 工作区，三个端的应用和共用的壳（§3.5，D-064）
├── deploy/            # Dockerfile、docker-compose.yml、nginx 示例
├── docs/              # decisions.md、api.md、guides/
├── Makefile
├── README.md
└── LICENSE
```

### 3.2 后端

```text
server/
├── main.go                   # 只做：读配置 → app.New → 显式注册模块 → Run；`go run .` 即启动（D-018）
├── core/                     # 框架内核
│   ├── app/                  # 生命周期、模块注册、路由装配、优雅关停        [公开]
│   ├── conf/                 # 配置加载、环境变量绑定、启动校验              [公开：Config 类型]
│   ├── logx/                 # slog 封装，从 ctx 取 request_id、用户          [公开]
│   ├── db/                   # GORM 初始化、事务助手                          [公开：From、Tx]
│   ├── httpx/                # 响应、分页、错误码、请求 ID                    [公开]
│   ├── portal/               # 端的定义与注册                                [公开]
│   ├── auth/                 # Principal 与 FromCtx                            [公开]
│   ├── rbac/                 # Perm、MenuNode、路由守卫                       [公开]
│   ├── oplog/                # Record 路由选项                                [公开]
│   ├── dict/                 # 字典的声明类型与服务                          [公开]
│   ├── monitor/              # 服务器状态的只读类型与接口（D-030）            [公开]
│   ├── scope/                # 主体端查询的归属条件，scopetest 是隔离测试模板（§7.1）[公开]
│   ├── ipacl/                # IP 黑名单与白名单（D-062）                       [公开]
│   ├── org/                  # 主体（代理商、商户）与主体账号（D-065）           [公开]
│   ├── orgportal/            # 主体端自己的后台：子账号、角色、会话、日志、个人中心（D-067）[公开]
│   └── internal/             # 以上包的实现细节：JWT、会话、密码、验证码、限流、缓存、
│                             # Casbin 封装、中间件、安全头……业务代码无法 import
├── modules/
│   ├── system/               # 系统管理：用户、角色、操作日志、登录日志、会话、个人中心、系统设置
│   ├── agent/                # 平台端的代理商管理（D-065）
│   ├── merchant/             # 平台端的商户管理（D-065）
│   ├── agentportal/          # 代理商端本身和它自己的后台、名下商户（D-067），只编进代理商程序
│   ├── merchantportal/       # 商户端本身和它自己的后台（D-067），只编进商户程序
│   └── <名>/                 # 二次开发的模块，在 main.go 里登记
├── cmd/agent/                # 代理商程序：只服务 agent 端（D-061）
├── cmd/merchant/             # 商户程序：只服务 merchant 端（D-061）
├── cmd/dev/                  # make dev：源码改动后自动重新编译、重启
├── internal/portalcmd/       # 代理商、商户程序共用的命令行和装配（D-061）
├── migrations/
│   └── core/                 # 框架自己的 SQL 迁移；业务模块的迁移放在各自模块目录下的 migrations/
├── config/config.example.yaml
└── go.mod
```

`core/` 下只有列为 [公开] 的包可以被 `modules/` import，其余一律放在 `core/internal/`，由 Go 编译器保证业务代码碰不到。公开包的导出符号就是框架的 API 面，见 §16。

包名避开标准库：用 `logx`、`conf`、`httpx`，不用 `log`、`config`、`http`、`errors`。不建 `utils`、`common`、`helper` 这类包。

### 3.3 依赖方向（必须）

```text
cmd → modules → core → 第三方库
```

- `core/` 禁止 import `modules/` 和任何业务包。
- 模块之间禁止互相 import。需要别的模块的能力时，在需要的一方定义接口，由 `main.go` 装配时注入。
- 模块内部只允许 `handler → service → repo → GORM` 单向调用。handler 不碰 GORM。
- 小模块用单包平铺文件：`user_model.go`、`user_dto.go`、`user_repo.go`、`user_service.go`、`user_handler.go`、`module.go`。大业务模块可以分子包，但同样要保持单向。

CI 里用 `go list -deps` 脚本检查前两条，违反即失败。

### 3.4 依赖注入与 context（必须）

- 禁止"包级全局变量里放数据库句柄"这种写法。`app.Deps` 持有 DB、Logger、Conf、权限注册表、认证与授权服务，模块在 `Init(deps)` 里拿到并通过构造函数传给 service、repo。
- 模块在 `main.go` 的 `modules()` 里**显式注册**。禁止靠匿名 import 加 `init()` 自注册，那种方式漏掉一行时编译不报错、功能静默消失。
- 所有 service、repo 方法第一个参数是 `context.Context`。数据库操作一律 `db.From(ctx)`。
- 事务：`db.Tx(ctx, func(ctx context.Context) error { ... })` 把事务句柄放进 ctx，`db.From(ctx)` 在事务内自动返回该句柄。跨 repo 的事务靠传同一个 ctx 实现。
- 检查之后还要再读、或几张表要成套读时用 `db.Snapshot(ctx, fn)`：只读、可重复读的事务，fn 里的查询看到的是同一时刻已提交的数据（D-051）。
- 清缓存这类副作用用 `db.AfterCommit(ctx, fn)` 挂到事务提交之后（不在事务里时立即执行）：写在事务体内的话，提交前并发请求会用旧数据把缓存填回去，回滚后也白清了。
- "先判断再写入"的规则（不能停用最后一个超管、余额够才扣款之类）必须和写入放在同一个事务里，并用 `SELECT ... FOR UPDATE` 锁住一行作为串行化点，否则两个并发请求会同时通过判断。

### 3.5 前端

```text
web/
├── pnpm-workspace.yaml
├── packages/
│   └── shell/                # @ga/shell，TypeScript strict，业务方不改
│       └── src/
│           ├── request/      # Axios 实例工厂：Bearer、单飞刷新、统一报错
│           ├── auth/         # useAuthStore：令牌（仅内存）、用户、权限码、菜单
│           ├── perm/         # hasPerm()、v-perm 指令、<GaPerm>
│           ├── router/       # 静态路由 + 菜单转动态路由、守卫、403/404
│           ├── layout/       # GaLayout：侧边栏、顶栏、面包屑、标签页、暗色、折叠、移动端
│           ├── views/        # 登录、强制改密、个人信息（默认页，端可用 pages.profile 替换）、403、404；
│           │                 # views/org/ 是主体端的后台页面（D-067）
│           ├── org/          # 主体端后台的接口（orgApi）
│           ├── i18n/         # 语言清单 languages.ts 和 11 种语言的文案（<语言代码>.ts）
│           └── styles/       # 设计变量，映射到 Element Plus 变量
└── apps/
    ├── platform/             # 平台端：系统管理页面、业务页面
    ├── agent/                # 代理商端（@ga/agent，D-064）
    └── merchant/             # 商户端（@ga/merchant）
```

三个端的应用只能引用自己目录里的文件和 `@ga/shell`（包根、`./styles`），不能用相对路径或包名引用别的端、也不能深入壳的内部文件。这条由壳导出的 ESLint 规则检查（`@ga/shell/eslint` 的 `boundary`，三个应用都启用），作用和后端的 `scripts/depcheck.sh` 一样（D-064）。用不到代理商端、商户端时删掉 `apps/agent`、`apps/merchant`，跑一次 `pnpm install` 即可。

`@ga/shell` 的 `package.json` 用 `exports` 字段只导出包根（和 `./styles`，以及给应用的 ESLint 配置、Vite 配置用的 `./eslint`、`./vite`），业务页面只能 `import { … } from '@ga/shell'`，深层路径 import 会被 pnpm 和 TypeScript 拒绝。

每个端是一个独立的 Vite 应用，入口只有一行装配：

```ts
createPortalApp({
  portal: 'platform',
  views: import.meta.glob('./views/**/*.vue'),
  locales,
}).mount('#app')
```

代理商端、商户端是主体端（D-067），入口多一项 `scoped: true`：登录页多一个主体编号（记住上次输入的），顶栏显示主体名称；后端菜单里组件名为 `org/<页面>/index` 的页面（概览、子账号、角色与权限、会话、登录日志、操作日志）由壳内置、按需加载，应用 `views` 里同名的视图优先；个人中心默认用主体端的。应用里只放各自特有的页面（代理商端的"名下商户"）。

三个端的构建编号（`version.json`）和第三方许可证清单（`third-party-licenses.txt`）的 Vite 插件在壳里（`@ga/shell/vite` 的 `buildId`、`thirdPartyLicenses`，D-027、D-028、D-067），是纯 JS（构建时由 Node 直接加载）。

`apps/` 下允许 JS 和 TS 页面共存（`allowJs: true`），方便迁入已有页面。`packages/shell` 必须是 TS（构建插件 `vite-plugins/` 除外，它带 `.d.ts`）。

---

## 4. 核心概念

### 4.1 Portal（端）

```go
type Portal struct {
    Code       string        // "platform"；用于路由前缀和 JWT aud
    Users      UserProvider  // 这个端的用户从哪来
    AccessTTL  time.Duration // 默认 15 分钟
    RefreshTTL time.Duration // 默认 7 天
    Login      LoginPolicy   // 验证码阈值、限流、锁定参数
}

type UserProvider interface {
    FindByUsername(ctx context.Context, username string) (*Account, error)
    FindByID(ctx context.Context, id uint64) (*Account, error)
    UpdatePasswordHash(ctx context.Context, id uint64, hash string) error
    TouchLogin(ctx context.Context, id uint64, ip string, at time.Time) error
}

type Account struct {
    ID            uint64
    Username      string
    DisplayName   string
    PasswordHash  string
    Status        int    // 1 启用，0 停用
    MustChangePwd bool
}
```

- v0.1 只注册 `platform` 一个端，它的 `UserProvider` 由 `modules/system` 提供，读 `ga_user`。
- `FindByUsername` 收到的登录名已经归一化（去首尾空白、小写）；返回的账号必须满足 `NormalizeUsername(Account.Username)` 等于它，否则框架按账号不存在处理（D-043）。登录防护按输入的登录名计数，底层查询若把重音变体、邮箱、手机号也当成同一个账号，每种写法就各有一份尝试配额；要支持邮箱登录，先把邮箱解析成账号名。
- 端的签名密钥来自环境变量 `GA_JWT_SECRET_<CODE 大写>`，每个端一个。
- 端的路由前缀固定为 `/api/<code>/v1`。
- 主体端（D-061）：`Portal.Scoped = true` 的端每个账号属于一个主体（代理商、商户），`Users` 必须实现 `portal.OrgUserProvider`（按编号、按 ID 查主体，在主体内按账号名查账号），`Account.OrgID` 是账号所属主体。账号名只在主体内唯一，登录要带主体编号（§5.8）；主体停用时它下面的账号都按停用处理。部门树接口叫 `portal.DeptProvider`（`Portal.Dept`，v0.1 叫 `OrgProvider`）。
- 认证、授权、菜单的实现都以 `Portal.Code` 为键，不得写死 `"platform"` 字符串。这样 v0.2 加第二个端（含 Scoped 主体隔离，§7）时只需注册，不需改内核。

### 4.2 Principal（当前身份）

认证中间件把它放进 ctx，后续代码只从这里取身份，**禁止**从请求参数取。

```go
type Principal struct {
    Portal      string
    UserID      uint64
    SessionID   string
    Username    string
    DisplayName string
    OrgID       uint64 // 主体端：所属主体，来自会话行（D-061）；平台端恒为 0
    Super       bool   // 平台端：超管；主体端：本主体的主账号
}

func FromCtx(ctx context.Context) (Principal, bool)
```

### 4.3 Module（模块）

```go
type Module interface {
    Name() string
    Init(deps *app.Deps) error
    Perms() []rbac.Perm                 // 本模块声明的权限码
    Menus() []rbac.MenuNode             // 本模块声明的菜单，挂到指定端
    Routes(r *app.Router)               // 路由注册
    Start(ctx context.Context) error    // 后台任务；没有就返回 nil
    Stop(ctx context.Context) error
}
```

### 4.4 Raw 路由

少数路由不属于任何端，例如对外开放 API 的自定义验签接口、第三方回调。它们通过 `r.Raw()` 直接挂在 Gin 根上，不经过端的认证和授权中间件。每条 Raw 路由必须登记用途，启动时打印清单，`docs/api.md` 里单列。v0.1 框架自身只有 `/healthz`、`/readyz` 两条 Raw 路由。

### 4.5 字典

字典把"值"翻译成显示文字、颜色和下拉选项（D-023）。两种来源存在同一张表里：

- **代码声明**：模块实现可选接口 `app.DictSource`，在 `Dicts()` 里返回 `dict.Dict`。编码必须是"模块名.名字"（`order.priority`）。启动时校验并同步进库，声明不合法拒绝启动。
- **后台新建**：编码不能含点，完全由管理员维护。

规则：

- **代码拥有值，后台拥有显示。** 代码字典本身只读，不能在后台加项、删项、改值；后台能改项的显示文字（含其他语言）、颜色、扩展值、排序和启用状态。改过的项标记 `overridden`，同步时不再覆盖，可以恢复默认。
- 代码里删掉的项不删行，解除锁定并停用（历史数据还要靠它显示文字）；不再有模块声明的整本字典转为后台字典。
- 值类型是 `string` 或 `int`；库里存字符串，读取接口按类型输出 JSON 字符串或数字。`int` 字典的值必须是规范的十进制整数。
- 每本字典属于一个端，或 `*` 所有端共用。读取接口 `GET /api/<portal>/v1/dicts?codes=a,b`（AuthOnly）只返回属于当前端或共用、且启用中的字典，文字按 `Accept-Language` 选择；停用的项也返回（历史数据要显示），前端下拉只列启用的项。
- 服务端校验用 `deps.Dict.Has(ctx, code, value)`，只认启用中的值。业务规则里用到的枚举值必须来自代码声明的字典，不要依赖后台字典的值。
- 管理接口在系统管理模块（§9.5），只在平台端。

```go
func (m *module) Dicts() []dict.Dict {
    return []dict.Dict{{
        Code: "order.priority", Portal: "platform", Name: "订单优先级", ValueType: dict.Int,
        Items: []dict.Item{
            {Value: "1", Label: "低", LabelI18n: map[string]string{"en-US": "Low"}, Color: "info"},
            {Value: "2", Label: "中", LabelI18n: map[string]string{"en-US": "Medium"}, Color: "primary"},
        },
    }}
}
```

---

## 5. 认证

### 5.1 令牌模型

| | Access Token | Refresh Token |
|---|---|---|
| 形式 | JWT，HS256 | 不透明随机串 `<sid>.<family>.<secret>`：secret 为 32 字节随机数、每次刷新都换；family 是会话的家族密钥（32 字节随机数），登录时生成、不轮换（D-104） |
| 有效期 | 15 分钟 | 7 天，滑动续期但不超过会话创建后 30 天 |
| 前端存放 | **仅内存**（Pinia），不进 localStorage | **HttpOnly Cookie**，JS 读不到 |
| 服务端存放 | 不存 | `ga_session` 里存 secret 的 SHA-256 |

JWT claims：

```json
{ "iss": "goalladmin", "aud": "platform", "sub": "10001",
  "sid": "9f2c…", "iat": 1790000000, "exp": 1790000900 }
```

- **角色和权限不进 JWT。** 每次请求由服务端按 `sub` 在授权快照里查角色（§6.3），分配、撤销、停用角色提交之后快照立即重新发布，所以改角色立即生效。
- 校验时必须固定算法（`jwt.WithValidMethods([]string{"HS256"})`），并校验 `iss`、`aud`、`exp`。`aud` 必须等于当前路由所属的端，否则 401。

### 5.2 Refresh Cookie

- release 模式：名称 `__Host-ga_rt_<portal>`、`Secure`、`Path=/`、不带 `Domain`（D-058）——浏览器只接受本主机经 https 写的这个 Cookie，同一主域下的其他主机写不进同名 Cookie；debug 模式（本机 http 开发）：名称 `ga_rt_<portal>`、`Path=/api/<portal>/v1/auth`。都是 `HttpOnly`、`SameSite=Strict`。刷新时带了多个同名的刷新 Cookie 一律拒绝（401，不带"会话已结束"）并记安全事件 `refresh_cookie_dup`。
- 服务端只在登录和刷新成功时写这个 Cookie，**从不删除它**（D-049）：同一浏览器的标签页共用一个 Cookie，退出、失败的刷新这类响应迟到时，浏览器里的 Cookie 可能已经是别人新登录的，一条"删除 Cookie"的迟到响应会把那个登录清掉。吊销过的会话的 Cookie 留着没有用处：下次刷新回 401，下次登录会换掉它。
- 前后端必须同源部署：生产用 nginx 把 `/api` 反代到后端，开发用 Vite proxy。因此**不需要也禁止**开启跨域携带凭证的 CORS。
- 登录、刷新和登出接口额外要求请求头 `X-GA-Client: web`，并校验 `Origin` 在白名单内，防跨站请求；登录还只收 `application/json`（D-051）。登录也要：登录成功会写刷新 Cookie，跨站页面顶层提交的表单拿到的响应里，`SameSite=Strict` 的 Cookie 照样会被写下，受害者的浏览器就登进了攻击者指定的账号。

### 5.3 每次请求的校验顺序

1. 验 JWT 签名、算法、`iss`、`aud`、`exp`。
2. 按 `sid` 查会话状态：不存在、不属于当前端、已吊销或过期则 401。
3. 按 `sub` 查账号状态：停用则 401。
4. 会话处于锁屏状态（`locked_at` 不为空，D-027）时，除 `/auth/me`、`/auth/unlock`、`/auth/logout` 外一律 423，错误码 1006。
5. `must_change_pwd` 为真时，除 `/auth/*` 外一律 403，错误码 `PWD_CHANGE_REQUIRED`。
6. 组装 `Principal` 放进 ctx。

第 2、3 步走内存 TTL 缓存，TTL 15 秒；每分钟回写一次"最后活动时间"时只换缓存里的值，不延长这 15 秒（D-097）。发生吊销、停用时，提交后立即清除本实例对应缓存。配了 Redis 时，同时发失效通知，别的实例收到后清缓存（D-075）；通知丢失或 Redis 不可用时仍靠 15 秒过期兜底。跨实例不保证同时生效，写操作和 `LiveAuth` 路由仍按库核对。

缓存在各个程序自己的内存里：别的程序改了库（平台程序停用主体、更换主账号、重置主账号密码、吊销主体的会话），这个程序的缓存最长 15 秒后才跟上；写操作都在锁内按库重新认定，不受影响，读有这段窗口。标了"按库核对"的路由分组（`PortalRouter.LiveAuth()`，子分组继承，公开路由不算）没有这段窗口：它们的第 2、3 步不用缓存、每个请求读库，读到的状态和缓存不一样时顺手把缓存清掉（D-073）。主体端后台套件的全部路由、代理商端的名下商户都标了；平台端、`/auth/*`、业务模块的路由默认不标——主体端的业务模块里只有主账号能看的、敏感的读，建议放进标了的分组。

### 5.4 刷新：轮换 + 重用检测

`ga_session` 每行是一次登录会话，保存当前 refresh 的哈希 `refresh_hash`、上一个的哈希 `prev_refresh_hash`，以及家族密钥的哈希 `family_hash`（D-104）。家族密钥不随轮换变化、只在 Cookie 里：它对得上，说明出示的人持有过这个会话的凭证。

| 客户端带来的 secret | 处理 |
|---|---|
| 等于当前值，家族密钥也对 | 正常轮换：生成新 secret，旧值移到 `prev_refresh_hash`，下发新 Cookie（家族密钥不变）和新 Access Token |
| 等于上一个值，且距上次轮换不超过 10 秒 | 判定为多标签页并发，返回 409 `REFRESH_RETRY`。客户端隔一小段时间重试（几次都在宽限期内），此时浏览器已带上新 Cookie |
| 等于上一个值但超过 10 秒 | 判定为令牌被盗用（拿它的人确实持有过有效凭证）：吊销整个会话，返回 401，记安全事件 `refresh_reuse` |
| 两者都不匹配，但家族密钥对得上 | 这份凭证被轮换掉不止一次了，持有人和别人各刷各的：吊销整个会话，返回 401，记安全事件 `refresh_reuse`（D-104） |
| 其余：secret 两者都不匹配、家族密钥也对不上（或 secret 是当前值而家族密钥不对） | 只拒绝（401），**不吊销**，记安全事件 `refresh_mismatch`（D-049）；不查账号、不判 IP 白名单（D-097）。会话号出现在会话列表、登录日志、操作日志里，只知道会话号的人也能发这样的请求，它证明不了合法持有人的凭证泄露；让会话下线只属于 `system:session:revoke` |

迁移 00019 之前建的会话没有家族密钥，客户端手里是两段的 `<sid>.<secret>`：照常能刷新，按 secret 的两代规则判定；第一次成功刷新时生成家族密钥并换发三段的 Cookie（D-104）。

以下事件吊销该用户的全部会话：修改密码、重置密码、账号被停用。登出只吊销当前会话。

同一浏览器的标签页共用一个刷新 Cookie，而每个标签页的登录身份只在自己内存里。刷新时前端用 `X-GA-Session` 声明要续的会话（登录、刷新的出参带 `sessionId`）；Cookie 不是这个会话（另一个标签页退出、换了人登录）时回 401，不轮换、不改 Cookie，这个标签页按登录失效处理，而不是悄悄变成另一个人（D-048）。页面刚打开、还不知道自己是谁时不带这个头，按 Cookie 恢复。登出不动 Cookie（§5.2）。

会话号只认 32 位小写十六进制（D-097）：别的写法在按会话号读、锁、吊销时一律当作不存在。会话的查询、刷新、吊销都以（端，`sid`）为键：`sid` 是全局随机的，但一个端的刷新凭证换不出别的端的令牌，一个端的管理员也下线不了别的端的会话——别的端的 `sid` 对本端等于不存在（404）。

### 5.5 前端配合（`@ga/shell` 内实现，业务页面无感）

- 收到 401 时发起刷新。**同一时刻只允许一个刷新请求**，其他请求排队等它的结果再重放。同源的标签页之间用 Web Locks 把刷新串起来（拿不到锁或等锁超时时直接刷新）。
- 收到 409 `REFRESH_RETRY` 时隔一小段时间再试（默认 200、500、1000、2000 毫秒，都在服务端的宽限期内）：别的标签页换来的新 Cookie 还没到达浏览器时才会拿到 409。间隔用完仍是 409 只让这次请求失败，不清登录状态、不算登录失效——会话没有失效，下一次请求再刷新（D-049）。刷新被服务端拒绝（401）才是登录失效。
- 页面刷新后内存里的 Access Token 丢失：启动时先调一次 `/auth/refresh`，成功再调 `/auth/me`，失败跳登录页。这个标签页登录的会话号另存一份在 `sessionStorage`（按标签页、重载后还在），启动刷新时带上：别的标签页换了人登录之后再重载这一页，服务端回 `auth.sessionSwitched`，登录页说明"这个浏览器已登录了另一个账号"，而不是悄悄恢复成那个人；退出、登录失效（服务端 401）或提示过换了账号时删掉这份记录，再重载就按 Cookie 恢复（D-050）；启动时断网、超时、服务端临时出错不删，下次重载照样带上（D-051）。
- 只有服务端明确拒绝刷新（401）才是登录失效：刷新遇到断网、超时、5xx 时登录状态留着，只让这次请求失败（D-050）。等锁超时才在锁外直接刷新；拿到锁以后的刷新失败不在锁外再刷一次——那次刷新可能已经在服务端轮换了凭证、只是响应丢了，再带旧 Cookie 刷一次过了宽限期会被当成重放、吊销整个会话。
- 退出没得到服务端确认时记下的标记绑定那次要退出的会话号（D-049）：下次启动先刷新，换来的正是这个会话才补做退出、不恢复；换来的是别的会话（同一浏览器里另一个人登录了）或刷新因 Cookie 不能用被拒，标记作废、照常恢复——这个浏览器已经没有那个会话的凭据，补不了也不需要补；不绑会话的标记会把新登录的人退掉。标记是一组会话号（几个标签页可能各有一次没确认的退出），每条只由它自己的结果删除：迟到的"退出成功"只删自己那条，登录只作废登录前就在的那些，刷新因为 Cookie 属于别的会话被拒时一条也不删（D-051）。
- 请求和刷新发出时记下登录身份的代数（登录、退出、登录失效时变），结果回来时代数已经变了，就说明这期间换了人：成功或失败的结果一律作废（前端错误码 `Superseded`，-2），不刷新、不重放、不写令牌、不触发锁屏或改密跳转、不提示（D-046、D-047）。启动时的恢复在途时用户已经登录了的，恢复失败也不清掉这次登录；退出在途时有人重新登录了的，迟到的退出也不清掉新登录。登录身份一变（退出、登录失效、换人登录），标签栏整个清空（D-048）。
- 身份在请求和刷新**发起**的那一刻就绑定（D-052）：请求进入请求层时取下当时的代数和访问令牌，发送时用这个令牌、不再读"现在的"令牌，真正交给网络之前再核对一次代数，变了就不发出去（写操作一旦发出服务端就执行了，响应回来时再作废已经晚了）；401 后的重放沿用第一次的代数。刷新在发起时记下代数和会话号（不是等到拿到跨标签页的锁之后），拿到锁、每次重试发出之前、写回令牌之前都核对，变了就作废——等锁期间这一页退出了，不能再按浏览器里还有效的 Cookie 把令牌写回来。
- 退出回 401、随后的刷新也失败时，只有刷新带 `auth.sessionEnded`（服务端确定会话已经不存在、已吊销或已过期）才算服务端确认；Cookie 属于别的会话、凭证对不上、没有 Cookie 这些 401 只说明刷新做不成，会话可能还活着，不算确认、待补退出的记录留着（D-052）。

### 5.6 密码

- 新哈希的算法在部署时选一种：配置 `server.passwordHash`，`argon2id`（默认）、`bcrypt`、`pbkdf2-sha256` 或 `pbkdf2-sha512`，别的值拒绝启动（D-070、D-072）。Argon2id：内存 19 MiB、2 轮、并行度 1，盐 16 字节、输出 32 字节，存成 `$argon2id$v=19$m=19456,t=2,p=1$<盐>$<哈希>`；bcrypt：cost 12；PBKDF2：HMAC-SHA256 迭代 60 万次或 HMAC-SHA512 迭代 22 万次，盐 16 字节、输出 32 字节，存成 `$pbkdf2-sha256$i=600000,l=32$<盐>$<哈希>`（SHA-512 的开头是 `$pbkdf2-sha512$`）。每种的参数都是固定的，不提供配置。明文超过 72 字节直接拒绝，选哪种都一样。
- 核对时按哈希开头认算法，不看配置：Argon2id、bcrypt（`$2a$`、`$2b$`、`$2y$`）、PBKDF2 的两种，哪种的哈希都核对。所以换配置不会让已有的密码失效。登录成功时存的哈希不是"选的算法 + 当前参数"的，就用这次输入的密码重算一份，在建会话的事务里换上——只换哈希这一列，"必须改密"、改密时间都不动，会话不受影响；按"哈希还是刚才核对的那个"才写，不会盖掉同时发生的改密、重置。用户来源实现可选接口 `portal.PasswordRehasher` 才升级，框架自带的两种都实现了。
- 库里读出来的哈希先看参数再算：Argon2id 的内存不超过 64 MiB、轮数不超过 8、并行度不超过 4，盐、输出的长度在合理范围内，数字和 base64 都是规范写法（不带换行）；bcrypt 的 cost 不超过 14；PBKDF2 的迭代次数 1～200 万（SHA-512 是 1～100 万）、盐 16～64 字节、输出 16 字节到摘要的长度而且和 `l` 一致，写法的规则同 Argon2id，别的摘要算法（包括 SHA-1）不认。超出的、看不懂的按"没有可用的密码"处理（做一次假的比较，返回不匹配）。上限以内、参数和自己生成的不一样的（别的系统导入的）照常核对，登录后同样升级。PBKDF2 的写法是本项目定的，别的系统的 PBKDF2 哈希各有各的写法，导入要先转成这种写法（D-072）。
- 升级让哈希变了而密码没变：同时在途的登录、解锁、改密在事务里发现哈希从"待升级的"变成了"当前参数的"时，拿这次输入的密码按新哈希再核对一次，核对得上照常继续；别的哈希变化照旧按密码已换处理（D-070）。
- 用默认的 Argon2id 时不能降级、不要新旧版本混跑：旧版本不认识 Argon2id 的哈希。要新旧并存或留回退余地的，先把 `server.passwordHash` 设成 `bcrypt` 再升级（D-070）。
- PBKDF2 的两种是给有 FIPS 140 要求的部署用的（D-072）：它们是批准的算法，只用标准库实现，按 Go 的 FIPS 模式构建（`GOFIPS140` 选定密码模块的版本，通过了 FIPS 140-3 认证的那个版本在这里指定）、运行（`GODEBUG=fips140=on`）时走的是 Go 自带的密码模块；Argon2id、bcrypt 不在批准之列。仓库里的 Makefile、镜像不带这两个设置，要的人自己构建。后端别处的密码学（随机数、SHA-256、HMAC-SHA256 的令牌签名）也都在标准库里。框架提供的是这个选项，**不是认证**：部署合不合规还取决于用哪个版本的模块构建、运行环境、反向代理和数据库（数据库账号用 `caching_sha2_password`、连接走 TLS：MySQL 驱动在别的情况下会用到 SHA-1）。框架不检查"FIPS 模式下选的是不是 PBKDF2"；选了 PBKDF2 之后库里留着的 Argon2id、bcrypt 哈希仍然核对一次、登录后换掉——要严格的，新装时就选 PBKDF2，或者换过来之后让这些账号重置密码。没有这类要求的部署用默认的 Argon2id：PBKDF2 一次约 0.1 秒 CPU，抗并行猜密码的能力也不如它。
- 始终要求：至少包含字母和数字，不得等于用户名，不得与上一次相同。
- 按端可配（`portals.<code>.password`，D-024）：最短长度（默认 10，范围 8–64）；可选要求大写字母、小写字母、符号；可选密码有效期（默认不过期，1–3650 天；代码注册端时声明的也必须是整天，D-034）。密码过期后登录照常，但与强制改密一样，除 `/auth/*` 外一律 403 `PWD_CHANGE_REQUIRED`，直到改密为止。
- 系统生成的密码（首个管理员、建用户不给密码、重置密码）至少 20 位且大写、小写、数字、符号齐全，满足任何合法策略。
- `GET /auth/me` 下发当前端的密码策略（`pwdPolicy`），改密页据此提示和预校验；安全以后端校验为准。
- 新建账号和管理员重置密码后 `must_change_pwd = 1`。
- 密码及其哈希永不进日志、操作记录、接口出参。

### 5.7 登录防护

- 主体端（D-061）：主体编号归一化为去首尾空白、转大写（`portal.NormalizeOrgCode`），登录防护里账号这一维的键是"编号/账号名"——商户 A 的 `admin` 被锁不影响商户 B 的 `admin`。编号不存在、主体停用和账号不存在走同一条路（假的密码比较、同样的错误码、同样计入失败），登录接口不能用来枚举主体编号。
- 登录名先归一化（去首尾空白、转小写，`portal.NormalizeUsername`）再做一切：查账号、限流、失败计数、锁定、登录日志都用归一化后的值。用户表的排序规则不区分大小写，如果按原始输入计数，`admin`、`Admin`、`ADMIN` 就各有一份配额。创建账号（接口和 `admin create` 命令）用同一条规则，并且只接受小写。
- 登录失败统一返回"账号或密码错误"。账号不存在时也对一个假哈希做一次同样参数的计算，抹平耗时差异。
- 限流：同 IP 每分钟 20 次登录请求，超出回 429。同账号（所有来源合计）每分钟 10 次，超出不拒绝，改为这次登录必须带图形验证码；没通过验证码的请求不占账号的次数（D-103）。
- 同一"账号 + IP"15 分钟内失败 3 次后，必须带图形验证码。
- 同一"账号 + IP"15 分钟内失败 10 次，锁定该组合 15 分钟。
- 同一账号 15 分钟内来自所有 IP 的失败累计 50 次，锁定账号 15 分钟并记安全事件。阈值定得高，是为了避免别人用错误密码把正常用户锁在外面。
- 未配置 Redis 时，这些计数在进程内存里；配置后同一部署、同一端的实例共用计数（D-076，§11.3）。每端登录防护的限流键与失败记录合计最多 30 万：一次登录或解锁可能用到的记录在核对密码之前就预留，放不下就按限流回 429；记失败、加锁不再新建记录；已有的失败次数和锁定一个都不逐出（D-056、D-057）。
- 客户端 IP 取法见 §12.2。
- 每次核对过密码的登录尝试都写 `ga_login_log`，含失败原因；密码核对通过但会话没建成也写（原因 `error`）。被限流、被锁定的请求没有核对密码，只记安全事件（同一来源一分钟合并成一行、次数不丢），不逐条写登录日志（D-058）。
- 按 IP 的限流和"账号 + IP"的计数：IPv6 按 /64 网段算（D-058）。密码计算（核对、生成哈希）的并发有上限：一个进程一组位置，个数是 `server.passwordParallel`（0 表示按 CPU 数 `max(8, 4×核数)`）；用 Argon2id 时每个在算的请求占 19 MiB，进程的内存峰值大约是"位置数 × 40 MB"（D-058、D-068、D-070）。
- 位置分两类用（D-071）：登录、解锁可以用全部位置，满了最多等 2 秒（同时在等的最多是位置数的 4 倍），等到了照常做，等不到回 429、不算一次失败；本人改密、后台生成密码哈希（建账号、重置密码、主体的初始密码）最多占一半位置，满了直接回 429、不等。位置只在计算的时候占着，进事务等行锁之前让出来。
- 按主体、按账号的限制（D-068、D-071）：主体端本人改密、解锁每个主体同时各最多 2 个；套件的建子账号、重置员工密码每个主体同时最多 2 个、每分钟最多 30 次；同一个账号 15 分钟内最多成功改密 5 次（没改成的不占，被要求改密的那一次不挡也不计），超出回 429、记安全事件 `pwd_change_throttled`。按主体的上限是固定的数：位置数配得比 8 小时，一个主体可能占满全部位置，建议不小于 8。建账号、重置密码的接口先做便宜的检查、再算哈希，哈希在拿锁之前算。
- 密码超过 72 字节、账号没有密码时，也做一次和正常比较耗时相当的比较再返回失败，不能靠响应快慢试出账号在不在（D-058）。
- 校验密码在事务外（密码计算慢）；建会话的事务里先锁住账号行，确认密码和状态仍是刚才校验的那样，才建会话（D-047）。这之间管理员重置了密码或停用了账号的，这次登录按密码错误或账号停用失败：重置、停用吊销的是当时已有的会话，不这样做就会漏掉这次新建的。锁账号行靠 `UserProvider` 可选实现的 `portal.UserLocker`；自定义用户来源没实现时退回普通读，这一条就只是尽力而为。
- 以上阈值是默认值，可以按端在 `portals.<code>.login` 里调整（D-024）；另有"每次登录都要验证码"选项。**没有关闭限流、锁定、强制改密的开关**，取值有底线：出验证码 ≤5 次、锁定 ≤20 次、账号锁定 ≤200 次、锁定时长和统计窗口 ≥1 分钟、每 IP ≤600 次/分钟、每账号 ≤120 次/分钟，越界拒绝启动。安全策略只能通过配置文件改，不提供网页编辑；后台"系统设置 → 安全设置"只读展示生效值、允许范围、来源和配置项（D-034）。

### 5.8 认证接口（每个端一套）

| 方法 | 路径（前缀 `/api/<portal>/v1`） | 守卫 | 说明 |
|---|---|---|---|
| GET | `/auth/captcha` | Public | 要求客户端头与来源检查（D-112），返回 `captchaId` 和图片 |
| POST | `/auth/login` | Public | 入参 `username`、`password`、`captchaId?`、`captchaCode?`，主体端另有必填的 `org`（主体编号，D-061）；出参 `accessToken`、`expiresIn`、`mustChangePwd` |
| POST | `/auth/refresh` | Public（靠 Cookie） | 出参同上；可带 `X-GA-Session`，Cookie 不是这个会话时 401 且不改 Cookie（D-048）；任何失败都不改 Cookie（D-049） |
| POST | `/auth/logout` | AuthOnly | 吊销当前会话；不动刷新 Cookie（D-049） |
| GET | `/auth/me` | AuthOnly | 用户信息、权限码数组、菜单树；锁屏时只有用户信息和 `locked: true` |
| PUT | `/auth/password` | AuthOnly | 入参 `oldPassword`、`newPassword`；会话已被吊销（例如管理员刚重置了密码）回 401（D-045）；验证旧密码之后密码已被换掉（例如同一会话的另一个改密请求先完成）按旧密码不对回 3001（D-047） |
| POST | `/auth/lock` | AuthOnly | 锁定当前会话（D-027） |
| POST | `/auth/unlock` | AuthOnly | 入参 `password`（当前账号的密码）；连续输错 5 次吊销会话 |

### 5.9 首个管理员

迁移脚本只建 `super` 角色，不建任何账号，不内置默认密码。首个管理员用命令行创建：

```bash
./server admin create --username admin
```

命令生成随机密码并只打印一次，账号 `must_change_pwd = 1`。

忘了密码的超管（以及任何账号）用命令行重置（D-035）：

```bash
./server admin reset-password -username admin
```

随机密码只打印一次，`must_change_pwd = 1`，吊销该账号全部会话，记一条 `cli` 安全事件。超管的密码在后台不能重置，这是唯一的办法。

---

## 6. 授权

### 6.1 权限码

固定三段：`<模块>:<资源>:<动作>`，全小写，段内用连字符。例：`system:user:create`、`system:role:grant`。

模块在 `Perms()` 里声明：

```go
rbac.Perm{
    Code:      "system:role:grant",
    Name:      "perm.system.role.grant", // i18n 键，前端翻译
    Portal:    "platform",
    Group:     "system.role",            // 授权界面上的分组（i18n 键 permGroup.system.role）
    Sensitive: true,                     // 见 6.5
}
```

数据库里出现注册表里没有的权限码时：启动打警告，判定时忽略，可用 `./server rbac prune` 清理。新增的权限码**不会**自动授予任何角色。

### 6.2 路由守卫

路由注册 API 把守卫做成必填参数，漏写无法编译：

```go
g := r.Portal("platform").Group("/system")
g.GET("/users",  rbac.Require("system:user:list"),   h.ListUsers)
g.POST("/users", rbac.Require("system:user:create"), h.CreateUser, oplog.Record("user.create"))
g.GET("/options/users", rbac.AuthOnly(), h.UserOptions)
```

| 守卫 | 含义 |
|---|---|
| `rbac.Public()` | 不需要登录。必须出现在 `docs/api.md` 的公开接口清单里 |
| `rbac.AuthOnly()` | 登录即可。只用于"个人中心""下拉选项"这类对所有登录用户开放的接口 |
| `rbac.Require(code)` | 需要权限码。默认应使用这一档 |
| `rbac.RequireSuper()` | 只有本端超管（D-035）。没有权限码，不能授给任何角色；只用于能接管别人账号的操作（重置他人密码） |

### 6.3 Casbin

Casbin 只回答一个问题："这个角色在这个端有没有这个权限码"。

```ini
[request_definition]
r = sub, ptl, obj

[policy_definition]
p = sub, ptl, obj

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = r.sub == p.sub && r.ptl == p.ptl && keyMatch(r.obj, p.obj)
```

- `sub` 是 `role:<角色ID>`。用角色 ID 而不是角色编码，角色改名不影响策略。
- `obj` 是权限码，支持尾部通配：`system:user:*`。
- **不使用** `g`。用户和角色的关系只存在 `ga_user_role`，判定时对用户的每个角色各 Enforce 一次。这样"谁有什么角色"只有一个数据源。
- "角色被授予了什么"只存在 `ga_casbin_rule`，没有额外的角色权限表和角色菜单表。
- 策略变更走 `rbac.Service`，先写库再更新内存，两步放在同一个互斥区内。
- 内存里的授权是一份整体发布的快照：谁有哪些角色、角色是否启用、角色的权限码，在一个只读快照事务里读（`db.Snapshot`），读好后一次换上，一次判定只取一份（D-051、D-053）——分开读的话，一次判定可能拿"撤销前的成员关系"配上"撤销之后才扩大的授权"，算出这个人从来没有过的权限。授权、分配角色、停用角色、删角色提交之后立即重新发布；快照超过 15 秒没重读时，下一次判定先重读（兜住命令行之类进程外的改动）。重载失败时（库里的授权已经变了、内存没跟上）判定先重试重载（最多每秒一次），仍失败就回 503，不拿可能更宽的旧快照判定。数据范围不走这份快照（D-054），见 §7.2。
- 本实例变更在提交并重载后生效。配了 Redis 时发送授权快照失效通知，别的实例在下一次判定时重载；通知丢失时仍靠 15 秒过期兜底（D-075）。

### 6.4 超级管理员

- 由角色上的 `is_super = 1` 标识，不看用户 ID，不看角色名。
- 超管跳过 `Require` 判定，但操作日志照常记录。
- 不能删除或停用最后一个启用状态的超管账号，不能修改 `super` 角色的 `is_super`。这条判断和随后的写入在同一个事务里，并先用 `SELECT ... FOR UPDATE` 锁住该端的超管角色行（`rbac.Service.WithSuperLock`）：两个超管并发互相停用或降级时，后拿到锁的看到的是前一个已提交的结果，不会双双通过。
- 修改密码、重置密码时，写新密码和吊销会话在同一个事务里；任何一步失败整体回滚，不会出现"密码已换、旧会话还活着"或"密码已换、没人知道新密码"的中间状态。
- 只有超管能把 `super` 角色分配给别人。
- 超管可以跳过检查的写操作，在超管锁里先按库重新认定操作人（`rbac.Service.CurrentActor`，D-045、D-046），不沿用请求认证时的结论：超管角色在认证之后被收回，在途的请求不能再按超管行事；账号已被停用、会话已被吊销的，在途请求整个作废（401）。
- 后台的每一条写接口（需要权限码、超管或只需登录的路由，认证接口除外）都通过 `rbac.Service.WithActor` 执行（`rbac` 内部的角色写操作在同一把锁里调用同一套认定，D-047）：拿超管锁、重新认定操作人，再把认定后的操作人交给业务代码，锁内需要的权限判断用 `AllowedLocked`、数据范围用 `DataFilterLocked`；写完的回显按锁内认定后的身份算查看范围。只改调用者本人数据的写操作（个人资料、头像、下线本人的其他设备）用 `rbac.Service.WithSelf`：同样在事务里确认账号启用、会话有效（否则 401），但只锁本人的账号行和会话行（D-059：吊销写的是会话行，两边排队）、不拿全端共用的超管锁——这些接口任何登录用户都能调，不能让一个普通账号把全端的管理写操作排在后面（D-058）。路由守卫（`Require`、`RequireSuper`）的要求在锁内按已提交的授权再核一遍：请求通过守卫之后、拿到锁之前权限被收回的，回 403、什么都不写（D-048）。"拿锁"和"重新认定"是一个函数，不能只做一半；§13.2 第 72 条按路由表逐条核对。
- **非超管不能对超管账号做任何写操作**（D-035）：改资料（含部门、岗位）、启用或停用、分配角色、吊销其会话，一律 403；内置 `super` 角色本身非超管也不能改。是否超管直接读库，只要拥有超管角色就算，不论角色和账号是否启用；判断和写入在同一把超管锁里。
- **重置他人密码只有超管能做**（路由守卫 `RequireSuper`）；**超管的密码在后台谁都不能重置**（包括自己，自己改密走修改密码），只能用 `server admin reset-password`（§5.9）。
- **主体端（D-063）**：没有超管角色，主体的主账号（`owner_user_id`）就是主体内的超管，上面各条里的"超管"都换成"本主体的主账号"：锁内按主体行重新认定，员工不能对主账号做写操作，主体内没有人能停用主账号（`HoldsSuperRole`、`IsLastSuper` 在主体端都指主账号）。"超管锁"换成主体行的排他锁（`portal.OrgLocker`）：同一主体的写操作串行，别的主体不受影响；这些事务用 READ COMMITTED、必须新开（在别的事务里调用直接报错，多步写操作放进 `WithActor`），共用的角色、授权表上没有间隙锁，不同主体不会互相等待。锁内的认定只能在 `WithActor` 里调用，不自行补拿主体锁。加锁顺序是"主体行 → 账号行 → 会话行"，登录建会话的事务也先锁主体行。

- **会话下线与管理事务（D-089）**：三个内置端在管理锁内先锁操作人账号行、再锁操作人会话行，直到业务事务提交或回滚。下线先完成，管理请求回 401；管理请求先获准，下线等待它结束，成功返回后该会话不再有已获准的管理写事务待提交。响应到达客户端的顺序不作为提交依据。自定义用户来源需要实现 `portal.UserLocker` 并装配 `rbac.Options.LockSession` 才能提供同样的串行化保证。

- 锁内辅助接口 `CurrentActor`、`AllowedLocked`、`DataFilterLocked`、`CheckEnableUser`、`IsLastSuper` 要求已有事务；事务外调用返回 `ErrNoTx`，不访问数据库。事务检查不代替调用方使用上述管理锁入口（D-122）。
- 相对部门范围按持有人实际部门展开：角色新增权限、扩大范围、启停时检查受影响的现有持有人；启用账号检查其全部角色（含停用角色）。移除授权的原角色管理资格也核对实际集合，账号角色移除只影响该目标账号。实际集合不得超过操作人在相同权限码下的部门集合；原样保留、未分配角色和超管管理沿用既有语义（D-120）。

### 6.5 敏感权限

标了 `Sensitive: true` 的权限码（角色授权、创建用户、分配角色等）只能由超管授予给角色。重置他人密码不是权限码，只有超管能做（§6.4，D-035）。非超管即使拥有 `system:role:grant`，也不能把敏感权限码授给任何角色，也不能授出自己没有的权限码。

这条规则堵的是"低权限账号给自己加权限"这条提权链，必须有对应的反向测试。

分配角色、启用或停用角色同样受这条约束（角色不能带敏感权限码、自己没有的权限码、比自己宽的数据范围；停用也算，D-100）。重新启用账号时，账号身上的角色不分启用还是停用都按这条检查。被拒时是角色里的哪个权限码、哪个范围挡住了，只回给能看角色的操作人——拥有声明了 `RoleView: true` 的权限码（平台端 `system:role:list`、主体端 `<端>:role:list`）；别人只得到 `rbac.role.notAssignable` 和角色 ID，完整的原因写进服务端日志（D-069）。有分配权限、没有查看权限的人不能靠试错把角色的内容套出来。

### 6.6 菜单与按钮

模块在 `Menus()` 里声明菜单节点：

```go
rbac.MenuNode{
    Portal: "platform", Parent: "system", Name: "system-user",
    Path: "/system/users", Component: "system/user/index",
    TitleKey: "menu.system.user", Icon: "User",
    Perm: "system:user:list", Sort: 10, KeepAlive: true,
}
```

- `GET /auth/me` 返回按当前用户权限过滤后的菜单树。节点没有 `Perm` 则对该端所有登录用户可见；目录节点在任一子节点可见时可见。
- `TitleKey` 是 i18n 键，由前端翻译，数据库和接口里不出现菜单标题文本。
- `Component` 是相对 `apps/<端>/views/` 的路径。前端用 `import.meta.glob` 得到的映射表解析，映射表里没有的键一律渲染 404，禁止拼接路径动态 import。
- 按钮权限就是权限码。前端用 `v-perm="'system:user:create'"` 或 `hasPerm()`。前端的判断只影响显示，安全以后端守卫为准。
- 菜单名小写字母开头，只含小写字母、数字和连字符，最长 64 位（`rbac.ValidMenuName`）。

**菜单管理（D-025）：结构归代码，后台只改显示和位置。**

- 后台能改：显示名（按语言，留空沿用 `TitleKey` 的翻译）、图标、同级排序、上级（挪到别的目录或分组下）、在侧边栏隐藏。不能改：路径、页面组件、权限码、所属端；代码里标了 `Hidden` 的菜单不能取消隐藏。接口严格解析请求体，夹带白名单之外的字段直接回 3002。
- 后台可以建分组：纯目录，没有路径、页面和权限码，名字由服务端生成（`@g-` 开头，不会和代码菜单重名）。删除分组时，里面的代码菜单回到代码声明的位置，里面的分组挪到被删分组的上级。不能在后台新建页面菜单，也没有外链菜单。
- 可见性不受影响：一个菜单要求的权限码 = 它自己的权限码 + 代码里所有祖先的权限码，只由代码决定，挪到哪里都不变；页面满足即可见，目录和分组还要有可见的子项，没有可见子项的分组和目录不下发。为此带权限码的目录（及它的代码子孙目录）只能放它在代码里本来的子项，分组也不能放进这样的目录，否则挪进去的菜单会被多要求一个权限码。改名、换图标、挪位置、隐藏都不会让任何人多看到一个菜单或多进一个页面；路由按路径注册，挪位置不改变地址。
- 拖动后一次提交该端全部节点的位置，服务端在一个事务里整体校验：节点集合与当前一致（否则 4001，请刷新）、上级是本端的目录或分组、不成环、不超过 4 层（代码声明得更深时以代码为准）。任何一项不合格都不写。恢复默认、删除分组、新建分组同样先算出结果再做这套校验。
- 调整只存与代码不同的部分（`ga_menu_custom`）；代码删掉或改动了菜单时，失效的调整被忽略，菜单回到代码位置，不影响启动和使用。
- 查看需要 `system:menu:list`；修改需要 `system:menu:update`（敏感，只有超管能授出），只能管理操作者所在端的菜单，全部写操作记操作日志。
- `GET /auth/me` 的菜单节点多一个 `titles`（按语言的显示名），前端优先用它，缺当前语言时翻译 `titleKey`；分组节点的 `path` 为空。

### 6.7 端边界

角色属于端（`ga_role.portal`），角色 ID 却是全局自增的。所以：

- `rbac.Service` 里按 ID 读、改、删角色和读、改角色授权的方法都带 `portal` 参数，存储层的条件是 `portal = ? AND id = ?`；别的端的角色对本端来说就是不存在（404），分配给本端用户时按校验失败处理。
- 这些方法还核对 `actor.Portal == portal`：操作者的身份来自哪个端，就只能以哪个端的名义操作角色。端内路由传进来的端本来就等于操作者的端，这条是最后一道防线，挡的是模块代码把端传错。
- 会话同理（§5.4）。
- 主体端再多一层主体（D-063）：存储层的条件是 `portal = ? AND org_id = ? AND id = ?`。主体只从身份来——带操作人的方法取 `actor.OrgID`，只读方法取 ctx 里的身份（没有主体端身份时报 `scope.ErrNoOrg`）；别的主体的角色对本主体就是不存在（404），分配时按校验失败处理，给别的主体的账号分配角色回 404。判定只认主体与身份一致的角色，库里被直接写进的跨主体成员关系不产生权限。

v0.1 只启用一个端，这些边界看不出效果；它们是启用第二个端之前必须已经存在的东西，并且有反向测试（§13.2 第 22–24 条）。

---

## 7. 数据隔离

§7.2 按部门的数据权限 v0.1 已实现（D-039）；§7.1 主体隔离 v0.2 起实现（D-061）：`Portal.Scoped`、`Account.OrgID`、`Principal.OrgID`，`ga_session` 等表的 `org_id` 列，`core/scope` 和 `scopetest`。

### 7.1 主体隔离

`Scoped = true` 的端里，每个用户属于一个主体（商户、代理商等），`Principal.OrgID` 就是主体 ID。规则：

1. 主体 ID **只能**来自 `Principal`。这类端的入参 DTO 里禁止出现主体 ID 字段，出现即视为缺陷。
2. 所有 repo 方法都要带隔离条件，包括按 ID 读取、更新、删除：`WHERE id = ? AND merchant_id = ?`。
3. 越界访问返回 404，不返回 403，避免暴露"这个 ID 存在"。
4. 失败即拒绝：Scoped 端里 `Principal` 缺失或 `OrgID == 0` 时，`scope.MustOrg` 直接报错终止请求，绝不退化成"不加条件"。

```go
func (r *OrderRepo) Get(ctx context.Context, id uint64) (*Order, error) {
    var o Order
    err := db.From(ctx).
        Scopes(scope.ByOrg(ctx, "merchant_id")).
        Where("id = ?", id).First(&o).Error
    return &o, err
}
```

`scope.ByOrg` 在非 Scoped 端被调用时报错。平台端要查全量数据时用不带隔离的独立 repo 方法，不要复用 Scoped 端的 repo。

授权同样按主体隔离（D-063，§6.4、§6.7）：角色属于主体（`ga_role.org_id`），主账号是主体内的超管，写操作在主体行的排他锁里串行。Scoped 端的用户来源必须实现 `portal.OrgLocker`（排他锁），注册时检查。主体端不提供部门，部门类的数据范围按仅本人算，"全部"是整个本主体；主体端的身份不能改菜单调整（它对全端生效）。

每个 Scoped 资源必须有隔离测试。`core/scope/scopetest` 提供模板：造两个主体各一条数据，用主体 A 的令牌对主体 B 的数据逐一尝试列表、详情、更新、删除，断言全部不可见或 404。

主体自助入驻见 D-080：代理商/商户可分别开启公开申请，默认关闭，必须经平台审核。申请与邀请放在 `ga_org_application`、`ga_org_invitation`，仍只由平台迁移；审核通过的同一事务才创建主体与主账号。邀请凭证只存摘要，固定 7 天、仅可提交一次，商户归属只能来自有效邀请；无邀请时直属平台。审核使用平台敏感权限和 `WithActor`，代理商邀请管理仅限本主体主账号。联系人须由平台核实，初始密码仅向审核人返回一次，首次登录强制改密。

主体端数据中心与安全设置（D-090）：数据中心位于菜单首位，以独立的 `<端>:dashboard:view` 权限访问；统计仅限当前端和主体，活跃账号排名在没有账号查看权限时仅列本人。安全设置下的 IP 白名单和 IP 黑名单仅主账号可见、可改；菜单 `SuperOnly` 条件继承代码祖先，接口另行核验。主体黑名单使用 `ga_ip_rule` 的 `deny/org` 范围，先于主体白名单检查，作用于登录、刷新及已认证请求，不影响其他主体或平台；匿名验证码和入驻仍使用全局规则。主体 ID 从身份取得，单主体生效黑名单最多 100 条、含过期项最多保留 1000 条，满后须删除旧记录才能新增（D-092）；全局黑名单生效中最多 10000 条，两类各自计数、互不占用（D-102）。平台在代理商管理、商户管理里可以查看和整体清空一个主体自己设的黑名单（D-102）。支持 IP/CIDR（D-091 另支持连续后缀的 IPv4 通配）、永久或最多一年有效期、续期和删除，不能封禁操作人当前地址。

### 7.2 按部门的数据权限（D-039）

权限码管"能不能做"，数据范围管"能对哪些数据做"，范围只收窄、不放宽。

- **范围**：仅本人（`self`）< 本部门（`dept`）< 本部门及下级（`dept_tree`）< 全部（`all`）。每种都包含本人；没分配部门的人，部门类范围按仅本人算。
- **数据资源**由模块在代码里声明（`app.DataResourceSource` 返回 `[]rbac.DataResource`）：编码 `<模块>:<资源>`、显示名、受约束的权限码（只能是本模块的）、默认范围、可选范围。一个权限码最多属于一个资源。
- **角色 × 资源**存范围（`ga_role_data_scope`），没存的取默认值。判断某个权限码的范围时，只看拥有这个权限码的启用角色，取最宽的；超管永远是全部。
- **授权**：非超管给角色加权限码或放宽范围时，角色在该资源上的范围不能宽于操作人在同一权限码上的范围；分配角色时同样检查。收窄须具备原角色的管理资格（D-117）。
- **强制**：`rbac.Service.DataFilter` 给出过滤条件（`Apply` 加到查询上、`Allows` 判断单条）。列表、详情、下拉按范围过滤；写操作的目标不在范围内回 404；新建和改部门时新部门必须在范围内，非超管不能改自己的部门，挪到"未分配"要全部范围；非超管改部门上级要用户资源的每个权限码都是全部范围；部门负责人只能选"查看用户"范围内的人；写操作的回显只给"查看用户"范围内的人。非超管重新启用角色按分配角色检查；非超管重新启用停用的账号，按"把这个账号当前启用的角色分配给他"检查（`rbac.Service.CheckEnableUser`，D-058）；被拒时只回通用的 `rbac.user.enableNotAssignable`，不说是哪个角色、哪个权限码（D-059）。
- 数据范围的全部输入——操作人的角色、角色是否启用、权限码、范围、操作人所在的部门和下级部门——按请求所在的数据库视图读（D-054），不用内存里的授权快照。算过滤条件和读被过滤的数据放进同一个 `db.Snapshot`：返回的数据、判断它可不可见用的范围和部门，都是同一时刻的。分开读的话，可能拿新的范围配上旧的部门（或反过来），算出操作人在任何时刻都没有过的可见范围。
- 框架自带的数据资源是 `system:user`（默认仅本人；升级时现有非超管角色写成全部）。聚合数字和审计日志不按范围过滤。

---

## 8. 数据表

### 8.1 约定

- 框架表统一前缀 `ga_`。业务表用业务自己的前缀，**框架不对业务表的主键、时间列、软删方式做任何要求**。
- 主键 `bigint unsigned` 自增。JSON 里按数字输出。
- 时间列 `datetime(3)`，存 UTC。连接串带 `parseTime=true&loc=UTC`。接口输出 RFC 3339。
- 框架表**不用软删**。账号和角色用 `status` 停用；账号不允许删除；角色在没有用户引用时才允许物理删除。这样唯一索引不会被软删行占住。
- 实体表底部固定块及其顺序：`status, sort, remark, created_at, updated_at[, deleted_at], created_by, updated_by[, deleted_by]`，详见仓库 `docs/conventions.md`（公开文件，以它为准）。日志表只有 `created_at`，关系表没有时间列。
- 字符集 `utf8mb4`，排序规则 `utf8mb4_0900_ai_ci`。
- 表结构只通过 SQL 迁移文件变更（框架的在 `server/migrations/core/`，模块的在模块目录下的 `migrations/`）。禁止在代码里调用 `AutoMigrate`。

### 8.2 表清单

**ga_user**（平台端用户）

| 列 | 类型 | 说明 |
|---|---|---|
| id | bigint unsigned PK | |
| username | varchar(64) | 唯一 |
| password_hash | varchar(255) | 密码哈希：Argon2id（默认）、bcrypt 或 PBKDF2，按 `server.passwordHash`（D-070、D-072）；迁移 `00016` 从 100 加宽 |
| display_name | varchar(64) | |
| email / phone | varchar(128) / varchar(32) | |
| avatar | varchar(255) | 空（显示名字首字母）、`preset:<名字>`（内置头像）或 `upload:<键>`（本人上传，图在 `ga_user_avatar`），D-040 |
| bio | varchar(255) | 个人简介，本人在个人中心维护（D-038） |
| status | tinyint | 1 启用，0 停用 |
| must_change_pwd | tinyint | |
| pwd_changed_at | datetime(3) | |
| mfa_secret | varbinary(255) NULL | v0.2 预留，v0.1 不读写 |
| last_login_at / last_login_ip | datetime(3) / varchar(64) | |
| created_by / updated_by | bigint unsigned | |
| created_at / updated_at | datetime(3) | |

**ga_role**

| 列 | 类型 | 说明 |
|---|---|---|
| id | bigint unsigned PK | |
| portal | varchar(32) | 角色属于哪个端 |
| code | varchar(64) | 同端内唯一，创建后不可修改 |
| name | varchar(64) | |
| is_super | tinyint | |
| status | tinyint | |
| sort | int | 越小越靠前 |
| remark | varchar(255) | |
| created_by / updated_by / created_at / updated_at | | |

另有 `org_id`（主体端的角色属于某个主体，平台端为 0，D-061）。唯一键 `(portal, org_id, code)`。

**ga_user_role**：`portal varchar(32)`、`user_id bigint unsigned`、`role_id bigint unsigned`，主键 `(portal, user_id, role_id)`。`user_id` 指向该端 `UserProvider` 的用户 ID。

**ga_role_data_scope**（角色的数据范围，D-039）：`role_id bigint unsigned`、`resource varchar(64)`（数据资源编码）、`scope varchar(16)`（self / dept / dept_tree / all），主键 `(role_id, resource)`。没有行的按资源声明的默认值；超管角色不存。关系表，没有时间列。

**ga_user_avatar**（本人上传的头像，D-040）：`user_id bigint unsigned` 主键、`avatar_key char(32)`（随机，唯一；换头像或清除后旧键失效）、`image mediumblob`（256×256 JPEG）、`thumb blob`（64×64 JPEG）、`created_at`。图片都是服务器重新编码的，不存原文件。

**ga_casbin_rule**：`id`、`ptype`、`v0`–`v5`，与常见的 Casbin 数据库适配器表结构兼容；`ptype = "p"`，`v0 = role:<id>`，`v1` 端，`v2` 权限码。

**ga_session**

| 列 | 类型 | 说明 |
|---|---|---|
| id | bigint unsigned PK | |
| sid | char(32) | 唯一，随机十六进制，写进 JWT |
| portal / user_id | | |
| refresh_hash | char(64) | 当前 refresh secret 的 SHA-256 |
| prev_refresh_hash | char(64) NULL | 上一个 |
| family_hash | char(64) NULL | 家族密钥的 SHA-256；空表示迁移 00019 之前建的会话，下次成功刷新时补上（D-104） |
| rotated_at | datetime(3) | |
| expires_at | datetime(3) | |
| revoked_at | datetime(3) NULL | |
| revoke_reason | varchar(32) | logout、pwd_change、disabled、reuse_detected、admin、unlock_failed |
| locked_at | datetime(3) NULL | 锁屏时间，空表示没有锁定（D-027） |
| unlock_failures | int unsigned | 锁定后连续解锁失败次数 |
| ip / user_agent | varchar(64) / varchar(255) | |
| created_at / last_seen_at | datetime(3) | |

另有 `org_id`（主体端：会话所属的主体，建会话时写入，`Principal.OrgID` 只从这里来；平台端为 0，D-061）。索引 `(portal, user_id)`、`(portal, org_id)`，以及清理使用的 `(portal, expires_at)`、`(portal, revoked_at)`。过期或吊销已超过 30 × 24 小时的行由后台任务清理（D-081）：每个应用启动成功 1 分钟后开始，只处理已注册端；每端每轮按过期、吊销分别最多删除 500 行，共用 5 秒超时。无删除且无错误时隔 1 小时检查，有删除或错误时隔 1 分钟继续。时间边界相等保留；应用停止会取消并等待任务退出，日志不清理。

**ga_dept**（部门，D-033）：`parent_id`（0 为顶级）、`name`（同一上级下唯一）、`leader_user_id`、`phone`、`email`，加底部固定块，不软删。最多 10 层。

**ga_post**（岗位，D-033）：`code`（唯一，创建后不可改）、`name`，加底部固定块，不软删。

**ga_user_post**：`user_id`、`post_id`，主键 `(user_id, post_id)`。`ga_user.dept_id`（D-033）是用户所属部门，0 表示未分配。部门、岗位只是组织资料，不影响任何权限判断。

**ga_login_log**：`id`、`portal`、`org_id`、`org_code`（主体端：编号对应的主体和登录时输入的编号，失败时按输入的编号记，D-061）、`username`、`user_id`、`session_id`（登录成功时建立的会话 `sid`，D-032）、`success`、`reason`、`ip`、`user_agent`、`request_id`、`created_at`。索引另有 `(session_id, created_at)`、`(ip, created_at)`、`(user_id, created_at)`，供调查时间线使用。

**ga_operation_log**：见 §10。

**ga_error_log**（错误日志，D-032）：`fingerprint`（唯一）、`kind`（`panic` / `error`）、`portal`、`method`、`route`（路由模板）、`code`、`http_status`、`message`（去掉具体值，最长 1 KB）、`stack`（只有 panic，最长 8 KB）、`count`、`first_at`、`last_at`，以及最近一次的 `last_request_id`、`last_user_id`、`last_username`、`last_session_id`、`last_ip`。同一类错误只占一行，写入时累加次数、更新"最近一次"；不删除。

**ga_security_event**（安全事件，D-032）：`dedup_key` 与 `window_start`（所在分钟）联合唯一，`portal`、`org_id`（主体端，D-061）、`kind`、`level`（1 提示、2 警告、3 严重）、`user_id`、`username`、`session_id`、`ip`、`user_agent`、`method`、`path`、`detail`、`request_id`（第一次的）、`count`、`first_at`、`last_at`。同一来源同一分钟内只累加次数和最后时间，分钟过去后不再变化；不删除。

**ga_dict**、**ga_dict_item**（字典，§4.5）：字典有 `portal`（端代号或 `*`）、`code`（唯一）、`name`、`name_i18n`、`value_type`、`source`（`code` / `admin`）；项有 `dict_id`、`parent_id`、`value`（同一字典内唯一）、`label`、`label_i18n`、`color`、`extra`、`locked`（代码声明）、`overridden`（显示被后台改过）。两张表都带底部固定块，不软删。`*_i18n` 列存 JSON 对象，键是语言代码。`code`、`value` 两列用 `utf8mb4_bin`，按字节比较（D-023）。

**ga_ip_rule**（IP 黑名单与白名单，D-062）：`kind`（`deny` / `allow`）、`scope`（`global` / `portal` / `org` / `user`）、`portal`、`org_id`、`user_id`、`cidr`（规范化后的网段，单个地址存成 /32、/128）、`expires_at`（只有黑名单用，空为永久），加底部固定块（不软删）；`(kind, scope, portal, org_id, user_id, cidr)` 唯一。**ga_change_seq**：`name` 主键、`seq`、`updated_at`，跨程序缓存的变更序号（`ip_rules`），写名单时在同一事务里加一，也是写名单的串行化点。

**ga_agent**、**ga_merchant**（主体，D-065）：`code`（编号：代理商 `A`、商户 `M` 加 8 位随机数字，唯一，登录时输入，创建后不可改）、`name`、`contact_name`、`contact_phone`、`owner_user_id`（主账号，主体内的超管）、`status`（停用后它的账号都按停用处理），加底部固定块，不软删；商户另有 `agent_id`（所属代理商，0 表示直属平台，只能在平台端改）。`created_by`、`updated_by` 是平台端的用户 ID。**ga_agent_user**、**ga_merchant_user**（主体账号）：`org_id`（账号不能换主体）、`username`（主体内唯一，`(org_id, username)` 唯一键）、`password_hash`、`display_name`、`email`、`phone`、`avatar`、`must_change_pwd`、`pwd_changed_at`、`last_login_at`、`last_login_ip`，加底部固定块；`created_by`、`updated_by` 是本端的用户 ID，平台随主体一起建的主账号和平台改的为 0。四张表只由内核的 `core/org` 读写。

**ga_menu_custom**（菜单管理，§6.6）：只存"与代码不同的部分"。`portal`、`name`（代码菜单名或分组名，`utf8mb4_bin`，与 `portal` 联合唯一）、`kind`（`code` 对代码菜单的调整 / `group` 后台分组）、`titles`（JSON 对象，键是语言代码）、`icon`（空表示沿用代码）、`hidden`（只能在代码之外再隐藏）、`moved`（位置已调整，此时 `parent`、`sort` 生效；分组总是 1）、`parent`（`utf8mb4_bin`）、`sort`，以及创建、更新时间和操作人。代码菜单的调整与代码不再有差异时整行删除。不软删（D-025）。

迁移文件按来源分目录，各用自己的版本表：框架的 `server/migrations/core/` 用 `ga_schema_version`，模块的 `server/modules/<模块>/migrations/` 用 `<模块前缀>_schema_version`（模块实现 `app.MigrationSource` 接口）。这样框架发新版增加迁移文件时，不会和业务方自己的编号冲突。

---

## 9. 接口约定

### 9.1 路径

`/api/<portal>/v1/<模块>/<资源>`。资源用复数名词，动作用 HTTP 方法；非 CRUD 的业务动作用 `POST /<资源>/:id/<动作>`。

### 9.2 响应

```json
{ "code": 0, "data": {}, "msg": "ok", "requestId": "…" }
```

- `code = 0` 表示成功，非 0 表示失败。
- HTTP 状态码规则：**成功和业务性失败返回 200**（参数校验失败也算业务性失败，返回 200 加 3xxx 错误码）；协议层问题用真实状态码：400 请求体无法解析、401 未认证、403 无权限或需改密、404 路由或资源不存在、409 刷新竞争、413 请求体过大、429 限流、500 服务端异常。非 200 的响应体仍然是同一个信封结构。
- 选这个折中是为了兼容现有大量按 `code === 0` 判断成功的前端页面，同时让网关、监控和 nginx 能按状态码识别认证失败、限流和故障。

错误码分段：

| 段 | 含义 | 例 |
|---|---|---|
| 1000–1999 | 认证 | 1001 账号或密码错误、1002 需要验证码、1003 已锁定、1004 令牌无效、1005 `REFRESH_RETRY`、1006 会话已锁屏（HTTP 423） |
| 2000–2999 | 授权 | 2001 无权限、2002 `PWD_CHANGE_REQUIRED` |
| 3000–3999 | 参数校验 | 3001 校验失败，`data.fields` 给出 `[{field, message, key?, params?}]` |
| 4000–4999 | 框架内业务错误 | 4001 用户名已存在、4002 不能停用最后一个超管、4101 由代码声明的内容不能在后台修改 |
| 5000–5999 | 系统 | 5000 内部错误（`msg` 不带内部细节） |
| 10000 以上 | 留给业务模块 | |

`msg` 按 `Accept-Language` 选择语言（§9.6），没有这个头时用简体中文。

### 9.6 多语言（D-026）

- 支持 11 种界面语言：`en-US`、`zh-CN`、`zh-TW`、`ja-JP`、`ko-KR`、`ms-MY`、`ta-IN`、`bn-BD`、`ru-RU`、`fr-FR`、`de-DE`。清单在 `server/core/httpx/lang.go` 和 `web/packages/shell/src/i18n/languages.ts`，两边一致。从右往左的语言暂不支持。
- 语言匹配（前后端同一套规则）：先完全匹配；`zh` 带 `Hant`、`TW`、`HK`、`MO` 的归繁体，其余 `zh` 归简体；其他语言按语言部分匹配（`ta-SG` → `ta-IN`）。后端按 `Accept-Language` 的 q 值从高到低找第一个支持的语言，都不支持时用英文。
- 缺文案时的回退：繁体 → 简体 → 英文；简体 → 英文；英文 → 简体；其他语言 → 英文 → 简体。前端文案、后端错误码的提示、字典文字都按这条链。
- 前端每个请求带 `Accept-Language: <当前界面语言>`。
- 字段错误 `{field, message, key?, params?}` 和业务自定的失败说明（信封的 `key`、`params`）带翻译键：前端有 `err.<key>` 的翻译时代入 `params` 显示，没有时显示 `message`/`msg`（英文）。翻译只维护在前端文案里。
- 每个 `locales` 目录每种语言一个文件（`<语言代码>.ts`），与 `zh-CN.ts` 的键、占位符完全一致，由测试保证。端用 `createPortalApp({ languages })` 决定启用哪些语言，默认全部。
- 语言选择器显示旗帜（`flag-icons` 的 SVG）和该语言自己的名字；繁体中文不配旗帜，显示"繁"字标。

### 9.3 分页

请求：`page`（从 1 开始）、`pageSize`（默认 20，上限 200）、`keyword`。
响应：`data: { list, total, page, pageSize }`。
排序参数 `sortBy`、`sortOrder` 必须过字段白名单，禁止拼进 SQL。

### 9.4 请求 ID

读取请求头 `X-Request-Id`，符合 `^[A-Za-z0-9-]{8,64}$` 则沿用，否则生成。写入 ctx、日志、操作记录、响应头和响应体。

### 9.5 系统管理接口（平台端，前缀 `/api/platform/v1/system`）

| 方法 | 路径 | 权限码 |
|---|---|---|
| GET | `/users` | `system:user:list` |
| POST | `/users` | `system:user:create`（敏感） |
| PUT | `/users/:id` | `system:user:update` |
| POST | `/users/:id/status` | `system:user:status` |
| POST | `/users/:id/reset-password` | `RequireSuper`：只有超管；超管账号的密码不能在这里重置（D-035） |
| PUT | `/users/:id/roles` | `system:user:assign-role`（敏感） |
| GET | `/options/users` | AuthOnly，只返回 `id`、`displayName` |
| GET | `/roles` | `system:role:list` |
| POST / PUT / DELETE | `/roles`、`/roles/:id` | `system:role:create` / `update` / `delete` |
| GET | `/roles/:id/perms` | `system:role:list` |
| PUT | `/roles/:id/perms` | `system:role:grant`（敏感） |
| GET | `/perms/tree` | `system:role:list`，返回注册表里该端的权限分组树 |
| GET | `/operation-logs` | `system:oplog:list` |
| GET | `/login-logs` | `system:loginlog:list` |
| GET | `/error-logs`、`/error-logs/:id` | `system:errorlog:list` / `system:errorlog:detail`（都敏感），错误日志（D-032） |
| GET | `/security-events` | `system:secevent:list`（敏感），安全事件（D-032） |
| GET | `/audit/timeline` | `system:audit:timeline`（敏感），调查时间线（D-032） |
| GET | `/sessions`、POST `/sessions/:sid/revoke` | `system:session:list` / `system:session:revoke`；非超管不能吊销超管的会话（D-035） |
| GET / POST / PUT / DELETE | `/depts`、`/depts/:id` | `system:dept:list` / `create` / `update` / `delete`（D-033） |
| GET / POST / PUT / DELETE | `/posts`、`/posts/:id` | `system:post:list` / `create` / `update` / `delete`（D-033） |
| GET | `/options/depts`、`/options/posts` | `system:user:list`，用户表单的下拉 |
| GET | `/dicts`、`/dicts/:id` | `system:dict:list` |
| POST / PUT / DELETE | `/dicts`、`/dicts/:id` | `system:dict:create` / `update` / `delete` |
| POST / PUT / DELETE | `/dicts/:id/items`、`/dicts/:id/items/:itemId`；POST `/dicts/:id/items/:itemId/reset` | `system:dict:update` |
| GET | `/options/portals` | AuthOnly，已注册的端代号 |
| GET | `/security-policy` | `system:security:view`（敏感），安全设置：本端生效的登录防护和密码策略，只读，没有写接口（D-034） |
| GET / POST / DELETE | `/ip-rules/deny`、`/ip-rules/deny/:id` | `system:ip:list` / `system:ip:deny`（敏感），IP 黑名单（D-062） |
| GET / PUT | `/ip-rules/allow` | `system:ip:list` / `system:ip:allow`（敏感），平台端 IP 白名单（D-062） |
| GET / PUT | `/users/:id/ip-allow` | `system:user:ip`（敏感），平台账号的 IP 白名单，目标按查看、修改用户的范围（D-062） |
| GET | `/dashboard` | `system:dashboard:view`，数据中心的统计（D-027、D-030） |
| GET | `/monitor/security`、`/monitor/server` | `system:monitor:view`（敏感），安全监控（原监控中心，D-030、D-036） |
| GET | `/workspace` | `system:workspace:view`，工作台，只返回本人的数据（D-030） |
| POST / PUT / DELETE | `/avatar` | AuthOnly，本人上传（请求体就是图片）、选内置头像、清除（D-040） |
| GET | `/avatars/:key` | AuthOnly，按随机键读头像（`data:` 地址），`?size=64` 取小图（D-040） |
| DELETE | `/users/:id/avatar` | `system:user:update`，管理员只能清除别人的头像，受数据范围约束（D-040） |
| GET / PUT | `/profile`；POST `/profile/revoke-other-sessions` | AuthOnly，个人中心（D-038）：只读写调用者本人；能改的只有显示名、邮箱、手机、简介，夹带别的字段回 3002；下线其他设备保留当前会话 |
| GET | `/menus` | `system:menu:list`，本端全部菜单节点（不按权限裁剪） |
| PUT | `/menus/:name`、`/menu-layout`；POST `/menus/:name/reset`、`/menu-groups`；DELETE `/menu-groups/:name` | `system:menu:update`（敏感） |

代理商管理、商户管理（平台端，前缀 `/api/platform/v1/agent`、`/api/platform/v1/merchant`，D-065）：两个模块 `modules/agent`、`modules/merchant` 是 `core/org` 的接口层，接口一一对应（详见 `docs/api.md`）。菜单是根目录下的"代理商管理"（排序 100）、"商户管理"（110），各有列表、登录日志、操作日志。

| 方法 | 路径（以商户为例） | 权限码 |
|---|---|---|
| GET | `/merchants`、`/merchants/:id`、`/merchants/:id/accounts`、`/merchants/:id/sessions`、`/merchants/:id/ip-allow`、`/merchants/:id/ip-deny`、`/options/agents` | `partner:merchant:list` |
| POST | `/merchants` | `partner:merchant:create`：同时建主账号，初始密码只在响应里出现一次 |
| PUT | `/merchants/:id` | `partner:merchant:update` |
| POST | `/merchants/:id/status`、`/merchants/:id/sessions/:sid/revoke` | `partner:merchant:status`：停用时吊销这个商户的全部会话 |
| POST / PUT / DELETE | `/merchants/:id/reset-owner-password`、`/merchants/:id/owner`、`/merchants/:id/ip-allow`、`/merchants/:id/ip-deny` | `partner:merchant:owner`（敏感）：重置、更换主账号，清空商户的 IP 白名单、清空商户自己设的 IP 黑名单（D-102） |
| PUT | `/merchants/:id/agent` | `partner:merchant:transfer`（敏感）：改归属代理商，仅商户 |
| GET | `/login-logs`、`/operation-logs` | `partner:merchant:log`：商户端的日志，可按商户编号筛选；查看本身记操作日志 |

代理商端、商户端自己的后台（前缀 `/api/agent/v1/org`、`/api/merchant/v1/org`，D-067）：内核的端后台套件 `core/orgportal` 提供，两个端的模块 `modules/agentportal`、`modules/merchantportal` 把它挂上（详见 `docs/api.md`）。权限码是 `<端>:` 加下表的后两段；"主账号"指 `RequireSuper`（主体端的超管就是本主体的主账号）。主体只取会话里的主体，请求里带 `orgId`、`agentId`、`merchantId` 一律 3002。菜单：概览（登录即可见）、"账号与权限"（子账号、角色与权限、会话）、"日志"（登录日志、操作日志），代理商端另有"名下商户"；组件由前端壳内置。

| 方法 | 路径 | 权限码 |
|---|---|---|
| GET | `/overview` | AuthOnly：本主体的资料和调用者有查看权限的计数 |
| GET / PUT | `/ip-allow` | 主账号：本主体的 IP 白名单（D-062），不为空时必须包含主账号当前的地址 |
| GET | `/accounts`、`/accounts/:id` | `account:list` |
| POST | `/accounts` | `account:create`（敏感）；顺手分配角色还要 `account:assign-role` |
| PUT | `/accounts/:id` | `account:update` |
| POST | `/accounts/:id/status` | `account:status`：停用时同一个事务吊销会话；主账号停不了、不能停用自己 |
| POST | `/accounts/:id/reset-password` | 主账号：只能重置员工的，主账号自己的只能由平台重置 |
| PUT | `/accounts/:id/roles` | `account:assign-role`（敏感） |
| GET | `/roles`、`/roles/:id/perms`、`/perms/tree` | `role:list` |
| POST / PUT / DELETE | `/roles`、`/roles/:id` | `role:create` / `update` / `delete` |
| PUT | `/roles/:id/perms` | `role:grant`（敏感） |
| GET | `/sessions`；POST `/sessions/:sid/revoke` | `session:list` / `session:revoke` |
| GET | `/login-logs`、`/operation-logs` | `loginlog:list` / `oplog:list`：只看本主体的；查看本身记操作日志 |
| GET / PUT | `/profile`、`/profile/avatar`；POST `/profile/revoke-other-sessions` | AuthOnly，个人中心：只读写本人；头像这一版只能选内置的 |
| GET | `/merchants`、`/merchants/:id`（前缀 `/api/agent/v1`） | `agent:merchant:list`：仅代理商端，名下商户只读 |

员工对主账号的写操作（改资料、启停、分配角色、让会话下线）一律 403，有全部账号权限也一样。

---

## 10. 操作日志与登录日志

- 操作日志按路由**显式开启**：注册路由时加 `oplog.Record("动作名")`。不加就不记，例如下发密钥的接口可以故意不挂，避免把响应里的密钥写进库。动作名用 `<资源>.<动作>` 这样的稳定编码（如 `user.create`），由前端翻译（D-011）。
- 记录字段：`id`、`request_id`、`portal`、`user_id`、`username`、`session_id`（D-032）、`action`、`method`、`path`、`query`、`body`、`http_status`、`code`、`latency_ms`、`ip`、`user_agent`、`error`、`created_at`。
- `body` 最多保留 4 KB。键名命中 `password|passwd|secret|token|key|credential|sign|authorization`（不区分大小写）的值替换成 `***`。查询串同样处理。
- 日志与绑定使用相同的请求类型依据（D-095），日志是脱敏且有界的摘要：JSON 接口只收 `Content-Type` 为 `application/json`（或 `+json` 结尾）的请求体，别的类型在绑定时回 `3002`、不执行。声明成表单、内容以 `{` 或 `[` 开头的请求体按 JSON 脱敏；查询串和表单里不像字段名的键只写占位符 `(key)`。单个字符串值超过 512 字节只留开头并标 `…(N bytes)`，JSON 里超过 128 字节的键不写原文；整段超过 4 KB 时结尾标 `…(truncated, N bytes)`。
- 操作日志不记响应体。
- 登录日志、操作日志、安全事件每写一条，同时以 `AUDIT` 级别往日志输出写一行（`audit.login`、`audit.operation`、`audit.security`，不含请求体和查询串），不受 `log.level` 影响，交给外部日志系统保存（D-032）。
- 操作日志、登录日志以及 D-032 的错误日志、安全事件都不提供更新和删除接口，也不自动清理（D-032）。
- "修改前后对比"式的审计放在 v0.2，由 service 层显式调用，不靠中间件猜。

---

## 11. 配置

### 11.1 文件与环境变量

`config/config.yaml`（不进 git）+ 环境变量覆盖，环境变量优先。机密（数据库密码、端的 JWT 密钥）在本地开发时可以写在 `config.yaml` 里；容器与生产环境用环境变量提供，`config/config.example.yaml` 里只留空位不填真值（D-017）。

Viper 的 `AutomaticEnv` 对"配置文件和默认值里都不存在的键"不生效，`Unmarshal` 会读到空值。因此 `core/conf` 必须为每一个可由环境变量提供的键**显式调用 `BindEnv`**，键和变量的对应关系写成一张表，单元测试覆盖。

| 环境变量 | 配置键 | 必填 |
|---|---|---|
| `GA_DB_PASSWORD` | `database.password` | 是 |
| `GA_JWT_SECRET_<PORTAL>` | `portals.<code>.jwtSecret` | 每个启用的端都要 |

```yaml
server:
  addr: 0.0.0.0:8080
  mode: release            # debug | release
  trustedProxies: []       # 见 12.2
  allowedOrigins: []       # 登录、刷新、登出接口的 Origin 白名单
  maxBodyBytes: 1048576
  passwordHash: argon2id   # 新密码哈希的算法：argon2id（默认）| bcrypt | pbkdf2-sha256 | pbkdf2-sha512；核对哪种都认（§5.6）
  passwordParallel: 0      # 同时做密码计算的上限；0 = 按 CPU 数。用 Argon2id 时内存峰值约为"上限 × 40 MB"（§5.6）
database:
  host: 127.0.0.1
  port: 3306
  name: goalladmin
  user: goalladmin
redis:                     # 可选，见 11.3；addr 留空 = 不用 Redis
  addr: ""                 # 主机:端口
  username: ""
  password: ""
  db: 0
  tls: false
  keyPrefix: "ga:"
  connectWait: 5s
portals:
  platform: { accessTTL: 15m, refreshTTL: 168h }
```

### 11.2 启动校验（不通过则拒绝启动）

release 模式下：

- 每个启用的端都有 JWT 密钥，长度不少于 32 字节，各端互不相同，且不等于示例值。
- 数据库密码非空。
- `allowedOrigins` 非空。
- `trustedProxies` 里没有覆盖全部地址的网段（D-098）。
- 内置入口不注册调试路由；增加业务模块时须保持这条部署约束（当前不对任意自定义 Raw 路由做调试用途识别）。

debug 模式不做上面这些校验；监听非回环地址时启动日志里有一条 WARN 说明（D-098）。

任何模式下：配了 `redis.addr` 时，地址必须是 主机:端口、`db` 在 0–255、`keyPrefix` 是 1–32 个字母数字和 `: _ -`、设了 `username` 就得有 `password`、`connectWait` 在 0–10 分钟；连上的 Redis 版本低于 6.0 拒绝启动（连不上不拒绝，见 11.3）。

### 11.3 Redis 与多实例（可选，D-074 至 D-078）

**什么时候要配。** 每个程序只跑一个实例时不用配，程序只用自己的内存，和以前一样。同一个程序跑多个实例（前面做负载均衡）时必须配。平台、代理商、商户三个程序各跑一个实例时可配可不配：配了，别的程序撤权之后没标"按库核对"（D-073）的路由也很快跟上。三个程序和它们的所有实例要连同一个 Redis、用同一个 `keyPrefix`；几套互不相干的部署共用一个 Redis 时各用各的前缀，而且一个前缀不能是另一个的开头。

**版本。** 客户端 `github.com/redis/go-redis/v9` v9.22.0。服务器最低 **6.0**（启动时检查，低于它拒绝启动）；自己装的推荐 **8.2 及以上**（长期支持的版本），用云厂商托管的选它提供的最高版本。代码只用 6.0 就有的命令，用 RESP2 协议（握手是一条 `HELLO 2`，服务器不认识时退到 `AUTH`、`SELECT`），不发客户端标识之类的可选命令——云厂商的代理接入也能用。客户端官方声明支持 8.0 以上、"7.0 以上应该能用"，对 6.x 没有说法：6.0 靠这里的测试保证。`make ci` 的测试容器用的就是最低版本 6.0：测试在最低版本上通过，才说支持它。只支持一个地址；哨兵、集群的多地址配置不做。

**Redis 用来做什么。** 内存里的缓存（会话和账号状态、授权快照、菜单调整、字典、IP 名单）留着，挡在前面。Redis 传缓存失效通知、保存共用的限流、计数和验证码答案；这些操作访问 Redis，普通缓存读取仍走内存。已提供连接、失效通知、共用计数、验证码答案，以及平台双实例 Compose 和独立进程冒烟（D-078）。部署方式、迁移顺序和验证边界见 [多实例部署指南](guides/multi-instance.md)。

**失效通知（D-075）。** 每个实例订阅 `<keyPrefix>invalidate` 通道。变更提交后先更新或清除本地缓存，再发布通知；回滚不发。CLI、平台对主体端的管理使用同一套提交后回调，不要求本程序注册被修改的端。消息只带版本、随机实例标识、缓存类型、端和键；自己的、未知协议、格式不对或超过 1024 字节的消息忽略。接收方只清内存，不立刻查库，也不再次发布：会话按会话号、账号按账号 ID；主体变更清该端账号缓存；授权快照整体失效，菜单按端、字典按编码，IP 名单标记待核对。读取途中收到通知时，失效代数阻止旧数据回填。发布订阅不保存历史；每次订阅确认和恢复回调清一遍缓存，补上漏收的窗口。

**共用计数（D-076）。** 同一前缀的同一端共用登录请求限流、失败次数、验证码阈值、组合锁定和跨 IP 的账号锁定，以及验证码生成次数、本人改密的旧密码尝试与成功次数、主体套件的密码操作次数。用户名、主体编号与 IP 沿用 §5.7 的归一化，字段中的用户维度使用散列；端、主体和用途互不混用。同一用途的策略、窗口和容量在各实例必须一致，各主机须同步时钟：脚本使用应用传入的毫秒时间，不取 Redis 服务器时间。

- 请求限流和简单计数从首个请求起算固定窗口；失败次数使用滑动窗口，跨实例的时间点按值清理。锁定先于限流；限流先计 IP、再计账号。成功登录在数据库提交后只清该账号与 IP 的组合失败，保留账号跨 IP 累计。解锁不占登录请求次数，失败计入相同记录，成功不清登录失败。
- Redis 6.0 的 Lua 脚本原子准入和收尾，多键使用相同 `{counts}` hash tag。每端登录防护合计 30 万逻辑记录，简单窗口每用途 10 万；预留失败记录后才核对密码，结束不新建，活跃失败及锁定不逐出。过期记录每次最多清理 128 条，清理积压时可暂时拒绝新键。共享尝试有随机票据和 15 分钟租期，进程退出后记录能释放；超过租期的收尾不修改新记录。
- 健康期间同时维护本实例的影子计数和在途预留，Redis 故障直接使用它们；恢复后保留共享计数与锁定，不清零、不把本地历史整体写回。原在途操作绑定原来的后端和窗口代数。网络错误导致结果不确定时不重试，可能已消耗的共享次数保留。
- 固定窗口的纯内存计数与本地影子共用过期最小堆（D-082）；新键遇到容量上限时最多清理 128 条，最早窗口尚未过期就停止，不扫描整表。重置同时移除索引节点，到期或重置的容量可立即复用，活跃记录不逐出。
- 退款与成功清零使用当次占用凭据，幂等且只影响当次窗口，旧窗口的迟到结果不影响新窗口。登录清组合失败和改密清旧密码尝试等到外层事务提交；事务回滚退回成功改密预留。强制改密、预算先行、便宜检查和未计算退款的原规则不变。
- 启动、恢复和业务脚本均在固定独立字段检查 EVAL 与实际内部命令权限，业务检查先于记录更新；权限变化不会半写用户计数。探测不动用户字段，失败残留有界，恢复可清理。

**验证码答案（D-077）。** 健康时，同一部署前缀、同一端的实例共享答案：A 生成的验证码可由 B 验证，跨端不得使用。答案仍是 5 位随机数字、5 分钟有效，ID 是不透明字符串；Redis Lua 原子取走后才比较，答错或空答案也作废。共享容器每部署最多 10240 条，本地容器每实例最多 10240 条，满时删除最早到期的；共享过期清理每次最多 128 条，容器自身也有过期时间。满了丢最早的而不是拒绝新的（D-105）：取验证码按来源 IP 限速，要在别人取到答案后的十几秒内把它挤掉需要上千个来源同时满速生成，被挤掉的一方重新取一张即可；改成满了拒绝，几十个来源就能让所有人取不到验证码。来源更多的洪水由入口层的限速处理。

共享答案不保留可验证的本地副本，ID 固定绑定原存储后端。共享消费超时、断线或结果不确定时不重试、不改走本地，按验证码未通过处理，前端重新获取。共享写入结果不确定时，用新的随机 ID 返回本地验证码，原共享 ID 不返回。故障期间新生成的验证码仅在生成实例可用，恢复后仍在原实例消费或过期，不复制回 Redis；负载均衡没有保持实例亲和时可能需要重新获取。图片格式、生成限流和绘图并发不变。

**Redis 不可用时。** 使用本实例内存继续服务：缓存靠自己到期（最长 15 秒，数据库的压力和没有 Redis 时一样）；计数退回每个实例各算各的；新验证码存在本实例。已在 Redis 中的验证码暂时无法校验，需要重新获取。

共享计数允许在故障或切换期间暂时失效：接受多个实例的合计配额放宽、其他实例的失败次数与锁定暂时不可见。各实例仍执行本地影子计数和原业务校验，正常处理业务；共享计数不可用不导致统一拒绝，也不改用数据库计数。恢复后使用已有共享状态，不合并故障期间的本地历史（D-076）。

- 什么算故障：网络错误（连不上、超时、连接断了），建立连接这一步失败（包括开了 TLS 时证书验不过），或者服务器明说自己现在用不了（正在加载数据、只读、内存满了、密码失效）。连接池一时取不到连接、请求自己结束了（被取消，或者它自己的时限到了）、普通的错误回复不算。
- 出一次故障就标成不可用，之后的操作立即走内存、不碰网络；故障发生时不清缓存。后台每秒探测一次（PING、版本、试写一个键、实际通道的订阅与发布权限，以及计数脚本与内部命令权限），连续通两次自动切回来，并把靠通知保持新鲜的缓存清一遍。出过故障就换一个新的连接池。刚恢复不久又出故障的，下一次探测前等得越来越久（最多 30 个探测周期），稳定一分钟后重新算——时好时坏的 Redis 不会让缓存每秒被清一遍。
- 每次操作有时限、不重试：建立连接 1 秒，单次读写 300 毫秒，一次操作合计 500 毫秒（包括正好要新建连接的那一次）。不用会阻塞的命令。
- 订阅使用独立长连接，接收不走短操作的 `Do`；空闲读超时只触发一次有时限的 PING，必须收到 PONG，空闲本身不算故障。断线、假死或权限失效报告给同一个故障状态机，不可用期间订阅不自行拨号，等恢复后重新订阅。应用停止时先取消并等待订阅退出，再关闭连接池。
- 启动时：版本低于 6.0、服务器明确拒绝这份配置（密码不对、没有权限、库号不存在、写不了、不认带用户名的登录）是配置错误，拒绝启动；连不上或暂时用不了不拒绝启动，最多等 `redis.connectWait`，还不行就按不可用处理、后台继续探测。命令行的子命令也会连 Redis。
- 状态变化记日志：`redis connected`、`redis unavailable at startup`、`redis unavailable; falling back to in-process state`、`redis still unavailable`（同一个原因在一次故障期间只记一次，最多记 8 种）、`redis available again`。密码不进日志和错误信息：服务器回的错误里出现密码时换成 `***`。

**仍然按实例算的。** 保护本实例 CPU 的东西：密码计算的位置（§5.6）、按主体的并发上限、同时画验证码的个数。服务器监控页显示的是处理这次请求的那个实例。

**部署要注意。** Redis 保存登录失败次数和锁定、操作计数、验证码答案，并传缓存失效通知：设密码，只让应用所在的网络访问。账号需要在实际前缀的通道上有发布、订阅和 PING 权限，以及版本与试写探测所需权限；计数键还需要 EVAL、HGET、HSET、HDEL、HINCRBY、ZADD、ZRANGEBYSCORE、ZREM，验证码键使用 EVAL、HGET、HSET、HDEL、HLEN、ZADD、ZRANGE、ZRANGEBYSCORE、ZREM、PEXPIRE。启动与恢复检查全部脚本权限，业务脚本更新答案之前同样检查，权限变化不半写用户答案。明确拒绝这些权限时拒绝启动；运行时失去权限按不可用处理，权限恢复并连续探测通过后切回。开了 `redis.tls` 时服务器的证书用系统信任的根证书校验；用私有根证书签的，把根证书装进系统或容器的信任库。

---

## 12. 安全基线

### 12.1 清单

- 请求体上限：全局中间件在**读取请求体之前**套 `http.MaxBytesReader`，默认 1 MB，个别路由可单独调大。操作日志预读发现超限时直接返回 413 / `4013`，不进入后续处理函数；已通过守卫的请求仍记录失败日志，请求体只记不可读占位符（D-083）。
- HTTP 超时：`http.Server` 的 `ReadHeaderTimeout`、`ReadTimeout`、`WriteTimeout`、`IdleTimeout` 四个都设（默认 10s / 30s / 60s / 120s），配置里四个值都必须大于 0——0 在 `net/http` 里是"不限时"，直接暴露后端时慢速请求就能占满连接。
- 数据库故障时的保护（D-037）：每个请求的 ctx 带处理时限 `server.handlerTimeout`（默认 30s，必须小于 `writeTimeout`），到点取消其中的查询、连接放回池子，请求回 503（5003）；连接串默认带连接超时 5s、单次读写超时 30s（`database.connectTimeout` / `readTimeout` / `writeTimeout`，都不能为 0，`readTimeout` 不能小于 `handlerTimeout`），作为没有 ctx 时限的后台任务的兜底，迁移用单独的连接池、不受读写超时限制；启动时可以用 `database.connectWait`（默认 0 = 连不上立即退出，最长 10m）等数据库就绪。请求结束后才写的日志去掉取消信号和时限再写，不因请求超时而丢。
- 数据库连接池（D-069）：`database.maxOpenConns`（默认 50）必须不小于 1，`database.maxIdleConns`（默认 10）不能是负数，否则拒绝启动——`database/sql` 把 0 和负数的上限当成"不限"，高峰时连接数没有上限，能把 MySQL 的连接数占满，三个程序共用一个库时互相拖累。
- 构建上下文：根目录 `.dockerignore` 把 `server/config`、`.env*`、`.git`、`.local` 排除在 Docker 构建上下文之外，Dockerfile 只 COPY 二进制需要的目录。密钥不得进入构建上下文，因为上下文里的每个文件都会发给 Docker daemon 并可能留在构建缓存里。
- 安全响应头：`X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`。CSP 和 HSTS 在 nginx 配置，`deploy/` 里给出示例。
- CORS：默认关闭。同源部署不需要它。
- SQL：禁止字符串拼接用户输入。动态表名、动态排序字段必须过白名单。
- 日志：不记录请求头里的 `Authorization`、`Cookie`，不记录密码、密钥、令牌。
- 错误：5xx 的 `msg` 不带堆栈、SQL、内部路径。
- 健康检查：`/healthz` 只表示存活，不访问数据库；`/readyz` 检查数据库，每实例缓存成功或失败结果 1 秒，并发请求共用一次最多 2 秒的检查（D-084）。请求取消只结束自身等待，App.Stop 取消并等待在途检查。缓存从检查完成起算，状态变化需等缓存过期并完成下一次检查；无请求时不轮询，Redis 不参与判定。每次响应独立生成信封、语言和请求 ID，保持 no-store，不带版本、配置或底层错误。
- 框架不提供"在线改配置文件""执行任意 SQL""上传并安装插件"这类能力，也不接受这类需求。
- 框架不内置任何默认业务角色，避免出现"出厂角色带着不该有的权限"。

- IP 访问控制（D-062）：黑名单对三个程序的每个请求生效（健康检查除外）；端白名单管这个端的每个接口；主体、账号白名单管登录和每个已登录的请求。名单在后台维护（`ga_ip_rule`），写入时加一 `ga_change_seq` 的序号，别的程序最长 5 秒跟上；改名单时不能把操作人自己挡在外面，配错了用 `server ip clear-allow` 清；主体自己设的黑名单用 `server ip clear-deny -portal P -org N` 清（D-102）。

### 12.2 客户端 IP

Gin 默认信任所有代理，`ClientIP()` 会直接采信 `X-Forwarded-For`，可被伪造。必须：

```go
engine.SetTrustedProxies(conf.Server.TrustedProxies) // 空切片 = 不信任任何代理，取 RemoteAddr
```

部署在 nginx 后面时，把 nginx 的地址填进 `trustedProxies`。登录限流、锁定、登录日志、操作日志都依赖这个值。只填反向代理自己的地址或网段：`0.0.0.0/0`、`::/0` 等于让请求方自己决定客户端 IP，release 模式拒绝启动（D-098）。反向代理必须覆盖（而不是追加）客户端自带的 `X-Forwarded-For`、`X-Real-IP`，`deploy/` 里的三份 nginx 示例都写在 server 级。

---

## 13. 测试与 CI

### 13.1 原则

认证、授权、隔离这三块的代码**没有测试就不算完成**。项目大量由 AI 协作者编写，人无法逐行审查，测试是唯一可靠的验收手段。重点是反向测试：证明"不该发生的事确实不会发生"。

后端测试连真 MySQL：测试从环境变量 `GA_TEST_DSN` 读连接串，`make test-db-up` 用 Docker Compose 起一个测试库，CI 用 MySQL 服务容器。需要 Redis 的测试连真 Redis（`GA_TEST_REDIS`，同一个 Compose 文件里的 `redis-test`，镜像是支持的最低版本 6.0；变量为空时这些测试跳过），Redis 不可用的情形用夹在中间的转发器切断、假死来模拟，不 mock 客户端。每个测试包在自己的事务里跑并在结束时回滚，或使用带随机后缀的独立数据库。不用 SQLite，不 mock GORM。

### 13.2 必须存在的反向测试

认证：

1. 错误密码、不存在的账号，返回相同的错误码和相近的耗时。
2. 过期、签名错误、`alg=none`、`alg=RS256` 的 JWT 一律 401。
3. `aud` 不等于当前端的令牌（用测试密钥签一个 `aud: "other"` 的）一律 401。
4. 登出后原 Access Token 立即 401，原 Refresh 不可用。
5. 账号停用后，已登录会话立即 401。
6. 修改密码后，该用户其他会话全部失效。
7. Refresh 轮换后旧值在 10 秒宽限期外重放，整个会话被吊销。
8. 两个并发刷新：一个成功，另一个得到 409，重试后成功，会话不被误吊销。
9. `must_change_pwd` 的账号访问非 `/auth/*` 接口得到 403。
10. 失败 3 次后无验证码的登录被拒；失败 10 次后被锁定。
11. 伪造 `X-Forwarded-For` 不能绕过限流（`trustedProxies` 为空时）。

授权：

12. 无权限码的用户访问 `Require` 路由 403。
13. 修改角色权限后，下一次请求立即按新权限判定，不需要重新登录。
14. 非超管拥有 `system:role:grant` 时，不能授予敏感权限码，不能授予自己没有的权限码。
15. 不能停用或降级最后一个超管。
16. 遍历路由表：每条端内路由都有守卫；`Public` 路由集合与 `docs/api.md` 的清单一致。
17. 数据库里存在未注册的权限码时，不产生任何授权效果。

其他：

18. 超过 `maxBodyBytes` 的请求在处理前被 413 拒绝。
19. 操作日志里的密码、密钥字段已脱敏。
20. release 模式下，弱密钥、示例密钥、空 `allowedOrigins` 都拒绝启动。
21. 业务模块除了自己模块内的包，只 import `core/` 的公开包；不 import 别的模块、入口包、`cmd/`、`migrations/`（`scripts/depcheck.sh`，`make ci` 里跑）。

端边界（§6.7，v0.1 只有一个端也必须存在）：

22. 别的端的角色：本端的角色读、改、删、授权接口一律 404，分配给本端用户时校验失败，角色和它的授权原封不动；服务层拒绝以别的端的名义创建、授权、删除、分配角色。
23. 别的端的会话：拿它的 `sid` 在本端吊销得到 404，会话仍可在自己的端里轮换；别的端的刷新凭证、拼上本端密钥的访问令牌都换不出本端身份。
24. 签名正确但会话不存在的访问令牌是 401，不是 500。

并发与原子性：

25. 登录名的大小写、空白变体共用一份验证码、限流和锁定配额；归一化后能登录同一个账号。
26. 两个超管并发互相停用、并发互相移除超管角色，每一轮都恰好一个成功、一个被拒，系统始终至少剩一个可用超管。
27. 重置密码时吊销会话失败，密码不会被换掉、旧会话仍然有效、接口不返回新密码。

字典（§4.5）：

28. 代码声明的字典：后台改、删字典本身，加项、删项、改值，一律 4101，库里原封不动。
29. 后台改过显示的项，重启同步后仍保持后台的文字；恢复默认回到代码里的当前值。停用的值 `Has` 返回 false。
30. 读取接口只返回当前端或共用、且启用中的字典；别的端的字典当作不存在。
31. 代码声明不合法（编码前缀、重复值、整数写法、颜色、端）时拒绝启动。

安全策略（§5.6、§5.7）：

32. 登录防护或密码策略越过底线（含负数）时拒绝启动。
33. 配置了"每次都要验证码"时，第一次登录就要求验证码。
34. 密码复杂度要求对改密、建用户同样生效；系统生成的密码满足策略。
35. 密码过期后仍能登录，但除 `/auth/*` 外一律 403，已登录的会话同样被拦下；改密后恢复。

菜单管理（§6.6，D-025）：

36. 改菜单的请求夹带路径、组件、权限码、端等白名单之外的字段时整体被拒绝（3002），库里不留任何调整；非法图标名、超长或带控制字符的显示名、非法语言代码被拒绝；代码里隐藏的菜单不能取消隐藏。
37. 拖动后的位置整体校验：成环、上级是页面、上级不存在、排序越界、超过 4 层、夹带字段、重复节点被拒绝，节点集合与当前不一致回 4001；任何一项不合格菜单都不变。删除分组后里面的代码菜单回到代码位置。
38. 改名、换图标、隐藏、挪进分组都不改变可见性：没有权限的菜单不会因为进了分组、改了名而出现；没有可见子项的分组和目录不下发；没有任何权限的用户看不到任何菜单；接口权限不受菜单影响。
39. `system:menu:update` 是敏感权限，有授权权限的非超管也授不出去；只有 `system:menu:list` 的人改不了；别的端的分组看不到、改不了、删不了、不能当上级；服务层的端取自操作者身份；修改和被拒绝的尝试都记操作日志。

界面改版（D-027）：

40. 锁屏后，除查看自己、解锁、登出外的接口一律 1006；`/auth/me` 不返回权限和菜单；用刷新凭证换来的新访问令牌照样锁着；同一用户的其他会话不受影响；输错密码提示剩余次数，输对后恢复；锁定、解锁都记操作日志且密码打码。
41. 锁屏后连续输错 5 次吊销会话，刷新凭证作废，只能重新登录，且输错次数计入登录防护；同时发来的一批解锁请求也一共只有 5 次机会。

控制台（D-030）：

42. 监控中心的两个接口要 `system:monitor:view`：只有数据中心权限的人回 403、未登录回 401；它是敏感权限，有授权权限的非超管即使自己有也授不出去；安全视图的登录列表不含 User-Agent 和请求 ID；数据库和 Go 的版本只到大版本；`monitor.server: false` 时服务器状态只回 `enabled: false`（D-031）。
43. 工作台只返回调用者本人的数据：请求里带别人的用户 ID 或用户名无效；没有 `system:workspace:view` 的人回 403；看不到别人的登录记录；会话列表不返回会话 ID。

运维中心与安全审计（D-032）：

44. 服务端故障（5xx、panic）写进错误日志，同一类只占一行并累加次数；只有 IP、数字不同的同一类错误合并；4xx 不记；错误文本里引号中的值、凭据、连接串里的账号密码被去掉；响应里没有错误细节和调用栈；列表不带调用栈，看调用栈要 `system:errorlog:detail`；两个权限都是敏感权限；同一个键攒着的次数在关停前写完。
45. 认证环节的攻击迹象写进安全事件：签名或格式不对的令牌（过期但受众、签发方也不对的同样算）、签名有效但会话不存在或内容不合规的令牌（严重）、刷新凭证重放（严重，带会话和用户）、已吊销会话的令牌、刷新和登出的来源不对；没带令牌、令牌过期、会话到最长寿命自然过期都不记。每条同时以 `AUDIT` 级别写进日志输出，日志级别设成 `error` 也照样输出，登录记录同样如此且不含密码。
46. 越权被拒写进安全事件：守卫拒绝的带权限码，业务层拒绝的（2001）同样记；同一来源一分钟内刷几百次只占一行、次数一个不少（写库失败的次数放回去下次再写），下一分钟另起一行；登录类事件不按攻击者填的账号拆行；严重事件不受新来源写入速度限制；登录限流、命令行建管理员也记；查看安全事件要敏感权限 `system:secevent:list`。
47. 调查时间线按会话、用户或 IP 把登录日志、操作日志、安全事件串成一条按时间倒序的线，不带请求体；按用户查时包括没有建立会话的失败登录；查询对象必须恰好一个；能用游标往前翻页，同一毫秒的记录不漏不重；要敏感权限 `system:audit:timeline`。
48. 查看审计数据本身留痕：查看操作日志、登录日志、错误日志列表和详情、安全事件、调查时间线各记一条操作日志（`audit.view.*`），带查询条件和会话；查看不存在的详情也记；没有权限被拒的查看不记操作日志，记为安全事件。所有日志资源只有读取路由。

部门与岗位（D-033）：

49. 部门树的完整性：同一上级下不能重名；上级必须存在；不能挪到自己或自己的下级下面；最多 10 层（新建和挪动子树都算）；名称不能全是空白、不能带控制字符和不可见字符；有下级部门或还有用户的部门不能删；两个管理员同时互相挪部门只能成功一个，树里不会出现环；编辑部门里的用户和删除这个部门同时发生时不会死锁，删除照样被拒绝；新选的负责人必须是启用的用户。
50. 用户的部门和岗位：新选的必须存在且启用，原来就有的停用了也能保留；分配失败时用户不会建出来；岗位去重、最多 20 个、整体替换，不传表示不改；还有人在用的岗位不能删；岗位编码格式合规、唯一、不可改；按部门筛选用户默认包括下级部门。
51. 部门、岗位接口各要各的权限码，用户表单的下拉要 `system:user:list`；增删改记操作日志；这些权限都不是敏感权限（部门、岗位不影响权限判断）。

系统设置（D-034）：

52. 安全设置返回的是认证器实际使用的生效值（配置文件写的锁定次数就是登录时实际锁定的次数），每项带允许范围、默认值、来源（配置文件 / 代码声明 / 默认值）和配置项路径；返回里没有 JWT 密钥等其他配置；要 `system:security:view`，没有的人 403；它是敏感权限，自己有的非超管也授不出去；`/security-policy` 只有 GET，其他方法都不存在。字典挪进"系统设置"后菜单名、路由、权限码不变，只有字典权限的人看得到"系统设置"和字典、看不到安全设置。

超管账号保护（D-035）：

53. 重置他人密码只有超管能做：拥有全部用户管理权限的非超管也是 403，并记安全事件（`forbidden`，说明 `super`）；没有 `system:user:reset-password` 这样的权限码，授不出去；超管被降级后下一次请求就不能再重置。
54. 超管的密码在后台谁都不能重置（别的超管、自己都不行），回 403 且不返回新密码、不吊销会话；`server admin reset-password` 能重置任何账号：新密码至少 20 位、旧密码和旧会话失效、登录后必须改密、记 `cli` 安全事件，停用的账号重置后仍是停用。
55. 非超管不能对超管账号做任何写操作：改资料（含部门、岗位）、停用（还有别的超管时也不行）、启用已停用的超管、分配角色、吊销其会话、改内置超管角色，都是 403、没有写进去、记安全事件；对普通账号照常可以；超管之间照常互相管理，超管被降级后下一次请求就失去豁免；会话列表标出超管的会话。删掉的权限码不会让角色授权保存失败，迁移 00009 清掉残留授权。

个人中心（D-038）：

56. 本人资料接口只读写调用者本人：没有任何权限码的账号也能看、能改自己的显示名、邮箱、手机、简介，改完 `/auth/me` 立即是新显示名，别人的资料一个字不动；夹带用户名、状态、角色、部门、头像、密码、备注等字段整体回 3002、库里不变；显示名和简介的控制字符、不可见字符、长度、邮箱格式都被拒；必须改密的账号进不去（2002），锁屏后一律 1006；下线其他设备只吊销本人除当前会话外的会话，当前会话照常、别人的会话不受影响，请求体里指定别人也无效；三条路由都是登录即可、都记操作日志。

按部门的数据权限（D-039）：

57. 数据范围（D-039）：仅本人、本部门、本部门及下级、全部四种范围下，用户列表、详情、下拉只返回范围内的人；范围外的人修改、启停、分配角色、会话列表和下线都是 404；没分配部门的人按仅本人算；超管永远是全部；新建角色的用户资源默认仅本人。
58. 范围按权限码合并：两个角色分别给"查看 + 全部"和"修改 + 仅本人"时，查看是全部、修改只能改自己；停用的角色不算。
59. 非超管不能把角色的范围设得比自己宽，不能给范围更宽的角色加自己有的权限码，也不能把范围更宽的角色分配给别人、或者把它重新启用；收窄须具备原角色的管理资格（D-117）；未声明的资源和不允许的范围被拒绝；功能权限和数据范围在一个事务里保存。
60. 部门类范围下新建或改部门时，新部门必须在范围内；非超管不能改自己的部门；挪到"未分配"要全部范围；非超管改部门上级要用户资源的每个权限码都是全部范围（查看全部、修改本部门及下级的人也不行）；部门负责人只能选范围内的人；修改范围比查看范围宽时写操作不回显；建用户时分配角色要有分配角色的权限码；升级迁移给现有非超管角色写上全部。

头像（D-040）：

61. 头像只能是空、内置头像名或本人上传的图片：上传只收 JPEG 和 PNG（按内容判断，不看文件名和声明的类型），`Content-Type` 不是 `image/jpeg` 或 `image/png` 的在操作日志之前拒绝，超过 1 MB、宽高超过 2048、渐进式 JPEG 扫描段超过 40 个的拒绝且不解码，存下来的是服务器重新编码的 256×256 和 64×64 JPEG，原文件里夹带的内容（EXIF、注释、追加在文件尾的脚本）一个字节都不留；不认识的内置头像名被拒；换头像或清除后旧键 404；读头像要登录；`PUT /profile` 仍然不接受 `avatar`；管理员只能清除，要 `system:user:update`、范围外 404、非超管动超管账号 403，`PUT /users/:id` 传 `avatar` 不改；升级迁移只留下合法的内置头像和确实有图的上传头像（可以重跑）；前后端的内置头像名表一致；没有头像时的 100 组配色对比度都不低于 4.5:1、底色不黑不白。

并发交错与缓存（D-043）：

62. 角色的判断和写入在同一把锁里看已提交的状态：非超管改角色时超管同时停用了它，非超管的写入必须看到"已停用"并走重新启用的检查（含敏感权限的角色被拒，状态仍是停用）；删角色时另一边刚把它分配给了用户，删除必须看到已提交的分配并拒绝，不留悬空的用户角色行；授权时角色刚被并发删除，授权报 404 且不留策略行。读库期间被撤销的角色、被清缓存的字典和菜单调整，不会被读方填回缓存；两次并发重载不会让先读到旧范围的那次最后覆盖新范围。
63. 登录名的别名碰不到账号：用户表的排序规则把 `ádmin` 当成 `admin` 时，拿着正确密码用别名登录也失败，响应和不存在的账号完全相同，别名上的失败不计入本账号的验证码和锁定，本账号的失败也不给别名开出第二份配额；自定义 `UserProvider` 同样适用。
64. 会话到期后缓存不再放行：到期时间随会话状态一起缓存，缓存命中时也要比一次，访问令牌仍有效、缓存周期未到也回 401。另外：`maxOpenConns` 为 1 时字典同步照样完成；后端镜像的 `COPY` 清单覆盖 `main.go` 直接引用的每个目录。
65. 操作人的权限在锁内按库判断（D-044）：超管把某个权限码从操作人的角色里收回并提交，在内存重载之前操作人把它授给别的角色、或把含它的角色分配给别人，都必须被拒；调用方的事务在拿超管锁之前做过读（先建用户再分配角色）时同样被拒，锁内看到的是已提交的状态；非超管不能把带通配规则的角色分配出去。
66. 内核公开包没有带 `TableName` 的导出类型：业务模块拿着内核的公开类型进不了内核的表（D-044）。
67. 数据中心的活跃用户排名只列操作人"查看用户"范围内的人：全部范围看整个端，本部门（及下级）只看范围内的用户，仅本人和没有查看用户权限的只看自己。另外：退出没得到服务端确认时记标记，下次启动先补做退出、不恢复会话，补做成功才清标记，登录后标记作废；安全监控缓存过期时同时到达的请求只查一次库，查库失败时一起失败；"下线其他设备"一条语句完成，会话再多也没有边界；工作台"上次登录"不依赖本次会话出现在会话列表里。

操作人身份与在途请求（D-045）：

68. 超管角色在请求认证之后、拿到锁之前被收回并提交：在途的请求不能再给自己或别人分配超管角色、重置别人的密码、让超管的会话下线、授出敏感权限码、改内置超管角色；账号被停用的在途请求同样不再有任何权限。
69. 本人改密的请求验证过旧密码之后，管理员重置了密码并吊销会话：改密回 401，密码仍是重置后的、强制改密标记还在，改密人选的密码登不上。
70. 收窄范围的授权提交后、内存重载前，在途的请求不能按旧的"全部"挪部门；建部门时负责人在检查期间被停用，部门不会带着这个负责人建出来。另外：工作台的"上次登录"能找到和本次同一毫秒的上一次登录；浏览器存储写不进去时，没确认的退出照样在下次打开时补做。
71. 账号在请求认证之后被停用、或会话被吊销：在途的写请求拿到锁后回 401，什么都不写（D-046）；本人改密和管理员重置同时发生不会死锁，重置的结果留着；前端收到 401 时登录身份已经换了人的请求不刷新、不重放。
72. 按路由表遍历平台端全部需要权限码、超管或只需登录的写路由（认证接口除外，D-048 加入只需登录的本人写操作）（用例表和路由表双向核对，新加的写路由没有用例测试就失败）：每条都由一个超管发起，请求认证之后、拿到锁之前账号被停用、会话被吊销，请求回 401，业务表逐行不变（D-047）。
73. 登录校验完密码、还没建会话时，管理员重置了密码或停用了账号：这次登录失败，不会留下有效会话（D-047）。
74. 同一个会话的两个改密请求并发：后一个拿到锁时密码已经被前一个换掉，按旧密码不对拒绝，前一个的结果留着。另外，前端：换了人之前发出的请求，成功的数据不交给页面，1006、2002 不触发锁屏和改密跳转；上一个人的刷新结果不写令牌，失败也不清掉现在的登录；现在的人的请求碰上上一个人的刷新，自己再刷新一次；登录失效时回调先于清除，能看到失效前是否已登录（据此提示并回登录页）；启动恢复在途时用户登录了，恢复失败不清掉这次登录；调查时间线回到没有查询条件的地址时清空结果（D-047）。
75. 刷新绑定会话：刷新请求声明的会话和 Cookie 里的不同（同一浏览器另一个标签页换了人登录），回 401，不轮换、不改 Cookie，那个人的 Cookie 照样能用；不声明时按 Cookie 刷新。前端知道自己的会话号时刷新都带上（D-048）。
76. 退出只清自己会话的 Cookie：退出请求带来的 Cookie 已经是别人新登录的，会话照样吊销，Cookie 不清；前端迟到的退出不清掉在途期间完成的新登录；登录身份一变，标签栏清空（D-048）。
77. 请求通过了路由的权限守卫，等锁期间这个权限被收回：拿到锁后回 403，什么都不写（D-048）。
78. 超管在途中被降级：写操作按剩下的角色照常执行，回显按降级后的查看范围，范围外的人不带资料（D-048）。

刷新（D-049）：

79. 只知道会话号换不出、也吊销不了会话：拿会话号配上编出来的 secret 刷新，回 401、不动 Cookie，会话照常可用、合法持有人照常轮换；记警告级安全事件 `refresh_mismatch`（带会话和用户），不算 `refresh_reuse`；上一个凭证在宽限期外重放仍然吊销整个会话（第 7 条）。
80. 服务端从不删除刷新 Cookie：退出、已吊销或不存在的会话、格式不对的 Cookie、重放导致的吊销，响应里都没有 `Set-Cookie`；写 Cookie 的只有登录和轮换。前端：待补退出的标记绑定会话号，刷新换来的是别的会话时不退出别人、标记作废、照常恢复，刷新被拒（401）时标记作废，网络故障时保留；不带会话号的标记被忽略并清掉；同一会话跨标签页并发刷新拿到 409 时按间隔重试，重试用尽仍是 409 不清登录状态、不算登录失效，登出遇到它也不算服务端确认；同源标签页的刷新用 Web Locks 串行化，等锁超时直接刷新。

认证时序与错误日志（D-050）：

81. 刷新时查账号遇到数据库临时故障：回 503，不轮换、不吊销，同一个凭证过了宽限期照样能刷新。
82. 错误日志按端隔离：本端只看本端和不属于任何端的错误，别的端的不在列表里、详情回 404；没登录的请求按路由模板归端。
83. 前端：重载后浏览器里已经是别的账号时不恢复成那个账号、登录页说明原因，普通的登录过期不算；刷新遇到 5xx 或断网不清登录状态；拿到锁之后刷新超过 5 秒再失败，不在锁外再刷一次。

授权快照、登录来源与读取一致性（D-051）：

84. 授权提交后内存重载失败：之后的权限码判定（路由守卫、`/auth/me` 的权限列表）回 503，不按旧快照放行刚撤掉的权限码；重试有间隔，库恢复后下一次判定就用上新状态。数据范围按请求的数据库视图算（D-054），收窄提交之后立即生效，不等重载。
85. 登录缺 `X-GA-Client`、`Origin` 不在白名单、请求体不是 `application/json`：403，不建会话、不写 Cookie、不记登录尝试；同源的正常登录照常。
86. 用户详情返回的就是检查过范围的那一行：检查之后、拼资料之前目标被调出范围并改了资料，返回的仍是检查时那一版；写后回显同样（账号、角色、部门、岗位在一个只读快照里读）。
87. 前端：启动时断网或服务端临时出错，这个标签页绑定的会话号留着，下次重载照样带上，服务端明确 401 才删；待补退出的记录按会话号各自保存，旧标签页迟到的退出成功只删自己那条，登录只作废登录前就在的，刷新因 Cookie 属于别的会话被拒时一条不删；登录请求带 `X-GA-Client`。

身份绑定与读取快照（D-052）：

88. 会话列表、数据中心的活跃用户排名：范围内有哪些人和这些人的数据在同一个只读快照里读（用户列表也这样读）。取完范围内的 ID 之后目标被调出范围并有了新会话、新操作：列出来的是取范围那一刻的，调走之后的新会话不列、新操作不计。
89. 前端：请求还没交给网络就换了人，请求作废、不发出去；发送用的令牌是请求进入时取下的；刷新等跨标签页的锁、等重试间隔时这一页退出了，拿到锁之后不再刷新、不写回令牌。
90. 刷新失败时服务端只在确定会话已经结束（不存在、已吊销、已过期、这次因重放被吊销）时带 `auth.sessionEnded`；Cookie 属于别的会话、凭证对不上、没有或看不懂 Cookie 都不带。前端退出只在登出成功或刷新带这个键时算确认。账号停用后刷新要吊销会话，吊销没写成时回 503、不带这个键（D-053）。

授权快照与认证动作（D-053）：

91. 成员关系、角色启用状态、权限码取自同一个授权快照：重载读完策略、还没读成员关系时有人成套提交"把他加进某角色、同时撤掉这个角色的权限码"，重载得到的是成套的旧状态，拼不出"新成员关系 + 旧权限码"；分配、撤销、停用角色提交之后新的判定立即生效；进程外的改动在快照有效期（15 秒）过后生效。
92. 解锁核对密码之后、解除锁定之前密码被改了：按输错处理，会话仍然锁着；用现在的密码照常解锁。
93. 前端认证动作（拉 `/auth/me`、登录、锁屏、解锁、改密、启动恢复）在每次等待之后核对登录身份：这期间退出或换了人，结果作废，不写回上一个人的资料、令牌、锁屏状态，也不替现在的人接着拉 `/auth/me`、补做退出。

数据范围与日志（D-054）：

94. 数据范围的全部输入在同一个数据库视图里：算范围时读完角色、还没读角色的范围，有人成套提交"把他从角色里撤下、同时放宽这个角色"，算出来的是成套的旧状态；读用户详情时读完目标之后，有人成套提交"把操作人调到别的部门、同时把他的范围从仅本人改成本部门"，两个状态下都看不到的人不会被放出来。
95. 出错和慢查询的 SQL 日志只记占位符模板和调用位置，不记绑定参数的值（密码哈希、刷新凭证的哈希、联系方式）；参数已被 gorm 展开的路径不记 SQL 文本。

撤权窗口、负责人、限流与容量（D-055）：

96. 改授权（授权、分配或撤销角色、启停角色、删角色、清理策略）写完、提交之前就标记"待发布"，到新快照发布才撤掉：这期间的权限码判定先等发布完成（最多 3 秒，超时 503），不在"已提交、未发布"的窗口里按旧快照放行刚撤掉的权限；事务回滚（包括 panic）时标记同样撤掉，不卡住之后的判定。
97. 部门列表和改部门的回显：负责人不在调用者"查看用户"的范围内时，不给 ID 和名字，只标记 `leaderHidden`；部门结构和人数照常给。改部门时不给 `leaderUserId` 就保持原来的负责人，给 0 才清空。
98. 本人改密核对旧密码：同一会话 15 分钟内最多 5 次（成功清零），之后连正确的旧密码也回 429、不做密码计算，记安全事件 `pwd_change_throttled`。公开的验证码接口：同一来源 IP 每分钟最多 30 张，同时最多画 8 张，超出回 429。
99. 容量上限：会话列表、活跃用户排名把"范围内的用户"作为子查询交给数据库，不先把全部 ID 取到内存；一本字典最多 1000 项、5 层（后台加项和代码声明都检查，正好 1000 项、5 层的合法），库里更深的历史数据拼树到 5 层为止；页码上限 5000。
100. 前端：发出时没有令牌的请求收到 401 不自动刷新（刚退出或登录失效之后旧组件发的新请求，不能按浏览器里的 Cookie 恢复成另一个标签页的账号）；恢复登录态只走启动时显式的刷新。门禁脚本（依赖方向、许可证清单、gofmt、命名检查）在底层工具出错时失败，不报 OK（`make gates-test` 自测）；许可证检查只认许可证正文，只有 NOTICE、PATENTS 的依赖不算有许可证。

写后回显与登录防护容量（D-056）：

101. 超管在途中被降级、剩下的角色仍能编辑部门：写入照常，但新建、修改部门的回显按降级后的"查看用户"范围遮蔽负责人，不沿用请求开始时的超管身份（同第 78 条用户回显）。
102. 登录防护的计数有键数上限：满了新来源按限流拒绝，已有来源照常计数，失败次数和锁定不被逐出；表满时提前清理过期数据，但两次提前清理之间有间隔，不会每个请求遍历一遍全表。
103. 上限是硬上限（D-057）：IP 和账号的限流记录都在、只缺"账号 + IP"失败记录的登录，同样要放得下才接纳；锁屏解锁的失败走同一套准入，放不下时在占用解锁次数、核对密码之前回 429；在途尝试的记录不会被清理删掉，记失败和加锁不新建键；任意交错下键数都不超过上限。

审计落库、登录细节、交出权限的统一规则与部署默认值（D-058）：

104. 请求头（User-Agent 等）、请求路径里的非法 UTF-8 字节，按字节截断切开的多字节字符，都不能让登录日志、操作日志、安全事件写不进去：入库前换成 U+FFFD 并在字符边界截断，带坏 User-Agent 的正确登录照常成功。这一行本身写不进去的审计记录（字符集、超长之类的数据错误）不放回去重试，不会一直占着合并队列的位置。
105. 超过 72 字节的密码、没有密码的账号，校验失败前同样做一次哈希比较，耗时和"账号不存在"相当。
106. 改部门时明确给出原负责人，也要他在调用者"查看用户"的范围内；看不到他的人给他的 ID，和给不存在的、范围外的 ID 回同一个错误。保持原负责人靠不给 `leaderUserId`。
107. 代码字典里已不在声明中的遗留项，后台不能改值、不能重新启用（`dict.legacyItem`）；库里即使是启用状态，也不算有效值（`Has` 为假，读接口按停用返回）。
108. 非超管重新启用停用的账号：账号当前启用的角色含敏感权限码、含自己没有的权限码或范围比自己宽的，拒绝（403）；超管不受限；停用账号不受这条限制。停用中的角色从 D-100 起也算，见第 184 条。
109. 只需登录的本人写操作（改资料、换头像、下线其他设备）不排队等全端共用的超管锁；账号被停用、会话被吊销照样作废（第 72 条）。
110. 被限流、被锁定的登录只记安全事件、不写登录日志（限流指来源 IP 的次数用完；账号的次数超限从 D-103 起是要求验证码，没带验证码的和别的验证码失败一样记一行登录日志）；IPv6 的限流和"账号 + IP"计数按 /64 网段；核对密码的并发有上限，满了回 429（登录、解锁先等一小会儿，D-071）、不算失败。
111. release 模式的刷新 Cookie 带 `__Host-` 前缀（`Secure`、`Path=/`、无 `Domain`）；带了多个同名刷新 Cookie 的刷新一律拒绝并记 `refresh_cookie_dup`。
112. 管理员建、改用户时，显示名和简介与本人在个人中心改的同一套字符校验；用户、角色、字典、字典项的排序值有上限（1000000），超出回校验错误。另外：服务端镜像默认 release 模式；示例 nginx 关 `server_tokens`，CSP 加 `object-src 'none'`、`base-uri 'self'`、`form-action 'self'`；新增模块指南的写操作示例放进 `WithActor`，并要求为每条写接口补在途用例；前端头像裁剪等待后核对登录身份、关闭或卸载时作废。

操作日志请求体与本人写操作（D-059、D-060）：

113. 操作日志的请求体每个分支（JSON、表单、其他类型的占位符）入库前都不超过 4 KB、都是合法 UTF-8；占位符里来自 Content-Type 的类型名有长度上限。请求头再长，这条日志照样写进去。
114. 本人写操作锁住自己的会话行再确认有效：吊销已经写下、还没提交时，本人写操作排在后面，吊销提交后回 401、什么都不写。另外：非超管重新启用账号被拒时只回通用错误键；https 下前端"待补退出"标记的 Cookie 名带 `__Host-` 前缀；https 下不带前缀的标记只作"不恢复"的依据：启动时换来的会话在这份标记里就不恢复、也不发退出请求，不在里面就删掉它（D-060）。刷新在途时别的标签页写进这份标记的会话号不跟着一起删、换来的正是它时也不恢复（D-069，壳的 `store.https.test.ts`）。

主体端与三个程序（D-061）：

115. 按主体登录：主体端登录必须带编号（缺了回字段校验失败，不核对密码、不写登录日志）；编号大小写、首尾空白归一化；会话和登录日志写入主体；身份里的主体来自会话，主账号的 `Super` 为真、员工为假；平台端不受影响（`org_id` 为 0）。
116. 编号不存在、主体停用、账号不存在、密码错、拿别的主体同名账号的密码：错误码、提示、出参完全相同；编号不存在也做一次假的密码比较；登录日志分别记原因和输入的编号。
117. 登录防护按"编号/账号名"计数：一个主体的 `admin` 被锁定，另一个主体的同名账号和本主体的其他账号照常登录；编号的大小写、空白变体共用一份配额。
118. 主体停用：刷新立即失败并说明会话已结束（会话被吊销），访问令牌最长一个状态缓存周期后失效，登录失败，别的主体不受影响；核对完密码、建会话之前主体被停用的，登录失败且不留会话；一条语句吊销主体的全部会话。
119. 账号所属主体和会话的主体对不上（数据被改过）、主体端里不带主体的会话：访问和刷新都是 401；主体端的令牌在平台端（以及反过来）一律 401。
120. `scope.ByOrg`：主体 A 的身份对主体 B 的数据列表看不到、详情修改删除都是"不存在"且 B 的数据不变；平台端身份、没有身份时查询直接报错（`scope.ErrNoOrg`），不退化成不加条件；列名只认小写标识符。`scopetest` 模板对漏了隔离条件的列表、详情、修改、删除都报错。
121. 三个程序：代理商、商户程序只有自己端的路由（平台端和另一个端的接口 404），依赖检查保证它们不编进平台和对方的模块、平台程序不编进两个端的模块；它们不执行迁移、不建表，表结构落后时拒绝启动；只加载自己端的配置和密钥，release 模式不要求别的端的密钥；`rbac prune` 不删别的程序的端的策略；迁移 00013 可以重跑，角色编码在主体内唯一。

IP 黑名单与白名单（D-062）：

122. 黑名单：被封的来源连登录、验证码、刷新、已登录的接口、不存在的路由都是 403 `2003`，健康检查照常；被挡的请求不动会话，换个来源照样能刷新；记安全事件 `ip_denied`。到期的黑名单自动失效；同一网段再封是更新有效期；网段宽于 IPv4 /8、IPv6 /16 被拒；不能盖住操作者自己的地址；增删和被拒的尝试都记操作日志。
123. 端白名单：名单不为空时必须包含操作者当前的地址；设好之后这个端的每个接口（含验证码、登录）只放行名单里的来源，别的端不受影响；清空就不再限制。
124. 主体、账号白名单：名单外的来源登录和密码错完全一样（正确密码也一样，不核对密码），登录日志记 `ip_denied`；已登录后换到名单外，接口回 `2003`，刷新回 `2003` 且不轮换、不吊销、不动 Cookie，登出照常；两层都设了都要满足，别的主体、别的账号不受影响。账号白名单的接口按查看、修改用户的范围约束（范围外 404），非超管改不了超管的，改自己的必须包含当前地址。
125. `system:ip:deny`、`system:ip:allow`、`system:user:ip` 是敏感权限，有授权权限的非超管也授不出去；只有查看权限的人改不了名单。
126. 名单的生效：一个程序改了名单，本程序立即生效，另一个程序在刷新间隔（5 秒）内沿用旧名单、过了就跟上；地址规范化（单个地址、IPv4 映射地址、主机位）；每份白名单最多 100 条；被拒的写入不生效。

主体端的授权（D-063）：

127. 角色按主体隔离：同一个编码在不同主体里各有一个；主体 A 的身份对主体 B 的角色列表看不到、详情和权限 404、改名停用授权删除都 404 且 B 的角色不变；分配 B 的角色按校验失败，给 B 的账号、不存在的账号分配角色 404，都不写；没有身份、身份是别的端、主体端身份不带主体时报 `scope.ErrNoOrg`，主体端没有服务器命令的身份（`UserID` 为 0 回 401），平台端身份带主体回 403。
128. 主账号与主体内授权：主账号在锁内按主体行认定为超管（不看身份里的 `Super`），能授敏感权限码；员工授不出敏感权限码和自己没有的权限码，改不了主账号的角色，身份里谎称 `Super` 也没用；`HoldsSuperRole`、`IsLastSuper`、`SuperUserIDs` 在主体端指主账号，`IsSuper` 恒为 false（库里有不属于任何主体的超管角色也一样）；`IsLastSuper`、`CheckEnableUser` 对别的主体的账号回 404，在 `WithActor` 之外调用直接报错；主账号换人、账号被挪到别的主体、主体停用之后，在途的写操作按新状态判断（403 或 401），本人写操作（`WithSelf`）同样 401；主体端身份改菜单调整回 403。
129. 主体行锁：另一个事务拿着主体 A 的行锁时，A 的写操作（授权、`WithActor`）排队，排队期间还没锁任何角色行；B 的写操作不等；排队期间主账号换了人，旧主账号的写操作按锁内的新状态被拒绝。
130. 判定忽略别的主体的成员关系和主体里的超管角色：库里被直接写进一条跨主体的成员关系、或者主体里出现了超管角色，`Allowed`、`Perms`、菜单、用户角色、锁内认定、数据范围都不因此多出任何东西；同样的权限码来自本主体的角色就算数。
131. 登录的加锁顺序：主体端登录建会话的事务先锁主体行、再锁账号行；用户来源没实现 `portal.OrgLocker` 的主体端注册失败。
132. 不同主体的写操作互不影响：两个主体的主账号、员工同时授权、同时给各自的新账号分配角色，反复多轮都不等待、不死锁；主体端的写操作在别的事务里调用（包括拿着别的主体的锁）直接报错，放进 `WithActor` 的多步写操作照常。

主体与主体账号（D-065）：

133. 建主体：编号是前缀加 8 位数字，撞上已有编号换一个再试，一直撞上时报错且不留下半个主体；主账号随主体一起建，库里只有哈希，初始密码只在建的结果里出现一次，用它按编号登录后必须先改密（别的接口回 `2002`），改完初始密码就不能用了；校验失败不留下任何行；迁移 00015 可以重跑，账号名只在主体内唯一。
134. 主体端的用户来源（`Deps.Orgs.Users`）：按编号（归一化后）、按主体加账号名查，账号带上主体；别的主体的同名账号是另一个账号；代理商和商户互不相干（ID 相同也一样）；主体行锁、账号行锁都是排他锁。
135. 停用主体：同一个事务里先锁这个主体的账号行、再吊销这个端这个主体的全部会话（账号行被别的事务拿着时停用排队、排队期间不碰会话行；别的主体、另一个端同 ID 的主体不受影响；外层事务回滚则都不生效），之后登录失败；重新启用不恢复旧会话；代理商停用不影响名下商户。
136. 重置主账号密码：新的随机密码只返回一次、下次登录必须改，主账号的会话全部吊销、员工的不受影响；更换主账号只能换成本主体里启用的账号（别的主体的账号和不存在的一样回校验失败），换完原主账号不再是主体内的超管（它的会话和角色见第 185 条）；平台列出、下线会话只限这个端这个主体，别的会话回 404。
137. 商户归属：只能挂到存在且启用的代理商下；`org.ByChildOrgs` 每次查询现算，改了归属原代理商下一次查询就看不到；没有身份、不是代理商端的身份、代理商端身份没有主体时报 `scope.ErrNoOrg`，列名只认小写标识符。

平台端的代理商管理、商户管理（D-065，用平台程序真实注册的全部模块测）：

138. `partner:agent:owner`、`partner:merchant:owner`、`partner:merchant:transfer` 是敏感权限，有授权权限的非超管授不出去，别的照常能授；只有查看权限的人对每个写接口都是 403，只有商户权限的人看不到代理商；没有日志权限看不到日志。主账号的初始密码（开户、重置）只出现在那一次响应里：详情、列表、账号列表、操作日志（请求体、查询串、错误摘要）里都没有。
139. 主体端的日志：平台按端看到这个端的全部（包括编号不存在的失败登录），按编号只看到这个主体的，编号不存在时结果为空而不是全部，另一种主体的编号查不到；看不到平台端、另一个端的日志；每次查看都记操作日志。
140. 两个模块的每一条写接口都在锁里重新认定操作人（第 72 条的同一做法，按两个模块的路由表遍历、双向核对）：操作人途中被停用回 401、主体表、账号表、IP 名单、主体端的会话都不变；同样的请求由正常的超管发出确实会写。
141. 两种主体、不同主体互不相干：代理商的接口按代理商表查，商户的 ID 在那里是 404；列出、下线会话只限 URL 里的这个主体（别的商户的、代理商端同 ID 主体的会话 404 且不被吊销）；停用只吊销这个商户的会话；更换主账号不能用别的商户的账号；清空 IP 白名单只清这一个主体的；改归属、按代理商筛选、选代理商。
142. 运维中心按"端"筛选（D-066）：错误日志、安全事件不带参数和以前一样只看平台端和不属于任何端的；选代理商端、商户端只看那个端的，详情看得到；别的端照旧看不到（列表 `3001`、详情 404）；调查时间线同样先选端。

代理商端、商户端自己的后台（D-067，`core/orgportal` 对两种主体各跑一遍，只注册这一个主体端的程序）：

143. 主账号保护：员工有全部账号、会话的权限，也改不了主账号的资料、状态、角色，下线不了主账号的会话，库里不变；重置密码只有主账号能做（员工的也一样）；主账号自己的密码主体内谁都重置不了，主账号也停不了自己；主账号重置员工密码后新密码只出现在那次响应里、必须改密、员工的会话全部吊销；停用员工时同一个事务吊销它的全部会话；员工重新启用不了带着自己分配不了的角色的账号（D-058），主账号可以。
144. 主体内授权：非主账号授不出自己没有的权限码，敏感权限码（`account:create`、`account:assign-role`、`role:grant`）自己有也授不出去，主账号能授；建账号时顺手分配角色要有分配权限（没有时账号也不建）；分配的角色不能带自己没有的权限码，给自己分配带敏感权限码的角色也不行；别的主体的角色按不存在回校验失败，什么都不写。
145. 跨主体：主体 A 的主账号对主体 B 的账号、角色、会话做详情、修改、启停、重置、分配、授权、删除、下线一律 404，库里不变；账号、角色、会话列表和登录日志、操作日志只有本主体的；查看日志本身记操作日志。
146. 套件的每一条写接口（含个人中心）都在锁里重新认定操作人（第 72、140 条的同一做法，按路由表遍历、双向核对）：拿到主体行锁（个人中心：账号行锁、会话行锁）之前操作人被停用并吊销会话、只被停用（会话没吊销）、只被吊销会话（账号仍启用）三种情况分开各走一遍，都回 401、账号、角色、授权、会话、IP 名单都不变；只收回员工的角色（账号、会话都还有效）时需要权限码的每一条回 403、不写，角色还在的员工（同样的角色）发同样的请求确实会写；只有主账号能做的接口（重置密码、IP 白名单、IP 黑名单新增与删除）在途中主账号被平台换掉时回 403、不写；同样的请求由正常的主账号发出确实会写。
147. 主体参数守卫：主体端每条非公开路由（套件、`/auth/*`、`/dicts`）查询串带 `orgId`、请求体带 `merchantId` 一律 3002、键 `scope.orgParam`，被拒的不写；大小写、`_`、`-`、`.`、方括号（`orgId[]`、`org[id]`）、复数、JSON 任意一层（数组里的对象也算）、Unicode 大小写折叠（长 s）、表单和 multipart 的字段名都查；请求体不论标成什么类型（`text/plain`、表单、没有或对不上分隔符的 multipart、不带 Content-Type）都按 JSON 再查一遍；值里出现这些字、键只是相近（`orgCode`）的照常通过。
148. 概览只给调用者有查看权限的计数、不含平台写的备注；主体的 IP 白名单只有主账号能看能改，必须包含主账号当前的地址，名单外的来源回 `2003`；个人中心只读写本人（改显示名的字符校验、内置头像只认名字表里的、下线其他设备保留当前会话）。
149. 代理商只读名下商户（`modules/agentportal`）：列表、详情只有 `agent_id` 是自己的商户（别的代理商的、直属平台的 404），不含平台的备注、排序、主账号；商户换了代理商，原代理商下一次请求就看不到；代理商端 `/merchants` 下只有 GET；请求里带 `merchantId`、`agentId` 回 3002；没有查看权限的员工 403、概览里也没有这项计数。
150. 密码计算的并发上限一个进程只有一个（D-068）：位置占满时登录（等够时限之后）、`auth.Service.HashPassword`、`org.NewInitialPassword` 都回 429，让出之后照常，算完哈希位置让出来；平台建用户、重置用户密码、开主体、重置主账号密码先做便宜的检查（字段不合法、目标不存在的不去算哈希），位置满了回 429、什么都不写（用户没建、密码没换、会话没吊销）。
151. 主体端建子账号、重置员工密码按主体限次（D-068）：字段不合法、密码不合策略、登录名已被占用、目标不在本主体、目标是主账号的请求一次哈希都不算、不占次数；一个主体有 2 个在算时第 3 个（两条接口都算）立即回 429、不算、不写，别的主体照常进得去，放行后停着的请求照常完成、两个位置都让出来（再来一轮照样进得去 2 个）；一个主体两条接口合起来一分钟 30 次，第 31 次回 429、不算、不写，别的主体不受影响；被进程闸门挡回来的那一次不占主体的次数。
152. 分配角色、重新启用角色被拒时的回显（D-069）：含敏感权限码、含自己没有的权限码、数据范围比自己宽三种原因，没有"查看角色"权限（声明了 `RoleView` 的权限码）的操作人只得到 `rbac.role.notAssignable` 和角色 ID，响应里没有权限码、资源、范围，原因写进服务端日志（warn）；有查看权限的照旧看到 `rbac.role.holdsSensitive`、`holdsNotOwned`、`widerScope` 和细节；两种人都分配不出去、启用不了，库里不变；一次带几个角色时每个被拒的各一条；没有声明这类权限码的端非超管一律按看不了算。平台端、主体端各有一条经 HTTP 的用例。
153. 连接池上限的启动校验（D-069）：`database.maxOpenConns` 是 0 或负数、`database.maxIdleConns` 是负数时配置校验不通过（程序拒绝启动），用环境变量改成 0 也一样；上限 1、空闲连接数大于上限都合法。

密码哈希换成 Argon2id、密码计算的位置怎么分（D-070、D-071）：

154. 新哈希是 Argon2id、格式是通行的那种：参考实现算出来的同参数哈希核对得上，每次的盐不同；升级前的 bcrypt 哈希（含 `$2b$`、`$2y$`）照常核对并被标记为该重算，cost 超过 14 的不算；格式不规范（段数、版本、参数顺序、前导零、正负号、带填充或有多余位的 base64、别的字母表）、带换行的、参数或长度超出上限（内存 64 MiB、轮数 8、并行度 4）的哈希一律不匹配、不重算，而且不按哈希里写的参数去算（一秒内返回）；上限以内、参考实现用别的参数（64 MiB、3 轮、并行度 4）算的哈希核对得上、被标记为该重算。
155. 登录后升级：旧哈希登录成功后换成当前参数的 Argon2id，只换哈希这一列（"必须改密"、改密时间不动，会话照常）；密码错的不换；已经是当前参数的不重写；参数不是当前的 Argon2id 哈希也升级；核对之后密码被别人换掉了，升级的那份不盖掉新密码；途中被另一个登录升级了（哈希变了、密码没变）的登录、解锁、改密照常成功、不记失败，途中真的被重置的照旧按密码不对处理，不是升级造成的哈希变化不重算；用户来源没实现 `PasswordRehasher` 的不升级、照常登录。平台用户表、两种主体账号表各有一条连库的用例；建账号、重置密码、本人改密写进去的都是 Argon2id。迁移 `00016` 把 100 个字符的列加宽到 255、已有的哈希原样留着、可以重跑，参数取到上限的哈希也放得下。
156. `server.passwordParallel`：默认 0（按 CPU 数），负数和超过 256 的值拒绝启动，可以用环境变量设；配了就按它建位置。
157. 位置分两类：后一类（本人改密、`HashPassword`、`NewInitialPassword`）最多占一半，满了立即回 429、什么都不变；前一类（登录、解锁）可以用全部，满了等到让出来的位置就照常做，等够时限才回 429；被挡的不占解锁次数、改密次数；让出来的位置优先给已经在等的，同时在等的人数有上限；登录、解锁、改密走到事务里等行锁时位置已经让出来了。
158. 同一个账号 15 分钟内成功改密 5 次之后回 429、密码不换、连旧密码都不核对、记安全事件；没改成的不占次数；换一个会话也一样；别的账号不受影响；被要求改密的那一次照常能改、不计数；过了 15 分钟重新计。
159. 主体端按主体限并发：一个主体 2 个改密在途时第 3 个立即回 429、密码不换、不占改密次数（之后输错 4 次还能改成），2 个解锁在途时第 3 个立即回 429、不占解锁次数；别的主体照常进得去；平台端三个人同时改密都占得到位置、都做得成。
160. 算法可以选 bcrypt：`server.passwordHash` 默认 `argon2id`，可以设成 `bcrypt`、可以用环境变量设，别的值（空、大小写不对的）拒绝启动；选 bcrypt 时这个程序写进去的新哈希（后台生成的、主体的初始密码、本人改密）都是 bcrypt，库里的 Argon2id 哈希照常登录、登录后换回 bcrypt，已经是选的算法和当前 cost 的不重写；两种哈希器都核对得了两种哈希；选 bcrypt 时"账号不存在"的假比较和一次真实的 bcrypt 比较耗时相当。

PBKDF2 的两种、主体端后台按库核对（D-072、D-073）：

161. `server.passwordHash` 还可以是 `pbkdf2-sha256`、`pbkdf2-sha512`，可以用环境变量设；不带摘要算法的 `pbkdf2`、SHA-1 的、SHA-384 的、大小写不对的拒绝启动，报错里列出可以取的值；配置可以取的值和哈希器认识的算法是同一组。选了之后新哈希是选的那一种、生产用的次数（60 万、22 万），盐 16 字节、输出 32 字节、格式 `$pbkdf2-<摘要>$i=<次数>,l=<字节数>$<盐>$<输出>`，每次的盐不同；另一套实现算出来的同参数、不同参数（次数、盐和输出长度不一样）的哈希都核对得上，参数不是当前的（次数、盐的长度、输出的长度有一样不同）被标记为该重算；格式不规范的（段数、参数的名字和顺序、前导零、正负号、带填充或有多余位的 base64、别的字母表、带换行）、摘要算法不认识的（SHA-1、SHA-384、没写）、参数或长度超出范围的（次数 0 或超过 200 万（SHA-512 是 100 万）、盐不到 16 或超过 64 字节、输出不到 16 字节或超过摘要长度、`l` 和输出的实际长度不一致）一律不匹配、不重算，而且不按哈希里写的次数去算（一秒内返回），边界值本身照常解析；"账号不存在"的假比较和一次真实比较耗时相当，超长密码、没有密码的账号、看不懂的哈希也花一次假比较的时间。四种算法两两之间：哪个哈希器都核对得了别的算法的哈希，不是自己选的那种（PBKDF2 的两种之间也算）就该重算。库里的 Argon2id、bcrypt、另一种 PBKDF2 的哈希照常登录、登录后换成选的那种，密码错的不换，已经是选的算法和当前次数的不重写，本人改密写进去的也是选的那种；换回默认配置后 PBKDF2 的哈希照常登录、换回 Argon2id。在 Go 的"只许批准的算法"模式（`GODEBUG=fips140=only`，另起进程）下，两种 PBKDF2 的生成、核对、假比较都做得成，另一套实现的哈希核对得上，范围边界上的哈希（盐 16 字节、输出 16 字节）也算得了。
162. 按库核对的路由（D-073）：`LiveAuth` 分组和它的子分组下需要登录的路由标上了（读、写、带参数的路径、分组的根路径都算），公开路由、同一个路径上用原来的分组注册的别的方法、认证接口都没标；没有变化时，标了的路由每个请求读一次库，没标的读一次之后走缓存，标了的路由读到的和缓存一样时缓存照常留着。别的程序改了库（不经过本程序的认证器）：会话被吊销、账号被停用、被删掉、被要求改密，标了的路由下一个请求就拒绝（401、401、401、2002），没标的路由在这之前照旧放行，在这之后（缓存被清掉）也拒绝——读库期间缓存正好有过别的失效（这次读到的不写回缓存）时也一样；读库出错（账号、会话表）回 503，不当作通过。缓存的 `Drop`：条目在就删、代数加一，不在或已过期时什么都不做、代数不变。主体端后台套件：`/org/` 下每一条路由（读和写）都标了、认证接口没标；按路由表遍历每一条读接口（用例表和路由表双向核对），平台停用主体、吊销主账号的会话、重置主账号密码之后回 401，更换主账号之后原主账号的会话已经吊销、所有路由回 401（D-101；重新登录后是普通账号，要主账号的路由 403）、换上来的主账号立即能看只有主账号能看的内容，账号在库里被停用回 401、被要求改密回 2002；每种改动之前同一个令牌访问 `/auth/me` 照旧放行（缓存是热的），之后 `/auth/me` 也按新状态处理。代理商端名下商户的两条路由同样标了，平台停用代理商、只吊销会话、只停用账号之后下一个请求都回 401。有角色的员工（权限码按角色判断）账号在库里被停用、会话被吊销之后，要权限码的读接口同样立即 401。

可选的 Redis（D-074）：

163. 连接：默认不配（`redis.addr` 为空），这时别的 `redis.*` 写什么都不检查、程序不连 Redis；配了才校验（地址、库号、键前缀的写法、用户名和密码、启动等待），每一项都能用环境变量设、都传到连接上（用户名、密码、库号、TLS、前缀、启动等待），写错了拒绝启动、不当成没配。连上真的 Redis：可用、读得到版本、键带配置的前缀、读写正常；键不存在、普通的错误回复、调用方自己的错误原样返回，不把 Redis 标成不可用。服务器版本低于 6.0 拒绝启动（错误里说明版本和最低要求），6.0 起都接受，版本读不出来（格式不认识、不让查、没有查的权限）照常用；握手只发 `HELLO 2`（被拒绝后是 `AUTH`、`SELECT`），探测发 PING、INFO、一次试写，并在实际通知通道验证 SUBSCRIBE、订阅 PING 和 PUBLISH（D-075），不发客户端标识之类的可选命令；可用时停止后台恢复探测，订阅空闲时仍用 PING 确认连接；没设密码不发 AUTH，库号 0 不发 SELECT；开了 TLS 不会用明文去连。服务器明确拒绝这份配置（要密码没给、密码不对、没有权限、库号不存在、没设密码却发了、账号写不了、6.0 之前的服务器不认带用户名的登录——这一种另外说明多半是版本太低）拒绝启动，错误里带服务器的说法、提示该检查的配置、不带密码；暂时用不了（正在加载、只读、内存满、落盘出错）照常启动、好了自动恢复。启动时连不上不拒绝启动：按不可用处理，操作立即返回"不可用"、不碰网络，后台探测到它起来了自动恢复；`connectWait` 大于 0 时在这段时间里反复试，试到了按正常连上处理，到点还连不上照常返回。运行中出一次网络错误、或者服务器回"我现在用不了"的那几类错误，就标成不可用，之后立即返回，恢复后自动标回可用（探测通一次不算，要连续通两次）；回调只在"从不可用恢复"时调用、每次恢复一次，回调 panic 不影响后面的回调和下一次恢复；每次断开和恢复各记一条日志。Redis 假死（连得上、不回话）时一次操作在时限内返回并标成不可用。连接池里的连接被悄悄丢掉之后，靠换一个新的连接池在几个探测周期内恢复，不是一个坏连接一个坏连接地试。时好时坏：刚恢复又出故障的，探测前等得越来越久、有上限，稳定够久之后重新从头算。请求自己被取消（调用前、等回复时）、请求自己的时限到了（哪怕读超时先返回）不算 Redis 的问题。开了 TLS、服务器的证书验不过：运行中遇到算故障（标成不可用、证书好了自动恢复），启动时遇到按不可用处理。换过连接池、已经恢复之后，旧池子上迟到的错误不把新的标成不可用。探测不通的日志：同一个原因在一次故障期间只记一次（两个原因来回换各记一次，只差本机临时端口的算同一个），最多 8 种，恢复之后再出故障重新记；不可用的时候关闭，不把"被关闭打断"记成故障。查版本时连接断了不算探测通过。启动时没连上、后来连上的是个老版本：一直按不可用处理、同一个原因只记一次，换成够新的版本后恢复。密码不进日志和错误信息；服务器把密码回显在错误里时，日志（启动、故障、探测、读不出版本）和返回的错误里都换成 `***`，错误的分类不变。程序停止时关闭连接，重复停止不出错。

164. 缓存失效通知：真实 MySQL 与 Redis 上的两个实例使用同一个前缀，冻结测试时钟，不靠 TTL 使断言通过。会话锁定、解锁、吊销、改密，账号资料和启停，授权变更、菜单、字典和 IP 名单传播；会话按号失效，不影响别的会话；平台未注册主体端的认证器时，更换主账号、重置主账号密码、停用主体仍传播到主体端，别的主体照常访问。外层事务提交前与回滚不通知，平台账号编辑和本人资料也遵守；提交后通知只清接收方内存，不立刻读库、不再次发布。收到通知前已经读到的旧账号、字典、授权和 IP 数据不得回填，IP 序号读取中的失效不能被覆盖。不同前缀互不影响；自己的消息、空消息、未知版本/类型/字段、尾随 JSON、非法端或键、超长消息忽略。订阅空闲正常心跳，不重订阅、不反复清缓存；接收和等待 PONG 时消息都能送达，客户端隐式重订阅确认同样清缓存。订阅断线与假死进入连接层故障状态，不可用期间不自行拨号、不清缓存、不增加请求查库；恢复与重新订阅后清缓存，补上断线期间漏收的变更。缺少发布、订阅或通道权限拒绝启动，密码删隐；旧池迟到的错误不影响新池。取消和关闭退出接收，应用重复停止不报错。

165. 共用计数：真实 MySQL 与 Redis 6.0 上的两实例合计登录 IP 限流和账号的请求次数（后者超限是要求验证码，D-103，见第 187 条）、分散失败后的验证码要求、组合锁定与跨 IP 账号锁定；锁定优先于限流，成功只清组合失败，解锁不占登录次数且失败共享、成功不清登录失败。用户名大小写与首尾空白、主体编号、IPv6 /64 与 IPv4 映射地址归一化；不同前缀、端、主体和用途隔离。验证码生成次数、同会话旧密码尝试、同账号成功改密、主体后台密码操作合计受限；强制改密免计、便宜检查和未计算退款保留。外层事务提交前不清失败和尝试，回滚不占成功改密次数，提交后两实例一致。窗口到期、失败时间点乱序、并发准入、容量满时已存在失败/锁定保留且不逐出、结束不新建；共享尝试租期释放及迟到收尾不影响新记录；GC 每次有界。退款和清零幂等、旧代数不改新代数，本地影子与并发预留不漏计。断线和假死按本地影子回退，不变成逐请求查库；恢复保留共享计数，故障期间在内存准入的尝试，恢复后收尾不改共享状态。逐项缺少 EVAL 或内部命令权限拒绝启动、运行中撤权后不半写业务状态，反复失败探测残留有界且不影响正常客户端，恢复后计数与容量继续工作；错误删隐。未配 Redis 的原内存路径保留。第 98、103、108、150、151、157–159、163、164 条相关断言保留。

166. 验证码答案共享：真实 Redis 6.0 上多个生成器与真实 MySQL 上的两个应用实例验证 A 生成、B 登录及反向操作；HTTP 只返回原有 ID 和图片字段。正确答案仅成功一次，答错及空答案同样消费，两个实例并发验证最多一次成功；过期时刻、未知/畸形 ID 拒绝。跨端及不同前缀隔离，跨端尝试不消费原端答案。共享答案不留可验证的本地副本；容量满删除最早到期的记录，并发生成不超容量，过期清理每次有界，闲置容器自动过期。断线和假死时共享答案不改走本地，新生成的本地答案只在原实例可用；恢复后本地答案不复制，共享答案再次跨实例可用。真实 Redis 已写入但回复丢失时使用新的本地 ID，已消费但回复丢失时不能重放，均不重试。请求取消/超时不放行、不生成本地回退答案，调用方超时不把 Redis 标为故障。逐项缺少脚本命令权限拒绝启动，运行时撤权不半写有效答案，反复失败探测残留有界且不影响正常客户端，恢复后可继续验证。原本地生成、图像尺寸与内容、随机数字、一次性和容量测试保留，相关第 98、163–165 条继续通过。

167. 独立进程冒烟：两个不同 PID、不同实例标识的应用通过真实 HTTP、同一临时 MySQL 库和 Redis 前缀验证 A 生成 B 登录及反向操作、消费后不得重放。两边先缓存授权，B 撤权后 A 收到通知并拒绝；应用时钟冻结，不能依赖 TTL 到期。失败分别发生在两进程时合计达到阈值，两个进程都返回锁定；一进程退出后另一进程仍能使用已有会话和完成新登录。测试控制操作只走进程管道，不增加生产 HTTP 接口。Compose 校验两个实例配置相同、只由一次性任务迁移、只公开 nginx 端口，nginx 配置可加载且不重试已发送的非幂等请求。

168. 三端交付：后端镜像覆盖三个入口及共用命令行依赖，只编译所选端，非法构建参数拒绝；每个镜像包含实际依赖许可证。三套前端独立构建。三端 Compose 只向后端注入本端 JWT 密钥和来源，统一在平台迁移成功后启动。nginx 域名只能为三个互不相同的有效名称，非法名称启动前拒绝；各域名只返回本端静态文件、SPA 回退及 API，其他端 API 和未知域名 404。真实镜像中验证三个端登录、改密、刷新、登出和跨端令牌拒绝。

169. 主体入驻：代理商/商户分别默认关闭；公开申请检查来源、JSON 白名单、一次性验证码和每小时来源限流。邀请仅代理商主账号可管理，凭证摘要存储、有效期 7 天、仅消费一次，跨主体、过期、撤销的拒绝；事务回滚不消费。待审不创建账号；审核需平台敏感权限，WithActor 中重新认定会话，申请行锁保证并发只开通一次；驳回与通过终结后不可重审。通过后的主账号必须改密，密码不出现在列表或日志。

170. 会话历史清理：只删除指定端中过期或吊销严格超过 30 天的记录，精确边界、有效会话（包括创建已久但仍有效的会话）、近期失效会话、其他端和日志保留。每次最多两批各 500 行，重复执行可排空；多个实例并发执行和外层事务回滚保持正确。数据库失败、超时及取消不影响业务，后续可继续清理；失败不阻止其他端。仅应用启动成功后运行，空闲/有积压/失败的等待间隔正确，取消和 Stop 等待任务结束，重复停止安全；索引迁移可重跑且保留数据。

171. 固定窗口本地清理：`Allow` 和 `Reserve` 在满表时单次最多回收 128 条过期记录，到期边界立即可用；活跃键保留原次数并可继续使用剩余配额。分批回收后容量仍有硬上限，重复重置不累积索引节点；过期回收、容量复用后的旧凭据不能退款或清掉新窗口。并发准入、到期和重置保持计数与容量一致；真实 Redis 下的本地影子沿用同样上限，原共享计数和故障恢复断言保留。调用方截止时间已到而 context 取消定时器尚未执行时仍拒绝准入，不消耗或回退到本地配额。

172. 操作日志预读的请求体边界：无 Content-Length 的请求超限时，在后续路由中间件和处理函数之前返回唯一的 413 / `4013` 信封，保留请求 ID 和响应语言；JSON、表单及其他类型一致。小于或等于上限的内容完整回放，空请求照常；读取不超过上限加一个判定字节并关闭原请求体。包装的 MaxBytesError 仍识别为超限，其他读取错误保留原处理方式，不回放残缺内容。通过守卫的超限请求记录一次失败操作日志，状态、业务码、请求 ID 与响应一致且不保存原始片段，业务状态保持；未通过守卫的不产生操作日志，正常操作继续生效。

173. 就绪检查：同实例首次与到期请求才检查数据库，成功和失败结果均从检查完成起缓存 1 秒，精确到期后刷新；多实例独立。并发探针只触发一次检查，发起方或等待方取消不影响其他请求、不写入取消导致的失败缓存，已取消请求不触发检查；检查最多 2 秒，超时后缓存失败，到期可恢复。App.Stop 取消并等待在途检查、重复停止安全，停止后不再检查。缓存命中仍生成各自的请求 ID、语言和 no-store 响应，200/503 不泄露底层信息；真实 MySQL 连接池繁忙时失败、释放后恢复，`/healthz` 始终不因数据库繁忙失败。

174. 入驻前端：代理商与商户通过实际请求客户端发送申请时均带 `X-GA-Client: web` 和 JSON 请求体；即使当前存有访问令牌也保持匿名，不读取或发送令牌、不改登录状态。申请字段与返回的申请编号正确传递。

175. 会话下线与管理写入：平台、代理商、商户的管理事务先取得操作人账号/会话锁时，单会话下线与下线其他设备都须等事务结束；等待取消不提交吊销，管理事务回滚也释放锁。下线完成后，直接角色服务和嵌套 `WithActor` 路径均不得再凭旧会话提交；平台已有普通读快照不影响锁定读认定已提交的吊销。主体会话列表同时要求会话行与账号行属于当前主体，分页和总数排除矛盾记录，主体 ID 缺失不退化为全端查询。

176. 两主体端数据中心与安全设置：菜单首位为数据中心；主账号可见黑白名单，员工即使拥有全部已声明权限也不能访问名单接口，菜单移动不能绕过 SuperOnly。主体黑名单的列表、新增/续期、删除限定端和主体，跨范围 ID 为 404，拒绝请求指定主体、自锁和超宽网段；登录、刷新和已有会话被拒，其他主体/端不受影响，过期、容量、回滚、跨实例传播和解除有效。新增写路由沿用第 146 条三种身份失效/主账号更换复核，新增读路由沿用第 162 条按库认证。数据中心以只读快照聚合本主体数据，验证日期/时区和参数边界，活跃账号排名按本次快照中的账号查看权限收窄到本人，数据中心权限撤回后拒绝；两端前端验证名单独立页面、参数契约和数据中心异步切换。

177. IPv4 通配网段（D-091）：完整四段、连续后缀星号可以表示 `/24`、`/16`、`/8`；单 IP/CIDR/IPv6 保持兼容。错误段数、前导零、数字溢出、星号混用、非连续星号和带通配的 IPv6 拒绝。等价通配/CIDR 去重、匹配边界、自锁与最宽黑名单约束不变；两主体端黑白名单 HTTP 和前端输入契约覆盖通配。

178. 主体名单和诊断边界（D-092）：含过期记录的单主体黑名单最多 1000 条，上限拒绝新增但允许续期和删除；生效仍限 100 条，不影响其他主体。快照只读取有效黑名单。普通菜单移入主账号专用目录被拒绝或从不相容的历史覆盖回退。严格 JSON 拒绝尾随非空白内容；仅 Cookie 不能认证主体业务接口，备注和排名按纯文本渲染。SQL 诊断、请求错误日志和数据库错误审计不含驱动回显的具体值，保留错误编号/类别；三份生产 Compose 的各服务都有 10m × 3 容器日志轮转配置。

179. JSON 接口的请求类型与操作日志（D-095）：`BindJSON`、`BindJSONStrict` 对没有声明成 JSON 的请求体（表单、`text/plain`、multipart、不带类型等）不解析、回 `3002`，`application/json`（可带参数）和 `+json` 结尾的类型照常。改密、建用户、授权用这些类型发 JSON 时都不执行，操作日志里不出现请求体里的密码（包括 URL 编码之后的）。声明成表单的 JSON 请求体按 JSON 脱敏，不合法的、JSON 值后面还有别的内容的只存占位符；不像字段名的键、过长的键不写原文；超长的字符串值、被截断的请求体都带长度标记，且不超过 4 KB、是合法 UTF-8（原文不是合法 UTF-8 时先整理再量长度）。

180. 头像的 JPEG 结构检查（D-096）：段标记位置上的 `FF 00`（紧跟文件开头、两个段之间、填充字节之后、长度盖住后面扫描段的）一律按读不懂处理，不解码，回 `system.avatar.type`；熵编码数据里的 `FF 00`、重启标记和填充字节照旧，正常图片照常处理。

181. 会话与日志的四处边界（D-097）：会话号只认 32 位小写十六进制，大写、带空白、全角等库里能匹配到同一行的写法，查归属、锁会话行和吊销都按不存在处理、会话不动，平台端、主体端和代理商、商户管理的下线接口对这些写法回 404，规范写法吊销后下一个请求立即 401。别的程序直接在库里吊销的会话，状态缓存到期（一个缓存周期）后即被拒绝，中途回写最后活动时间不延长这段时间。只知道会话号、凭证是编的刷新请求，不论来源在不在 IP 白名单里、账号是否停用，得到的都是 401，不产生"IP 不允许"的记录，会话不被吊销；真凭证在名单外仍回 `2003`、不轮换。主体端有 `oplog:list` 的员工看得到改 IP 白名单、黑名单的记录，但请求体是 `***`，主账号照常看得到；别的操作的请求体不受影响。

182. 启动配置的提醒与限制（D-098）：release 模式下 `server.trustedProxies` 里有覆盖全部地址的网段（`0.0.0.0/0`、`::/0`、`::ffff:0:0/96`）拒绝启动，别的网段和 debug 模式不受影响。debug 模式监听非回环地址时启动日志里有一条 WARN，说明放宽了什么、怎么切到 release；只听本机或 release 模式时没有。

183. 六处输入与显示边界（D-099）：主体端个人中心的返回里没有 `remark`、`sort`、`status`，账号列表照常有。代理商端的邀请菜单只下发给主账号；重复撤销同一条邀请返回成功且不改第一次的撤销时间，不存在的仍是 404。角色名、字典名称及其各语言文字、字典项显示文字及其各语言文字含零宽空格、从右到左覆盖符、换行、字节顺序标记时回 `3001`，被拒绝的不写入；零宽连接符、零宽非连接符（孟加拉语等文字的正常拼写）照常接受；扩展值照常可以换行。日志查询的 `from`、`to` 换算成 UTC 后年份超出 1–9999 时当作没传、不出 500、不产生错误日志（平台端、主体端和代理商、商户管理的日志接口都一样）；时间线游标超出范围回 `3001`。命令行重置密码对带重音、全角的登录名写法按不存在处理、密码不变，大小写和首尾空白照旧归一化。

184. 角色启停与账号启用（D-100）：非超管停用数据范围比自己宽的角色、含敏感权限码的角色回 403（回显同第 152 条），角色仍是启用；只改名字不动状态照常可以；自己分配得出去的角色可以停用也可以再启用；超管不受限；主体端同一条规则，员工受限、主账号不受限。账号身上有停用中的、操作人分配不出去的角色时，非超管重新启用这个账号回 403（只有通用错误键），账号仍是停用；停用中的角色是自己分配得出去的可以启用；角色恢复后那个账号仍是停用，超管可以启用。

185. 更换主账号（D-101）：同一个事务里原主账号的会话全部吊销（原因 `admin`）、角色清空，账号仍在、仍是启用；新主账号、同主体别的账号、别的主体、另一个端同 ID 的账号的会话和角色都不动；角色本身还在。换不成的（别的主体的账号）和换成自己都什么不动。清角色失败时整个更换回滚。`ClearUserRoles` 不在事务里调用回错误、回滚的不生效、提交后授权判定立即没有这些权限；有超管角色的端不接受（本进程注册了的看是不是主体端，没注册的看库里这个端有没有超管角色）。

186. 主体黑名单的平台入口与容量（D-102）：平台 `GET …/:id/ip-deny` 只返回这个端这个主体自己设的黑名单（含已过期的记录，分页），全局名单和白名单不在里面；主体不存在 404；查看要列表权限，清空要主账号管理权限，商户的权限不管代理商。`DELETE …/:id/ip-deny` 删掉这个主体的全部黑名单记录并返回条数，别的主体、另一个端同 ID 的主体、全局名单、这个主体的白名单都不动，被拒的请求什么都不动，成功的清空记操作日志；清空立即生效，回滚的不生效。容量：一个主体写满 100 条时它自己加不了、全局和别的主体照常；全局名单到 10000 条时全局加不了、主体照常；主体的条目不占全局额度。

187. 账号的登录请求次数（D-103）：同账号一分钟内第 11 次请求不回 429，而是必须带验证码，带上有效验证码照常核对密码，验证码错的不放行；换了窗口重新计。一个来源不带验证码对着某个账号持续请求（它自己三次失败后每次都要验证码），另一个来源用正确的密码直接登录成功、不多出验证码，全程没有按账号次数的限流事件。没通过验证码的请求退回它占的那一次：只退一次（重复调用、之后再结束都不再改次数），窗口换了之后退回不减新窗口的次数，没有记次数的准入（锁屏解锁）退回不新建记录。来源 IP 的次数用完仍回 429、不写登录日志、只记合并的安全事件。内存实现和两个实例共用计数时结果相同；过了验证码的失败照样累计到锁定。

188. 刷新凭证的家族密钥（D-104）：登录下发三段的凭证，库里只有家族密钥的哈希；轮换不改家族密钥。同一份凭证被另一方连刷两次之后，原来的持有人再拿它刷新：会话整体吊销（原因 `reuse_detected`），双方的刷新凭证和访问令牌都不再可用，记 `refresh_reuse`、不记 `refresh_mismatch`，响应不改 Cookie。家族密钥是编的（连同编的 secret、两段的编的 secret、当前 secret 配编的家族密钥、当前 secret 不带家族密钥）只回 401、不吊销、不改 Cookie，记 `refresh_mismatch`，合法持有人照常轮换。没有家族密钥的会话用两段的凭证照常刷新，第一次成功刷新后库里有了家族密钥、Cookie 换成三段；升级那一刻在途的两段凭证在宽限期内是 409；它落到两代之外之后只拒绝、不吊销；升级之后的三段凭证落到两代之外则吊销。上一个凭证配了编的家族密钥仍按上一个值处理（宽限期内 409）。会导致吊销的旧凭证也排在 IP 白名单的检查之后：名单外的来源出示它得到 `2003`、会话不吊销，名单内的来源出示才吊销。Cookie 的解析只认三段或两段、各段是规范的十六进制。

189. 锁屏与后台写事务（D-106）：`WithActor`、`WithSelf` 和本人改密在会话行锁内核对未锁屏；认证已通过但锁屏先提交的写操作回 423 / 1006，业务回调不执行、密码不改变。写事务先取得会话锁时，锁屏等待它提交或回滚，三个内置端均按同一顺序处理。锁屏后 `LockActive` 仍报告有效会话，`LockWritable` 拒绝；解锁后后台写入恢复。查看本人、解锁、登出、刷新保持可用。

190. JSON 文档绑定（D-108）：两种 JSON 绑定和通用 Bind 的 JSON 分支都拒绝未知结构体字段、重复对象键、指向同一结构体字段的大小写别名、嵌套对象/结构体数组中的重复字段、尾随文档和超过 64 层的嵌套；被拒文档不进入业务写入。岗位原始数组超过既有 20 项上限也拒绝，不能靠重复 ID 去重绕过请求容量；普通 map 中不同大小写的业务键仍可以并存；合法字段、既有 binding 校验、JSON 类型与 BodyLimit 错误处理保持有效。

191. 账号部门与实际范围（D-109）：调整用户部门逐个保持用户资源各权限码的覆盖边界；合法同范围移动允许。新建、分配和部门变更时，相对部门角色按目标部门展开，范围不能超出操作人在同码下的实际覆盖部门；停用角色也检查，超管按原契约允许。拒绝时账号部门、角色和资料保持原状态。

192. 角色状态与显示写入（D-110）：新建默认启用，更新省略 status 在角色锁内保留现值；显式启停仍遵守既有双向检查。空角色创建和仅改名字/排序/备注不触发授权发布屏障；首次授权/分配和显式状态变化正确发布。

193. 验证码与刷新准入（D-112）：无客户端头、跨站 Fetch Metadata 或非法 Origin 的验证码请求被拒且不占验证码配额；同源 GET 仅带客户端头可用。刷新按来源窗口在数据库读取前限制，超限 429、不动 Cookie；未知或其他端会话不进入轮换事务。正常轮换、家族密钥、旧凭证、跨标签页重试与 Redis 降级保持原契约。

194. 共享名单与简要选择（D-113）：触发请求已取消时，独立时限内的名单序号查询/刷新仍完成；失败短暂退避且 CAS 不覆盖后来的成功检查或失效通知。主体简要选项只按编号/名称匹配，联系人和电话不影响结果；仅返回公开简要字段，数量有界。

195. 记录边界（D-114）：部分查询解析失败仍保存有效筛选并标明不完整；查询截断带长度标记、脱敏仍有效。安全事件合并键使用路由模板或固定未匹配值、方法类别、已注册端和 IPv6 /64；匿名新键额度用完不影响已认证事件预算，严重事件保持优先，合并次数正确。

196. 授权容量与索引（D-115）：角色/端判定器保持 exact/keyMatch 语义与并发读，其他主体策略不进入目标角色扫描集合；只加载实例注册端。每主体第 201 个角色、超过 20 个分配 ID、超过 1024 项权限码被拒且不保存，既有超量数据不自动删除。失效重载的并发读有界等待同一次重载，成功使用最新快照、失败保持 503 与退避。

197. 写入准入（D-116）：本人同账号第 3 个写入口、管理同主体第 3 个入口和全进程第 33 个入口立即 429，不打开事务；合法嵌套调用不重复占位。角色写请求的主体窗口在事务前限制。头像同账号第 2 个/全进程第 3 个上传不进入日志预读或解码，位置释放后恢复；直接服务调用也受限。

198. 授权移除与潜在范围（D-117）：非超管移除账号角色、移除角色权限或收窄范围须具备原角色的管理资格，高权限角色保留原样仍允许；拒绝不写入。显式保存无关联权限码的资源范围也不宽于本人各相关权限码；默认仅本人和超管设置保持可用。

199. 调用方期限传播（D-118）：连接层错误发生在调用方期限之后、调用方的 context 定时器尚未更新 Err 时，Redis 返回的错误链仍含 DeadlineExceeded，保留原错误、不改变可用状态；验证码存储拒绝生成本地答案。真实断流超时回归仍校验拒绝、无回退和恢复后可用。

200. 相对部门角色的现有持有人（D-120）：非超管给已分配到范围外部门的角色新增相关权限或放宽范围，启用/停用这类角色，以及重新启用范围外账号（含停用角色），均按实际集合拒绝且不写库。角色撤权检查全部持有人，账号角色移除只检查该目标；同集合、未分配角色、超管、无关权限修改和原样保留仍可用。

201. 事务与搜索分组（D-122）：五个锁内辅助接口在事务外统一返回 ErrNoTx，不访问未配置数据库；原有锁内流程仍可用。主体账号、代理商名下商户和平台用户列表的各关键词分支与主体、部门范围、状态条件相交，范围外关键词不能进入列表或总数。

202. 名单重载等待（D-123）：已有重载持锁时，新重载在自身期限或取消到来时返回，不等待前一操作结束、不访问数据库；锁位置不泄漏，后续合法重载正常。原有串行发布、失效代数与失败保留规则不变。

203. 邀请额度的锁内身份（D-125）：认证后作废的会话/主账号和管理写准入拒绝不预留每日额度；当前主账号按锁内主体预留，超额不创建邀请。通过资格后的创建尝试仍计数，正常邀请与原有共享计数语义不变。

### 13.3 前端测试

Vitest：共享包子账号页的一次性密码关闭后清空（D-098）；平台主体详情抽屉覆盖结果身份绑定、确认期间切换、关闭后迟到结果不恢复（D-111）。平台账号与代理商/商户创建、重置组件覆盖正常显示、手填密码关闭清空、关闭/缓存停用/卸载后的迟到结果作废；主体抽屉清名单、换主账号、下线覆盖正常确认、关闭及切换目标（D-121）；前端依赖方向的 lint 规则（应用只能引用自己目录和 `@ga/shell`，D-064）、主体端登录页的编号（去空白转大写、记住上次的、平台端没有）和主体端的文案层（D-067）、权限指令、菜单转路由、组件键白名单、菜单显示名的取用顺序、单飞刷新与 409 重试、登出只在服务端确认（或服务端说会话已经结束）时才算成功、字典的批量读取与缓存、语言匹配与回退链、请求带 `Accept-Language`、错误说明按键翻译、全部文案文件与 `zh-CN.ts` 的键和占位符一致、偏好设置从本地存储读取时只接受合法值、主题色色阶计算、固定标签不可关闭、数字跳动按帧推进到终值且减少动态效果时直接显示终值、迷你图的几何（0 值不画柱子）、字节数与相对时间的格式化、页脚版权说明（11 种语言都有本语言的文案并带版权人的注册名称，布局里在内容区之后且内容区最大化时保留，登录页底部在原有的一句话下面，D-119）。
Playwright 冒烟：登录、强制改密、菜单按权限显示、切换语言（带国旗、繁体字标、切到日语后菜单随之变化）、建用户、建角色并授权、改字典显示后业务页面随之更新、菜单改名和建分组拖入后侧边栏随之更新、菜单填写排序值后侧边栏顺序随之变化、安全设置五个页签只读展示生效值、范围和配置项且页面上没有输入框和保存按钮、超管登录后进入控制台的数据中心（数字跳到终值、活跃用户排名、活跃时段、切换 90 天）、安全监控（实时登录、运行时长、数据库状态、热门接口）和工作台（问候、当前设备、最近动态、快捷入口能打开页面）、⌘K / Ctrl+K 搜索菜单并回车打开、偏好设置切换标签栏、锁屏后刷新页面仍是锁定状态且输错密码提示剩余次数、输对密码解锁、个人中心（改显示名后顶栏立即更新、安全设置只有真实状态没有开关、另一处登录后会话数加一、下线其他设备后另一处被踢出而本处照常、在个人中心改密后用新密码登录）、开通代理商和挂在它下面的商户（初始密码只显示一次，详情里看账号、重置主账号密码，停用，从详情去按编号筛好的登录日志，运维中心的安全事件选端，D-065、D-066）、代理商端和商户端（平台开商户 → 主账号用编号首次登录、被要求改密 → 建角色并授权、建员工 → 员工登录只看到授权的菜单、没授权的页面 404；主账号默认进入首位数据中心，概览不再含白名单，安全设置下独立黑白名单均支持三种后缀通配的保存和删除，白名单刷新后仍可登录，只获账号查看权限的员工看不到数据中心及安全设置且直接访问受限页面返回 404，D-090、D-091；代理商登录看到名下商户、看不到直属平台的商户，D-067；`scripts/e2e.sh` 起平台、代理商、商户三个程序，Playwright 起三个端的预览服务器）、被授权用户看到对应菜单、登出。

### 13.4 CI

格式、静态检查和测试收敛到 `make ci`：

```text
后端：gofmt 检查 → go vet → golangci-lint → 依赖方向与命名检查 → 门禁自测 → go mod tidy 检查 → 第三方许可证文件检查 → go test -race -count=1 ./...（带 MySQL 和 Redis、覆盖率报告）
前端：pnpm install --frozen-lockfile → eslint → vue-tsc → vitest → vite build
```

依赖公告扫描 `make vuln` 和浏览器验证 `make e2e` 是独立目标；CI 工作流在 `make ci` 后分别调用。`make ci` 成功本身不表示这两个目标已经运行。

### 13.5 给协作者（人和 AI）的工作规则

- 必须在能运行 `go build`、`go test` 和 MySQL 的环境里工作。不能编译和运行测试的环境不得用于编写 `core/`。
- 声称"完成"之前必须贴出 `make ci` 的通过结果。
- 不得为了让测试通过而删除或弱化 §13.2 的测试。
- 不得扩展 §1.3"不做"清单里的功能。
- 遵守 §1.4 洁净室规则。
- 仓库根目录的 `CLAUDE.md` 是给 AI 协作者的入口，规则与本节一致。

---

## 14. v0.1.0 的验收标准

| 范围 | 内容 | 验收 |
|---|---|---|
| 骨架 | `conf`、`logx`、`db`、`httpx`、`app` 与模块注册、迁移器、Makefile、CI、Compose、健康检查 | `make ci` 通过；`docker compose up` 后 `/readyz` 返回 200；依赖方向检查生效 |
| 认证 | `portal`、`auth`、`ga_session`、密码、验证码、登录防护、登录日志、`admin create` 命令 | §13.2 第 1–11 条 |
| 授权 | 权限注册表、Casbin、三档守卫、超管、敏感权限、菜单下发、`system` 模块的用户、角色、会话接口 | §13.2 第 12–17 条 |
| 日志与依赖方向 | `oplog`、请求体上限、脱敏、启动校验、安全头、依赖方向检查 | §13.2 第 18–21 条 |
| 端边界与并发 | 角色、会话以端为界；最后超管、改密在并发下的正确性 | §13.2 第 22–27 条 |
| 字典 | `core/dict`、`app.DictSource`、读取接口、字典管理、`useDict`；数量与层数上限，遗留项的值归代码 | §13.2 第 28–31 条，第 99、107 条 |
| 安全策略 | `portals.<code>.login`、`portals.<code>.password`、底线校验、密码有效期 | §13.2 第 32–35 条 |
| 多语言 | `httpx/lang.go`（11 种语言、`Accept-Language` 匹配、回退链）、带键的字段错误、壳的语言清单与带国旗的语言选择、`GaI18nInputs`、全部文案文件 | 规范 §9.6；`httpx` 与 `dict` 的语言测试、Vitest 的文案一致性测试 |
| 菜单管理 | `ga_menu_custom`、`rbac` 的菜单调整、`httpx.BindJSONStrict`、系统管理的菜单接口和页面、壳的 `menuTitle` | §13.2 第 36–39 条 |
| 界面与锁屏 | 布局、概览页（`system:dashboard:view`、`GET /system/dashboard`；D-030 起为控制台的数据中心）、全局搜索、全屏、偏好设置、服务端锁屏（`ga_session.locked_at`、`/auth/lock`、`/auth/unlock`、1006）、构建产物附第三方许可证 | §13.2 第 40–41 条；§13.3 的相关用例 |
| 控制台（D-030） | 数据中心、监控中心（`core/monitor`、进程内请求统计）、工作台，数字与图表动效，菜单直接填排序值 | §13.2 第 42–43 条；§13.3 的相关用例 |
| 运维中心与安全审计（D-032） | `core/audit`、错误日志（`ga_error_log`）、安全事件（`ga_security_event`）、调查时间线、查看留痕、`logx.Audit`；日志没有删除和修改接口 | §13.2 第 44–48 条 |
| 部门与岗位（D-033） | `ga_dept`、`ga_post`、`ga_user_post`、`ga_user.dept_id`，部门和岗位管理页面，用户表单的部门和岗位 | §13.2 第 49–51 条 |
| 系统设置（D-034） | "系统设置"分组、只读的安全设置页（`GET /system/security-policy`、`auth.Service.Policies`） | §13.2 第 52 条 |
| 超管账号保护（D-035） | `rbac.RequireSuper`、重置密码只归超管、`server admin reset-password`、非超管不能写超管账号 | §13.2 第 53–55 条 |
| 个人中心（D-038） | `ga_user.bio`、`GET/PUT /system/profile`、`POST /system/profile/revoke-other-sessions`、平台端个人中心页（`pages.profile`） | §13.2 第 56 条；§13.3 的相关用例 |
| 按部门的数据权限（D-039） | `rbac.DataResource`、`DataScope`、`DataFilter`、`app.DataResourceSource`、`portal.OrgProvider`、`ga_role_data_scope`，授权对话框的"数据权限"页签 | §13.2 第 57–60 条 |
| 头像（D-040） | `ga_user_avatar`、本人上传或选内置头像、管理员清除、壳的 `GaAvatar` 和内置头像 | §13.2 第 61 条 |
| 并发一致性与操作人认定（D-043～D-048） | `rbac.Service.Reload` 持锁、角色写操作进超管锁事务、字典和菜单缓存 `SetIfGen`、登录名别名、会话到期复核、字典同步单连接、`Dockerfile.server` 的 COPY 清单；`rbac.Service.WithActor`、`CurrentActor`、`AllowedLocked`、`DataFilterLocked`、`rbac.Options.SessionActive`；登录和改密在锁住账号行的事务里核对密码（`portal.UserLocker`），改密与重置同一加锁顺序；前端按登录身份代数作废换了人之前的请求和刷新；刷新绑定会话（`X-GA-Session`）；锁内复核路由权限码 | §13.2 第 62–78 条 |
| 刷新、退出与多标签页（D-049、D-050、D-052） | 对不上的刷新凭证只拒绝不吊销（`refresh_mismatch`）、服务端从不删除刷新 Cookie、待补退出标记绑定会话、409 按间隔重试且不算登录失效、跨标签页刷新锁、刷新先查账号再轮换、按标签页记会话号、请求和刷新在发起时绑定身份、退出确认只认 `auth.sessionEnded`、错误日志按端隔离 | §13.2 第 79–83、88–90 条 |
| 授权快照与读取一致性（D-051、D-053、D-054） | 权限码、成员关系、角色状态在一个快照里整体发布，重载失败不按旧快照判定；数据范围和被读的数据在同一个数据库视图里；用户详情和回显返回检查过的那一行；登录防跨站；认证动作在等待之后核对身份；解锁在锁内复核密码；SQL 日志只记模板 | §13.2 第 84–87、91–95 条 |
| 撤权窗口、限流与容量（D-055～D-057） | 改授权到发布之间判定先等；部门负责人按用户范围遮蔽；改密旧密码和验证码限流；范围子查询、字典和页码上限；无令牌请求不自动刷新；门禁在工具出错时失败；部门写后回显按库认定；登录防护键数硬上限 | §13.2 第 96–103 条 |
| 审计落库、登录细节与部署默认值（D-058） | 审计字符串入库前整理成合法 UTF-8、写不进去的不重试；超长密码照样做比较；负责人不能试探；代码字典遗留项不能改值和启用；重新启用账号按分配规则；本人写操作只锁本人（`WithSelf`）；限流和锁定不写登录日志、IPv6 按 /64、密码核对并发上限；`__Host-` 刷新 Cookie 与重复检测；镜像默认 release、nginx 收紧 | §13.2 第 104–112 条 |
| 操作日志请求体与本人写操作（D-059、D-060） | 操作日志请求体所有分支入库前截断；`WithSelf` 锁会话行（`rbac.Options.LockSession`）；重新启用账号被拒只回通用错误；https 下待补退出标记用 `__Host-` 前缀 | §13.2 第 113–114 条 |
| 前端 | `@ga/shell`、登录与强制改密、布局、动态菜单、权限指令、单飞刷新、系统管理页面、11 种界面语言 | §13.3 全部通过 |
| 文档与部署 | nginx 示例、README、新增模块指南、`docs/api.md` | 新人按 README 在 10 分钟内跑起来并登录 |

`CHANGELOG.md` 从 v0.1.0 起按版本记录。

---

## 15. 尚未定案的事项

| # | 事项 | 现状 |
|---|---|---|
| 1 | Go module 路径与代码托管的组织名 | 代码里用 `github.com/goalladmin/goalladmin` 占位；定下来后全局替换 `go.mod` 与 import 路径 |
| 2 | 平台端 TOTP 二次验证 | v0.2；`ga_user.mfa_secret` 已预留列。处理资金类业务的部署建议在上线前先做 |
| 3 | 是否支持 PostgreSQL | v0.1 不做；需要时迁移脚本要按方言各维护一套 |

已定案：开源许可证 Apache-2.0；CI 用 `make ci`，与平台无关（仓库自带 GitHub Actions 配置）。

---

## 16. 扩展与升级

业务方以"模块"的形式开发（§4.3），框架升级靠 `git merge upstream`。要让升级不出问题，靠的不是模块机制本身，而是下面五条。

### 16.1 业务代码在物理上碰不到框架内部

- 后端：`core/` 只有 §3.2 标为 [公开] 的包可被 import，其余在 `core/internal/`，Go 编译器强制。
- 前端：`@ga/shell` 用 `exports` 只导出包根，深层 import 被拒绝。
- 数据库：业务代码不读写 `ga_*` 表；业务迁移与框架迁移分目录、分版本表（§8.2）。
- CI 的依赖方向检查（§3.3）覆盖以上后端规则。

### 16.2 公开 API 面（框架对业务方的承诺）

| 包 | 导出符号 |
|---|---|
| `app` | `New`、`Option`（`WithDB`、`WithLogger`、`WithClock`、`WithPasswordHashParams`、`WithBcryptCost`、`WithPBKDF2Iterations`（测试用：调低 Argon2id、bcrypt、PBKDF2 的参数，D-070、D-072）、`WithStatusCacheTTL`、`WithoutMigrations`（D-061））、`App.Register`、`App.Run`、`App.Migrate`、`App.CheckMigrations`、`ErrMigrationsDisabled`、`App.Setup`、`App.Context`、`App.Handler`、`App.Routes`、`Deps`（含 `Dict`、`Monitor`、`Audit`、`IPACL`、`Orgs`、`NewRateWindow`（创建同部署共用的计数窗口，D-076））、`RateWindow`（`Reserve` 返回准入结果和幂等退款函数）、`Module`、`MigrationSource`、`DictSource`、`DataResourceSource`（D-039）、`Router`（`Portal`、`Raw`）、`PortalRouter`（`Group`、`Handle`、HTTP 方法、`LiveAuth`（这个分组下的路由认证时按库核对，D-073））、`RouteOption`（`WithMiddleware`、`WithOpName`）、`Route`、`PortalPrefix` |
| `conf` | `Config` 及其子结构（只读）、`Load`、`LoadFor`（只加载某几个端，D-061）、`Default`、`DefaultPortalCode` |
| `logx` | `From(ctx)` 返回 `*slog.Logger`、`With`、`Audit`、`LevelAudit`（D-032） |
| `db` | `From(ctx)`、`Tx(ctx, fn)`、`TxReadCommitted(ctx, fn)`（D-063）、`Snapshot(ctx, fn)`（只读快照事务，D-051）、`AfterCommit(ctx, fn)`、`AfterEnd(ctx, fn)`（事务结束后不论提交还是回滚，D-055）、`IsDataError`（这一行数据本身写不进去的错误，D-058）、`InTx`、`Has`、`WithDB`、`MigrateUp`、`MigrateStatus`、`MigrateCheck`、`ErrMigrationsPending`（只读核对，D-061）、`OpenTestDB`、`TestContext` |
| `httpx` | `OK`、`OKPage`、`Fail`、`Error` 类型（`New`、`Newf`、`Wrap`、`WithFields`、`WithData`、`WithStatus`、`WithCause`）、错误码常量与 `Err*` 哨兵、`Bind*`、`PageQuery`、`BindPage`（页码上限 `MaxPage`，D-055）、`RequestID(ctx)` |
| `portal` | `Portal`、`AvatarPresets`、`AvatarPresetPrefix`、`ValidAvatarPreset`（内置头像，D-067）、`UserProvider`、`UserLocker`（D-047）、`PasswordRehasher`（可选：登录后把哈希换成选的算法和当前参数，D-070）、`DeptProvider`（D-039，v0.1 叫 `OrgProvider`，D-061 改名）、`Org`、`OrgUserProvider`、`OrgLocker`（D-063 起主体端必须实现，排他锁）、`ErrOrgNotFound`、`NormalizeOrgCode`（D-061）、`Account`、`LoginPolicy`、`PasswordPolicy`、`DefaultLoginPolicy`、`DefaultPasswordPolicy`、`ErrAccountNotFound`、`NormalizeUsername` |
| `auth` | `Principal`（含 `OrgID`，D-061）、`FromCtx`、`MustFromCtx`、`Service`（会话吊销、密码哈希（`HashPassword`：和登录核对密码占同一个并发上限，满了回 429，D-068）与策略、会话与登录日志查询、登录与安全统计、生效策略 `Policies`（D-034）、会话归属 `SessionOwner`（D-035）、按用户列会话 `ListSessionsIn`（D-039；用户是子查询，D-055）、下线本人其他会话 `RevokeOtherSessions`（D-044））、`SessionInfo`、`LoginLogInfo` |
| `rbac` | `Perm`（`RoleView`：拥有它的人分配角色被拒时看得到原因，D-069）、`MenuNode`（`SuperOnly`，D-090）、`Public`、`AuthOnly`、`Require`、`RequireSuper`（D-035）、`Service`（角色与授权读写——主体端按主体隔离，D-063、`IsSuper`、`HoldsSuperRole`、`IsLastSuper`、`WithSuperLock`、`CurrentActor`（D-045）、`WithActor`、`WithSelf`、`CheckEnableUser`（D-058）、`ClearUserRoles`（主体端、事务内，D-101）、`AllowedLocked`（D-047）、`Allowed`、`AllowedInSnapshot`（D-090）、`Perms`、`PermTree`，数据范围 `GrantRole`、`RoleDataScopes`、`DataResources`、`DataScopeOf`、`DataFilter`（D-039）、`DataFilterLocked`（D-045）、失效入口 `InvalidatePolicy`、`InvalidateMenu`、`InvalidateAllMenus`（D-075，只清本地缓存））、`Role`、`SuperRoleCode`、`DataResource`、`DataScope` 与 `Scope*` 常量、`AllScopes`、`DataFilter`（`All`、`Allows`、`AllowsDept`、`Apply`） |
| `oplog` | `Record`、`List`、`Filter`、`Entry`、`IsSensitiveKey`、`DailyCounts`、`TopActions`、`HourOfDayCounts`、`TopUsers`、`TopUsersIn`（D-044；用户是子查询，D-055）、`UserTopActions` 与对应的计数类型 |
| `audit` | `Service`（`RecordSecurity`、`ListSecurityEvents`、`ListErrors`、`GetError`、`Timeline`）、`SecurityEvent`、`NewSecurityEvent`、`SecurityFilter`、`ErrorEntry`、`ErrorFilter`、`TimelineQuery`（`IncludeGlobal`，D-066）、`TimelineItem`、`Level*`、`ErrNotFound`（D-032） |
| `ipacl` | `Service`（`Blocked`、`PortalAllows`、`AccountAllows`、`AddDeny`、`RemoveDeny`、`AddOrgDeny`、`RemoveOrgDeny`、`ListOrgDeny`（D-090）、`ClearOrgDeny`（D-102）、`Remove`、`ListDeny`、`ListAllow`、`SetAllow`、`ClearAllow`、`All`、`Load`、`Invalidate`（D-075，标记本地快照待核对））、`New`、`Options`（含提交后 `OnChange` 回调，D-075）、`Rule`、`Entry`、`Target`（`PortalTarget`、`OrgTarget`、`UserTarget`）、`DenyInput`、`DenyFilter`、`Kind`、`Scope` 与常量、`ParseCIDR`、`Covers`（D-062）；`app.Deps.IPACL` |
| `org` | `Kind`（`Agent`、`Merchant`、`Portal`）、`Service`（`Users`——主体端的用户来源；平台管理 `List`、`Get`、`Briefs`、`Create`、`Update`、`SetStatus`、`ResetOwnerPassword`、`ChangeOwner`、`SetAgent`、`Accounts`、`Sessions`、`RevokeSession`，以及在拿锁之前生成初始密码的 `NewInitialPassword`（用 `Options.Hash` 给的函数算：进程的哈希器加密码计算的位置，满了回 429，D-068、D-070）、算哈希之前的便宜检查 `CheckCreate`、`CheckResetOwner`，D-068；主体端自己用的 `Summary`、`DataCenter`（D-090）、`Member`、`LockMember`、`CreateMember`、`UpdateMember`、`SetMemberStatus`、`SetMemberPassword`、`UpdateProfile`、`SetAvatar`、`ChildMerchants`、`ChildMerchant`，D-067；建账号之前的便宜检查 `CheckMember`，D-068）、`New`、`Options`（含提交后账号、主体和会话变更回调，D-075；`ClearRoles`——更换主账号时清空原主账号角色的回调，D-101）、`InitialPassword`（`Plain`）、`Info`、`Brief`、`Summary`、`Account`、`Session`、`Filter`、`CreateInput`、`Created`、`UpdateInput`、`MemberInput`、`MemberUpdate`、`ProfileInput`、`ByChildOrgs`、`Status*` 与长度常量（D-065）；`app.Deps.Orgs` |
| `orgportal` | `New`、`Option`、`WithCount`、`Count`、`Kit`（`Code`、`Perm`、`Portal`、`Perms`、`Menus`、`Routes`）、`Perm*` 权限码后两段、`Op*` 操作日志动作名、`AccountView`、`RoleRef`、`SessionView`、`OverviewView`、`ProfileView`（D-067） |
| `scope` | `MustOrg`、`ByOrg`、`ErrNoOrg`（D-061）；`scope/scopetest`：`Run`、`Resource`、`Ctx`、`IsNotFound`、`TB` 和测试用的主体常量 |
| `monitor` | `Service`（`Server`）、`Server`、`Runtime`、`CPU`、`Memory`、`DB`、`Requests`、`Minute`、`Route` |
| `dict` | `Dict`、`Item`、`ValueType`（`String`、`Int`）、`AllPortals`、`MaxItems`、`MaxDepth`（D-055）、`Service`（`Get`、`Many`、`Has`、`Label`、`Invalidate`（空编码清全部本地缓存）与管理方法）、`Options`（含提交后 `OnChange` 回调，D-075）、`View`、`ViewItem` |

前端 `@ga/shell` 的公开面就是 `packages/shell/src/index.ts` 导出的符号（构建插件另在 `@ga/shell/vite`）：`createPortalApp`（`scoped` 选项，D-067）、`GaDataCenter` / `DashboardData`（D-090）、`orgApi`、`orgPerm`、`isScoped` 和 `Org*` 类型（D-067）、`useAuthStore`、`useRequest`、`useTable`、`useI18n`、`hasPerm` / `vPerm` / `GaPerm`、`useDict` / `invalidateDicts` / `GaDictTag`、`RouteNames`、`formatTime`、`ApiError` / `Codes` 与各类型。

表里的类型只是数据结构：内核公开包不暴露带 `TableName` 的 gorm 模型（`rbac.Role` 也不是），业务模块拿着这些类型进不了内核的表（D-044）。

这张表随版本维护。v0.x 期间允许破坏性变更，但必须写进 `CHANGELOG.md` 的"升级注意"段；v1.0 起遵守语义化版本，废弃的符号至少保留一个次版本并打 `// Deprecated:` 注释。

### 16.3 业务需要框架多做一件事时，加扩展点，不改 fork

业务仓库永远不修改 `core/` 和 `packages/shell/`。需要框架新能力时：先在 GoAllAdmin 仓库加一个扩展点并发版，再升级业务仓库。可预见的扩展点（v0.1 不预先实现，出现第一个真实需求时再加）：登录成功、登出、改密的事件钩子；端级中间件插槽；自定义登录页组件；主题变量；自定义 `UserProvider`（已有）。

### 16.4 升级流程

脚手架形态（v0.x）：`git fetch upstream && git merge upstream/vX.Y`。因为业务不改框架目录，冲突只会出现在 `main.go` 的注册行、`package.json` 和配置文件。合并后跑 `make ci` 和业务自己的测试。

库形态（API 稳定后，预计 v0.3 之后评估）：把 `core/` 抽成独立 Go module、`@ga/shell` 发成独立前端包，升级变成改版本号。§3.3 禁止 `core/` import `modules/` 就是为这一步铺路。

### 16.5 明确不做运行时插件

"模块"是编译期概念。不支持上传压缩包安装插件、不支持 Go 的 `plugin` 包热加载、不支持从远程拉取代码。加模块的方式只有一种：在 `main.go` 里加一行注册，重新编译。
