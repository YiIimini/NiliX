package api

// 便签管理(2026-08-29 用户需求:「视频管理」页「使用说明」前新增便签按钮,
// 弹窗内自行添加/管理用户输入的纯文本记录;2026-08-29 升级:增加标题字段)。
//   GET  /api/manju/notes                    — 全部便签(按更新时间倒序)
//   POST /api/manju/notes {action,id,title,text} — add(新增) / update(改标题/内容) / delete(删除)
// 存储:应用根 notes.json(与 settings.json 同目录,随项目整体移动)

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// manjuNote 单条便签
type manjuNote struct {
	ID      string `json:"id"`
	Title   string `json:"title"` // 标题(可空;前端空时取内容前缀)
	Text    string `json:"text"`
	Created int64  `json:"created"`
	Updated int64  `json:"updated"`
}

var manjuNotesMu sync.Mutex

func manjuNotesPath() string {
	return filepath.Join(manjuEngineDir, "notes.json")
}

// manjuNotesLoad 读便签文件(不存在返回空列表)
func manjuNotesLoad() []manjuNote {
	b, err := os.ReadFile(manjuNotesPath())
	if err != nil {
		return nil
	}
	var notes []manjuNote
	if json.Unmarshal(b, &notes) != nil {
		return nil
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].Updated > notes[j].Updated })
	return notes
}

func manjuNotesSave(notes []manjuNote) error {
	b, _ := json.MarshalIndent(notes, "", "  ")
	return os.WriteFile(manjuNotesPath(), b, 0644)
}

// manjuNotesGet GET /api/manju/notes
func manjuNotesGet(w http.ResponseWriter, r *http.Request) {
	manjuNotesMu.Lock()
	notes := manjuNotesLoad()
	manjuNotesMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"notes": notes})
}

// manjuNotesPost POST /api/manju/notes {action,id,text}
func manjuNotesPost(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	action := str(body["action"])
	manjuNotesMu.Lock()
	defer manjuNotesMu.Unlock()
	notes := manjuNotesLoad()
	switch action {
	case "add":
		text := strings.TrimSpace(str(body["text"]))
		if text == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "内容为空"})
			return
		}
		now := time.Now().Unix()
		note := manjuNote{ID: fmt.Sprintf("%d", now), Title: strings.TrimSpace(str(body["title"])), Text: text, Created: now, Updated: now}
		notes = append(notes, note)
		if err := manjuNotesSave(notes); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": note})
	case "update":
		id := str(body["id"])
		text := strings.TrimSpace(str(body["text"]))
		for i := range notes {
			if notes[i].ID == id {
				if text == "" {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "内容为空"})
					return
				}
				notes[i].Title = strings.TrimSpace(str(body["title"]))
				notes[i].Text = text
				notes[i].Updated = time.Now().Unix()
				if err := manjuNotesSave(notes); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": notes[i]})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "便签不存在"})
	case "delete":
		id := str(body["id"])
		out := notes[:0]
		for _, n := range notes {
			if n.ID != id {
				out = append(out, n)
			}
		}
		if err := manjuNotesSave(out); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "未知操作: " + action})
	}
}
