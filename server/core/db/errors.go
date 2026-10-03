package db

import (
	"context"
	"errors"
	"fmt"

	mysqldrv "github.com/go-sql-driver/mysql"
)

// sqlErrorForLog 只保留诊断类别，不记录可能回显绑定值的驱动文本（D-092）。
// 不包装或修改原错误，调用方仍可用 errors.Is/As 分类。
func sqlErrorForLog(err error) string {
	var me *mysqldrv.MySQLError
	switch {
	case errors.As(err, &me):
		return fmt.Sprintf("mysql error %d", me.Number)
	case errors.Is(err, context.Canceled):
		return "database operation canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "database operation timed out"
	default:
		return fmt.Sprintf("database error (%T)", err)
	}
}

// IsDataError 报告错误是不是"这一行数据本身写不进去"（D-058）：字符串不是合法字符、超长、数值越界、
// 日期不合法、非空列给了空值。这类错误重试多少次都一样，调用方不应放回去重试；连接断开、超时、锁等待等
// 其他错误可能过一会儿就好。
func IsDataError(err error) bool {
	var me *mysqldrv.MySQLError
	if !errors.As(err, &me) {
		return false
	}
	switch me.Number {
	case 1048, // 列不能为空
		1264, // 数值越界
		1265, // 数据被截断
		1292, // 日期、数值格式不对
		1366, // 字符串不是这一列字符集的合法字符
		1406: // 数据超长
		return true
	}
	return false
}
