-- 安全事件（docs/decisions.md D-032 第 4 条）：越权被拒、令牌异常、凭证重放等攻击迹象。
-- 同一来源在同一分钟内合并成一行（dedup_key + window_start 唯一），只累加 count、更新 last_at；
-- 分钟过去后这一行不再变化。不删除，也没有删除接口。

CREATE TABLE IF NOT EXISTS `ga_security_event` (
  `id`           bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `dedup_key`    char(32)        NOT NULL COMMENT '合并键：端、类型、用户、会话、IP、方法、路径、说明的指纹',
  `window_start` datetime        NOT NULL COMMENT '所在分钟',
  `portal`       varchar(32)     NOT NULL DEFAULT '' COMMENT '所属端',
  `kind`         varchar(32)     NOT NULL COMMENT '事件类型',
  `level`        tinyint         NOT NULL DEFAULT 2 COMMENT '级别 1提示 2警告 3严重',
  `user_id`      bigint unsigned NOT NULL DEFAULT 0 COMMENT '用户 ID，不知道时为 0',
  `username`     varchar(64)     NOT NULL DEFAULT '' COMMENT '账号',
  `session_id`   char(32)        NOT NULL DEFAULT '' COMMENT '会话 ID',
  `ip`           varchar(64)     NOT NULL DEFAULT '' COMMENT '来源 IP',
  `user_agent`   varchar(255)    NOT NULL DEFAULT '' COMMENT '客户端标识（第一次的）',
  `method`       varchar(10)     NOT NULL DEFAULT '' COMMENT 'HTTP 方法',
  `path`         varchar(255)    NOT NULL DEFAULT '' COMMENT '路由模板或路径',
  `detail`       varchar(255)    NOT NULL DEFAULT '' COMMENT '说明，如被拒绝的权限码',
  `request_id`   varchar(64)     NOT NULL DEFAULT '' COMMENT '第一次的请求 ID',
  `count`        bigint unsigned NOT NULL DEFAULT 0 COMMENT '这一分钟内的次数',
  `first_at`     datetime(3)     NOT NULL COMMENT '第一次',
  `last_at`      datetime(3)     NOT NULL COMMENT '最后一次',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_security_event_dedup` (`dedup_key`, `window_start`),
  KEY `idx_security_event_first` (`first_at`),
  KEY `idx_security_event_kind` (`kind`, `first_at`),
  KEY `idx_security_event_ip` (`ip`, `first_at`),
  KEY `idx_security_event_user` (`user_id`, `first_at`),
  KEY `idx_security_event_session` (`session_id`, `first_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='安全事件';
