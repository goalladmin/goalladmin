-- 只用于 deploy/docker-compose.test.yml 起的本地 MySQL（数据在 tmpfs 里，容器一停就没）。
-- 1. 测试会为每个用例创建 ga_test_<随机> 临时库，所以 ga 需要这些库上的建库、删库权限；
-- 2. 顺便建一个本地开发库 goalladmin，`make run` 默认连它。
GRANT ALL PRIVILEGES ON `ga\_test\_%`.* TO 'ga'@'%';
CREATE DATABASE IF NOT EXISTS `goalladmin` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
GRANT ALL PRIVILEGES ON `goalladmin`.* TO 'ga'@'%';
FLUSH PRIVILEGES;
