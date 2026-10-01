-- 部门与岗位（docs/decisions.md D-033）：只是组织资料，不影响权限判断。
-- MySQL 8.0 的 ADD COLUMN / ADD KEY 没有 IF NOT EXISTS：先查 information_schema，不存在才执行，文件可以重跑（D-019）。

CREATE TABLE IF NOT EXISTS `ga_dept` (
  `id`             bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `parent_id`      bigint unsigned NOT NULL DEFAULT 0 COMMENT '上级部门，0 表示顶级',
  `name`           varchar(64)     NOT NULL COMMENT '部门名，同一上级下唯一',
  `leader_user_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT '负责人（ga_user.id），0 表示未设置',
  `phone`          varchar(32)     NOT NULL DEFAULT '' COMMENT '联系电话',
  `email`          varchar(128)    NOT NULL DEFAULT '' COMMENT '邮箱',
  `status`         tinyint         NOT NULL DEFAULT 1 COMMENT '状态 1启用 0禁用',
  `sort`           int unsigned    NOT NULL DEFAULT 0 COMMENT '排序，越小越靠前',
  `remark`         varchar(255)    NOT NULL DEFAULT '' COMMENT '备注',
  `created_at`     datetime(3)     NOT NULL COMMENT '创建时间',
  `updated_at`     datetime(3)     NOT NULL COMMENT '更新时间',
  `created_by`     bigint unsigned NOT NULL DEFAULT 0 COMMENT '创建人',
  `updated_by`     bigint unsigned NOT NULL DEFAULT 0 COMMENT '更新人',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_dept_parent_name` (`parent_id`, `name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='部门';

CREATE TABLE IF NOT EXISTS `ga_post` (
  `id`          bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `code`        varchar(64)     NOT NULL COMMENT '岗位编码，唯一，创建后不可修改',
  `name`        varchar(64)     NOT NULL COMMENT '岗位名',
  `status`      tinyint         NOT NULL DEFAULT 1 COMMENT '状态 1启用 0禁用',
  `sort`        int unsigned    NOT NULL DEFAULT 0 COMMENT '排序，越小越靠前',
  `remark`      varchar(255)    NOT NULL DEFAULT '' COMMENT '备注',
  `created_at`  datetime(3)     NOT NULL COMMENT '创建时间',
  `updated_at`  datetime(3)     NOT NULL COMMENT '更新时间',
  `created_by`  bigint unsigned NOT NULL DEFAULT 0 COMMENT '创建人',
  `updated_by`  bigint unsigned NOT NULL DEFAULT 0 COMMENT '更新人',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_post_code` (`code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='岗位';

CREATE TABLE IF NOT EXISTS `ga_user_post` (
  `user_id` bigint unsigned NOT NULL COMMENT '用户 ID',
  `post_id` bigint unsigned NOT NULL COMMENT '岗位 ID',
  PRIMARY KEY (`user_id`, `post_id`),
  KEY `idx_user_post_post` (`post_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='用户-岗位';

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_user` ADD COLUMN `dept_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT ''所属部门（ga_dept.id），0 表示未分配'' AFTER `avatar`',
    'SELECT 1')
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_user' AND COLUMN_NAME = 'dept_id'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_user` ADD KEY `idx_user_dept` (`dept_id`)',
    'SELECT 1')
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_user' AND INDEX_NAME = 'idx_user_dept'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;
