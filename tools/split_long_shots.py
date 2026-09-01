#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""超 15s 语音预算镜拆镜(2026-09-01 镜头时长×配音时长同步配套):
H3 API 单镜 ≤15s,台词语音(去标点字数÷4)超 60 字即念不完截断。
对超预算镜:台词行按 60 字/组打包,第 2 组起拆为新镜(紧随原镜,同场景同构图),
新镜 h3_prompt 从原镜拆分(风格句+场景句+对应 <d> 台词),shot_id 重排。
幂等:已不超预算的镜不动。用法: python split_long_shots.py [--books 书1,书2]
"""
import argparse
import glob
import json
import os
import re
import sys

sys.stdout.reconfigure(encoding="utf-8")
ROOT = os.path.join(r"D:\Ai\NiliX", "novel")
MAX_CHARS = 60  # 15s × 4字/s


def speech_chars(dlg):
    n = 0
    for ln in dlg.split("\n"):
        ln = ln.strip()
        if not ln:
            continue
        i = min([x for x in (ln.find(":"), ln.find("：")) if x >= 0] or [10**9])
        if i < 16:
            ln = ln[i + 1 :].strip()
        n += len(re.findall(r"[\u4e00-\u9fffA-Za-z0-9]", ln))
    return n


def split_dial_lines(lines):
    """台词行打包成组,每组语音 ≤MAX_CHARS"""
    groups, cur, cur_n = [], [], 0
    for ln in lines:
        n = speech_chars(ln)
        if cur and cur_n + n > MAX_CHARS:
            groups.append(cur)
            cur, cur_n = [], 0
        cur.append(ln)
        cur_n += n
    if cur:
        groups.append(cur)
    return groups


def pick_d_segments(hp, lines):
    """从 h3_prompt 提取对应台词行的 <d> 段(按内容包含匹配)"""
    segs = []
    for ln in lines:
        body = re.sub(r"^\([^)]*\)[^:：]*[:：]\s*", "", ln).strip('"“”')
        key = re.sub(r"[\s，。！？；：、—…·\"\'(),.!?;:]", "", body)
        if not key:
            continue
        for m in re.finditer(r"(<Subject \d+>\s*\(S\d+\)\s*(?:says|adds):\s*<d>(?:\[(?:中文|Chinese)\]\s*)?[^<]+</d>)", hp):
            dkey = re.sub(r"[\s，。！？；：、—…·\"\'(),.!?;:]", "", re.sub(r"<d>(?:\[(?:中文|Chinese)\]\s*)?([^<]+)</d>", r"\1", m.group(1)))
            if key[:8] and (dkey == key or dkey.startswith(key[:8]) or key.startswith(dkey[:8])):
                segs.append(m.group(1))
                break
    return segs


def build_shot_h3(tpl_hp, shot_id, dsegs, scene_act):
    """新镜 h3:风格句+[Shot N]+场景句+搬来的台词段+简略音效/配乐"""
    style = ""
    m = re.search(r"^(.{0,120}?)\[Shot \d+\]", tpl_hp, re.S)
    if m:
        style = m.group(1).strip()
    body = ""
    m2 = re.search(r"detailed_description:\s*\n?(.{0,260}?)(?:\n\s*(?:<Subject|overall_soundscape|non_diegetic_music))", tpl_hp, re.S)
    if m2:
        body = re.sub(r"\[Shot \d+\](?:\s+At\s+[\d:.]+)?\s*", "", m2.group(1)).strip()
    dlines = "\n".join(dsegs)
    return (
        f"{style} [Shot {shot_id}]\n"
        f"detailed_description:\n{body} The scene continues with the character(s) speaking. {scene_act}\n\n"
        f"{dlines}\n"
        f"overall_soundscape: continuing ambient sound of the scene.\n"
        f"non_diegetic_music: low background music, consistent with the previous shots."
    )


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--books", help="逗号分隔书名(默认全部)")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()
    books = [b.strip() for b in (args.books or "").split(",") if b.strip()]
    if not books:
        books = sorted(d for d in os.listdir(ROOT) if os.path.isdir(os.path.join(ROOT, d)))
    total = 0
    for b in books:
        nsplit = 0
        for f in sorted(glob.glob(os.path.join(ROOT, b, "素材", "分镜脚本", "*.json"))):
            j = json.load(open(f, encoding="utf-8"))
            shots = j.get("shots", [])
            changed = False
            new_shots = []
            for s in shots:
                dlg = (s.get("dialogue") or "").strip()
                if not dlg:
                    new_shots.append(s)
                    continue
                need = (speech_chars(dlg) + 3) // 4
                if need <= 15:
                    new_shots.append(s)
                    continue
                lines = [x for x in dlg.split("\n") if x.strip()]
                groups = split_dial_lines(lines)
                if len(groups) <= 1:
                    new_shots.append(s)
                    continue
                hp = s.get("h3_prompt", "") or ""
                action = s.get("action", "") or ""
                for gi, g in enumerate(groups):
                    if gi == 0:
                        s["dialogue"] = "\n".join(g)
                        s["duration"] = min(15, max(4, (speech_chars("\n".join(g)) + 3) // 4))
                        new_shots.append(s)
                    else:
                        dsegs = pick_d_segments(hp, g)
                        new = {
                            "shot_id": 0,
                            "shot_size": s.get("shot_size", "中景"),
                            "camera": s.get("camera", "固定"),
                            "action": action,
                            "dialogue": "\n".join(g),
                            "characters": list(s.get("characters") or []),
                            "light": s.get("light", ""),
                            "sound": s.get("sound", ""),
                            "duration": min(15, max(4, (speech_chars("\n".join(g)) + 3) // 4)),
                            "style": s.get("style", ""),
                            "h3_prompt": build_shot_h3(hp, 0, dsegs or ["<Subject 1> (S1) says: <d>[Chinese] " + re.sub(r"^\([^)]*\)[^:：]*[:：]\s*", "", g[0]).strip('"“”') + "</d>"], action[:50]),
                        }
                        new_shots.append(new)
                        nsplit += 1
                        changed = True
            if changed:
                for i, s in enumerate(new_shots, 1):
                    s["shot_id"] = i
                j["shots"] = new_shots
                if not args.dry_run:
                    json.dump(j, open(f, "w", encoding="utf-8", newline="\n"),
                              ensure_ascii=False, indent=1)
        if nsplit:
            print(f"{b}: 拆出 {nsplit} 镜" + (" [dry]" if args.dry_run else ""))
        total += nsplit
    print(f"合计拆镜: {total}")


if __name__ == "__main__":
    main()
