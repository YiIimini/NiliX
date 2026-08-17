// Package autostart 提供 Windows 开机自启（注册表 HKCU Run 键）控制。
package autostart

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName = "NiliX"
)

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
