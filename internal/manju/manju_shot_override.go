package manju

// 按镜参数覆盖机制(2026-09-01 二合一阶段一:镜头级可视化调试面板)。
// 覆盖存 analysis/<ep>_shot_overrides.json(独立文件,不动 plan 结构):
//   {"<shot_id>": {"seed": N, "steps": N, "sampler": "...", "turbo_lora": "...",
//    "pdd": bool, "sage": bool, "neg_prompt": "...", "note": "..."}}
// 读取点:seedFor(seed 优先)、renderShotTo(R 副本参数注入)、shotRenderFingerprint
// (覆盖纳入指纹——覆盖变化→该镜 stale→自动重渲,其它镜不动)。
// 生命周期:全部重做保留 override(用户显式参数);「重置默认」显式清空。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// manjuShotOverride 单镜参数覆盖(空值=沿用全局默认)
type manjuShotOverride struct {
	Seed      *int   `json:"seed,omitempty"`       // 覆盖 seed(否则全局 seed+镜号派生)
	Steps     *int   `json:"steps,omitempty"`      // 采样步数
	Sampler   string `json:"sampler,omitempty"`    // 采样器名(如 euler)
	Scheduler string `json:"scheduler,omitempty"`  // 调度器(如 simple)
	TurboLora string `json:"turbo_lora,omitempty"` // turbo LoRA 文件名(空=按全局)
	PDD       *bool  `json:"pdd,omitempty"`        // PDD Acc 引擎(覆盖全局自动判定)
	Sage      *bool  `json:"sage,omitempty"`       // SageAttention
	NegPrompt string `json:"neg_prompt,omitempty"` // 负面词(图片类;H3 无 CFG 通路仅作展示)
	Note      string `json:"note,omitempty"`       // 备注(为什么覆盖)
}

// manjuShotOverrides 全部覆盖(shot_id → override)
type manjuShotOverrides map[string]manjuShotOverride

func (ctx *manjuCtx) shotOverridesPath() string {
	return filepath.Join(ctx.analysisDir, ctx.episode+"_shot_overrides.json")
}

// loadShotOverrides 读覆盖(无文件返回空 map)
func (ctx *manjuCtx) loadShotOverrides() manjuShotOverrides {
	out := manjuShotOverrides{}
	if b, err := os.ReadFile(ctx.shotOverridesPath()); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

// saveShotOverrides 落盘
func (ctx *manjuCtx) saveShotOverrides(ov manjuShotOverrides) error {
	return atomicWriteJSON(ctx.shotOverridesPath(), ov)
}

// shotOverrideFor 单镜覆盖(无则零值)
func (ctx *manjuCtx) shotOverrideFor(shotID int) manjuShotOverride {
	return ctx.loadShotOverrides()[strconv.Itoa(shotID)]
}

// setShotOverride 保存/更新单镜覆盖;override 全空=清除该镜
func (ctx *manjuCtx) setShotOverride(shotID int, o manjuShotOverride) error {
	ov := ctx.loadShotOverrides()
	empty := o.Seed == nil && o.Steps == nil && o.Sampler == "" && o.Scheduler == "" &&
		o.TurboLora == "" && o.PDD == nil && o.Sage == nil && o.NegPrompt == "" && o.Note == ""
	if empty {
		delete(ov, strconv.Itoa(shotID))
	} else {
		ov[strconv.Itoa(shotID)] = o
	}
	return ctx.saveShotOverrides(ov)
}

// clearShotOverrides 清空全部覆盖(「重置默认」)
func (ctx *manjuCtx) clearShotOverrides() error {
	return ctx.saveShotOverrides(manjuShotOverrides{})
}

// shotOverrideFingerprint 该镜覆盖的指纹串(空覆盖返回空串;纳入 shotRenderFingerprint
// ——覆盖变化→该镜 stale→自动重渲,其它镜不受影响)
func shotOverrideFingerprint(o manjuShotOverride) string {
	if o.Seed == nil && o.Steps == nil && o.Sampler == "" && o.Scheduler == "" &&
		o.TurboLora == "" && o.PDD == nil && o.Sage == nil && o.NegPrompt == "" {
		return ""
	}
	var parts []string
	if o.Seed != nil {
		parts = append(parts, "seed="+strconv.Itoa(*o.Seed))
	}
	if o.Steps != nil {
		parts = append(parts, "steps="+strconv.Itoa(*o.Steps))
	}
	if o.Sampler != "" {
		parts = append(parts, "sampler="+o.Sampler)
	}
	if o.Scheduler != "" {
		parts = append(parts, "sched="+o.Scheduler)
	}
	if o.TurboLora != "" {
		parts = append(parts, "lora="+o.TurboLora)
	}
	if o.PDD != nil {
		parts = append(parts, "pdd="+strconv.FormatBool(*o.PDD))
	}
	if o.Sage != nil {
		parts = append(parts, "sage="+strconv.FormatBool(*o.Sage))
	}
	if o.NegPrompt != "" {
		parts = append(parts, "neg="+o.NegPrompt)
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

// mergeOverrideBool 覆盖优先的 bool 取值
func mergeOverrideBool(ov *bool, def bool) bool {
	if ov != nil {
		return *ov
	}
	return def
}
