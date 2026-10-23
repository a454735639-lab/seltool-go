package main

import (
	"os"
	"path/filepath"
	"strings"
)

// 首启注册保护。
//
// 旧行为：只要还没有用户，任何人都能调 /api/auth/register 成为唯一管理员。
// 实例一旦暴露在公网（哪怕只是扫描器先撞上），抢注者就能读走全部 PT cookie、
// 暂停定时任务、改通知配置。这属于「拿到控制权」级别的问题，不是配置瑕疵。
//
// 现在：首次注册必须提供一次性初始化令牌。
//   - 优先读环境变量 SELTOOL_SETUP_TOKEN（部署方显式指定）；
//   - 否则启动时随机生成，写入 config/setup-token.txt（0600）并打印到容器日志一次；
//   - 注册成功后立即删除该文件并清空内存中的值。
//
// 已经注册过用户的实例完全不受影响。

const setupTokenEnv = "SELTOOL_SETUP_TOKEN"

type setupGuard struct {
	token string
	file  string
}

func newSetupGuard(configDir string, hasUser bool) *setupGuard {
	g := &setupGuard{file: filepath.Join(configDir, "setup-token.txt")}
	if hasUser {
		// 已初始化过，令牌无用，也不该在日志里制造噪音。
		return g
	}
	if v := strings.TrimSpace(os.Getenv(setupTokenEnv)); v != "" {
		g.token = v
		logf("首次注册令牌来自环境变量 %s", setupTokenEnv)
		return g
	}
	// 已有用户时不必生成（也不会被用到）。
	if tok, err := os.ReadFile(g.file); err == nil {
		if v := strings.TrimSpace(string(tok)); v != "" {
			g.token = v
			logf("首次注册令牌沿用 %s", g.file)
			return g
		}
	}
	g.token = randomToken(16)
	if err := os.WriteFile(g.file, []byte(g.token+"\n"), 0o600); err != nil {
		logf("写入初始化令牌文件失败: %v", err)
	}
	logf("首次注册令牌：%s（也写在 %s，注册成功后自动删除）", g.token, g.file)
	return g
}

// Verify 校验调用方提供的初始化令牌。
func (g *setupGuard) Verify(provided string) bool {
	if g == nil || g.token == "" {
		// 没有可用令牌时一律拒绝，绝不「因为配置缺失就放行」。
		return false
	}
	return sigEqual(g.token, strings.TrimSpace(provided))
}

// Consume 在注册成功后销毁令牌。
func (g *setupGuard) Consume() {
	if g == nil {
		return
	}
	g.token = ""
	if err := os.Remove(g.file); err != nil && !os.IsNotExist(err) {
		logf("删除初始化令牌文件失败: %v", err)
	}
}
