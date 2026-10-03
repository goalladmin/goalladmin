-- D-080：只由平台迁移。申请和邀请是系统流程记录，不套用实体状态/排序块。
CREATE TABLE IF NOT EXISTS `ga_org_application` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `reference` varchar(32) COLLATE utf8mb4_bin NOT NULL DEFAULT '' COMMENT '随机申请编号',
  `portal` varchar(16) NOT NULL DEFAULT '' COMMENT '申请端 agent 或 merchant',
  `name` varchar(64) NOT NULL DEFAULT '' COMMENT '主体名称',
  `contact_name` varchar(64) NOT NULL DEFAULT '' COMMENT '联系人',
  `contact_phone` varchar(32) NOT NULL DEFAULT '' COMMENT '联系电话',
  `owner_username` varchar(64) NOT NULL DEFAULT '' COMMENT '期望主账号名',
  `agent_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT '邀请代理商，0 表示直属平台',
  `review_state` varchar(16) NOT NULL DEFAULT 'pending' COMMENT 'pending approved rejected',
  `review_note` varchar(255) NOT NULL DEFAULT '' COMMENT '审核说明',
  `org_code` varchar(16) NOT NULL DEFAULT '' COMMENT '审核通过后的主体编号',
  `created_at` datetime(3) NOT NULL COMMENT '申请时间',
  `reviewed_at` datetime(3) NULL COMMENT '审核时间',
  `reviewed_by` bigint unsigned NOT NULL DEFAULT 0 COMMENT '平台审核人',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_application_reference` (`reference`),
  KEY `idx_application_state` (`review_state`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='主体入驻申请';

CREATE TABLE IF NOT EXISTS `ga_org_invitation` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键',
  `token_hash` char(64) COLLATE utf8mb4_bin NOT NULL DEFAULT '' COMMENT '邀请凭证 SHA256 摘要',
  `agent_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT '邀请代理商',
  `expires_at` datetime(3) NOT NULL COMMENT '有效期',
  `used_at` datetime(3) NULL COMMENT '提交申请时间',
  `revoked_at` datetime(3) NULL COMMENT '撤销时间',
  `created_at` datetime(3) NOT NULL COMMENT '创建时间',
  `created_by` bigint unsigned NOT NULL DEFAULT 0 COMMENT '代理商端创建人',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_invitation_token` (`token_hash`),
  KEY `idx_invitation_agent` (`agent_id`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='商户入驻邀请';
