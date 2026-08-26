// Package autostart 提供 Windows 开机自启（注册表 HKCU Run 键）控制。
package autostart

import (
	"log"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName = "NiliX"
)

// SelfHeal 自启路径自愈:整个 NiliX 目录拷贝到新电脑后,Run 键里的旧绝对路径指向
// 不存在的位置(自启静默失效)。已启用自启但值不含当前 exe 路径时自动更新;
// 未启用自启则尊重现状不动。
func SelfHeal(exePath string) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	v, _, err := k.GetStringValue(valueName)
	if err != nil {
		return
	}
	if exePath != "" && !strings.Contains(v, exePath) {
		if err := Enable(exePath); err == nil {
			log.Printf("[自愈] 开机自启路径已更新 → %s", exePath)
		}
	}
}

// Enabled 查询是否已设置开机自启。
func Enabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(valueName)
	return err == nil
}

// Enable 设置开机自启（exePath 为可执行文件绝对路径）。
func Enable(exePath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	// 路径含空格时加引号，避免 Run key 命令解析错误。
	if strings.ContainsAny(exePath, " \t") {
		exePath = `"` + exePath + `"`
	}
	return k.SetStringValue(valueName, exePath)
}

// Disable 取消开机自启。
func Disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return nil // 键不存在，无需取消
	}
	defer k.Close()
	return k.DeleteValue(valueName)
}
