-- 菜单管理（docs/decisions.md D-025）。
-- 菜单的结构（路径、页面组件、权限码）由模块在代码里声明，不进这张表；这里只存"与代码不同的部分"：
--   kind = code  ：对代码菜单的显示覆盖（显示名、图标、隐藏、位置），与代码不再有差异时整行删除；
--   kind = group ：后台新建的分组（纯目录，没有路径、页面和权限码）。
-- 框架表不软删；列约定见 docs/conventions.md。可重跑（D-019）。
-- name 和 parent 是标识符，用 utf8mb4_bin 按字节精确匹配（同 D-023）。

CREATE TABLE IF NOT EXISTS `ga_menu_custom` (
  `id`          bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `portal`      varchar(32)     NOT NULL COMMENT '所属端',
  `name`        varchar(64)     COLLATE utf8mb4_bin NOT NULL COMMENT '菜单名：代码菜单的 Name，或服务端生成的分组名（@g- 开头）',
  `kind`        varchar(8)      NOT NULL DEFAULT 'code' COMMENT '类型 code 代码菜单的覆盖 group 后台分组',
  `titles`      varchar(1024)   NOT NULL DEFAULT '' COMMENT '显示名，JSON 对象 {"zh-CN": "...", "en-US": "..."}；空表示沿用代码的翻译键',
  `icon`        varchar(64)     NOT NULL DEFAULT '' COMMENT '图标名；代码菜单为空表示沿用代码',
  `hidden`      tinyint         NOT NULL DEFAULT 0 COMMENT '在侧边栏隐藏 1是 0否；只能在代码之外再隐藏',
  `moved`       tinyint         NOT NULL DEFAULT 0 COMMENT '位置已调整 1是 0否；是则 parent 和 sort 生效（分组总是 1）',
  `parent`      varchar(64)     COLLATE utf8mb4_bin NOT NULL DEFAULT '' COMMENT '上级菜单名；空表示顶级',
  `sort`        int unsigned    NOT NULL DEFAULT 0 COMMENT '排序，越小越靠前',
  `created_at`  datetime(3)     NOT NULL COMMENT '创建时间',
  `updated_at`  datetime(3)     NOT NULL COMMENT '更新时间',
  `created_by`  bigint unsigned NOT NULL DEFAULT 0 COMMENT '创建人',
  `updated_by`  bigint unsigned NOT NULL DEFAULT 0 COMMENT '更新人',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_menu_custom_name` (`portal`, `name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='菜单的后台调整';
