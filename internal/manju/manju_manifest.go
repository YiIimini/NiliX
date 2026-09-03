package manju

// 产物清单时效(ArcReel artifact manifest 四态的桌面单机版):
// analysis/<ep>_manifest.json 记录每镜「渲染时的全部输入指纹」(提示词/角色/场景/画幅/帧数/资产指纹),
// 与现存产物比对得出时效——current(最新)/ stale(输入已变,产物是旧的)/ missing(无产物)/
// unknown(无记录,旧项目兼容视为 current)。stale 镜头在渲染阶段自动删旧重渲:
// 修复「改了定妆照/提示词,旧镜头产物仍被跳过复用」的隐性失效(条件缓存 .pt 同步删除,
// 换定妆照自动失效链路完整恢复)。

import (
	"nilix/internal/util"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// manjuShotManifest 单镜渲染记录
type manjuShotManifest struct {
	Fingerprint string `json:"fp"`             // 渲染时全部输入指纹(prompt/chars/scene/画幅/帧数/资产)
	OutSize     int64  `json:"size,omitempty"` // 产物大小(信息展示用)
	Seam        bool   `json:"seam,omitempty"` // 渲染时是否 MotionContext 接缝(合成转场硬切依据)
	RenderedAt  int64  `json:"at"`
}

type manjuManifest struct {
	Shots map[string]*manjuShotManifest `json:"shots"`
}

var manjuManifestMu sync.Mutex

func (ctx *manjuCtx) manifestPath() string {
	return filepath.Join(ctx.analysisDir, ctx.episode+"_manifest.json")
}

func (ctx *manjuCtx) manifestLoad() *manjuManifest {
	m := &manjuManifest{Shots: map[string]*manjuShotManifest{}}
	b, err := os.ReadFile(ctx.manifestPath())
	if err == nil {
		_ = json.Unmarshal(b, m)
	}
	if m.Shots == nil {
		m.Shots = map[string]*manjuShotManifest{}
	}
	return m
}

func (ctx *manjuCtx) manifestSave(m *manjuManifest) {
	_ = atomicWriteJSON(ctx.manifestPath(), m)
}

// shotRenderFingerprint 渲染输入全指纹 = 条件指纹(提示词/角色/场景/画幅/帧数) + 资产指纹(定妆照/场景图)
// + 渲染质量参数(审计 H7:steps/turbo_lora/sampler/sage/seed_policy——此前改这些参数后重跑被判
// current 直接跳过,新参数永不生效。seed 有意不参与:避免改 seed 触发全量重渲,定点重渲/fresh 覆盖)
func (ctx *manjuCtx) shotRenderFingerprint(s manjuShot) string {
	R := ctx.R
	lora := str(R["turbo_lora"])
	if r2v := str(R["turbo_lora_r2v"]); r2v != "" {
		lora += "|r2v=" + r2v
	}
	spec := turboLoRASpecOf(str(R["turbo_lora"]))
	// 2026-09-01 二合一:按镜覆盖纳入指纹(镜头调试面板)——覆盖变化→该镜 stale→
	// 自动重渲,其它镜不受影响(manifest 按镜记录天然隔离)
	ovFp := shotOverrideFingerprint(ctx.shotOverrideFor(s.ID))
	q := fmt.Sprintf("|steps=%d|sampler=%s|sched=%s|lora=%s|sage=%v|policy=%s|ov=%s",
		ctx.steps, spec.Sampler, spec.Scheduler, lora, R["sage_attention"], ctx.seedPolicy, ovFp)
	return md5Hex(ctx.shotCondFingerprint(s) + "|a=" + ctx.assetsFingerprint() + q)
}

// manifestMark 渲染成功后记录(仅定稿产物;draft 目录不入清单)
func (ctx *manjuCtx) manifestMark(s manjuShot, seam bool) {
	manjuManifestMu.Lock()
	defer manjuManifestMu.Unlock()
	m := ctx.manifestLoad()
	p := filepath.Join(ctx.clipsDir, ctx.episode, fmt.Sprintf("%02d.mp4", s.ID))
	size := int64(0)
	if fi, err := os.Stat(p); err == nil {
		size = fi.Size()
	}
	m.Shots[strconv.Itoa(s.ID)] = &manjuShotManifest{
		Fingerprint: ctx.shotRenderFingerprint(s), OutSize: size, Seam: seam, RenderedAt: nowUnix(),
	}
	ctx.manifestSave(m)
}

// manifestRemove 删除镜头产物时同步移除记录(clearShotArtifacts 调用)
func (ctx *manjuCtx) manifestRemove(shotID int) {
	manjuManifestMu.Lock()
	defer manjuManifestMu.Unlock()
	m := ctx.manifestLoad()
	if _, ok := m.Shots[strconv.Itoa(shotID)]; ok {
		delete(m.Shots, strconv.Itoa(shotID))
		ctx.manifestSave(m)
	}
}

// shotManifestStatus 产物时效:current / stale / missing / unknown(产物在但清单无记录)。
// 2026-08-26 修复:旧版 unknown 视为 current 直接跳过——「无记录」实为不可信(旧项目兼容语义),
// 输入已变却无指纹可比时,旧镜头被当作最新复用,正是「视频与分镜不同步」漏网路径之一。
// 漫剧项目库已清空重建档,无兼容包袱:unknown 一律按 stale 删旧重渲,宁可重烧不可用旧。
func (ctx *manjuCtx) shotManifestStatus(s manjuShot) string {
	p := filepath.Join(ctx.clipsDir, ctx.episode, fmt.Sprintf("%02d.mp4", s.ID))
	if !fileExists(p) {
		return "missing"
	}
	manjuManifestMu.Lock()
	m := ctx.manifestLoad()
	manjuManifestMu.Unlock()
	e := m.Shots[strconv.Itoa(s.ID)]
	if e == nil {
		return "stale" // 无指纹记录=不可信,重渲
	}
	if e.Fingerprint != ctx.shotRenderFingerprint(s) {
		return "stale"
	}
	return "current"
}

func nowUnix() int64 { return util.NowUnix() }
