package api

// manju_fingerprint.go — 反同质化指纹机制
//
// 整合 ai-film-skills `directing/variables.md` + `directing/fingerprint.md` 的实测方法论:
//   - 每集方案落一条指纹到 <项目>/analysis/fingerprints.jsonl(追加写,同集号幂等替换)
//   - 新方案生成前:注入同小说历史摘要,要求五维与历史拉开距离
//   - 新方案生成后:validatePlan 复查 —— vars 撞 ≥3 维(近 3 集)或 signature 撞 ≥2 项(同小说全部历史)
//     即判问题,复用既有「带意见修复重试」闭环
//   - 分集例外(仓库明文):同一系列(同一小说)各集的 structure 层不查;
//     vars 和 signature 两层照常逐集查
//   - 局限:文本字段用精确匹配判定(仓库原版是语义等价判定,Go 侧先做第一刀,文档已注明)

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// manjuFingerprint 单集方案指纹(对应仓库 films.jsonl 一行)
type manjuFingerprint struct {
	ID            string            `json:"id"`   // 集号,如 EP01
	Name          string            `json:"name"` // 项目名
	Date          string            `json:"date"` // 记录日期 YYYY-MM-DD
	Novel         string            `json:"novel"` // 小说文件名(basename),查重按同小说范围
	Style         string            `json:"style"` // 渲染风格(系列标识;structure 层按系列豁免的依据)
	Vars          map[string]string `json:"vars"` // 五维变量表 time/pov/tempo/audio/ending
	ShotCount     int               `json:"shot_count"`
	OpeningSize   string            `json:"opening_size"`
	EndingSize    string            `json:"ending_size"`
	PeakDevice    string            `json:"peak_device"`    // 情绪最高点手法(写手法不写题材)
	ClimaxPattern string            `json:"climax_pattern"` // 高潮段镜头组织方式
}

// manjuFingerprintDims 五维(仓库 variables.md)
var manjuFingerprintDims = []string{"time", "pov", "tempo", "audio", "ending"}

const manjuFingerprintFile = "fingerprints.jsonl"

func manjuFingerprintPath(analysisDir string) string {
	return filepath.Join(analysisDir, manjuFingerprintFile)
}

// manjuFingerprintLoad 读指纹历史(坏行跳过,不阻塞)
func manjuFingerprintLoad(path string) []manjuFingerprint {
	var recs []manjuFingerprint
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 512*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r manjuFingerprint
		if json.Unmarshal([]byte(line), &r) == nil && r.ID != "" {
			if r.Vars == nil {
				r.Vars = map[string]string{}
			}
			recs = append(recs, r)
		}
	}
	return recs
}

// planFingerprint 从方案推导指纹(vars 缺省空,老方案不强制)
func (ctx *manjuCtx) planFingerprint(plan map[string]any) manjuFingerprint {
	fp := manjuFingerprint{
		ID:    ctx.episode,
		Name:  ctx.project,
		Date:  time.Now().Format("2006-01-02"),
		Novel: filepath.Base(ctx.novel),
		Style: ctx.style,
		Vars:  map[string]string{},
	}
	if d, ok := plan["directing"].(map[string]any); ok {
		for _, k := range manjuFingerprintDims {
			if v := str(d[k]); v != "" {
				fp.Vars[k] = v
			}
		}
		if v := str(d["peak_device"]); v != "" {
			fp.PeakDevice = v
		}
		if v := str(d["climax_pattern"]); v != "" {
			fp.ClimaxPattern = v
		}
	}
	shots, _ := planShots(plan)
	fp.ShotCount = len(shots)
	if len(shots) > 0 {
		fp.OpeningSize = shots[0].ShotSize
		fp.EndingSize = shots[len(shots)-1].ShotSize
	}
	return fp
}

// recordPlanFingerprint 记录/更新本集指纹(同集号幂等替换,方案复用也回填)
func (ctx *manjuCtx) recordPlanFingerprint(plan map[string]any, lg *manjuLogger) {
	path := manjuFingerprintPath(ctx.analysisDir)
	if err := os.MkdirAll(ctx.analysisDir, 0o755); err != nil {
		return
	}
	fp := ctx.planFingerprint(plan)
	recs := manjuFingerprintLoad(path)
	out := recs[:0]
	for _, r := range recs {
		if r.ID != fp.ID || r.Novel != fp.Novel {
			out = append(out, r) // 同集号同小说旧记录替换
		}
	}
	out = append(out, fp)
	var sb strings.Builder
	for _, r := range out {
		b, err := json.Marshal(r)
		if err != nil {
			continue
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err == nil && lg != nil {
		lg.logf("  🧬 已记录方案指纹: " + manjuFingerprintString(fp))
	}
}

// sameNovelRecords 同小说(系列)历史,排除本集旧记录
func (ctx *manjuCtx) sameNovelRecords(plan map[string]any) []manjuFingerprint {
	novel := filepath.Base(ctx.novel)
	var same []manjuFingerprint
	for _, r := range manjuFingerprintLoad(manjuFingerprintPath(ctx.analysisDir)) {
		if r.Novel != novel || r.ID == ctx.episode {
			continue
		}
		same = append(same, r)
	}
	// 按集号逆序(ID 形如 EP01/EP02,字符串序够用),最近的在前面
	sort.Slice(same, func(i, j int) bool { return same[i].ID > same[j].ID })
	return same
}

// fingerprintVarsCollision vars 撞 ≥3 维(最近 3 集):返回撞的维度清单
func fingerprintVarsCollision(fp manjuFingerprint, recent []manjuFingerprint) []string {
	if len(recent) == 0 {
		return nil
	}
	var hits []string
	for _, d := range manjuFingerprintDims {
		v := fp.Vars[d]
		if v == "" {
			continue // 本集该维未填,不算撞
		}
		allEqual := true
		for _, r := range recent {
			if r.Vars[d] != v {
				allEqual = false
				break
			}
		}
		if allEqual {
			hits = append(hits, d)
		}
	}
	if len(hits) >= 3 {
		return hits
	}
	return nil
}

// fingerprintSignatureCollision signature 撞 ≥2 项(同小说全部历史):
// shot_count ±10% 内算撞;景别/手法文本精确匹配(仓库语义等价判定暂降级)
func fingerprintSignatureCollision(fp manjuFingerprint, all []manjuFingerprint) []string {
	if len(all) == 0 {
		return nil
	}
	var hits []string
	for _, r := range all {
		var per []string
		if fp.ShotCount > 0 && r.ShotCount > 0 {
			diff := float64(absInt(fp.ShotCount-r.ShotCount)) / float64(r.ShotCount)
			if diff <= 0.10 {
				per = append(per, fmt.Sprintf("镜头数 %d≈%d", fp.ShotCount, r.ShotCount))
			}
		}
		if fp.OpeningSize != "" && fp.OpeningSize == r.OpeningSize {
			per = append(per, "开场景别="+fp.OpeningSize)
		}
		if fp.EndingSize != "" && fp.EndingSize == r.EndingSize {
			per = append(per, "结尾景别="+fp.EndingSize)
		}
		if fp.PeakDevice != "" && fp.PeakDevice == r.PeakDevice {
			per = append(per, "高潮手法="+fp.PeakDevice)
		}
		if fp.ClimaxPattern != "" && fp.ClimaxPattern == r.ClimaxPattern {
			per = append(per, "高潮组织="+fp.ClimaxPattern)
		}
		if len(per) >= 2 {
			hits = append(hits, "与"+r.ID+"("+r.Date+") 撞: "+strings.Join(per, "; "))
		}
	}
	if len(hits) >= 1 {
		return hits
	}
	return nil
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// planFingerprintProblems 方案级反同质化复查(挂进 validatePlan):
// 返回问题清单(空=通过)。vars 近 3 集撞 ≥3 维 → 重选(硬);signature 全历史撞 ≥2 项 → 重设计。
func (ctx *manjuCtx) planFingerprintProblems(plan map[string]any) []string {
	all := ctx.sameNovelRecords(plan)
	if len(all) == 0 {
		return nil
	}
	fp := ctx.planFingerprint(plan)
	var problems []string
	if hits := fingerprintVarsCollision(fp, all[:minInt(3, len(all))]); len(hits) >= 3 {
		problems = append(problems, fmt.Sprintf(
			"【防同质化】directing 五维与近 3 集撞 ≥3 维(%s),请重选至少 2 个维度与历史拉开距离(历史: %s)",
			strings.Join(hits, "/"), manjuFingerprintVarsBrief(all[:minInt(3, len(all))])))
	}
	if hits := fingerprintSignatureCollision(fp, all); len(hits) > 0 {
		problems = append(problems, "【防同质化】signature 撞 ≥2 项(全部历史): "+strings.Join(hits, " | ")+"。请重设计开场/结尾景别或高潮手法")
	}
	return problems
}

// manjuFingerprintHistorySummary 历史摘要(新方案生成前注入系统提示词;空=无同小说历史)
func (ctx *manjuCtx) manjuFingerprintHistorySummary(plan map[string]any) string {
	all := ctx.sameNovelRecords(plan)
	if len(all) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("同小说历史方案指纹(新方案必须与以下历史拉开距离):\n")
	for i, r := range all {
		if i >= 3 {
			break
		}
		sb.WriteString("- " + r.ID + "(" + r.Date + ") " + manjuFingerprintVarsBrief([]manjuFingerprint{r}) +
			" 首镜=" + orDefault(r.OpeningSize, "?") + " 尾镜=" + orDefault(r.EndingSize, "?") +
			" 镜数=" + fmt.Sprint(r.ShotCount) + "\n")
	}
	sb.WriteString("directing 五维禁止与近 3 集撞 ≥3 维;高潮手法/首尾景别禁止与全部历史重复(写手法不写题材)。")
	return sb.String()
}

func manjuFingerprintVarsBrief(recs []manjuFingerprint) string {
	if len(recs) == 0 {
		return ""
	}
	var parts []string
	for _, d := range manjuFingerprintDims {
		vals := map[string]bool{}
		for _, r := range recs {
			if v := r.Vars[d]; v != "" {
				vals[v] = true
			}
		}
		if len(vals) > 0 {
			keys := make([]string, 0, len(vals))
			for k := range vals {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts = append(parts, d+"="+strings.Join(keys, "|"))
		}
	}
	return strings.Join(parts, " ")
}

func manjuFingerprintString(fp manjuFingerprint) string {
	return fmt.Sprintf("%s %s 五维[%s] 首=%s 尾=%s 镜数=%d",
		fp.ID, fp.Novel, manjuFingerprintVarsBrief([]manjuFingerprint{fp}),
		orDefault(fp.OpeningSize, "?"), orDefault(fp.EndingSize, "?"), fp.ShotCount)
}
