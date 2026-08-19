package api

// 产物清单时效(ArcReel artifact manifest 四态的桌面单机版):
// analysis/<ep>_manifest.json 记录每镜「渲染时的全部输入指纹」(提示词/角色/场景/画幅/帧数/资产指纹),
// 与现存产物比对得出时效——current(最新)/ stale(输入已变,产物是旧的)/ missing(无产物)/
// unknown(无记录,旧项目兼容视为 current)。stale 镜头在渲染阶段自动删旧重渲:
// 修复「改了定妆照/提示词,旧镜头产物仍被跳过复用」的隐性失效(条件缓存 .pt 同步删除,
// 换定妆照自动失效链路完整恢复)。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
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
func (ctx *manjuCtx) shotRenderFingerprint(s manjuShot) string {
	return md5Hex(ctx.shotCondFingerprint(s) + "|a=" + ctx.assetsFingerprint())
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

// shotManifestStatus 产物时效:current / stale / missing / unknown(无记录=旧项目兼容,视为 current)
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
		return "unknown"
	}
	if e.Fingerprint != ctx.shotRenderFingerprint(s) {
		return "stale"
	}
	return "current"
}

func nowUnix() int64 { return time.Now().Unix() }
