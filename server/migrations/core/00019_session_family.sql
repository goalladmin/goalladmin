-- 会话加一个不轮换的"家族密钥"（docs/decisions.md D-104）：登录时随机生成，库里只存它的 SHA-256，
-- 明文只在刷新 Cookie 里。刷新凭证每次轮换都会换，家族密钥在会话的整个生命周期里不变。
-- 这一列可以为空：迁移之前建的会话没有它，下一次成功刷新时补上，不需要所有人重新登录。
-- 只加一列，不动已有的数据。平台程序执行这个迁移；代理商、商户程序只核对版本（D-061）。
-- MySQL 8.0 的 ADD COLUMN 没有 IF NOT EXISTS，按 D-019 先查列，支持重跑。
-- 列加在末尾、不指定位置：这样在 8.0 的各个小版本上都是只改元数据，不重建会话表。
SET @ga_sql = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE `ga_session` ADD COLUMN `family_hash` char(64) NULL COMMENT ''家族密钥的 SHA-256；空表示这个会话还没有（D-104）''',
    'SELECT 1')
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_session' AND COLUMN_NAME = 'family_hash'
);
PREPARE ga_stmt FROM @ga_sql;
EXECUTE ga_stmt;
DEALLOCATE PREPARE ga_stmt;
