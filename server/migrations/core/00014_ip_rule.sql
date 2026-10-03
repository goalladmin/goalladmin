-- IP 黑名单与白名单（docs/decisions.md D-062）。三个程序（平台、代理商、商户）都读这张表，在内存里匹配。
-- 黑名单只有 global 一种；白名单按端、主体、账号三层。规则不修改，只增删（白名单整份替换）；停用的规则不生效。

CREATE TABLE IF NOT EXISTS `ga_ip_rule` (
  `id`          bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `kind`        varchar(8)      NOT NULL DEFAULT ''  COMMENT 'deny 黑名单 / allow 白名单',
  `scope`       varchar(8)      NOT NULL DEFAULT ''  COMMENT 'global 全部程序 / portal 端 / org 主体 / user 账号',
  `portal`      varchar(32)     NOT NULL DEFAULT ''  COMMENT '端代号；global 为空',
  `org_id`      bigint unsigned NOT NULL DEFAULT 0   COMMENT '主体（scope 为 org、主体端的 user 时）；否则为 0',
  `user_id`     bigint unsigned NOT NULL DEFAULT 0   COMMENT '账号（scope 为 user 时，端内的用户 ID）；否则为 0',
  `cidr`        varchar(64)     NOT NULL DEFAULT ''  COMMENT '规范化后的网段，单个地址存成 /32、/128',
  `expires_at`  datetime(3)     NULL                 COMMENT '到期时间（只有黑名单用）；空表示永久',
  `status`      tinyint         NOT NULL DEFAULT 1   COMMENT '状态 1启用 0禁用',
  `sort`        int unsigned    NOT NULL DEFAULT 0   COMMENT '排序，越小越靠前',
  `remark`      varchar(255)    NOT NULL DEFAULT ''  COMMENT '备注',
  `created_at`  datetime(3)     NOT NULL             COMMENT '创建时间',
  `updated_at`  datetime(3)     NOT NULL             COMMENT '更新时间',
  `created_by`  bigint unsigned NOT NULL DEFAULT 0   COMMENT '创建人',
  `updated_by`  bigint unsigned NOT NULL DEFAULT 0   COMMENT '更新人',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_ip_rule` (`kind`, `scope`, `portal`, `org_id`, `user_id`, `cidr`),
  KEY `idx_ip_rule_target` (`scope`, `portal`, `org_id`, `user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='IP 黑名单与白名单';

-- 变更序号：写名单时在同一事务里加一，其他程序据此判断要不要重读名单（不用定时扫全表）。
-- 这一行同时是写名单的串行化点：每次写先更新它，并发的写入按提交顺序排队。
CREATE TABLE IF NOT EXISTS `ga_change_seq` (
  `name`        varchar(32)     NOT NULL             COMMENT '数据集名称',
  `seq`         bigint unsigned NOT NULL DEFAULT 0   COMMENT '变更序号，每次改动加一',
  `updated_at`  datetime(3)     NOT NULL             COMMENT '最后一次改动的时间',
  PRIMARY KEY (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='跨程序缓存的变更序号';

INSERT IGNORE INTO `ga_change_seq` (`name`, `seq`, `updated_at`) VALUES ('ip_rules', 0, UTC_TIMESTAMP(3));
