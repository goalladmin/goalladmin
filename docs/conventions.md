# 约定

代码和数据库层面的固定做法。规则本身不长，但每一条都是全项目统一的，不接受局部例外；确有必要偏离时先在 `decisions.md` 记一条。

## 数据表

### 通用

- 表名小写下划线，框架表前缀 `ga_`，业务表用业务自己的前缀。
- 主键 `id bigint unsigned NOT NULL AUTO_INCREMENT`，JSON 里按数字输出。
- 时间列 `datetime(3)`，存 UTC；连接串带 `parseTime=true&loc=UTC`；接口输出 RFC 3339。
- 字符集 `utf8mb4`，排序规则 `utf8mb4_0900_ai_ci`。例外：按字节精确匹配的标识符列（字典编码、字典值、菜单名）用 `utf8mb4_bin`，见 D-023、D-025。
- 每一列都写 `COMMENT`。字符串列 `NOT NULL DEFAULT ''`，数值列 `NOT NULL DEFAULT 0`；只有"未发生"才有意义的时间列（`deleted_at`、`last_login_at` 这类）允许 `NULL`。
- 表结构只通过 `migrations/` 下的编号 SQL 文件变更，代码里不做自动建表。

### 实体表的底部固定块

所有实体表（用户、角色、以及业务方的商品、订单这类"有人维护的记录"）的最后几列固定为下面这组，顺序不变：

```sql
  `status`      tinyint         NOT NULL DEFAULT 1  COMMENT '状态 1启用 0禁用',
  `sort`        int unsigned    NOT NULL DEFAULT 0  COMMENT '排序，越小越靠前',
  `remark`      varchar(255)    NOT NULL DEFAULT '' COMMENT '备注',
  `created_at`  datetime(3)     NOT NULL            COMMENT '创建时间',
  `updated_at`  datetime(3)     NOT NULL            COMMENT '更新时间',
  `deleted_at`  datetime(3)     NULL                COMMENT '删除时间',   -- 仅软删表
  `created_by`  bigint unsigned NOT NULL DEFAULT 0  COMMENT '创建人',
  `updated_by`  bigint unsigned NOT NULL DEFAULT 0  COMMENT '更新人',
  `deleted_by`  bigint unsigned NOT NULL DEFAULT 0  COMMENT '删除人',     -- 仅软删表
```

- `status`：1 启用、0 禁用，全项目统一这个口径；需要更多状态的业务字段另起名字（如 `audit_status`），不复用 `status`。
- `sort`：升序排列，越小越靠前；相同 `sort` 时按 `id` 升序，保证顺序稳定。给菜单、字典这类手工排序的数据留间隔（10、20、30）。
- `created_by` / `updated_by` / `deleted_by` 存操作人的用户 ID；由系统自动写入的行为 0。
- **`deleted_at` / `deleted_by` 只出现在真正使用软删除的表上。** 软删表不能在用户可编辑的列上建唯一索引，否则已删除的行会一直占着唯一键，同名记录再也建不出来。
- 框架表一律不软删：账号用 `status` 停用，角色只在没有引用时物理删除，日志表只增不改。

### 不套底部块的表

- **日志表**（登录日志、操作日志）：只增不改，只有 `created_at`，没有状态、排序、备注和修改人。
- **关系表**（用户-角色）：只有外键列和主键，没有时间列。
- **系统自动维护的表**（会话、授权规则、迁移版本）：按各自需要设计，不加 `status`、`sort`、`remark`。

## 字典

- 代码声明的字典编码：`<模块名>.<名字>`，全小写，段内用下划线（`order.pay_status`）；后台新建的字典编码不含点。
- 业务规则里判断的枚举值必须来自代码声明的字典；后台字典只用于显示和选择，代码不依赖它的值。
- 表里存字典值的列用与字典值类型一致的类型（`int` 字典对应整数列），列注释写明字典编码。
- 状态、排序这类全项目统一口径的列（`status` 1 启用 0 禁用）不做成字典。

## 代码里的对应

- GORM 模型的字段顺序与表一致，底部块用 `gorm:"column:..."` 显式标列名，不依赖自动推断。
- 更新用显式列白名单（`Select(...)` 或 `Updates(map)`），不用整个结构体 `Save`，避免把 `created_at`、`created_by` 一起覆盖。
- `created_by` / `updated_by` 由 service 层从当前身份取值写入，不从请求参数里读。

## 前端

- 页面放在 `web/apps/<端>/src/views/` 下，路径就是后端菜单声明里的 `Component`：`system/user/index` ↔ `views/system/user/index.vue`。映射表来自 `import.meta.glob`，不在表里的键渲染 404，禁止拼接路径动态 import。
- 业务页面只从 `@ga/shell` 引用框架能力（`useRequest`、`useAuthStore`、`useTable`、`useI18n`、`formatTime`、`hasPerm` 等）；`@ga/shell` 的 `exports` 只开放包根和 `./styles`（另有给应用的 ESLint 配置用的 `./eslint`），深层路径 import 会被拒绝。每个端的应用只能引用自己目录里的文件和 `@ga/shell`，不能用相对路径或包名引用别的端，ESLint 规则 `ga/boundary` 检查（D-064）。
- 接口调用集中在 `src/api/*.ts`，页面不直接拼 URL；类型与 `docs/api.md` 对应。
- 文案键：菜单 `menu.<name>`、权限分组 `permGroup.<group>`、权限名 `perm.<a>.<b>.<c>`、操作日志动作 `op.<action>`、登录失败原因 `loginReason.<reason>`；文案放在 `src/locales/<语言代码>.ts`，每种语言一个文件（`zh-CN.ts` 是基准，其他文件键和占位符必须与它一致，测试会检查），用 `localesFromGlob(import.meta.glob('./locales/*.ts', { eager: true }))` 传给 `createPortalApp`，与壳的深合并。文案里不能出现 `@`、`|`、`$`，花括号只用于 `{占位符}`。接口错误说明的键放在 `err` 下（`err: { 'order.stockShort': '...' }`），与后端 `httpx.NewField` / `httpx.NewKey` 的键一致，键以模块名开头；后端的 `message` 写英文兜底。带点的键（如 `'system.user'`）和目录键（`system`）可以并存。
- 显示字典值用 `<GaDictTag code="..." :value="..." />`，下拉用 `useDict(code).options`（只含启用的项）；不要在页面里另写一份"值 → 文字"的对照表。
- 访问令牌只在内存里；`localStorage` 只放语言、暗色、折叠这类偏好。
- 按钮权限用 `v-perm="'a:b:c'"` / `v-perm:all="[...]"` / `<GaPerm perm="...">`；只影响显示，安全以后端守卫为准。
- 给冒烟测试留 `data-test="..."` 属性；放在 `el-input` 上时会透传到内层 `<input>`。
