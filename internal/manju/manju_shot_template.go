package manju

// 按镜参数模板(2026-09-03 画布三期):把一镜的覆盖参数(seed/steps/采样器/引擎/负面词)
// 存为命名模板,批量应用到其它镜头或整集——同类镜头(夜景/打斗/空镜)统一手感。
// 存储与 overrides 同目录:analysis/shot_templates.json(项目级、不带集前缀,跨集复用)。
//   GET  /api/manju/shot/templates?config=            → 模板列表
//   POST /api/manju/shot/template/save                {config, name, note, override}
//   POST /api/manju/shot/template/delete              {config, name}
//   POST /api/manju/shot/template/apply               {config, episode, shots:[], override}
// apply 语义:模板非空字段覆盖到目标镜已有 override 之上(未涉及字段保留),覆盖纳入
// 渲染指纹(shotRenderFingerprint)——模板应用后相关镜自动 stale,下次渲染生效。

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// manjuShotTemplate 参数模板
type manjuShotTemplate struct {
	Name     string           `json:"name"`               // 模板名(唯一键,同名保存=更新)
	Note     string           `json:"note,omitempty"`     // 说明
	Override manjuShotOverride `json:"override"`           // 参数内容(与按镜覆盖同结构)
	Updated  string           `json:"updated"`            // 最后更新时间(RFC3339)
}

func (ctx *manjuCtx) shotTemplatesPath() string {
	return filepath.Join(ctx.analysisDir, "shot_templates.json")
}

// loadShotTemplates 读模板(按名排序)
func (ctx *manjuCtx) loadShotTemplates() []manjuShotTemplate {
	out := []manjuShotTemplate{}
	b, err := os.ReadFile(ctx.shotTemplatesPath())
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// saveShotTemplates 落盘
func (ctx *manjuCtx) saveShotTemplates(list []manjuShotTemplate) error {
	return atomicWriteJSON(ctx.shotTemplatesPath(), list)
}

// upsertShotTemplate 存/更新模板(空参数模板拒绝——没有可应用内容)
func (ctx *manjuCtx) upsertShotTemplate(name string, note string, o manjuShotOverride) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("模板名不能为空")
	}
	if shotOverrideFingerprint(o) == "" {
		return errors.New("模板内容为空(至少一个参数)")
	}
	list := ctx.loadShotTemplates()
	replaced := false
	for i := range list {
		if list[i].Name == name {
			list[i].Note = note
			list[i].Override = o
			list[i].Updated = time.Now().Format(time.RFC3339)
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, manjuShotTemplate{Name: name, Note: note, Override: o,
			Updated: time.Now().Format(time.RFC3339)})
	}
	return ctx.saveShotTemplates(list)
}

// deleteShotTemplate 删除模板(不存在=幂等成功)
func (ctx *manjuCtx) deleteShotTemplate(name string) error {
	list := ctx.loadShotTemplates()
	out := list[:0]
	for _, t := range list {
		if t.Name != name {
			out = append(out, t)
		}
	}
	return ctx.saveShotTemplates(out)
}

// applyShotTemplate 批量应用:模板非空字段覆盖到目标镜已有覆盖之上;shots 空=整集全部镜
func (ctx *manjuCtx) applyShotTemplate(shotIDs []int, tpl manjuShotOverride) (int, error) {
	plan, _, err := ctx.loadPlan()
	if err != nil {
		return 0, err
	}
	shots, _ := planShots(plan)
	if len(shots) == 0 {
		return 0, errors.New("方案无镜头")
	}
	targets := shotIDs
	if len(targets) == 0 {
		for _, s := range shots {
			targets = append(targets, s.ID)
		}
	}
	valid := map[int]bool{}
	for _, s := range shots {
		valid[s.ID] = true
	}
	applied := 0
	for _, id := range targets {
		if !valid[id] {
			continue
		}
		ov := ctx.shotOverrideFor(id)
		if tpl.Seed != nil {
			ov.Seed = tpl.Seed
		}
		if tpl.Steps != nil {
			ov.Steps = tpl.Steps
		}
		if tpl.Sampler != "" {
			ov.Sampler = tpl.Sampler
		}
		if tpl.Scheduler != "" {
			ov.Scheduler = tpl.Scheduler
		}
		if tpl.TurboLora != "" {
			ov.TurboLora = tpl.TurboLora
		}
		if tpl.PDD != nil {
			ov.PDD = tpl.PDD
		}
		if tpl.Sage != nil {
			ov.Sage = tpl.Sage
		}
		if tpl.NegPrompt != "" {
			ov.NegPrompt = tpl.NegPrompt
		}
		if tpl.Note != "" {
			ov.Note = tpl.Note
		}
		if err := ctx.setShotOverride(id, ov); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

// manjuShotTemplatesHandler GET 模板列表
func manjuShotTemplatesHandler(w http.ResponseWriter, r *http.Request) {
	configPath := strings.TrimSpace(r.URL.Query().Get("config"))
	ctx, err := newManjuCtx(configPath, "", "", "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "templates": ctx.loadShotTemplates()})
}

// manjuShotTemplateSaveHandler POST 存/更新模板
func manjuShotTemplateSaveHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config   string            `json:"config"`
		Name     string            `json:"name"`
		Note     string            `json:"note"`
		Override manjuShotOverride `json:"override"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	ctx, err := newManjuCtx(body.Config, "", "", "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := ctx.upsertShotTemplate(body.Name, body.Note, body.Override); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": body.Name})
}

// manjuShotTemplateDeleteHandler POST 删除模板
func manjuShotTemplateDeleteHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config string `json:"config"`
		Name   string `json:"name"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	ctx, err := newManjuCtx(body.Config, "", "", "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := ctx.deleteShotTemplate(body.Name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// manjuShotTemplateApplyHandler POST 批量应用模板内容(shots 空=整集)
func manjuShotTemplateApplyHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config   string            `json:"config"`
		Episode  string            `json:"episode"`
		Shots    []int             `json:"shots"`
		Override manjuShotOverride `json:"override"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	ctx, err := newManjuCtx(body.Config, body.Episode, "", "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if shotOverrideFingerprint(body.Override) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "模板内容为空"})
		return
	}
	n, err := ctx.applyShotTemplate(body.Shots, body.Override)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "applied": n})
}
