# 新增一个业务模块

下面用"订单 `order`"作为要新增的模块名，资源是"订单 `orders`"，从表到页面走一遍。系统管理模块（后端 `server/modules/system/`、前端 `web/apps/platform/src/views/system/`）用的是同一套写法，可以对照着看。

原则只有三条：业务代码只放在 `server/modules/<模块>/` 和 `web/apps/<端>/`，框架目录不改；后端只 import `server/core/` 下的公开包，前端只从 `@ga/shell` 引用；权限码、菜单在代码里声明，数据库只存授权关系。

## 后端

### 1. 建目录

```text
server/modules/order/
├── module.go          实现 app.Module；有自己的表时再实现 app.MigrationSource
├── perms.go           权限码常量、Perms()、Menus()、操作日志动作名
├── order_model.go     gorm 模型，列顺序与迁移一致
├── order_repo.go      数据访问，只通过 db.From(ctx) 拿句柄
├── order_service.go   业务规则，当前身份从 ctx 取
├── handlers.go        HTTP：绑定参数 → 调 service → 输出信封
├── migrations/
│   └── 00001_init.sql
└── order_test.go      接口测试
```

### 2. 表与迁移

`migrations/00001_init.sql`。业务列在前，底部按 `docs/conventions.md` 的固定块：`status, sort, remark, created_at, updated_at[, deleted_at], created_by, updated_by[, deleted_by]`。要软删就加 `deleted_at` / `deleted_by`，不软删就不要这两列。

```sql
CREATE TABLE IF NOT EXISTS `biz_order` (
  `id`          bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `order_no`    varchar(32)     NOT NULL COMMENT '订单号',
  `amount`      bigint          NOT NULL DEFAULT 0 COMMENT '金额，分',
  `status`      tinyint         NOT NULL DEFAULT 1 COMMENT '状态',
  `sort`        int unsigned    NOT NULL DEFAULT 0 COMMENT '排序，越小越靠前',
  `remark`      varchar(255)    NOT NULL DEFAULT '' COMMENT '备注',
  `created_at`  datetime(3)     NOT NULL,
  `updated_at`  datetime(3)     NOT NULL,
  `deleted_at`  datetime(3)     NULL,
  `created_by`  bigint unsigned NOT NULL DEFAULT 0,
  `updated_by`  bigint unsigned NOT NULL DEFAULT 0,
  `deleted_by`  bigint unsigned NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_order_no` (`order_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='订单';
```

表前缀用你业务自己的（这里是 `biz_`），`ga_` 留给框架。迁移文件名 `NNNNN_name.sql`，编号递增，语句以行尾 `;` 分隔，只向前，不支持 `DELIMITER`。

每个文件都写成**可以重跑**的：MySQL 的 DDL 会隐式提交，文件里第 3 条语句失败时前 2 张表已经建好但版本表没有记录，下次启动会从第 1 条重来。所以建表用 `CREATE TABLE IF NOT EXISTS`，种子数据用 `INSERT ... ON DUPLICATE KEY UPDATE`，加列前用 `information_schema` 判断或者拆成单独的小文件（一个文件只做一件事，失败了也好定位）。

在 `module.go` 里把目录嵌进二进制并实现 `Migrations()`；版本表用模块自己的名字，和框架的 `ga_schema_version` 互不干扰：

```go
//go:embed migrations/*.sql
var migrationFiles embed.FS

func (m *module) Migrations() (fs.FS, string, string) {
	return migrationFiles, "migrations", "biz_order_schema_version"
}
```

`server migrate up` 会先跑框架迁移，再按注册顺序跑每个模块的迁移；`server migrate status` 分来源列出状态。

### 3. 模型

字段顺序与表一致，列名显式写在 tag 里。软删用 `gorm.DeletedAt`，查询会自动排除已删行：

```go
type Order struct {
	ID        uint64         `gorm:"column:id;primaryKey" json:"id"`
	OrderNo   string         `gorm:"column:order_no" json:"orderNo"`
	Amount    int64          `gorm:"column:amount" json:"amount"`
	Status    int            `gorm:"column:status" json:"status"`
	Sort      uint           `gorm:"column:sort" json:"sort"`
	Remark    string         `gorm:"column:remark" json:"remark"`
	CreatedAt time.Time      `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt time.Time      `gorm:"column:updated_at" json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at" json:"-"`
	CreatedBy uint64         `gorm:"column:created_by" json:"createdBy"`
	UpdatedBy uint64         `gorm:"column:updated_by" json:"updatedBy"`
	DeletedBy uint64         `gorm:"column:deleted_by" json:"-"`
}

func (Order) TableName() string { return "biz_order" }
```

### 4. repo

所有方法第一个参数是 `ctx`，句柄用 `db.From(ctx)` 拿——调用方开了事务（`db.Tx`）时自动落在同一个事务里。列表页的排序字段过白名单，关键字做 LIKE 转义；更新用列名 map，不用整个结构体 `Save`；软删显式写 `deleted_at` 和 `deleted_by`。查询和更新可以对照 `server/modules/system/org.go` 里的岗位（`Post`，岗位是硬删）。

```go
// ErrOrderNotFound 由 service 翻译成 httpx.ErrNotFound。
var ErrOrderNotFound = errors.New("order: not found")

// SoftDelete 软删：写 deleted_at 和 deleted_by。gorm.DeletedAt 会让之后的查询自动排除它。
func (r *OrderRepo) SoftDelete(ctx context.Context, id, by uint64) error {
	now := time.Now().UTC()
	res := db.From(ctx).Model(&Order{}).Where("id = ?", id).
		Updates(map[string]any{"deleted_at": now, "deleted_by": by, "updated_at": now, "updated_by": by})
	if res.Error != nil {
		return fmt.Errorf("order: delete order: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrOrderNotFound
	}
	return nil
}

// escapeLike 转义 LIKE 的通配符，关键字搜索用 "%"+escapeLike(kw)+"%"。
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
```

### 5. service

业务规则都在这一层：校验、状态流转、谁能做什么。当前身份只从 ctx 取。

**写操作一律放进 `deps.RBAC.WithActor` 的回调里做**（规范 §6.4，D-047、D-048）：路由守卫只在请求进来时判断一次，
从那一刻到写库之间，操作人的账号可能被停用、会话可能被吊销、权限可能被收回。`WithActor` 开一个事务、拿锁，
按已提交的状态重新认定操作人（账号停用、会话吊销回 401，路由要求的权限码没了回 403），再把认定后的身份交给回调；
回调里的读和写都在这个事务里。只用 `db.Tx` 不够——它不重新认定操作人。后面的判断（包括"是不是超管"）只用回调收到的 `actor`，
不用请求开始时的那个：

```go
func (s *OrderService) Create(ctx context.Context, in OrderInput) (*Order, error) {
	// 校验入参（不依赖身份的）放在锁外……不通过返回 httpx.ErrValidation.WithFields(httpx.NewField("orderNo", "validation.required", "required"))
	var o *Order
	err := s.deps.RBAC.WithActor(ctx, auth.MustFromCtx(ctx), func(ctx context.Context, actor auth.Principal) error {
		o = &Order{OrderNo: in.OrderNo, Amount: in.Amount, Status: 1, CreatedBy: actor.UserID, UpdatedBy: actor.UserID}
		return s.repo.Create(ctx, o)
	})
	return o, err
}
```

只改调用者本人数据、对所有登录用户开放的写操作（`rbac.AuthOnly()`，比如"我的设置"）用 `deps.RBAC.WithSelf`：只锁本人的账号行，
不排队等全端共用的锁（D-058）。按数据范围过滤的读操作，把"算范围"和"读数据"放进同一个 `db.Snapshot`（D-054），
写操作在 `WithActor` 里用 `deps.RBAC.DataFilterLocked` 检查目标在不在范围内。

"不存在"翻译成 `httpx.ErrNotFound`，其他错误原样返回——`httpx.Fail` 会把非 `*httpx.Error` 的错误按 500 处理、只记日志不泄露细节。

### 6. handler

只做三件事：绑定参数、调 service、输出信封。分页参数用 `httpx.BindPage(c)`，请求体用 `httpx.BindJSON(c, &req)`（`binding` tag 做格式校验；只收 `Content-Type: application/json` 的请求体，别的类型回 `3002`，D-095），成功 `httpx.OK` / `httpx.OKPage`，失败 `httpx.Fail`。看 `server/modules/system/handlers_org.go`。

### 7. 权限码、菜单、动作名（`perms.go`）

权限码三段 `<模块>:<资源>:<动作>`，全小写。`Name` 和 `Group` 是 i18n 键，前端翻译。需要只让超管授出的加 `Sensitive: true`。

```go
const (
	PermOrderList   = "order:order:list"
	PermOrderCreate = "order:order:create"
	PermOrderUpdate = "order:order:update"
	PermOrderDelete = "order:order:delete"
)

func (m *module) Perms() []rbac.Perm {
	p := func(code, name string) rbac.Perm {
		return rbac.Perm{Code: code, Name: name, Portal: PortalCode, Group: "order.order"}
	}
	return []rbac.Perm{p(PermOrderList, "perm.order.order.list"), p(PermOrderCreate, "perm.order.order.create"), /* … */}
}

func (m *module) Menus() []rbac.MenuNode {
	return []rbac.MenuNode{
		{Portal: PortalCode, Name: "order", Path: "/order", TitleKey: "menu.order", Icon: "Tickets", Sort: 200},
		{Portal: PortalCode, Parent: "order", Name: "order-list", Path: "/order/orders", Component: "order/order/index",
			TitleKey: "menu.order.order", Icon: "List", Perm: PermOrderList, Sort: 10, KeepAlive: true},
	}
}
```

`Name` 在端内唯一，小写字母开头，只含小写字母、数字和连字符。`Component` 是相对前端 `views/` 的路径，第 11 步会建这个文件。`Icon` 是 Element Plus 图标名。目录节点不写 `Perm`，任一子节点可见时它就可见。`Sort` 越小越靠前：根目录里框架的页面是 10–30，运维中心、权限管理、系统设置三个目录是 500、600、800（D-042），业务菜单一般放在 100–400 之间。

操作日志的动作名用稳定编码：`OpOrderCreate = "order.create"`，前端用 `op.order.create` 翻译。

### 7b. 字典（`dicts.go`，可选）

业务规则里要判断的枚举值（订单状态、支付渠道……）在代码里声明成字典：前端拿到按语言翻译好的文字、颜色和下拉选项，后台能调整显示但改不了值（D-023）。模块实现 `app.DictSource`：

```go
const DictOrderStatus = "order.status" // 必须以"模块名."开头

func (m *module) Dicts() []dict.Dict {
	en := func(s string) map[string]string { return map[string]string{"en-US": s} }
	return []dict.Dict{{
		Code: DictOrderStatus, Portal: PortalCode, ValueType: dict.Int,
		Name: "订单状态", NameI18n: en("Order status"),
		Items: []dict.Item{
			{Value: "1", Label: "待支付", LabelI18n: en("Unpaid"), Color: "warning", Sort: 10},
			{Value: "2", Label: "已支付", LabelI18n: en("Paid"), Color: "success", Sort: 20},
		},
	}}
}
```

启动时会校验并同步进库，写错了（编码没有模块前缀、值重复、整数写成 `01`、颜色不认识）直接拒绝启动。service 里校验入参用 `deps.Dict.Has(ctx, DictOrderStatus, strconv.Itoa(v))`，它只认启用中的值——后台停用某个值，新数据就不能再用它。注意 `Init` 时 `deps.Dict` 还是 `nil`，service 里存 `deps`、用的时候再取（看 `server/modules/system/handlers_dict.go` 开头的写法）。

运营自己维护、代码不判断的列表（银行、行业分类）不用声明，在"系统设置 → 字典管理"里新建即可。

### 8. 路由（`module.go`）

每条路由的守卫是必填参数，漏写编译不过；写操作挂 `oplog.Record`：

```go
func (m *module) Routes(r *app.Router) {
	g := r.Portal(PortalCode).Group("/order")
	h := m.h
	g.GET("/orders", rbac.Require(PermOrderList), h.list)
	g.GET("/orders/:id", rbac.Require(PermOrderList), h.get)
	g.POST("/orders", rbac.Require(PermOrderCreate), h.create, oplog.Record(OpOrderCreate))
	g.PUT("/orders/:id", rbac.Require(PermOrderUpdate), h.update, oplog.Record(OpOrderUpdate))
	g.DELETE("/orders/:id", rbac.Require(PermOrderDelete), h.remove, oplog.Record(OpOrderDelete))
}
```

三档守卫：`rbac.Require(code)` 是默认；`rbac.AuthOnly()` 只用于对所有登录用户开放的接口（下拉选项之类）；`rbac.Public()` 必须登记进 `docs/api.md` 的公开接口清单（有测试校验）。另有只给超管的 `rbac.RequireSuper()`，只用于能接管别人账号的操作（框架里只有重置他人密码，D-035），一般业务用不到。路径写进 `docs/api.md` 的对应小节，否则 `cmd/server` 的文档一致性测试会失败。

### 9. 注册

`server/main.go` 顶部 import 你的模块，在 `modules()` 里加一行。顺序即初始化顺序：`system` 在最前（它注册 `platform` 端），然后是你的模块。

```go
func modules() []app.Module {
	return []app.Module{
		system.Module(),
		order.Module(),
	}
}
```

`main_test.go` 的文档一致性测试用的也是 `modules()`，不用再改测试。

### 10. 测试

照 `server/modules/system` 的测试写 fixture：真实 MySQL（`db.OpenTestDB`）、真实 `system` 模块提供超管、`a.Migrate` 跑迁移、`httptest` 走完整 HTTP 链路。至少覆盖：增删改查主路径、校验失败、没有权限码的用户 403、未登录 401。

框架自带的"写操作在锁里重新认定操作人"用例（`server/modules/system/route_actor_test.go`）只遍历系统模块的路由，
不会自动覆盖你的模块。每条写接口都要补一组在途用例：请求通过路由守卫之后、拿到锁之前，另一条事务停用这个账号
（或吊销它的会话、收回这条路由的权限码）并提交；请求必须回 401（或 403），业务表没有任何变化。写法照
`route_actor_test.go` 和 `stale_actor_test.go` 里的 `inFlight`。

`make ci` 会跑依赖方向检查：模块之间不能互相 import，不能碰 `core/internal`。

## 主体端模块的区别

上面的例子默认注册到平台。写代理商或商户模块时，保留同一套 handler → service → repo 结构，并增加以下约束（规范 §7.1、D-061 至 D-067、D-073）。

### 入口、迁移与页面

商户模块注册到 `server/cmd/merchant/main.go` 的 `modules()`，放在 `merchantportal.Module()` 后面；代理商模块对应 `cmd/agent` 和 `agentportal.Module()`。使用已经注册的端，不在业务模块里重新注册端，也不把另一个端的后台模块 import 进来。权限码和菜单的 `Portal` 固定为本端。

页面放进 `web/apps/merchant/src/views` 或 `web/apps/agent/src/views`，接口使用本端 `useRequest()`，不传主体 ID，不引用另一个应用的源码。主体编号只用于登录，登录后的归属来自会话。

新业务表仍只由平台程序迁移。若业务模块只在主体端提供路由，可以在该业务包中自行提供一个 `SchemaModule()` 工厂：返回只提供 `Migrations()`、其他生命周期方法为空的模块，在平台 `modules()` 注册；主体端注册正常模块，使用相同嵌入文件和版本表核对迁移。这里的 `SchemaModule` 是业务方需要实现的工厂，不是框架现成 API。不要让平台注册主体端路由，也不要复制两份迁移文件或跳过主体端的迁移核对。

### 每次查询都限制归属

主体拥有的数据带明确归属列，例如 `merchant_id`。按业务需要给它建索引，主体内唯一的业务编号使用 `(merchant_id, order_no)` 联合唯一索引。创建时从重新认定的操作人 `actor.OrgID` 写入，不接受请求体或查询参数中的 `orgId`、`merchantId` 作为当前归属。

列表、详情、统计、导出、关联查询以及按 ID 更新/删除都必须加 scope；仅在列表加条件不够。repo 示例：

```go
func (r *OrderRepo) Get(ctx context.Context, id uint64) (*Order, error) {
	var row Order
	err := db.From(ctx).Scopes(scope.ByOrg(ctx, "merchant_id")).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, httpx.ErrNotFound
	}
	return &row, err
}
```

更新/删除同样在 SQL 上应用 `scope.ByOrg`，不要先做一次无条件查询再在 handler 里判断。其他主体的数据统一作为不存在处理。身份缺失或没有主体时 scope 报错；不能捕获这个错误后改成无条件查询。平台全量查看使用独立的 repo 方法和平台权限，不复用主体端方法绕过 scope。

主键也通过占位符绑定；不要把请求字符串传给 `First(&row, value)` 等内联条件接口，字符串可能被当作 SQL 条件。当前程序各注册自己的端；扩展到一个程序提供多个主体端时，仓库还须核对 `Principal.Portal` 与本仓库所属端相同，不能只比较可能重号的主体 ID。

代理商查看名下商户的数据时，对业务表的商户归属列使用 `org.ByChildOrgs(ctx, "merchant_id")`。它只接受代理商身份，按当前代理商名下商户过滤；不要把代理商的 OrgID 直接和 merchant_id 比较，也不要信任前端传来的商户集合。选择单个商户仍要保留这个过滤条件。

### 敏感读与多步写

主账号专属、账号管理及其他需要即时认定会话的读接口放入 `LiveAuth()` 分组，再配本来需要的权限守卫：

```go
g := r.Portal("merchant").Group("/order")
g.LiveAuth().GET("/private-summary", rbac.RequireSuper(), h.privateSummary)
g.LiveAuth().GET("/orders", rbac.Require(PermOrderList), h.list)
```

`RequireSuper()` 在主体端表示本主体的主账号。`LiveAuth()` 核对会话和账号状态，不会代替权限码或数据归属检查；需要员工使用的接口用 `Require`，不能一概限制主账号。

多步写操作放进一个 `deps.RBAC.WithActor` 回调，回调里使用传入的 ctx 和重新认定的 actor。主体端会按主体行串行化，账号失效或权限收回时整个操作拒绝。主体事务必须新开，不在外层 `db.Tx` 中再调用 `WithActor`，也不要把同一个业务动作拆成几个各自提交的回调。创建行从 actor.OrgID 写归属；更新时继续应用 scope。需要额外权限时在锁内用 `AllowedLocked`，缓存失效等副作用挂 `db.AfterCommit`。

### 隔离测试

使用真 MySQL，先用 `scope/scopetest.Run` 覆盖 repo 的列表、详情、更新、删除和无主体身份；再用实际主体端令牌覆盖 HTTP 路由。至少验证两个主体互不可见、跨主体 ID 统一 404、主账号权限和员工权限不同、平台/其他端令牌不能通用。代理商场景还要验证其他代理商名下和直属平台商户不可见、商户归属改变后访问范围改变。

写接口还要有在途测试：请求通过入口检查后账号停用、会话吊销或权限收回，必须拒绝且多步数据全部不变。只有 scope 测试不能证明事务和路由守卫正确。

## 前端

页面、接口封装和文案都放在端里：页面在 `web/apps/platform/src/views/order/`，接口在 `src/api/order.ts`，文案写进 `src/locales/` 下每种语言的文件。

### 11. 页面

`src/views/order/order/index.vue`——`views/` 之后的路径就是第 7 步 `Component` 的值加 `.vue`。不在映射表里的键渲染 404。

页面照系统管理的页面写（例如 `src/views/system/post/index.vue`）：`useTable` 管列表状态，`el-dialog` 做新建/编辑，`v-perm` 控制按钮显示，`data-test` 属性给冒烟测试用。页面只从 `@ga/shell` 引用框架能力：

```ts
import { formatTime, useI18n, useTable } from '@ga/shell'
import { orderApi } from '../../../api/order'
```

用到字典的字段：列表里 `<GaDictTag code="order.status" :value="row.status" />` 显示带颜色的标签；表单里 `const status = useDict('order.status')`，下拉用 `status.options`（只含启用的项），显示文字用 `status.label(v)`。同一页的多个 `useDict` 合并成一次请求，切换语言自动重拉。

### 12. 接口调用

`src/api/order.ts`，类型化封装，页面不直接拼 URL：

```ts
import { useRequest } from '@ga/shell'
import type { PageData, PageQuery } from '@ga/shell'

export interface Order { id: number; orderNo: string; amount: number; status: number; /* … */ }

export const orderApi = {
  list: (params: PageQuery & { status?: number }) => useRequest().get<PageData<Order>>('/order/orders', { params }),
  create: (input: OrderInput) => useRequest().post<Order>('/order/orders', input),
  update: (id: number, input: OrderInput) => useRequest().put<Order>(`/order/orders/${id}`, input),
  remove: (id: number) => useRequest().delete<null>(`/order/orders/${id}`),
}
```

`useRequest()` 返回的客户端已经处理了 Bearer、401 刷新重放、信封拆包和统一报错：成功直接拿到 `data`，失败抛 `ApiError`（默认已经弹过提示，页面通常只需要 `catch` 后什么都不做；传 `{ silent: true }` 可以自己处理）。

### 13. 文案

`src/locales/` 里每种语言一个文件（`zh-CN.ts`、`en-US.ts`、`ja-JP.ts`……共 11 种，D-026），每个文件的键必须和 `zh-CN.ts` 一致（有测试检查）。键与后端声明一一对应；带点的键和目录键可以并存。建议都放在模块名下：`order.*`，或 `menu`、`permGroup`、`perm`、`op`、`err`、`dict` 下以 `order` 开头的键。暂时没有翻译的语言可以先填英文，上线前再请人翻译：

```ts
// 在每个语言文件里加上这些键（这里是 zh-CN.ts）
menu: { order: '订单', 'order.order': '订单列表' },
permGroup: { 'order.order': '订单' },
perm: { order: { order: { list: '查看订单', create: '创建订单', update: '编辑订单', delete: '删除订单' } } },
op: { 'order.create': '创建订单', 'order.update': '编辑订单', 'order.delete': '删除订单' },
order: { orderNo: '订单号', amount: '金额' },
// 后端 httpx.NewField("qty", "order.stockShort", "not enough stock", "left", n) 返回的错误说明
err: { 'order.stockShort': '库存不足，只剩 {left} 件' },
```

### 14. 跑起来看

```bash
make migrate && make dev      # 后端；新表会被建出来，之后改源码自动重启
make web-dev                  # 前端
```

用超管登录就能看到新菜单（超管跳过权限判定）。上线后想改菜单的显示名、图标、顺序，或者把它归到某个分组下，在"权限管理 → 菜单管理"里调，不用改代码；路径、页面和权限码仍以代码为准。给普通角色用：角色管理 → 授权，勾上 `order:order:*`，该角色的用户下一次请求立即生效，不用重新登录。

`make ci` 全绿、`docs/api.md` 登记完，就可以提交了。
