-- 主体与主体账号（docs/decisions.md D-065）：代理商、商户各一张主体表、一张账号表。
-- 平台程序执行这个迁移；代理商、商户程序只核对版本（D-061）。可重跑。

CREATE TABLE IF NOT EXISTS `ga_agent` (
  `id`              bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `code`            varchar(16)     NOT NULL             COMMENT '代理商编号：A 加 8 位随机数字，登录时输入；创建后不可改',
  `name`            varchar(64)     NOT NULL DEFAULT ''  COMMENT '名称',
  `contact_name`    varchar(64)     NOT NULL DEFAULT ''  COMMENT '联系人',
  `contact_phone`   varchar(32)     NOT NULL DEFAULT ''  COMMENT '联系电话',
  `owner_user_id`   bigint unsigned NOT NULL DEFAULT 0   COMMENT '主账号（ga_agent_user.id），主体内的超管；0 表示还没有',
  `status`          tinyint         NOT NULL DEFAULT 1   COMMENT '状态 1启用 0停用；停用后它的账号都按停用处理',
  `sort`            int unsigned    NOT NULL DEFAULT 0   COMMENT '排序，越小越靠前',
  `remark`          varchar(255)    NOT NULL DEFAULT ''  COMMENT '备注',
  `created_at`      datetime(3)     NOT NULL             COMMENT '创建时间',
  `updated_at`      datetime(3)     NOT NULL             COMMENT '更新时间',
  `created_by`      bigint unsigned NOT NULL DEFAULT 0   COMMENT '创建人（平台端的用户 ID）',
  `updated_by`      bigint unsigned NOT NULL DEFAULT 0   COMMENT '更新人（平台端的用户 ID）',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_agent_code` (`code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='代理商';

CREATE TABLE IF NOT EXISTS `ga_merchant` (
  `id`              bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `code`            varchar(16)     NOT NULL             COMMENT '商户编号：M 加 8 位随机数字，登录时输入；创建后不可改',
  `name`            varchar(64)     NOT NULL DEFAULT ''  COMMENT '名称',
  `contact_name`    varchar(64)     NOT NULL DEFAULT ''  COMMENT '联系人',
  `contact_phone`   varchar(32)     NOT NULL DEFAULT ''  COMMENT '联系电话',
  `agent_id`        bigint unsigned NOT NULL DEFAULT 0   COMMENT '所属代理商（ga_agent.id）；0 表示直属平台。只能在平台端改',
  `owner_user_id`   bigint unsigned NOT NULL DEFAULT 0   COMMENT '主账号（ga_merchant_user.id），主体内的超管；0 表示还没有',
  `status`          tinyint         NOT NULL DEFAULT 1   COMMENT '状态 1启用 0停用；停用后它的账号都按停用处理',
  `sort`            int unsigned    NOT NULL DEFAULT 0   COMMENT '排序，越小越靠前',
  `remark`          varchar(255)    NOT NULL DEFAULT ''  COMMENT '备注',
  `created_at`      datetime(3)     NOT NULL             COMMENT '创建时间',
  `updated_at`      datetime(3)     NOT NULL             COMMENT '更新时间',
  `created_by`      bigint unsigned NOT NULL DEFAULT 0   COMMENT '创建人（平台端的用户 ID）',
  `updated_by`      bigint unsigned NOT NULL DEFAULT 0   COMMENT '更新人（平台端的用户 ID）',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_merchant_code` (`code`),
  KEY `idx_merchant_agent` (`agent_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='商户';

CREATE TABLE IF NOT EXISTS `ga_agent_user` (
  `id`              bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键（代理商端内的用户 ID）',
  `org_id`          bigint unsigned NOT NULL             COMMENT '所属代理商（ga_agent.id）；账号不能换主体',
  `username`        varchar(64)     NOT NULL             COMMENT '登录账号，代理商内唯一',
  `password_hash`   varchar(100)    NOT NULL             COMMENT 'bcrypt 哈希',
  `display_name`    varchar(64)     NOT NULL DEFAULT ''  COMMENT '显示名',
  `email`           varchar(128)    NOT NULL DEFAULT ''  COMMENT '邮箱',
  `phone`           varchar(32)     NOT NULL DEFAULT ''  COMMENT '手机号',
  `avatar`          varchar(255)    NOT NULL DEFAULT ''  COMMENT '头像（这一版只有内置头像）',
  `must_change_pwd` tinyint         NOT NULL DEFAULT 0   COMMENT '下次登录必须改密 1是 0否',
  `pwd_changed_at`  datetime(3)     NULL                 COMMENT '上次改密时间',
  `last_login_at`   datetime(3)     NULL                 COMMENT '最后登录时间',
  `last_login_ip`   varchar(64)     NOT NULL DEFAULT ''  COMMENT '最后登录IP',
  `status`          tinyint         NOT NULL DEFAULT 1   COMMENT '状态 1启用 0禁用',
  `sort`            int unsigned    NOT NULL DEFAULT 0   COMMENT '排序，越小越靠前',
  `remark`          varchar(255)    NOT NULL DEFAULT ''  COMMENT '备注',
  `created_at`      datetime(3)     NOT NULL             COMMENT '创建时间',
  `updated_at`      datetime(3)     NOT NULL             COMMENT '更新时间',
  `created_by`      bigint unsigned NOT NULL DEFAULT 0   COMMENT '创建人（本端的用户 ID；平台随主体一起建的主账号为 0）',
  `updated_by`      bigint unsigned NOT NULL DEFAULT 0   COMMENT '更新人（本端的用户 ID；平台改的为 0）',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_agent_user_org_username` (`org_id`, `username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='代理商账号';

CREATE TABLE IF NOT EXISTS `ga_merchant_user` (
  `id`              bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键（商户端内的用户 ID）',
  `org_id`          bigint unsigned NOT NULL             COMMENT '所属商户（ga_merchant.id）；账号不能换主体',
  `username`        varchar(64)     NOT NULL             COMMENT '登录账号，商户内唯一',
  `password_hash`   varchar(100)    NOT NULL             COMMENT 'bcrypt 哈希',
  `display_name`    varchar(64)     NOT NULL DEFAULT ''  COMMENT '显示名',
  `email`           varchar(128)    NOT NULL DEFAULT ''  COMMENT '邮箱',
  `phone`           varchar(32)     NOT NULL DEFAULT ''  COMMENT '手机号',
  `avatar`          varchar(255)    NOT NULL DEFAULT ''  COMMENT '头像（这一版只有内置头像）',
  `must_change_pwd` tinyint         NOT NULL DEFAULT 0   COMMENT '下次登录必须改密 1是 0否',
  `pwd_changed_at`  datetime(3)     NULL                 COMMENT '上次改密时间',
  `last_login_at`   datetime(3)     NULL                 COMMENT '最后登录时间',
  `last_login_ip`   varchar(64)     NOT NULL DEFAULT ''  COMMENT '最后登录IP',
  `status`          tinyint         NOT NULL DEFAULT 1   COMMENT '状态 1启用 0禁用',
  `sort`            int unsigned    NOT NULL DEFAULT 0   COMMENT '排序，越小越靠前',
  `remark`          varchar(255)    NOT NULL DEFAULT ''  COMMENT '备注',
  `created_at`      datetime(3)     NOT NULL             COMMENT '创建时间',
  `updated_at`      datetime(3)     NOT NULL             COMMENT '更新时间',
  `created_by`      bigint unsigned NOT NULL DEFAULT 0   COMMENT '创建人（本端的用户 ID；平台随主体一起建的主账号为 0）',
  `updated_by`      bigint unsigned NOT NULL DEFAULT 0   COMMENT '更新人（本端的用户 ID；平台改的为 0）',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_merchant_user_org_username` (`org_id`, `username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='商户账号';
