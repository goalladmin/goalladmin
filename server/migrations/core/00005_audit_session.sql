-- 调查时间线（docs/decisions.md D-032）：登录日志和操作日志记会话 ID，按会话、按 IP 查询要有索引。
-- MySQL 8.0 的 ADD COLUMN / ADD KEY 没有 IF NOT EXISTS：先查 information_schema，不存在才执行，文件可以重跑（D-019）。

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_login_log` ADD COLUMN `session_id` char(32) NOT NULL DEFAULT '''' COMMENT ''会话 ID（ga_session.sid）；登录失败时为空'' AFTER `user_id`',
    'SELECT 1')
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_login_log' AND COLUMN_NAME = 'session_id'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_operation_log` ADD COLUMN `session_id` char(32) NOT NULL DEFAULT '''' COMMENT ''会话 ID（ga_session.sid）'' AFTER `username`',
    'SELECT 1')
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_operation_log' AND COLUMN_NAME = 'session_id'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_login_log` ADD KEY `idx_login_log_session` (`session_id`, `created_at`)',
    'SELECT 1')
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_login_log' AND INDEX_NAME = 'idx_login_log_session'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_login_log` ADD KEY `idx_login_log_ip` (`ip`, `created_at`)',
    'SELECT 1')
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_login_log' AND INDEX_NAME = 'idx_login_log_ip'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_login_log` ADD KEY `idx_login_log_uid` (`user_id`, `created_at`)',
    'SELECT 1')
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_login_log' AND INDEX_NAME = 'idx_login_log_uid'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_operation_log` ADD KEY `idx_oplog_session` (`session_id`, `created_at`)',
    'SELECT 1')
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_operation_log' AND INDEX_NAME = 'idx_oplog_session'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_operation_log` ADD KEY `idx_oplog_ip` (`ip`, `created_at`)',
    'SELECT 1')
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_operation_log' AND INDEX_NAME = 'idx_oplog_ip'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;
