-- GoAllAdmin 框架表。
-- 列约定见 docs/conventions.md：实体表底部固定为
--   status, sort, remark, created_at, updated_at[, deleted_at], created_by, updated_by[, deleted_by]
-- 框架表不软删（账号用 status 停用，角色只在无引用时物理删除，日志只增），所以没有 deleted_at / deleted_by。
-- bigint unsigned 自增主键；datetime(3) 存 UTC；utf8mb4_0900_ai_ci。

CREATE TABLE IF NOT EXISTS `ga_user` (
  `id`              bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `username`        varchar(64)     NOT NULL COMMENT '登录账号',
  `password_hash`   varchar(100)    NOT NULL COMMENT 'bcrypt 哈希',
  `display_name`    varchar(64)     NOT NULL DEFAULT '' COMMENT '显示名',
  `email`           varchar(128)    NOT NULL DEFAULT '' COMMENT '邮箱',
  `phone`           varchar(32)     NOT NULL DEFAULT '' COMMENT '手机号',
  `avatar`          varchar(255)    NOT NULL DEFAULT '' COMMENT '头像',
  `must_change_pwd` tinyint         NOT NULL DEFAULT 0 COMMENT '下次登录必须改密 1是 0否',
  `pwd_changed_at`  datetime(3)     NULL COMMENT '上次改密时间',
  `mfa_secret`      varbinary(255)  NULL COMMENT '二次验证密钥，预留',
  `last_login_at`   datetime(3)     NULL COMMENT '最后登录时间',
  `last_login_ip`   varchar(64)     NOT NULL DEFAULT '' COMMENT '最后登录IP',
  `status`          tinyint         NOT NULL DEFAULT 1 COMMENT '状态 1启用 0禁用',
  `sort`            int unsigned    NOT NULL DEFAULT 0 COMMENT '排序，越小越靠前',
  `remark`          varchar(255)    NOT NULL DEFAULT '' COMMENT '备注',
  `created_at`      datetime(3)     NOT NULL COMMENT '创建时间',
  `updated_at`      datetime(3)     NOT NULL COMMENT '更新时间',
  `created_by`      bigint unsigned NOT NULL DEFAULT 0 COMMENT '创建人',
  `updated_by`      bigint unsigned NOT NULL DEFAULT 0 COMMENT '更新人',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_username` (`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='平台端用户';

CREATE TABLE IF NOT EXISTS `ga_role` (
  `id`          bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `portal`      varchar(32)     NOT NULL COMMENT '所属端',
  `code`        varchar(64)     NOT NULL COMMENT '角色编码，同端内唯一，创建后不可修改',
  `name`        varchar(64)     NOT NULL COMMENT '角色名',
  `is_super`    tinyint         NOT NULL DEFAULT 0 COMMENT '超级管理员 1是 0否',
  `status`      tinyint         NOT NULL DEFAULT 1 COMMENT '状态 1启用 0禁用',
  `sort`        int unsigned    NOT NULL DEFAULT 0 COMMENT '排序，越小越靠前',
  `remark`      varchar(255)    NOT NULL DEFAULT '' COMMENT '备注',
  `created_at`  datetime(3)     NOT NULL COMMENT '创建时间',
  `updated_at`  datetime(3)     NOT NULL COMMENT '更新时间',
  `created_by`  bigint unsigned NOT NULL DEFAULT 0 COMMENT '创建人',
  `updated_by`  bigint unsigned NOT NULL DEFAULT 0 COMMENT '更新人',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_role_portal_code` (`portal`, `code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='角色';

-- 关系表：没有状态、排序、时间列。
CREATE TABLE IF NOT EXISTS `ga_user_role` (
  `portal`  varchar(32)     NOT NULL COMMENT '所属端',
  `user_id` bigint unsigned NOT NULL COMMENT '该端用户 ID',
  `role_id` bigint unsigned NOT NULL COMMENT '角色 ID',
  PRIMARY KEY (`portal`, `user_id`, `role_id`),
  KEY `idx_user_role_role` (`role_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='用户-角色';

-- 授权规则表：结构由授权库决定，只改表名。
CREATE TABLE IF NOT EXISTS `ga_casbin_rule` (
  `id`    bigint unsigned NOT NULL AUTO_INCREMENT,
  `ptype` varchar(100) NULL,
  `v0`    varchar(100) NULL,
  `v1`    varchar(100) NULL,
  `v2`    varchar(100) NULL,
  `v3`    varchar(100) NULL,
  `v4`    varchar(100) NULL,
  `v5`    varchar(100) NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_casbin_rule` (`ptype`, `v0`, `v1`, `v2`, `v3`, `v4`, `v5`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='角色 → 权限码';

-- 会话表：系统自动维护，不套实体表的底部块。
CREATE TABLE IF NOT EXISTS `ga_session` (
  `id`                bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `sid`               char(32)        NOT NULL COMMENT '会话 ID，写进访问令牌',
  `portal`            varchar(32)     NOT NULL COMMENT '所属端',
  `user_id`           bigint unsigned NOT NULL COMMENT '用户 ID',
  `refresh_hash`      char(64)        NOT NULL COMMENT '当前刷新凭证的 SHA-256',
  `prev_refresh_hash` char(64)        NULL COMMENT '上一个刷新凭证的 SHA-256',
  `rotated_at`        datetime(3)     NOT NULL COMMENT '上次轮换时间',
  `expires_at`        datetime(3)     NOT NULL COMMENT '过期时间',
  `revoked_at`        datetime(3)     NULL COMMENT '吊销时间',
  `revoke_reason`     varchar(32)     NOT NULL DEFAULT '' COMMENT '吊销原因 logout pwd_change disabled reuse_detected admin',
  `ip`                varchar(64)     NOT NULL DEFAULT '' COMMENT '登录 IP',
  `user_agent`        varchar(255)    NOT NULL DEFAULT '' COMMENT '客户端标识',
  `last_seen_at`      datetime(3)     NOT NULL COMMENT '最后活动时间',
  `created_at`        datetime(3)     NOT NULL COMMENT '创建时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_session_sid` (`sid`),
  KEY `idx_session_portal_user` (`portal`, `user_id`),
  KEY `idx_session_expires` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='登录会话';

-- 日志表：只增不改，只有 created_at。
CREATE TABLE IF NOT EXISTS `ga_login_log` (
  `id`         bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `portal`     varchar(32)     NOT NULL COMMENT '所属端',
  `username`   varchar(64)     NOT NULL DEFAULT '' COMMENT '尝试登录的账号',
  `user_id`    bigint unsigned NOT NULL DEFAULT 0 COMMENT '用户 ID，账号不存在时为 0',
  `success`    tinyint         NOT NULL DEFAULT 0 COMMENT '是否成功 1是 0否',
  `reason`     varchar(64)     NOT NULL DEFAULT '' COMMENT '失败原因',
  `ip`         varchar(64)     NOT NULL DEFAULT '' COMMENT '来源 IP',
  `user_agent` varchar(255)    NOT NULL DEFAULT '' COMMENT '客户端标识',
  `request_id` varchar(64)     NOT NULL DEFAULT '' COMMENT '请求 ID',
  `created_at` datetime(3)     NOT NULL COMMENT '创建时间',
  PRIMARY KEY (`id`),
  KEY `idx_login_log_user` (`portal`, `username`, `created_at`),
  KEY `idx_login_log_created` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='登录日志';

CREATE TABLE IF NOT EXISTS `ga_operation_log` (
  `id`          bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `request_id`  varchar(64)     NOT NULL DEFAULT '' COMMENT '请求 ID',
  `portal`      varchar(32)     NOT NULL COMMENT '所属端',
  `user_id`     bigint unsigned NOT NULL DEFAULT 0 COMMENT '操作人 ID',
  `username`    varchar(64)     NOT NULL DEFAULT '' COMMENT '操作人账号',
  `action`      varchar(64)     NOT NULL DEFAULT '' COMMENT '动作名',
  `method`      varchar(10)     NOT NULL DEFAULT '' COMMENT 'HTTP 方法',
  `path`        varchar(255)    NOT NULL DEFAULT '' COMMENT '路径',
  `query`       varchar(1024)   NOT NULL DEFAULT '' COMMENT '查询串，已脱敏',
  `body`        text            NULL COMMENT '请求体，已脱敏，最多 4 KB',
  `http_status` int             NOT NULL DEFAULT 0 COMMENT 'HTTP 状态码',
  `code`        int             NOT NULL DEFAULT 0 COMMENT '业务错误码',
  `latency_ms`  int             NOT NULL DEFAULT 0 COMMENT '耗时毫秒',
  `ip`          varchar(64)     NOT NULL DEFAULT '' COMMENT '来源 IP',
  `user_agent`  varchar(255)    NOT NULL DEFAULT '' COMMENT '客户端标识',
  `error`       varchar(512)    NOT NULL DEFAULT '' COMMENT '错误摘要',
  `created_at`  datetime(3)     NOT NULL COMMENT '创建时间',
  PRIMARY KEY (`id`),
  KEY `idx_oplog_user` (`portal`, `user_id`, `created_at`),
  KEY `idx_oplog_created` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='操作日志';

-- 只建超级管理员角色，不建任何账号；首个账号用命令行创建。
-- 写成可重复执行的形式：MySQL 的 DDL 会隐式提交，一个迁移文件中途失败时前面建好的表不会回滚，
-- 重跑必须能跳过已经存在的表和已经插入的行（上面的 IF NOT EXISTS 也是为此）。
INSERT INTO `ga_role` (`portal`, `code`, `name`, `is_super`, `status`, `sort`, `remark`, `created_at`, `updated_at`, `created_by`, `updated_by`)
VALUES ('platform', 'super', 'Super Administrator', 1, 1, 0, 'built-in', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), 0, 0)
ON DUPLICATE KEY UPDATE `is_super` = 1;
