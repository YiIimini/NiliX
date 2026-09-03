package manju

// 渲染检查点 + 崩溃恢复(ArcReel provider_job_id 语义的本地版):
// ComfyUI prompt_id 在提交后立即落盘(analysis/<ep>_render_ck.json),收产物后清除;
// NiliX 崩溃/被杀/服务重启后续跑时,先查 ComfyUI /history 收回「已提交未收产物」的任务:
// 已完成 → 直接复制产物免重渲(不重复烧 GPU);仍在执行 → 等它跑完再收;
// history 无记录(ComfyUI 也重启过,任务丢失)→ 重新提交。失败/错误任务自愈:下次
// 续跑读到 error 记录即清检查点重新渲染。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// manjuRenderCK 检查点文件结构:镜头key → prompt_id。
// 镜头key = 镜头号("03");草稿预审的草稿产物追加 "@d"("03@d")与定稿区分。
type manjuRenderCK struct {
	Shots map[string]string `json:"shots"`
}

var manjuRenderCKMu sync.Mutex

func (ctx *manjuCtx) renderCKPath() string {
	return filepath.Join(ctx.analysisDir, ctx.episode+"_render_ck.json")
}

func (ctx *manjuCtx) renderCKLoad() *manjuRenderCK {
	ck := &manjuRenderCK{Shots: map[string]string{}}
	b, err := os.ReadFile(ctx.renderCKPath())
	if err == nil {
		_ = json.Unmarshal(b, ck)
	}
	if ck.Shots == nil {
		ck.Shots = map[string]string{}
	}
	return ck
}

func (ctx *manjuCtx) renderCKSave(ck *manjuRenderCK) {
	_ = atomicWriteJSON(ctx.renderCKPath(), ck)
}

func (ctx *manjuCtx) renderCKSet(key, pid string) {
	manjuRenderCKMu.Lock()
	defer manjuRenderCKMu.Unlock()
	ck := ctx.renderCKLoad()
	ck.Shots[key] = pid
	ctx.renderCKSave(ck)
}

func (ctx *manjuCtx) renderCKGet(key string) string {
	manjuRenderCKMu.Lock()
	defer manjuRenderCKMu.Unlock()
	return ctx.renderCKLoad().Shots[key]
}

func (ctx *manjuCtx) renderCKClear(key string) {
	manjuRenderCKMu.Lock()
	defer manjuRenderCKMu.Unlock()
	ck := ctx.renderCKLoad()
	if _, ok := ck.Shots[key]; ok {
		delete(ck.Shots, key)
		ctx.renderCKSave(ck)
	}
}

// manjuReclaimLostWait history 无记录时判定「任务丢失」的等待窗(测试可缩短)
var manjuReclaimLostWait = 90 * time.Second

// tryReclaim 收回上次未收产物的渲染任务。
// 返回 (产物是否已收回, 错误):
//   - history 有记录且已完成 → 复制产物,true
//   - history 有记录且执行中 → 正常等待(长超时)后复制
//   - history 有记录且 error → false + 错误(调用方清检查点重新提交)
//   - history 无记录持续到超时(ComfyUI 重启任务丢失)→ false + nil(调用方重新提交)
func (ctx *manjuCtx) tryReclaim(pid, dst string, lg *manjuLogger) (bool, error) {
	deadline := time.Now().Add(manjuReclaimLostWait)
	for {
		entry := ctx.comfy.History(pid)
		if entry != nil {
			if st, _ := entry["status"].(map[string]any); st != nil {
				if ss, _ := st["status_str"].(string); ss == "error" {
					return false, fmt.Errorf("上次任务失败: %s", comfyErrMsg(st))
				}
				if done, _ := st["completed"].(bool); done {
					rel := comfyOutputVideo(entry)
					if rel == "" {
						return false, fmt.Errorf("上次任务完成但无视频输出")
					}
					if err := copyFile(filepath.Join(ctx.comfyOutput, rel), dst); err != nil {
						return false, err
					}
					lg.logf("  🔧 崩溃恢复:收回上次已完成的渲染产物 -> " + dst)
					return true, nil
				}
			}
			// 队列中/执行中:正常长等待,完成后回到上面 completed 分收取产物
			if err := ctx.comfy.Wait(pid, 3600*time.Second, 10*time.Second, lg.stopped); err != nil {
				return false, err
			}
			continue
		}
		if time.Now().After(deadline) {
			// 审计 M1:超窗且确认不在队列才判定"任务丢失"——忙队列长排队时 history 持续为空,
			// 直接判丢失会重新提交 → 原任务仍在队列 → 同一镜头双任务烧两遍 GPU
			// 查询失败(ComfyUI 忙/接口超时)= 无法确认,不判丢失,延长观察窗
			inQ, qerr := ctx.comfy.InQueue(pid)
			if qerr != nil {
				deadline = time.Now().Add(30 * time.Second)
			} else if !inQ {
				return false, nil // 任务丢失:history 无记录、队列明确无此任务、超窗
			} else {
				deadline = time.Now().Add(30 * time.Second) // 仍在队列:延长观察窗
			}
		}
		time.Sleep(3 * time.Second)
	}
}
