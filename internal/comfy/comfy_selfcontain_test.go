package comfy

import (
	"nilix/internal/paths"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 自包含自愈回归测试(2026-08-26):整个 NiliX 拷到新电脑后,
// pyvenv.cfg / extra_model_paths.yaml 的路径绑定必须被自动校准。

func withDirs(t *testing.T, root, shared string) (restore func()) {
	t.Helper()
	oc, os_ := paths.ComfyRootDir, paths.ComfySharedDir
	paths.ComfyRootDir, paths.ComfySharedDir = root, shared
	return func() { paths.ComfyRootDir, paths.ComfySharedDir = oc, os_ }
}

func TestPyvenvHomeParseRewrite(t *testing.T) {
	cfg := "home = C:\\Old\\path\r\nimplementation = CPython\r\nuv = 0.12.0\r\n"
	if h := pyvenvHome(cfg); h != `C:\Old\path` {
		t.Fatalf("pyvenvHome = %q", h)
	}
	nb := rewritePyvenvHome(cfg, `D:/new/env`)
	if !strings.Contains(nb, "home = D:/new/env") || strings.Contains(nb, "Old") {
		t.Fatalf("rewritePyvenvHome = %q", nb)
	}
	if !strings.Contains(nb, "implementation = CPython") {
		t.Fatalf("rewrite 应保留其余行: %q", nb)
	}
}

func TestHealPyvenvCfgRewritesBrokenHome(t *testing.T) {
	root := t.TempDir()
	shared := t.TempDir()
	defer withDirs(t, root, shared)()
	// venv 带坏 home;standalone-env 在 root 旁(自包含布局 <exe>/comfyui/{ComfyUI,standalone-env})
	base := filepath.Join(filepath.Dir(root), "standalone-env")
	_ = os.MkdirAll(filepath.Join(root, ".venv"), 0755)
	_ = os.MkdirAll(filepath.Join(base), 0755)
	_ = os.WriteFile(filepath.Join(base, "python.exe"), []byte("x"), 0644)
	cfgPath := filepath.Join(root, ".venv", "pyvenv.cfg")
	_ = os.WriteFile(cfgPath, []byte("home = C:\\Ghost\\standalone-env\nversion_info = 3.13.12\n"), 0644)

	healPyvenvCfg()
	b, _ := os.ReadFile(cfgPath)
	if h := pyvenvHome(string(b)); h != filepath.ToSlash(base) {
		t.Fatalf("home 未被校准: %q (want %q)", h, filepath.ToSlash(base))
	}
}

func TestHealPyvenvCfgKeepsValidHome(t *testing.T) {
	root := t.TempDir()
	shared := t.TempDir()
	defer withDirs(t, root, shared)()
	base := filepath.Join(root, "base")
	_ = os.MkdirAll(filepath.Join(root, ".venv"), 0755)
	_ = os.MkdirAll(filepath.Join(base), 0755)
	_ = os.WriteFile(filepath.Join(base, "python.exe"), []byte("x"), 0644)
	cfgPath := filepath.Join(root, ".venv", "pyvenv.cfg")
	_ = os.WriteFile(cfgPath, []byte("home = "+base+"\n"), 0644)

	healPyvenvCfg()
	b, _ := os.ReadFile(cfgPath)
	if h := pyvenvHome(string(b)); h != base {
		t.Fatalf("有效 home 不应被改写: %q", h)
	}
}

func TestHealExtraModelPaths(t *testing.T) {
	root := t.TempDir()
	shared := t.TempDir()
	defer withDirs(t, root, shared)()
	yaml := filepath.Join(root, "extra_model_paths.yaml")

	// 旧绝对路径 → 校准到当前 Shared
	_ = os.WriteFile(yaml, []byte("shared:\n    base_path: C:/Users/x/ComfyUI-Shared/\n    checkpoints: models/checkpoints/\n"), 0644)
	healExtraModelPaths()
	b, _ := os.ReadFile(yaml)
	if !strings.Contains(string(b), "base_path: "+filepath.ToSlash(shared)+"/") {
		t.Fatalf("base_path 未校准: %s", b)
	}
	// 幂等:再跑不重复改写(文件内容不变)
	healExtraModelPaths()
	b2, _ := os.ReadFile(yaml)
	if string(b) != string(b2) {
		t.Fatalf("幂等校验失败")
	}
	// 文件缺失 → 重建标准映射
	_ = os.Remove(yaml)
	healExtraModelPaths()
	b3, _ := os.ReadFile(yaml)
	if !strings.Contains(string(b3), "base_path: ") || !strings.Contains(string(b3), "diffusion_models") {
		t.Fatalf("重建映射不完整: %s", b3)
	}
}
