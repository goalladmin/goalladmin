-- 个人中心（docs/decisions.md D-038）：用户自己维护的个人简介。
-- MySQL 8.0 的 ADD COLUMN 没有 IF NOT EXISTS：先查 information_schema，不存在才执行，文件可以重跑（D-019）。

SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_user` ADD COLUMN `bio` varchar(255) NOT NULL DEFAULT '''' COMMENT ''个人简介，本人在个人中心维护'' AFTER `avatar`',
    'SELECT 1')
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_user' AND COLUMN_NAME = 'bio'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;
