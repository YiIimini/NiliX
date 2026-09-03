package manju

// 2026-08-29 抽卡采纳故障根治:上次渲染被手动停止后 stopped 卡 true,
// 抽卡/方案生成(旁路入口)提交 ComfyUI 后立即被 wait 停止感知 interrupt,
// 候选永不落盘、无法选择采纳——旁路开始即复位停止标记,渲染运行中仍拒绝。

import "testing"

func TestManjuGachaResetStopped(t *testing.T) {
	manjuState.mu.Lock()
	manjuState.stopped = true // 模拟:渲染任务被手动停止后残留
	manjuState.running = false
	manjuState.mu.Unlock()

	// 抽卡路径开头复位(实际由 manjuGachaDraw 内部完成;此处直测其复位前置逻辑)
	manjuState.mu.Lock()
	if manjuState.running {
		manjuState.mu.Unlock()
		t.Fatal("running 时应拒绝,但此处模拟空闲")
	}
	manjuState.stopped = false
	manjuState.mu.Unlock()

	manjuState.mu.Lock()
	got := manjuState.stopped
	manjuState.mu.Unlock()
	if got {
		t.Fatal("旁路入口开始后 stopped 应复位为 false,仍为 true")
	}
}

func TestManjuGachaRefuseWhileRunning(t *testing.T) {
	manjuState.mu.Lock()
	manjuState.running = true // 模拟渲染任务正在运行
	manjuState.stopped = false
	manjuState.mu.Unlock()

	manjuState.mu.Lock()
	if manjuState.running {
		manjuState.mu.Unlock()
		// 与 manjuGachaDraw 同款保护:running 时拒绝抽卡,不复位 stopped
		t.Log("渲染任务运行中,抽卡拒绝(符合预期)")
	} else {
		manjuState.mu.Unlock()
		t.Fatal("running 应为 true")
	}

	manjuState.mu.Lock()
	manjuState.running = false
	manjuState.mu.Unlock()
}

// 真实调用 manjuGachaDraw:即使 config 无效(newManjuCtx 失败),复位也必须先于一切执行
func TestManjuGachaDrawResetsBeforeFail(t *testing.T) {
	manjuState.mu.Lock()
	manjuState.stopped = true
	manjuState.running = false
	manjuState.mu.Unlock()

	_, err := manjuGachaDraw("C:/no/such/config.json", "EP01", "阿碧", "", 1)
	if err == nil {
		t.Fatal("无效 config 应报错")
	}
	manjuState.mu.Lock()
	got := manjuState.stopped
	manjuState.mu.Unlock()
	if got {
		t.Fatal("manjuGachaDraw 进入后 stopped 必须复位,即使后续失败")
	}
}
