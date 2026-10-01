-- 头像（docs/decisions.md D-040）：本人上传的头像存在数据库里，不落服务器磁盘。
-- 每人一行；avatar_key 是随机的，换头像或清除后旧键失效。图片都是服务器重新编码的 JPEG，不存原文件。

CREATE TABLE IF NOT EXISTS `ga_user_avatar` (
  `user_id`     bigint unsigned NOT NULL COMMENT '用户（ga_user.id）',
  `avatar_key`  char(32)        NOT NULL COMMENT '随机键，读取时用它，不用用户 ID',
  `image`       mediumblob      NOT NULL COMMENT '256×256 JPEG',
  `thumb`       blob            NOT NULL COMMENT '64×64 JPEG',
  `created_at`  datetime(3)     NOT NULL COMMENT '上传时间',
  PRIMARY KEY (`user_id`),
  UNIQUE KEY `uk_user_avatar_key` (`avatar_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='本人上传的头像';

-- 升级：ga_user.avatar 以后只会是空、preset:<名字> 或 upload:<键>。原来管理员填的任意地址（外部图片）清空；
-- 只留下名字完全对得上的内置头像（BINARY 比较，大小写变体不算）和 ga_user_avatar 里真有这张图的上传头像，
-- 所以这个文件可以重跑（D-019）：重跑时合法的上传头像有对应的行，不会被清掉。
-- 内置头像的名字和 server/modules/system/avatar.go 的 avatarPresets 是同一份。
UPDATE `ga_user` u SET u.`avatar` = ''
WHERE u.`avatar` <> ''
  AND BINARY u.`avatar` NOT IN (
    'preset:aurora', 'preset:ocean', 'preset:forest', 'preset:sunset', 'preset:berry', 'preset:slate',
    'preset:sand', 'preset:mint', 'preset:coral', 'preset:violet', 'preset:sky', 'preset:amber')
  AND NOT EXISTS (
    SELECT 1 FROM `ga_user_avatar` a
    WHERE a.`user_id` = u.`id` AND BINARY u.`avatar` = BINARY CONCAT('upload:', a.`avatar_key`));
