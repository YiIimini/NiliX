package api

// ─────────────────────────────────────────────────────────────────────────────
// 安全护栏助手（2026-08-22 审计修复 S1/H3）
// 统一校验 /api/manju/* 的 config / novel 路径参数归属：
//   - config 必须解析为 ManjuRootDir/<项目>/config.json（禁止任意路径读/写 JSON）
//   - novel 必须位于 NovelRootDir 之内（禁止任意文件整读，防密钥泄漏与 OOM）
// 所有从 HTTP 参数接收路径的入口必须过此关（manjuProject/manjuOutputs/manjuPlan/
// manjuNovelInfo/manjuSaveRender/manjuSettingsPost/manjuStatus/manjuRun/manjuKill 等）。
// ─────────────────────────────────────────────────────────────────────────────

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// manjuGuardConfig 校验 config 参数归属项目根目录。
// 返回规范化绝对路径；非法返回 error。不查文件存在性（各 handler 自行处理缺文件语义）。
func manjuGuardConfig(configPath string) (string, error) {
	cp := filepath.Clean(configPath)
	root := filepath.Clean(ManjuRootDir)
	if cp == root || !strings.HasPrefix(cp, root+string(filepath.Separator)) {
		return "", fmt.Errorf("config 路径必须在项目根目录 %s 内", ManjuRootDir)
	}
	if filepath.Base(cp) != "config.json" {
		return "", fmt.Errorf("config 参数必须是 config.json")
	}
	proj := filepath.Base(filepath.Dir(cp))
	if proj == "" || proj == "." || proj == ".." || manjuSkipDirs[proj] || strings.ContainsAny(proj, `\/`) {
		return "", fmt.Errorf("非法项目名")
	}
	return cp, nil
}

// manjuGuardNovel 校验 novel 正文路径归属小说库根目录。
// 返回规范化绝对路径；非法返回 error。文件不存在时交由调用方按"小说缺失"处理。
func manjuGuardNovel(novelPath string) (string, error) {
	np := filepath.Clean(novelPath)
	root := filepath.Clean(NovelRootDir)
	if np == root || !strings.HasPrefix(np, root+string(filepath.Separator)) {
		return "", fmt.Errorf("小说路径必须在小说库根目录 %s 内", NovelRootDir)
	}
	return np, nil
}

// manjuStateRunningFor 判断指定 config 所属项目是否正在渲染。
// 任务闸门(S7):删除/清理等破坏性操作与渲染并发撕扯产物/状态,运行中必须拒绝。
func manjuStateRunningFor(configPath string) bool {
	manjuState.mu.Lock()
	defer manjuState.mu.Unlock()
	if !manjuState.running {
		return false
	}
	proj := manjuState.project
	if proj == "" {
		return true // 有任务但项目未知:保守拒绝
	}
	return strings.EqualFold(filepath.Base(filepath.Dir(filepath.Clean(configPath))), proj)
}

// manjuConfigLocks per-config 读-改-写互斥(审计 S8):qcAcceptClear/manjuSaveRender/
// manjuSettingsPost 等端点并发 read-modify-write 同一 config.json 会互相覆盖丢更新
var manjuConfigLocks sync.Map // key=cleaned configPath → *sync.Mutex

func manjuConfigLock(configPath string) *sync.Mutex {
	v, _ := manjuConfigLocks.LoadOrStore(filepath.Clean(configPath), &sync.Mutex{})
	return v.(*sync.Mutex)
}
