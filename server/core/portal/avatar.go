package portal

import "slices"

// 内置头像（D-040、D-067）：账号表的 avatar 列存 AvatarPresetPrefix + 名字，图在前端壳里（web/packages/shell/src/avatar.ts
// 的 AVATAR_PRESETS，和这里是同一份表，由测试对齐）。平台端和主体端共用。

// AvatarPresetPrefix 是内置头像在 avatar 列里的前缀。
const AvatarPresetPrefix = "preset:"

// AvatarPresets 返回内置头像的名字（每次一份新的切片，调用方可以随意改）。
func AvatarPresets() []string {
	return []string{
		"aurora", "ocean", "forest", "sunset", "berry", "slate",
		"sand", "mint", "coral", "violet", "sky", "amber",
	}
}

// ValidAvatarPreset 报告 name 是不是内置头像的名字。
func ValidAvatarPreset(name string) bool { return slices.Contains(AvatarPresets(), name) }
