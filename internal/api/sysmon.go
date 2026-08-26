package api

import (
	"encoding/json"
	"net/http"

	"nilix/internal/sysmon"
)

// handleStats 返回系统监测快照（CPU/内存/GPU/磁盘/网络 + 设备控制中心 hw 段）。
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.sysmon.Snapshot())
}

// ecExec EC 控制路由(2026-08-26 用户体验定稿——授权即点即弹,拒绝「解锁」前置概念):
// 主服务自身管理员 → 直执;提权助手健康 → 文件通道下发;否则拉起助手(UAC 一次)并返回 pending。
func (s *Server) ecExec(act string, val interface{}) (pending bool, err error) {
	if sysmon.IsAdmin() {
		ec := s.sysmon.EC()
		switch act {
		case "mode":
			v, _ := val.(float64)
			return false, ec.SetMode(uint32(v))
		case "cool":
			v, _ := val.(bool)
			return false, ec.SetQuickCool(v)
		}
		return false, nil
	}
	if _, ok := sysmon.ReadHWAgentState(); ok {
		if err := sysmon.SendHWCmd(act, val); err != nil {
			return false, err
		}
		s.sysmon.RefreshHW() // 助手已在命令后立即采样,即时入缓存省 3s 轮询
		return false, nil
	}
	// 未授权:拉起提权助手(UAC 弹窗一次);授权后数据自动点亮,本次操作稍后重试即可
	if e := sysmon.EnsureHWAgent(); e != nil {
		return false, e
	}
	return true, nil
}

// handleHWCtl 设备控制中心(雷神同源通道):
//
//	{"act":"mode","val":2}    性能模式 0 轻效 / 1 进阶 / 2 巅峰
//	{"act":"cool","val":true} 快速制冷 开/关
//	{"act":"oc","val":true}   一键超频 开/关(NVAPI,免管理员)
func (s *Server) handleHWCtl(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Act string          `json:"act"`
		Val json.RawMessage `json:"val"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Act == "" {
		writeErr(w, http.StatusBadRequest, "参数错误")
		return
	}
	switch req.Act {
	case "mode":
		var v float64
		if json.Unmarshal(req.Val, &v) != nil || v > 3 {
			writeErr(w, http.StatusBadRequest, "模式值非法(0 轻效 / 1 进阶 / 2 巅峰)")
			return
		}
		if pending, err := s.ecExec("mode", v); pending || err != nil {
			writeHWCtlResult(w, pending, err)
			return
		}
	case "cool":
		var v bool
		if json.Unmarshal(req.Val, &v) != nil {
			writeErr(w, http.StatusBadRequest, "布尔值非法")
			return
		}
		if pending, err := s.ecExec("cool", v); pending || err != nil {
			writeHWCtlResult(w, pending, err)
			return
		}
	case "oc":
		var v bool
		if json.Unmarshal(req.Val, &v) != nil {
			writeErr(w, http.StatusBadRequest, "布尔值非法")
			return
		}
		core, mem := 0, 0
		if v {
			core, mem = 200, 1000 // 雷神同档:+200MHz 核心 / +1000MHz 显存
		}
		if !sysmon.NVSetOverclock(core, mem) {
			writeErr(w, http.StatusConflict, "超频设置未生效(NVAPI 不可用或显卡拒绝)")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "未知操作: "+req.Act)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "pending": false})
}

// writeHWCtlResult pending=已弹 UAC 待确认(200,前端提示);err=真失败(403)。
func writeHWCtlResult(w http.ResponseWriter, pending bool, err error) {
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "pending": true, "msg": "已请求管理员授权(仅一次),确认后风扇/模式/温度自动点亮"})
}
