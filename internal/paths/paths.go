package paths

// 自包含部署路径解析:换电脑把整个 NiliX 目录拷走即用(ComfyUI/技能/数据目录都收在 exe 目录下)。
// 解析链:settings.json 显式值(设置弹窗「目录与部署」) → exe 目录自包含子目录(存在时) → 旧硬编码(兼容老安装)。
// InitPaths 必须在任何路径使用前调用(main 在 Chdir 到 exe 目录后立即执行)。

import (
	"nilix/internal/util"
	"os"
	"path/filepath"
	"strings"
)

// 解析后的运行路径(包内/导出供 main 与前端设置使用)
var (
	ManjuRootDir   string // 漫剧项目目录
	NovelRootDir   string // 小说库根目录(fs 白名单)
	ComfyRootDir   string // ComfyUI 安装目录(含 main.py / .venv)
	ComfySharedDir string // ComfyUI 共享目录(models/input/output)
	NovelSkillDir  string // 小说续作技能目录(词库/写作规范/qa 脚本)
	VoiceLibDir    string // 配音音色库权威目录(asset_lib/voices,跨项目共享)
	CharLibDir     string // 角色资产库权威目录(asset_lib/characters,跨项目共享,2026-09-02)
	AssetLibDir    string // 资产统一根(asset_lib:角色+音色,2026-09-03 用户规则)
)

// 旧硬编码路径(老安装的默认位置;新安装优先自包含目录)
const (
	legacyManjuRoot   = `C:\Mi\Ai\WorkBench\manju`
	legacyNovelRoot   = `C:\Mi\Ai\WorkBench\novel`
	legacyComfyRoot   = `C:\Users\Administrator\AppData\Local\Comfy-Desktop\ComfyUI-Installs\ComfyUI (1)\ComfyUI`
	legacyComfyShared = `C:\Users\Administrator\AppData\Local\Comfy-Desktop\ComfyUI-Shared`
	legacyNovelSkill  = `C:\Users\Administrator\.agents\skills\NiliX-Novel`
)

// InitPaths 解析全部运行路径。exeDir 为可执行文件所在目录(自包含根)。
func InitPaths(exeDir string, manjuRoot, novelRoot, comfyRoot, comfyShared, novelSkill string) {
	ManjuRootDir = pickPath(manjuRoot, filepath.Join(exeDir, "manju"), legacyManjuRoot)
	NovelRootDir = pickPath(novelRoot, filepath.Join(exeDir, "novel"), legacyNovelRoot)
	ComfyRootDir = pickPath(comfyRoot, filepath.Join(exeDir, "comfyui", "ComfyUI"), legacyComfyRoot)
	ComfySharedDir = pickPath(comfyShared, filepath.Join(exeDir, "comfyui", "shared"), legacyComfyShared)
	NovelSkillDir = pickPath(novelSkill, filepath.Join(exeDir, "skills", "NiliX-Novel"), legacyNovelSkill)
	// 资产统一目录(2026-09-03 用户规则:角色与语音同属资产,统一 asset_lib 管理):
	// asset_lib/characters=角色资产库(2026-09-02 需求,跨项目形象指纹复用),
	// asset_lib/voices=音色库(2026-08-29「音色保存到稳定位置」,与 ComfyUI input
	// 解耦防误删)。换电脑整体拷贝即随迁;旧分立 char_lib/voice_lib 启动时自动迁入。
	AssetLibDir = filepath.Join(exeDir, "asset_lib")
	VoiceLibDir = filepath.Join(AssetLibDir, "voices")
	CharLibDir = filepath.Join(AssetLibDir, "characters")
	MigrateAssetLibs(exeDir)
	// 汇总索引启动重建(2026-09-03:平台/技能侧单文件直读;写操作另有实时同步)
	onPathsMigrated()
}

// migrateAssetLibs 旧分立资产库统一迁入 asset_lib(2026-09-03)。幂等:旧目录不
// 存在无事;目标已有同名项跳过(不覆盖已有资产);旧目录移空后移除,仍有残留
// (重名跳过)则保留原位不删。
func MigrateAssetLibs(exeDir string) {
	merge := func(oldRoot, newRoot string) {
		if !util.DirExists(oldRoot) {
			return
		}
		_ = os.MkdirAll(newRoot, 0755)
		entries, err := os.ReadDir(oldRoot)
		if err != nil {
			return
		}
		for _, e := range entries {
			dst := filepath.Join(newRoot, e.Name())
			if _, err := os.Stat(dst); err == nil {
				continue
			}
			_ = os.Rename(filepath.Join(oldRoot, e.Name()), dst)
		}
		if left, _ := os.ReadDir(oldRoot); len(left) == 0 {
			_ = os.Remove(oldRoot)
		}
	}
	merge(filepath.Join(exeDir, "char_lib"), filepath.Join(exeDir, "asset_lib", "characters"))
	merge(filepath.Join(exeDir, "voice_lib"), filepath.Join(exeDir, "asset_lib", "voices"))
}

// pickPath 显式配置优先;否则自包含目录存在时用之;否则回退旧路径。
func pickPath(setting, selfContained, legacy string) string {
	if s := strings.TrimSpace(setting); s != "" {
		return s
	}
	if selfContained != "" {
		if st, err := os.Stat(selfContained); err == nil && st.IsDir() {
			return selfContained
		}
	}
	return legacy
}

// manjuPathsGet 返回路径配置:显式值 + 生效值 + ComfyUI/技能就绪状态(设置弹窗「目录与部署」区)。

// OnPathsMigrated 路径迁移完成回调(资产索引重建等 manju 域动作;由 manju 包
// init 注入,paths 保持零业务依赖)
var OnPathsMigrated func()

func onPathsMigrated() {
	if OnPathsMigrated != nil {
		OnPathsMigrated()
	}
}


// LegacyNovelSkill 技能库旧固定路径(测试环境用)
func LegacyNovelSkill() string { return legacyNovelSkill }

// migrateAssetLibs 旧名包装(包内)
func migrateAssetLibs(exeDir string) { MigrateAssetLibs(exeDir) }
