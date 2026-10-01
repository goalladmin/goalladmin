-- 错误日志（docs/decisions.md D-032 第 3 条）：服务端故障（5xx、panic）按指纹合并，一类错误一行。
-- 计数和"最近一次"的列会被更新；行不删除，也没有删除接口。

CREATE TABLE IF NOT EXISTS `ga_error_log` (
  `id`              bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `fingerprint`     char(32)        NOT NULL COMMENT '指纹：同一类错误相同',
  `kind`            varchar(16)     NOT NULL COMMENT 'panic 或 error',
  `portal`          varchar(32)     NOT NULL DEFAULT '' COMMENT '所属端；未登录的请求为空',
  `method`          varchar(10)     NOT NULL DEFAULT '' COMMENT 'HTTP 方法',
  `route`           varchar(255)    NOT NULL DEFAULT '' COMMENT '路由模板；没匹配到路由时为空',
  `code`            int             NOT NULL DEFAULT 0 COMMENT '业务错误码',
  `http_status`     int             NOT NULL DEFAULT 0 COMMENT 'HTTP 状态码',
  `message`         varchar(1024)   NOT NULL DEFAULT '' COMMENT '错误文本，已去掉具体值并截断',
  `stack`           text            NULL COMMENT 'panic 的调用栈，只留文件名和行号，最多 8 KB',
  `count`           bigint unsigned NOT NULL DEFAULT 0 COMMENT '累计次数',
  `first_at`        datetime(3)     NOT NULL COMMENT '首次出现',
  `last_at`         datetime(3)     NOT NULL COMMENT '最近一次',
  `last_request_id` varchar(64)     NOT NULL DEFAULT '' COMMENT '最近一次的请求 ID',
  `last_user_id`    bigint unsigned NOT NULL DEFAULT 0 COMMENT '最近一次的用户',
  `last_username`   varchar(64)     NOT NULL DEFAULT '' COMMENT '最近一次的账号',
  `last_session_id` char(32)        NOT NULL DEFAULT '' COMMENT '最近一次的会话',
  `last_ip`         varchar(64)     NOT NULL DEFAULT '' COMMENT '最近一次的来源 IP',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_error_log_fingerprint` (`fingerprint`),
  KEY `idx_error_log_last` (`last_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='错误日志';
