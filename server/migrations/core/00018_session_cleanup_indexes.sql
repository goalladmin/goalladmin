-- D-081：按端、过期时间或吊销时间分批清理会话；只加索引，不删除数据。
-- MySQL 8.0 的 ADD KEY 没有 IF NOT EXISTS，按 D-019 先查索引，支持重跑。
SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_session` ADD KEY `idx_session_portal_expires` (`portal`, `expires_at`)',
    'SELECT 1')
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_session' AND INDEX_NAME = 'idx_session_portal_expires'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_session` ADD KEY `idx_session_portal_revoked` (`portal`, `revoked_at`)',
    'SELECT 1')
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_session' AND INDEX_NAME = 'idx_session_portal_revoked'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;
