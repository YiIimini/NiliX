package api

import (
	"strings"
	"testing"
)

func TestStyleRealizeAndTiers(t *testing.T) {
	// 写实判定:万怪之主旧串(含次世代3D)是 3D;新串纯写实不是
	if !manjuStyleIs3D("real+次世代3D渲染+半写实国漫AI漫剧+虚拟数字人+BJD人偶质感") {
		t.Error("旧混搭串应判 3D")
	}
	if manjuStyleIs3D("real+写实电影级+精致亚洲五官+细腻真实皮肤纹理+电影级布光+不要3D游戏渲染+不要CG质感") {
		t.Error("纯写实串不应判 3D")
	}
	// 动漫判定:「不要2D漫画线条」是负面 token,不得误判为动漫向
	if manjuStyleIsAnime("real+写实电影级+不要2D漫画线条+不要手绘") {
		t.Error("负面「不要2D漫画线条」不得判为动漫向")
	}
	if !manjuStyleIsAnime("anime+2.5d+国漫") {
		t.Error("动漫串应判动漫向")
	}
	// 写实化:替换动漫化措辞,幂等
	hp := "The target video uses a realistic live-action film style with subtly anime-stylized semi-realistic characters, cinematic lighting."
	out := manjuRealizeStyle(hp)
	if strings.Contains(out, "anime-stylized") || strings.Contains(out, "semi-realistic") {
		t.Error("动漫化措辞未替换:", out)
	}
	// 2026-08-29 物种路由(审计 V6):替换词去 human——物品/兽形镜不被注入人形主体词
	if !strings.Contains(out, "photorealistic cinematic characters and subjects with natural detailed textures") {
		t.Error("缺少写实替换结果:", out)
	}
	if strings.Contains(out, "human characters") {
		t.Error("写实替换不得写 human(污染物品/兽镜):", out)
	}
	if out2 := manjuRealizeStyle(out); out2 != out {
		t.Error("写实化不幂等")
	}
	// 无动漫词时原样返回
	if manjuRealizeStyle("realistic live-action film style with cinematic lighting") != "realistic live-action film style with cinematic lighting" {
		t.Error("无动漫词不应改动")
	}
}
