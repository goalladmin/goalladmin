-- 密码哈希列加宽（docs/decisions.md D-070）：默认的哈希换成 Argon2id 之后一份是 97 个字符，原来的 100 只剩 3 个余量，
-- 以后调参数（内存、轮数多一位数）就放不下了。只加宽，不动已有的数据；bcrypt 哈希（60 个字符）照常核对。
-- 可选的 PBKDF2 哈希是 95 个字符（D-072）。
-- 平台程序执行这个迁移；代理商、商户程序只核对版本（D-061）。可重跑。

ALTER TABLE `ga_user` MODIFY `password_hash` varchar(255) NOT NULL COMMENT '密码哈希：Argon2id（默认）、bcrypt 或 PBKDF2';

ALTER TABLE `ga_agent_user` MODIFY `password_hash` varchar(255) NOT NULL COMMENT '密码哈希：Argon2id（默认）、bcrypt 或 PBKDF2';

ALTER TABLE `ga_merchant_user` MODIFY `password_hash` varchar(255) NOT NULL COMMENT '密码哈希：Argon2id（默认）、bcrypt 或 PBKDF2';
