// Package cleanup 每日维护:凌晨 00:00 清空 ComfyUI 共享 input/output。
// 2026-08-26 自包含整合时接替原 Windows 计划任务 ComfyUI_Cleanup——其 ps1 护栏
// 锁死旧 AppData 路径,对自包含新位置自动失效;内置后随 NiliX 目录拷贝到新电脑
// 即自带该行为,不再依赖宿主机任务注册。
package cleanup

import (
	"log"
	"os"
	"path/filepath"
	"time"
)

// Start 启动每日清理 goroutine。enabled=false 时静默不注册。
// busy 为可选的"渲染进行中"探测(ComfyUI 队列忙则跳过当天,防凌晨批次被清)。
func Start(enabled bool, inputDir, outputDir string, busy func() bool) {
	if !enabled {
		return
	}
	// 护栏:只清这两个显式传入的目录本身的内容,且路径必须绝对、非根、非空
	dirs := make([]string, 0, 2)
	for _, d := range []string{inputDir, outputDir} {
		d = filepath.Clean(d)
		if d == "" || !filepath.IsAbs(d) || len(d) <= 3 { // <=3 挡住 "C:\" 根
			continue
		}
		dirs = append(dirs, d)
	}
	if len(dirs) == 0 {
		return
	}
	go func() {
		lastRun := ""
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			if !next.After(now) {
				next = next.Add(24 * time.Hour)
			}
			select {
			case <-time.After(time.Until(next)):
			}
			// 跨零点后复核(睡眠误差/休眠唤醒),同一天只跑一次
			today := time.Now().Format("2006-01-02")
			if today == lastRun {
				continue
			}
			lastRun = today
			if busy != nil && busy() {
				log.Printf("[清理] ComfyUI 队列忙,跳过今日 input/output 清理")
				continue
			}
			for _, d := range dirs {
				n := clearDir(d)
				if n > 0 {
					log.Printf("[清理] %s: 清除 %d 项", d, n)
				}
			}
		}
	}()
}

// clearDir 删除目录下全部子项(保留目录本身与 h3_context 接缝 latent),返回清除数量。
// h3_context 是 MotionContext 跨镜/跨天续接的状态数据而非临时产物,误删会断接缝链。
func clearDir(dir string) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() && e.Name() == "h3_context" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err == nil {
			n++
		}
	}
	return n
}
