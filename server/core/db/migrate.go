package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/logx"
)

// 内置迁移器（见 docs/decisions.md D-001）。
//
// 规则：
//   - 迁移文件名 NNNNN_name.sql，NNNNN 是递增整数版本号；只向前，没有 Down。
//   - 语句以行尾的 ";" 分隔，"--" 开头的行是注释；不支持 DELIMITER / 存储过程。
//   - 每个来源（框架、各业务模块）用自己的目录和自己的版本表，互不干扰（规范 §8.2）。
//   - 同一时刻只有一个进程能执行迁移（MySQL GET_LOCK）。
//   - MySQL 的 DDL 会隐式提交，所以一个文件里 DDL 失败时之前的 DDL 不会回滚；
//     版本号只在整个文件成功后写入，重跑会从失败的文件重新开始。写迁移时让每个文件尽量小。

// Migration 是一个迁移文件。
type Migration struct {
	Version int64
	Name    string
	File    string
	SQL     string
}

// MigrationStatus 是某个迁移的应用状态。
type MigrationStatus struct {
	Version   int64
	Name      string
	AppliedAt *time.Time // nil 表示未应用
}

var (
	migrationFileRe = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_-]+)\.sql$`)
	versionTableRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

// LoadMigrations 从 fsys 的 dir 目录读取全部迁移文件并按版本号排序。
func LoadMigrations(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("db: read migrations dir %q: %w", dir, err)
	}
	seen := map[int64]string{}
	out := make([]Migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := migrationFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("db: 迁移文件名不合规范 %q（应为 NNNNN_name.sql）", e.Name())
		}
		ver, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || ver <= 0 {
			return nil, fmt.Errorf("db: 迁移文件 %q 的版本号无效", e.Name())
		}
		if prev, dup := seen[ver]; dup {
			return nil, fmt.Errorf("db: 迁移版本 %d 重复: %s 与 %s", ver, prev, e.Name())
		}
		seen[ver] = e.Name()
		raw, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("db: read %s: %w", e.Name(), err)
		}
		out = append(out, Migration{Version: ver, Name: m[2], File: e.Name(), SQL: string(raw)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// MigrateUp 执行 dir 下所有未应用的迁移，版本记录在 table 里。返回本次应用的迁移。
func MigrateUp(ctx context.Context, gdb *gorm.DB, fsys fs.FS, dir, table string) ([]Migration, error) {
	if !versionTableRe.MatchString(table) {
		return nil, fmt.Errorf("db: 版本表名 %q 无效", table)
	}
	migrations, err := LoadMigrations(fsys, dir)
	if err != nil {
		return nil, err
	}
	log := logx.From(ctx).With("component", "migrate", "table", table)

	var applied []Migration
	err = gdb.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		if err := ensureVersionTable(conn, table); err != nil {
			return err
		}
		if err := acquireLock(conn, table); err != nil {
			return err
		}
		defer releaseLock(conn, table)

		done, err := appliedVersions(conn, table)
		if err != nil {
			return err
		}
		for _, m := range migrations {
			if _, ok := done[m.Version]; ok {
				continue
			}
			log.Info("applying migration", "version", m.Version, "file", m.File)
			if err := applyOne(conn, table, m); err != nil {
				return fmt.Errorf("db: 迁移 %s 失败: %w", m.File, err)
			}
			applied = append(applied, m)
		}
		return nil
	})
	if err != nil {
		return applied, err
	}
	return applied, nil
}

// MigrateStatus 返回 dir 下每个迁移的应用状态。
func MigrateStatus(ctx context.Context, gdb *gorm.DB, fsys fs.FS, dir, table string) ([]MigrationStatus, error) {
	if !versionTableRe.MatchString(table) {
		return nil, fmt.Errorf("db: 版本表名 %q 无效", table)
	}
	migrations, err := LoadMigrations(fsys, dir)
	if err != nil {
		return nil, err
	}
	conn := gdb.WithContext(ctx)
	if err := ensureVersionTable(conn, table); err != nil {
		return nil, err
	}
	done, err := appliedVersions(conn, table)
	if err != nil {
		return nil, err
	}
	out := make([]MigrationStatus, 0, len(migrations))
	for _, m := range migrations {
		s := MigrationStatus{Version: m.Version, Name: m.Name}
		if at, ok := done[m.Version]; ok {
			t := at
			s.AppliedAt = &t
		}
		out = append(out, s)
	}
	return out, nil
}

func ensureVersionTable(conn *gorm.DB, table string) error {
	ddl := fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s` ("+
		"`version` bigint NOT NULL, `name` varchar(255) NOT NULL DEFAULT '', "+
		"`applied_at` datetime(3) NOT NULL, PRIMARY KEY (`version`)"+
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci", table)
	if err := conn.Exec(ddl).Error; err != nil {
		return fmt.Errorf("db: 创建版本表 %s: %w", table, err)
	}
	return nil
}

func acquireLock(conn *gorm.DB, table string) error {
	var got *int
	if err := conn.Raw("SELECT GET_LOCK(?, 30)", "ga_migrate_"+table).Scan(&got).Error; err != nil {
		return fmt.Errorf("db: 获取迁移锁: %w", err)
	}
	if got == nil || *got != 1 {
		return errors.New("db: 30 秒内未获得迁移锁，可能有另一个进程正在迁移")
	}
	return nil
}

func releaseLock(conn *gorm.DB, table string) {
	_ = conn.Exec("DO RELEASE_LOCK(?)", "ga_migrate_"+table).Error
}

func appliedVersions(conn *gorm.DB, table string) (map[int64]time.Time, error) {
	var rows []struct {
		Version   int64
		AppliedAt time.Time
	}
	if err := conn.Table(table).Select("version, applied_at").Order("version").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("db: 读取版本表 %s: %w", table, err)
	}
	out := make(map[int64]time.Time, len(rows))
	for _, r := range rows {
		out[r.Version] = r.AppliedAt
	}
	return out, nil
}

func applyOne(conn *gorm.DB, table string, m Migration) error {
	stmts := SplitStatements(m.SQL)
	if len(stmts) == 0 {
		return errors.New("文件里没有 SQL 语句")
	}
	// 注意：MySQL 的 DDL（CREATE/ALTER/DROP）会隐式提交，这个事务只能保住 DML 和版本记录。
	// 一个文件中途失败时，前面已执行的 DDL 留在库里而版本表没有记录，重跑会从第一条重新来。
	// 所以迁移文件要写成可重复执行的：CREATE TABLE IF NOT EXISTS、ADD COLUMN 前先判断、
	// 种子数据用 INSERT ... ON DUPLICATE KEY UPDATE（见 docs/guides/new-module.md）。
	return conn.Transaction(func(tx *gorm.DB) error {
		for i, s := range stmts {
			if err := tx.Exec(s).Error; err != nil {
				return fmt.Errorf("第 %d 条语句: %w", i+1, err)
			}
		}
		return tx.Exec(fmt.Sprintf("INSERT INTO `%s` (version, name, applied_at) VALUES (?, ?, ?)", table),
			m.Version, m.Name, time.Now().UTC()).Error
	})
}

// SplitStatements 把一段 SQL 按行尾分号拆成语句，去掉 "--" 注释行和空语句。
// 单引号字符串里的分号不算分隔符。
func SplitStatements(sql string) []string {
	var (
		out     []string
		cur     strings.Builder
		inQuote bool
	)
	flush := func() {
		s := strings.TrimSpace(cur.String())
		cur.Reset()
		if s != "" {
			out = append(out, s)
		}
	}
	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if !inQuote && (trimmed == "" || strings.HasPrefix(trimmed, "--")) {
			continue
		}
		for i := 0; i < len(line); i++ {
			c := line[i]
			// 引号外的 "-- " 起是行尾注释，丢弃
			if !inQuote && c == '-' && i+1 < len(line) && line[i+1] == '-' && (i+2 == len(line) || line[i+2] == ' ' || line[i+2] == '\t') {
				break
			}
			if c == '\'' {
				// 连续两个单引号是转义，不切换状态
				if inQuote && i+1 < len(line) && line[i+1] == '\'' {
					cur.WriteByte(c)
					cur.WriteByte(line[i+1])
					i++
					continue
				}
				inQuote = !inQuote
			}
			cur.WriteByte(c)
		}
		cur.WriteByte('\n')
		if !inQuote && strings.HasSuffix(strings.TrimSpace(cur.String()), ";") {
			s := strings.TrimSpace(cur.String())
			cur.Reset()
			s = strings.TrimSuffix(s, ";")
			if strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	flush()
	return out
}
