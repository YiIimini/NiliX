#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""分镜画面列【场景名】标注补全(2026-09-01 存量治理):
storyboard_check 场景锚定 WARN「N 镜画面列缺【场景名】开头标注」——渲染端有场景继承
兜底(无标注镜继承上一镜),但技能规范要求每镜标注。机械补标:无标注镜继承上一镜
场景名,显式写到 action 开头(与渲染端继承结果一致,显式化零风险);首镜无标注
用本章最高频场景名(与渲染端全集兜底同款);场景名逐字取自有标注镜,不新增卡。

用法: python fill_scene_anchors.py --root <novel库根> [--books 书1,书2]
"""
import argparse
import glob
import json
import os
import re
import sys
from collections import Counter

sys.stdout.reconfigure(encoding="utf-8")

ANCHOR = re.compile(r"^【([^】]+)】")


def fill_book(book_dir):
    files = sorted(glob.glob(os.path.join(book_dir, "素材", "分镜脚本", "*.json")))
    total_filled = 0
    for f in files:
        j = json.load(open(f, encoding="utf-8"))
        shots = j.get("shots", [])
        # 本章最高频场景(首镜无标注兜底,渲染端全集最高频同款)
        freq = Counter()
        for s in shots:
            m = ANCHOR.match((s.get("action", "") or "").strip())
            if m:
                freq[m.group(1).strip()] += 1
        fallback = freq.most_common(1)[0][0] if freq else ""
        last = ""
        changed = False
        for s in shots:
            act = (s.get("action", "") or "").strip()
            m = ANCHOR.match(act)
            if m:
                last = m.group(1).strip()
                continue
            name = last or fallback
            if not name:
                continue  # 全章无任何标注:渲染端兜底处理,不凭空造名
            s["action"] = f"【{name}】" + act
            last = name
            changed = True
            total_filled += 1
        if changed:
            json.dump(j, open(f, "w", encoding="utf-8", newline="\n"),
                      ensure_ascii=False, indent=1)
            print(f"  ✓ {os.path.basename(f)}")
    print(f"{os.path.basename(book_dir)}: 补标 {total_filled} 镜")
    return total_filled


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default=r"D:\Ai\NiliX\novel")
    ap.add_argument("--books", help="逗号分隔书名(默认全部)")
    args = ap.parse_args()
    books = [b.strip() for b in (args.books or "").split(",") if b.strip()]
    if not books:
        books = sorted(d for d in os.listdir(args.root)
                       if os.path.isdir(os.path.join(args.root, d)))
    total = 0
    for b in books:
        total += fill_book(os.path.join(args.root, b))
    print(f"完成: 共补标 {total} 镜")


if __name__ == "__main__":
    main()
