// 代理商端程序（D-061）：只服务 agent 端，可以和平台程序分开部署在别的服务器上。
//
// 只注册 agent 端的模块（server/modules/agentportal/，以及在这里登记的二次开发模块）；平台端和另一个端的代码不会编进来，
// 依赖检查（scripts/depcheck.sh）和路由表测试（main_test.go）把关。表结构由平台程序迁移，这个程序启动时只核对。
// 命令和配置见 server/internal/portalcmd。
package main

import (
	"os"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/internal/portalcmd"
	"github.com/goalladmin/goalladmin/server/modules/agentportal"
	"github.com/goalladmin/goalladmin/server/modules/onboarding"
	// 代理商端的业务模块（server/modules/<名>/）在这里 import，并加进下面 modules() 的列表。
)

// program 返回这个程序的定义：端代号、默认配置文件 config/agent.yaml、模块。
func program() portalcmd.Program {
	return portalcmd.Program{Name: "agent", Portal: "agent", Modules: modules}
}

// modules 返回要注册的模块，顺序即初始化顺序。
func modules() []app.Module {
	return []app.Module{
		agentportal.Module(), // 代理商端本身和它自己的后台（D-067）；必须有，端由它注册
		onboarding.Module("agent"),
	}
}

func main() { os.Exit(program().Main(os.Args[1:], os.Stderr)) }
