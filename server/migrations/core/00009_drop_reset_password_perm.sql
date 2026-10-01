-- 删除权限码 system:user:reset-password 的授权（docs/decisions.md D-035）：重置他人密码改为只有超管能做，
-- 这个权限码已经不存在。留在授权表里的行不会产生任何权限，但角色授权对话框会把它原样提交回去而被拒，
-- 非超管也会因为"角色含自己没有的权限码"而分配不了这个角色，所以升级时直接删掉。DELETE 可以重跑（D-019）。

DELETE FROM `ga_casbin_rule` WHERE `ptype` = 'p' AND `v1` = 'platform' AND `v2` = 'system:user:reset-password';
