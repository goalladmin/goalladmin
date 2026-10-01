package db

import (
	"errors"

	mysqldrv "github.com/go-sql-driver/mysql"
)

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
