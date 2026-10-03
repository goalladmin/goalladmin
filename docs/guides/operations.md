# 运维指南

本文面向维护服务器、数据库和部署配置的人。首次安装见 [README](../../README.md#快速开始)，三端域名与容器启动见[三端部署](portals-deployment.md)，同端扩容见[多实例部署](multi-instance.md)。命令示例需要替换为目标环境的连接信息，并先在测试环境验证。

## 配置与密钥

配置优先级为显式绑定的环境变量覆盖 YAML。默认文件为 `server/config/config.yaml`（相对于仓库运行方式）；也可以用后端命令的 `-config` 参数指定文件，完整键见[配置示例](../../server/config/config.example.yaml)。环境变量是否进入容器取决于 Compose 的 `environment`，只把变量写进 `.env` 不会自动传入。

- 各端使用不同的持久 JWT 密钥；同端副本使用同一密钥。替换密钥会使旧访问令牌无法通过验证，应作为需要重新登录的维护操作安排。
- release 模式使用可信 HTTPS，允许来源填写浏览器实际看到的协议、域名和端口。不要用关闭证书校验或放开所有来源解决部署错误。
- 将配置、密钥和数据库备份分别限制访问，并保存可恢复的副本。不要把真实 `.env`、密码、令牌写进 Git、构建参数或问题截图。
- 修改 `.env` 后重建需要更新的容器，单独 `restart` 不加载新环境变量。升级时保持三个端与同端副本的关键策略一致。

## 升级与迁移

部署示例的新安装默认 MySQL 8.4 LTS，`GA_MYSQL_IMAGE` 可以固定已验证的版本或镜像摘要。MySQL 8.0 已于 2026 年 4 月结束生命周期，应安排迁移到受维护版本。[官方发布说明](https://dev.mysql.com/doc/relnotes/mysql/8.0/en/)

**已有 8.0 数据卷：更新 Compose 或重新创建容器前，先在部署 `.env` 中将 `GA_MYSQL_IMAGE` 固定为当前使用的原镜像。** 随后单独安排数据库引擎升级：检查官方升级要求及账号认证插件、配置兼容性，保存可恢复备份，在隔离副本演练升级与业务读写，再在维护窗口执行。[MySQL 8.4 升级指南](https://dev.mysql.com/doc/refman/8.4/en/upgrading.html) 不能把“换成 8.4 镜像并启动旧卷”当成无风险的应用更新；已升级的数据目录不能通过切回 8.0 镜像恢复，回退应恢复备份到兼容的独立实例。不要把示例 `.env` 直接覆盖到已有环境。

“先由平台执行迁移”指先运行**新版平台后端程序**的 `migrate up`，把共享数据库更新到目标版本。不是在平台网页点击按钮。代理商和商户程序只能核对迁移状态，不能执行升级。

升级顺序：

1. 记录旧版本、镜像标识、配置和数据库版本，阅读目标版本的变更记录及新迁移。
2. 做可恢复的数据库备份，在隔离环境验证目标程序和业务模块的迁移。
3. 关闭入口并停止全部受影响后端，保留数据库及数据卷。
4. 使用目标版本平台程序执行 `migrate status`、`migrate up`，确认成功后再次查看状态。
5. 启动相同目标版本的各端后端，核对就绪状态，再恢复入口和前端。
6. 分别检查登录、刷新、退出、主账号/员工权限、IP 规则、关键业务读写和日志。

三端 Compose 的完整命令见[升级与验收](portals-deployment.md#升级与验收)。原单端 `docker-compose.yml` 为便于起步启用了自动迁移；采用分离迁移账号或正式停机升级流程时，需要在自有部署配置中关闭它。不要给主体端开启自动迁移，也不要在未知兼容性的情况下让新旧程序混跑。

迁移 `00019_session_family.sql` 给会话表加一列，配合三段的刷新 Cookie（D-104）：升级之后已登录的用户不需要重新登录，下一次刷新时自动换成新格式；但新格式旧版本的程序读不懂，所以各端要一起换成目标版本，回退到旧版本时这期间登录或刷新过的用户要重新登录。

迁移器只向前执行。失败时保持入口关闭、保留数据库，查明失败的版本和语句后处理；MySQL DDL 可能已部分生效，不能假设整个文件自动回滚。不要手动伪造迁移完成标记。回退应用前确认旧程序认识新表结构和密码哈希；否则使用经过演练的备份恢复方案，并明确恢复点之后的数据如何处理。

## 数据库账号

区分迁移账号、运行账号、备份账号和临时测试账号，限制可连接的来源主机及目标库。

| 用途 | 权限原则 |
| --- | --- |
| 迁移 | 根据实际迁移授予目标库的建表、改表、索引及数据读写权限；不把管理凭据留给常驻程序 |
| 常驻运行 | 目标库所需数据读写权限；关闭自动迁移，不授予建库和改表权限 |
| 审计表 | `ga_login_log`、`ga_operation_log` 只需 SELECT/INSERT；`ga_error_log`、`ga_security_event` 还需 UPDATE 做合并计数；都不需要 DELETE |
| 会话等可清理表 | 运行程序需要 DELETE，不能把审计表的限制直接套到所有表 |
| 备份 | 按备份对象和 MySQL 版本授予只读导出所需权限，不复用应用或 root 账号 |
| 自动化测试 | 仅在隔离 MySQL 上允许创建和删除 `ga_test_*` 临时库，不指向生产数据库 |

如果账号已经拥有库级 `ALL` 或 `DELETE`，再添加表级只读授权不会抵消更大的授权。实施最小权限应使用新的独立账号和精确授权清单，并对启动同步、迁移状态查询、日志合并、会话清理及自有业务模块验证；本表不是一份可直接套用的完整 GRANT 脚本。

## 备份与恢复

开发用 `docker-compose.test.yml` 的 MySQL 数据放在 tmpfs，停止后可能丢失，只用于开发和测试。三个部署示例使用的数据卷应独立保存；正常升级不要执行带 `-v` 的 Compose down。

下面使用 MySQL 客户端的登录配置，避免把密码直接写在命令行。连接实际数据库的私有地址，不要为了备份临时把数据库公开到互联网。

```sh
mysql_config_editor set --login-path=ga-backup \
  --host=127.0.0.1 --port=3306 --user=ga_backup --password

umask 077
backup_file="goalladmin-$(date -u +%Y%m%dT%H%M%SZ).sql"
mysqldump --login-path=ga-backup \
  --single-transaction --no-tablespaces --set-gtid-purged=OFF \
  --default-character-set=utf8mb4 goalladmin > "$backup_file"
```

只有导出命令退出码为 0 才接受该备份，失败时输出文件可能不完整。`--single-transaction` 适用于 InnoDB 的一致性读取；导出期间不执行 DDL。`--no-tablespaces` 避免导出表空间定义。这里关闭 GTID 写入，示例用于普通逻辑恢复，不用于配置复制节点。框架没有存储过程或事件；自有业务增加视图、触发器、过程或事件后，应相应补齐导出对象和账号权限。

定期在**另一台隔离数据库实例**演练恢复。下面的登录配置必须指向该隔离实例，不能复用生产连接；源导出命令没有 `--databases`，便于导入单独的恢复库：

```sh
mysql_config_editor set --login-path=ga-restore \
  --host=127.0.0.1 --port=13306 --user=ga_restore --password
mysql --login-path=ga-restore -e \
  'CREATE DATABASE goalladmin_restore CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci'
mysql --login-path=ga-restore goalladmin_restore < "$backup_file"
```

恢复后使用与备份兼容的程序、隔离配置和新的测试凭据检查迁移版本、主体归属、角色、审计、业务数量及抽样关联。备份文件含账号哈希、会话和业务资料，应加密保存、验证可读取并设置留存期限。仅复制一个 `.sql` 文件或看到文件非空，不代表恢复演练完成。

## 健康、日志与容量

`/healthz` 表示进程存活；`/readyz` 检查数据库，在每个实例内缓存结果 1 秒。Redis 不可用仍可能就绪，因为计数可降级。分别观察每个后端、数据库、Redis 和入口代理，不能用一次入口请求代替所有副本的状态。

MySQL 容器的 `mysqladmin ping` 不携带 root 密码，只确认服务器响应；它即使收到认证拒绝也可能成功退出。应用账号、目标库和查询是否可用，应由后端连接及 `/readyz` 核对，不能只看数据库容器的 `healthy`。

平台“安全监控”的 CPU、内存、协程、连接池和请求统计属于处理该请求的进程；它不是整套集群的汇总，也不是长期监控系统。连接池预算应累加三端及所有副本，并为迁移、备份和其他客户端留余量。

后端日志写标准输出。使用 `GA_LOG_FORMAT=json` 便于采集，通过 `request_id` 关联请求；登录、操作和安全事件另写 AUDIT 日志，不受普通日志级别过滤。外部日志的可信性仍取决于采集链、存储权限和保留设置。

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.portals.yml logs --tail=100 platform agent merchant
docker compose --env-file deploy/.env -f deploy/docker-compose.portals.yml ps -a
```

部署 Compose 为各容器配置 `json-file` 轮转，每文件 10m、保留 3 个；它仅约束单个容器的该类日志。长期审计应另接采集、归档、告警和存储控制。数据库里的登录、操作、错误、安全事件不自动清理，也没有网页删除接口。

各后端会分批清理本端过期或吊销超过 30 天的会话，不删除有效会话和审计日志。清理失败不停止业务，关注 `session history cleanup failed` 日志并检查权限、索引和容量。迁移 `00018_session_cleanup_indexes.sql` 提供所需索引。

## 账号与 IP 规则恢复

平台超级管理员忘记密码，在连接正确数据库的服务器上执行平台程序：

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.portals.yml \
  exec platform /server admin reset-password -username admin
```

命令生成新随机密码并吊销该账号会话。主体主账号由平台管理页重置，员工由其管理者处理。不要直接改密码哈希或删除会话表。

误配 IP 规则时，先由平台或主体主账号通过仍可访问的管理入口处理。需要服务器恢复时，平台 CLI 可以列出规则、删除指定规则或清空指定层的白名单；这些是修改访问策略的管理操作，先确认目标范围并保留处置记录：

```sh
# 下列命令在三端部署的 platform 容器内使用 /server 执行。
/server ip list
/server ip remove -id RULE_ID
/server ip clear-allow -portal merchant -org ORG_ID
/server ip clear-deny -portal merchant -org ORG_ID
```

`RULE_ID` 和 `ORG_ID` 换成核对过的数字 ID；`ORG_ID` 不是登录时的 `M…` 编号。`clear-allow` 不清黑名单，不带 `-org` / `-user` 时作用于端白名单；`clear-deny` 清空一个代理商或商户自己设的黑名单（必须带 `-org`），不动全局黑名单。平台后台的代理商、商户详情里也能查看和清空这份名单。只修正需要的那条或那一层，随后确认真实客户端 IP、重新设置正确规则并检查审计记录。
