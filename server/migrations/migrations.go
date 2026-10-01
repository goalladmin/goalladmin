// Package migrations 把框架自己的 SQL 迁移文件嵌进二进制。
//
// 业务模块的迁移放在各自模块目录下，用自己的版本表（规范 §8.2）。
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed core/*.sql
var files embed.FS

// CoreDir 是框架迁移在 FS 里的目录名。
const CoreDir = "core"

// CoreTable 是框架迁移的版本表。
const CoreTable = "ga_schema_version"

// Core 返回包含 core/ 目录的只读文件系统。
func Core() fs.FS { return files }
