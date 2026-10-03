-- 主体端（docs/decisions.md D-061）：会话、登录日志、操作日志、安全事件、角色按主体归属。平台端的行 org_id 恒为 0。
-- MySQL 8.0 的 ADD COLUMN / ADD KEY 没有 IF NOT EXISTS：先查 information_schema，不存在才执行，文件可以重跑（D-019）。

-- 会话：主体停用时一条语句吊销它的全部会话
SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_session` ADD COLUMN `org_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT ''所属主体（代理商、商户）；平台端为 0'' AFTER `user_id`',
    'SELECT 1')
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_session' AND COLUMN_NAME = 'org_id'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_session` ADD KEY `idx_session_portal_org` (`portal`, `org_id`)',
    'SELECT 1')
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_session' AND INDEX_NAME = 'idx_session_portal_org'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

-- 登录日志：失败的登录按输入的编号记 org_code，主体可能不存在
SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_login_log` ADD COLUMN `org_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT ''所属主体（代理商、商户）；平台端为 0'' AFTER `portal`',
    'SELECT 1')
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_login_log' AND COLUMN_NAME = 'org_id'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_login_log` ADD COLUMN `org_code` varchar(32) NOT NULL DEFAULT '''' COMMENT ''登录时输入的主体编号（归一化后）；平台端为空'' AFTER `org_id`',
    'SELECT 1')
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_login_log' AND COLUMN_NAME = 'org_code'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_login_log` ADD KEY `idx_login_log_org` (`portal`, `org_id`, `created_at`)',
    'SELECT 1')
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_login_log' AND INDEX_NAME = 'idx_login_log_org'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

-- 操作日志
SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_operation_log` ADD COLUMN `org_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT ''所属主体（代理商、商户）；平台端为 0'' AFTER `portal`',
    'SELECT 1')
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_operation_log' AND COLUMN_NAME = 'org_id'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_operation_log` ADD KEY `idx_oplog_org` (`portal`, `org_id`, `created_at`)',
    'SELECT 1')
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_operation_log' AND INDEX_NAME = 'idx_oplog_org'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

-- 安全事件
SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_security_event` ADD COLUMN `org_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT ''所属主体（代理商、商户）；平台端为 0'' AFTER `portal`',
    'SELECT 1')
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_security_event' AND COLUMN_NAME = 'org_id'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_security_event` ADD KEY `idx_security_event_org` (`portal`, `org_id`, `first_at`)',
    'SELECT 1')
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_security_event' AND INDEX_NAME = 'idx_security_event_org'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

-- 角色：主体端的角色属于某个主体，编码在主体内唯一；唯一键从 (portal, code) 改成 (portal, org_id, code)
SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_role` ADD COLUMN `org_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT ''所属主体（代理商、商户）；平台端为 0'' AFTER `portal`',
    'SELECT 1')
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_role' AND COLUMN_NAME = 'org_id'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_role` ADD UNIQUE KEY `uk_role_portal_org_code` (`portal`, `org_id`, `code`)',
    'SELECT 1')
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_role' AND INDEX_NAME = 'uk_role_portal_org_code'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;

SET @ga_sql = (
  SELECT IF(COUNT(*) > 0,
    'ALTER TABLE `ga_role` DROP INDEX `uk_role_portal_code`',
    'SELECT 1')
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_role' AND INDEX_NAME = 'uk_role_portal_code'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;
