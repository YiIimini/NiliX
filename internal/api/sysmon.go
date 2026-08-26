package api

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"nilix/internal/sysmon"
)

// handleStats 返回系统监测快照（CPU/内存/GPU/磁盘/网络 + 设备控制中心 hw 段）。
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.sysmon.Snapshot())
}

// handleHWCtl 设备控制中心(雷神同源通道):
//
//	{"act":"mode","val":2}          性能模式 0 轻效 / 1 进阶 / 2 巅峰(需管理员)
//	{"act":"cool","val":true}       快速制冷 开/关(需管理员)
//	{"act":"oc","val":true}         一键超频 开/关(NVAPI,免管理员)
//	{"act":"elevate"}               以管理员重启 NiliX 解锁 EC 通道
func (s *Server) handleHWCtl(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Act string          `json:"act"`
		Val json.RawMessage `json:"val"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Act == "" {
		writeErr(w, http.StatusBadRequest, "参数错误")
		return
	}
	ec := s.sysmon.EC()
	switch req.Act {
	case "mode":
		var v uint32
		if json.Unmarshal(req.Val, &v) != nil || v > 3 {
			writeErr(w, http.StatusBadRequest, "模式值非法(0 轻效 / 1 进阶 / 2 巅峰)")
			return
		}
		if err := ec.SetMode(v); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
	case "cool":
		var v bool
		if json.Unmarshal(req.Val, &v) != nil {
			writeErr(w, http.StatusBadRequest, "布尔值非法")
			return
		}
		if err := ec.SetQuickCool(v); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
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
	case "elevate":
		exe, err := os.Executable()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "定位自身失败: "+err.Error())
			return
		}
		if err := sysmon.ElevateRestart(exe, "-config settings.json -port 8787"); err != nil {
			writeErr(w, http.StatusForbidden, "提权被取消或失败: "+err.Error())
			return
		}
		// 新管理员实例即将拉起;旧实例让出端口(响应先送达,再延迟退出)
		go func() {
			time.Sleep(1200 * time.Millisecond)
			os.Exit(0)
		}()
	default:
		writeErr(w, http.StatusBadRequest, "未知操作: "+req.Act)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
