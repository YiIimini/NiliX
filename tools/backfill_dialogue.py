#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""h3 台词回填 dialogue 列(2026-09-01 崩溃修复配套):
LLM 直出/补对白时台词写进 h3 <d> 但 dialogue 列空(198 镜实测)——渲染端
dialogueSpeakerIDs 拿不到说话人(音色挂载/QC 期望缺失),且空 dialogue 触发了
nil map 崩溃路径。回填:h3 画面段 `<Subject N> (Sx) says/adds: <d>[Chinese] 内容</d>`
→ dialogue 行 `(Sx)角色名:"内容"`,角色名取该镜 characters[N-1](挂载顺序=Subject 编号)。
幂等:已有 dialogue 的镜不动。用法: python backfill_dialogue.py [--books 书1,书2]
"""
import argparse
import glob
import json
import os
import re
import sys

sys.stdout.reconfigure(encoding="utf-8")
ROOT = os.path.join(r"D:\Ai\NiliX", "novel")

SAYS_RE = re.compile(
    r"<Subject (\d+)>\s*\((S\d+)\)\s*(?:says|adds)(?: in an off-screen voiceover)?:\s*<d>\s*(?:\[(?:中文|Chinese)\]\s*)?([^<]+)</d>")


def backfill_file(path):
    j = json.load(open(path, encoding="utf-8"))
    n = 0
    for s in j.get("shots", []):
        dlg = (s.get("dialogue") or "").strip()
        hp = s.get("h3_prompt", "") or ""
        if dlg or not hp:
            continue
        chars = s.get("characters") or []
        lines = []
        for m in SAYS_RE.finditer(hp):
            subj = int(m.group(1))
            sx = m.group(2)
            content = m.group(3).strip()
            if subj < 1 or subj > len(chars):
                continue
            name = chars[subj - 1]
            # 群演·前缀剥掉(说话人格式对齐:群演·老周 → 老周)
            name = name.split("·")[-1].strip()
            lines.append(f'({sx}){name}:"{content}"')
        if lines:
            s["dialogue"] = "\n".join(lines)
            n += 1
    if n:
        json.dump(j, open(path, "w", encoding="utf-8", newline="\n"),
                  ensure_ascii=False, indent=1)
    return n


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
        n = 0
        for f in sorted(glob.glob(os.path.join(ROOT, b, "素材", "分镜脚本", "*.json"))):
            n += backfill_file(f)
        if n:
            print(f"{b}: 回填 {n} 镜")
        total += n
    print(f"合计回填: {total}")


if __name__ == "__main__":
    main()
