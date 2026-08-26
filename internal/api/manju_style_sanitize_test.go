package api

// 2026-08-25 style 风格词净化回归测试(防污染:非美术风格词不得再混入 image_prompt/H3 提示词)。

import (
	"strings"
	"testing"
)

func TestManjuStyleSanitizeDropsRuleWords(t *testing.T) {
	// 历史污染串:角色塑造规则/括号指令/跨书残留题材词——全部应被净化丢弃
	style := "real+玄幻修仙+电影级运镜+电影动漫写实风格+场景唯美+反派磕碜+Q版呆萌可爱小角色(用于对应角色的动态内心独白)+二次元动漫写实风+真情实意+正能量+正角帅气或美丽+配角谄媚"
	spec := manjuStyleDesc(style)
	for _, bad := range []string{"玄幻", "Q版呆萌", "反派", "真情实意", "正能量", "电影级", "配角", "唯美"} {
		if strings.Contains(spec.asset, bad) {
			t.Fatalf("asset 仍含污染词 %q: %s", bad, spec.asset)
		}
		if strings.Contains(spec.opening, bad) {
			t.Fatalf("opening 仍含污染词 %q: %s", bad, spec.opening)
		}
		if strings.Contains(spec.shot1, bad) {
			t.Fatalf("shot1 仍含污染词 %q: %s", bad, spec.shot1)
		}
	}
	if !strings.Contains(spec.asset, "semi-realistic") {
		t.Fatalf("real 预设措辞丢失: %s", spec.asset)
	}
	if len(manjuStyleLastDropped) == 0 {
		t.Fatal("应记录被净化的风格词")
	}
}

func TestManjuStyleSanitizeZhWhitelist(t *testing.T) {
	// 中文白名单词应翻译为英文措辞
	spec := manjuStyleDesc("real+水墨")
	if !strings.Contains(spec.asset, "ink wash") {
		t.Fatalf("水墨未翻译: %s", spec.asset)
	}
	spec2 := manjuStyleDesc("赛博朋克+real")
	if !strings.Contains(spec2.asset, "cyberpunk") {
		t.Fatalf("赛博朋克未翻译: %s", spec2.asset)
	}
}

func TestManjuStyleSanitizeKeepEn(t *testing.T) {
	// 英文自定义风格词保留
	spec := manjuStyleDesc("real+cyberpunk+neon lighting")
	for _, want := range []string{"semi-realistic", "cyberpunk", "neon lighting"} {
		if !strings.Contains(spec.asset, want) {
			t.Fatalf("缺少 %q: %s", want, spec.asset)
		}
	}
}

func TestManjuStyleSanitizeAllDroppedFallsBackReal(t *testing.T) {
	// 全部被净化时回退 real 预设,不产生空提示词
	spec := manjuStyleDesc("反派磕碜+真情实意+正能量")
	if !strings.Contains(spec.asset, "semi-realistic") {
		t.Fatalf("未回退 real: %s", spec.asset)
	}
}

func TestManjuStyleSanitizeSingleToken(t *testing.T) {
	// 单个自定义 token(未用 + 组合)
	spec := manjuStyleDesc("cyberpunk")
	if spec.asset != "cyberpunk" {
		t.Fatalf("单 token 保留失败: %s", spec.asset)
	}
	spec2 := manjuStyleDesc("玄幻修仙")
	if !strings.Contains(spec2.asset, "semi-realistic") {
		t.Fatalf("单污染 token 未回退 real: %s", spec2.asset)
	}
}

func TestManjuStyleSanitizeDedupPreset(t *testing.T) {
	// 预设 key 直接查表不受净化影响
	spec := manjuStyleDesc("real")
	if spec.asset != manjuStyles["real"].asset {
		t.Fatalf("real 预设被改动: %s", spec.asset)
	}
}

func TestManjuKBChunksResolvesNewRoot(t *testing.T) {
	// 2026-08-25 knowledge 路径修复:模板实际在 zhishiku/创作管理/AI漫剧 下(旧 AI漫剧生产 已废弃),
	// 相对路径应自动解析到新根,且缺失模板记入 manjuKBLastMissing 而非静默。
	cfg := map[string]any{
		"knowledge": map[string]any{
			"characters": []any{"角色卡与提示词模板.md", "不存在的模板.md"},
		},
	}
	manjuKBLastMissing = nil
	out := manjuKBChunks(cfg, []string{"characters"})
	if !strings.Contains(out, "角色卡与提示词模板") {
		t.Fatalf("角色卡与提示词模板.md 未加载(根目录解析失败): %s", out[:min(len(out), 200)])
	}
	if len(manjuKBLastMissing) != 1 || manjuKBLastMissing[0] != "不存在的模板.md" {
		t.Fatalf("缺失模板未记录: %v", manjuKBLastMissing)
	}
}
