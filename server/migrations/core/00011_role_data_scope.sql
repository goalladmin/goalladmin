-- 按部门的数据权限（docs/decisions.md D-039）：角色在每个数据资源上的范围。没有行的按资源声明的默认值。
-- 范围只收窄权限码，永远不放宽；超管角色不存行（永远是全部）。

CREATE TABLE IF NOT EXISTS `ga_role_data_scope` (
  `role_id`   bigint unsigned NOT NULL COMMENT '角色（ga_role.id）',
  `resource`  varchar(64)     NOT NULL COMMENT '数据资源编码 <模块>:<资源>',
  `scope`     varchar(16)     NOT NULL COMMENT 'self 仅本人 / dept 本部门 / dept_tree 本部门及下级 / all 全部',
  PRIMARY KEY (`role_id`, `resource`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='角色的数据范围';

-- 升级：用户资源的默认范围是"仅本人"，已经在用的非超管角色写成"全部"，行为不变。INSERT IGNORE 可以重跑（D-019）。
INSERT IGNORE INTO `ga_role_data_scope` (`role_id`, `resource`, `scope`)
SELECT `id`, 'system:user', 'all' FROM `ga_role` WHERE `portal` = 'platform' AND `is_super` = 0;
