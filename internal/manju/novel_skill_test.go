package manju
import "testing"

func TestNovelSkillAssets(t *testing.T) {
	if n := novelChineseCount("沈烬低声道 abc 123,你敢。"); n != 8 {
		t.Errorf("中文口径计数=%d, want 8(7汉字+1中文句号,半角逗号与abc/123不计)", n)
	}
	q := novelChapterQA("短文")
	if len(q) == 0 {
		t.Error("短文应报字数不足")
	}
	hit := false
	for _, p := range novelChapterQA("此处的正文含有'此外'一词用于测试去AI味校验逻辑是否生效且长度足够长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长") {
		if p == "去AI味禁用词:此外" {
			hit = true
		}
	}
	if !hit {
		t.Error("禁用词'此外'未检出")
	}
	hard := novelHardBanned()
	if len(hard) < 10 {
		t.Errorf("违规词库加载=%d 条(技能库应远多于10)", len(hard))
	}
}
