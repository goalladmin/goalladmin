// GoAllAdmin 服务入口。就放在 server/ 根目录，所以 `go run .`（或 `go run main.go`）即可启动，不带子命令等于 serve。
//
// 用法：
//
//	server              [-config path]   启动 HTTP 服务（等于 serve）
//	server serve        [-config path]   启动 HTTP 服务
//	server migrate up   [-config path]   执行迁移
//	server migrate status [-config path] 查看迁移状态
//	server admin create -username NAME [-config path] 创建超级管理员（随机密码只打印一次）
//	server admin reset-password -username NAME [-config path] 重置任意账号的密码（超管的密码只能这样重置）
//	server rbac prune   [-config path]   清理策略表里未注册的权限码
//	server healthcheck  [-addr host:port] 容器健康检查
//
// 配置路径优先级：-config 参数 > GA_CONFIG 环境变量 > config/config.yaml（存在时）> 仅默认值和环境变量。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/audit"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/modules/system"
	// 二次开发的模块（server/modules/<名>/）在这里 import，并加进下面 modules() 的列表。
)

const defaultConfigPath = "config/config.yaml"

func main() {
	// 不带子命令、或者第一个参数是 -config 这样的选项时，当作 serve
	if len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-") {
		os.Args = append([]string{os.Args[0], "serve"}, os.Args[1:]...)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "migrate":
		err = cmdMigrate(os.Args[2:])
	case "admin":
		err = cmdAdmin(os.Args[2:])
	case "rbac":
		err = cmdRBAC(os.Args[2:])
	case "healthcheck":
		err = cmdHealthcheck(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `用法:
  server                [-config path]      启动 HTTP 服务（不带子命令等于 serve）
  server serve          [-config path]      启动 HTTP 服务
  server migrate up     [-config path]      执行数据库迁移
  server migrate status [-config path]      查看迁移状态
  server admin create   -username NAME      创建超级管理员，随机密码只打印一次
  server admin reset-password -username NAME
                                            重置账号密码（超管的密码只能这样重置），新密码只打印一次
  server rbac prune     [-config path]      清理策略表里未注册的权限码
  server healthcheck    [-addr host:port]   容器健康检查`)
}

// configFlag 解析 -config，并按优先级决定配置文件路径。
func configFlag(fs *flag.FlagSet) *string {
	def := os.Getenv("GA_CONFIG")
	if def == "" {
		if _, err := os.Stat(defaultConfigPath); err == nil {
			def = defaultConfigPath
		}
	}
	return fs.String("config", def, "配置文件路径（空表示只用默认值和环境变量）")
}

// build 构造 App 并显式注册模块。这是整个进程唯一的装配点。
func build(configPath string) (*app.App, error) {
	cfg, err := conf.Load(configPath)
	if err != nil {
		return nil, err
	}
	a, err := app.New(cfg)
	if err != nil {
		return nil, err
	}
	a.Register(modules()...)
	return a, nil
}

// modules 返回要注册的模块，顺序即初始化顺序：system 必须在前（它注册 platform 端，其他模块把路由挂在这个端下），
// 然后是二次开发的模块（server/modules/<名>/）。
func modules() []app.Module {
	return []app.Module{
		system.Module(),
	}
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := build(*cfgPath)
	if err != nil {
		return err
	}
	return a.Run(context.Background())
}

func cmdMigrate(args []string) error {
	if len(args) < 1 {
		return errors.New("migrate 需要子命令: up | status")
	}
	sub := args[0]
	fs := flag.NewFlagSet("migrate "+sub, flag.ExitOnError)
	cfgPath := configFlag(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	a, err := build(*cfgPath)
	if err != nil {
		return err
	}
	ctx := a.Context(context.Background())
	defer func() { _ = a.Stop(ctx) }()

	switch sub {
	case "up":
		return a.Migrate(ctx)
	case "status":
		fmt.Printf("%-10s %-30s %-8s %-30s %s\n", "SOURCE", "TABLE", "VERSION", "NAME", "APPLIED AT")
		for _, set := range a.MigrationSets() {
			st, err := db.MigrateStatus(ctx, a.Deps().DB, set.FS, set.Dir, set.Table)
			if err != nil {
				return fmt.Errorf("%s: %w", set.Name, err)
			}
			for _, s := range st {
				at := "pending"
				if s.AppliedAt != nil {
					at = s.AppliedAt.UTC().Format(time.RFC3339)
				}
				fmt.Printf("%-10s %-30s %-8d %-30s %s\n", set.Name, set.Table, s.Version, s.Name, at)
			}
		}
		return nil
	default:
		return fmt.Errorf("未知的 migrate 子命令 %q（可用: up | status）", sub)
	}
}

func cmdAdmin(args []string) error {
	if len(args) < 1 || (args[0] != "create" && args[0] != "reset-password") {
		return errors.New("admin 需要子命令: create | reset-password")
	}
	if args[0] == "reset-password" {
		return cmdAdminResetPassword(args[1:])
	}
	fs := flag.NewFlagSet("admin create", flag.ExitOnError)
	cfgPath := configFlag(fs)
	username := fs.String("username", "", "管理员账号（必填）")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *username == "" {
		return errors.New("请用 -username 指定账号")
	}
	a, err := build(*cfgPath)
	if err != nil {
		return err
	}
	ctx := a.Context(context.Background())
	defer func() { _ = a.Stop(ctx) }()
	// 和 serve 一样：migrate.auto 打开时先把表建齐，这样全新的库上第一条命令就可以是 admin create。
	if a.Deps().Conf.Migrate.Auto {
		if err := a.Migrate(ctx); err != nil {
			return err
		}
	}
	if err := a.Setup(); err != nil {
		return err
	}
	plain, err := system.CreateAdmin(ctx, a.Deps(), *username)
	if err != nil {
		return err
	}
	fmt.Printf("已创建超级管理员 %s\n初始密码（只显示这一次，首次登录后必须修改）：%s\n", *username, plain)
	return nil
}

// cmdAdminResetPassword 在服务器上重置一个账号的密码（D-035）。
func cmdAdminResetPassword(args []string) error {
	fs := flag.NewFlagSet("admin reset-password", flag.ExitOnError)
	cfgPath := configFlag(fs)
	username := fs.String("username", "", "要重置密码的账号（必填）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		return errors.New("请用 -username 指定账号")
	}
	a, err := build(*cfgPath)
	if err != nil {
		return err
	}
	ctx := a.Context(context.Background())
	defer func() { _ = a.Stop(ctx) }()
	if err := a.Setup(); err != nil {
		return err
	}
	res, err := system.ResetPasswordByCLI(ctx, a.Deps(), *username, cliOperator())
	if err != nil {
		return err
	}
	who := *username
	if res.Super {
		who = "超级管理员 " + who
	}
	fmt.Printf("已重置 %s 的密码，该账号的全部会话已吊销（正在运行的服务最迟 15 秒后生效）\n", who)
	fmt.Printf("新密码（只显示这一次，下次登录后必须修改）：%s\n", res.Password)
	if !res.Enabled {
		fmt.Println("注意：这个账号目前是停用状态，重置密码不会让它恢复登录。")
	}
	fmt.Println("如果这个账号正处于登录锁定，要等锁定时长过去（默认 15 分钟）或重启服务后才能登录。")
	if res.AuditErr != nil {
		fmt.Fprintln(os.Stderr, "警告：密码已重置，但安全事件没有记下来：", res.AuditErr)
	}
	return nil
}

// cliOperator 返回执行命令的系统用户和主机名，写进命令行操作的安全事件，事后能查到是谁在哪台机器上做的。
func cliOperator() string {
	name := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return name + "@" + host
}

func cmdRBAC(args []string) error {
	if len(args) < 1 || args[0] != "prune" {
		return errors.New("rbac 需要子命令: prune")
	}
	fs := flag.NewFlagSet("rbac prune", flag.ExitOnError)
	cfgPath := configFlag(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	a, err := build(*cfgPath)
	if err != nil {
		return err
	}
	ctx := a.Context(context.Background())
	defer func() { _ = a.Stop(ctx) }()
	if err := a.Setup(); err != nil {
		return err
	}
	n, err := a.Deps().RBAC.Prune(ctx)
	if err != nil {
		return err
	}
	// 命令行改授权数据：记一个安全事件（D-032）
	if err := a.Deps().Audit.RecordSecurity(ctx, audit.NewSecurityEvent{Kind: system.KindCLI, Detail: fmt.Sprintf("rbac prune: %d", n)}); err != nil {
		return err
	}
	fmt.Printf("已删除 %d 条未注册权限码的策略\n", n)
	return nil
}

func cmdHealthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "服务地址")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+*addr+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz 返回 %d", resp.StatusCode)
	}
	return nil
}
