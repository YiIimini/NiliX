package api

import (
	"strings"
	"testing"
)

// 2026-08-27 用户规则:叙述优先群众议论化(画外·前缀),旁白禁止复述画面
func TestManjuIsOffScreenSpeaker(t *testing.T) {
	if !manjuIsOffScreenSpeaker("画外·路人甲") {
		t.Fatal("画外·前缀应识别为画外说话人")
	}
	if !manjuIsOffScreenSpeaker("  画外·围观妇女 ") {
		t.Fatal("画外·前缀(含首尾空格)应识别为画外说话人")
	}
	for _, s := range []string{"叶澜", "路人甲", "画外音", "旁白", ""} {
		if manjuIsOffScreenSpeaker(s) {
			t.Fatalf("「%s」不应识别为画外说话人(只有 画外· 前缀才算)", s)
		}
	}
}

// 台词说话人强制入画:画外群杂豁免(不进登场角色,不占 3 角色名额)
func TestOffScreenSpeakerNotForcedIntoCharacters(t *testing.T) {
	dlg := `(S1)画外·路人甲:"这不是叶家那个废物吗。"`
	speakers := []string{}
	for _, dm := range reDialogue.FindAllStringSubmatch(dlg, -1) {
		speaker := strings.TrimSpace(dm[1])
		if speaker == "" {
			speaker = strings.TrimSpace(dm[3])
		}
		speakers = append(speakers, speaker)
	}
	if len(speakers) != 1 || speakers[0] != "画外·路人甲" {
		t.Fatalf("画外说话人应正常解析出: %v", speakers)
	}
	// 豁免逻辑:画外说话人不进 charSet(与 scriptParsePlan 同款判断)
	charSet := map[string]bool{}
	for _, sp := range speakers {
		if sp != "" && !manjuIsOffScreenSpeaker(sp) {
			charSet[sp] = true
		}
	}
	if len(charSet) != 0 {
		t.Fatalf("画外说话人不应入登场角色: %v", charSet)
	}
}
