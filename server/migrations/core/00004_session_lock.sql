-- 锁屏（docs/decisions.md D-027）：锁定记在会话上，由服务端判定，刷新页面或直接调接口都绕不过。
-- MySQL 8.0 的 ADD COLUMN 没有 IF NOT EXISTS：先查 information_schema，列不存在才执行，文件可以重跑（D-019）。

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_session` ADD COLUMN `locked_at` datetime(3) NULL COMMENT ''锁屏时间；空表示没有锁定'' AFTER `revoke_reason`',
    'SELECT 1')
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_session' AND COLUMN_NAME = 'locked_at'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_session` ADD COLUMN `unlock_failures` int unsigned NOT NULL DEFAULT 0 COMMENT ''锁定后连续解锁失败次数；到上限吊销会话'' AFTER `locked_at`',
    'SELECT 1')
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_session' AND COLUMN_NAME = 'unlock_failures'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;
