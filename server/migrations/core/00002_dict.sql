-- 字典（docs/decisions.md D-023）。
-- 两种来源共用这两张表：模块在代码里声明的字典（source = code，启动时同步），后台新建的字典（source = admin）。
-- 框架表不软删；列约定见 docs/conventions.md。可重跑（D-019）。
-- code 和 value 用 utf8mb4_bin：它们是标识符，按字节精确匹配。用表默认的 utf8mb4_0900_ai_ci 的话，
-- "e" 与 "é"、"ss" 与 "ß" 会被唯一键视为相同，而 Go 代码里的比较认为不同，两边对不上。

CREATE TABLE IF NOT EXISTS `ga_dict` (
  `id`          bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `portal`      varchar(32)     NOT NULL DEFAULT '*' COMMENT '所属端；* 表示所有端共用',
  `code`        varchar(64)     COLLATE utf8mb4_bin NOT NULL COMMENT '字典编码；代码声明的形如 模块名.xxx，后台新建的不含点；按字节比较',
  `name`        varchar(64)     NOT NULL DEFAULT '' COMMENT '名称（默认语言）',
  `name_i18n`   varchar(1024)   NOT NULL DEFAULT '' COMMENT '其他语言的名称，JSON 对象 {"en-US": "..."}',
  `value_type`  varchar(8)      NOT NULL DEFAULT 'string' COMMENT '值类型 string 或 int',
  `source`      varchar(8)      NOT NULL DEFAULT 'admin' COMMENT '来源 code 代码声明 admin 后台新建',
  `status`      tinyint         NOT NULL DEFAULT 1 COMMENT '状态 1启用 0禁用',
  `sort`        int unsigned    NOT NULL DEFAULT 0 COMMENT '排序，越小越靠前',
  `remark`      varchar(255)    NOT NULL DEFAULT '' COMMENT '备注',
  `created_at`  datetime(3)     NOT NULL COMMENT '创建时间',
  `updated_at`  datetime(3)     NOT NULL COMMENT '更新时间',
  `created_by`  bigint unsigned NOT NULL DEFAULT 0 COMMENT '创建人',
  `updated_by`  bigint unsigned NOT NULL DEFAULT 0 COMMENT '更新人',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_dict_code` (`code`),
  KEY `idx_dict_portal` (`portal`, `sort`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='字典';

CREATE TABLE IF NOT EXISTS `ga_dict_item` (
  `id`          bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `dict_id`     bigint unsigned NOT NULL COMMENT '所属字典',
  `parent_id`   bigint unsigned NOT NULL DEFAULT 0 COMMENT '父项；0 表示顶层',
  `value`       varchar(64)     COLLATE utf8mb4_bin NOT NULL COMMENT '值；同一字典内唯一，按字节比较（区分大小写和重音）',
  `label`       varchar(128)    NOT NULL DEFAULT '' COMMENT '显示文字（默认语言）',
  `label_i18n`  varchar(1024)   NOT NULL DEFAULT '' COMMENT '其他语言的显示文字，JSON 对象 {"en-US": "..."}',
  `color`       varchar(16)     NOT NULL DEFAULT '' COMMENT '标签颜色：primary/success/warning/danger/info 或 #RRGGBB',
  `extra`       varchar(255)    NOT NULL DEFAULT '' COMMENT '扩展值，业务自定',
  `locked`      tinyint         NOT NULL DEFAULT 0 COMMENT '代码声明的项 1是 0否；是则值不可改、不可删',
  `overridden`  tinyint         NOT NULL DEFAULT 0 COMMENT '代码项的显示已被后台修改 1是 0否；是则同步不覆盖',
  `status`      tinyint         NOT NULL DEFAULT 1 COMMENT '状态 1启用 0禁用',
  `sort`        int unsigned    NOT NULL DEFAULT 0 COMMENT '排序，越小越靠前',
  `remark`      varchar(255)    NOT NULL DEFAULT '' COMMENT '备注',
  `created_at`  datetime(3)     NOT NULL COMMENT '创建时间',
  `updated_at`  datetime(3)     NOT NULL COMMENT '更新时间',
  `created_by`  bigint unsigned NOT NULL DEFAULT 0 COMMENT '创建人',
  `updated_by`  bigint unsigned NOT NULL DEFAULT 0 COMMENT '更新人',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_dict_item_value` (`dict_id`, `value`),
  KEY `idx_dict_item_parent` (`dict_id`, `parent_id`, `sort`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='字典项';
