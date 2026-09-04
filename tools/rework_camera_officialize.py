# -*- coding: utf-8 -*-
"""运镜黑话返正·全库存量落盘(2026-09-04,画质升级的存量同步)。

精确反解 2026-09-03 rework_cinematic_phrases.py 写入的 6 个"电影术语"模式——
官方 VIDEO_PROMPT_WRITING_GUIDE 词表只有 15 个 motion type,H3 训练对齐官方
动词句式("the camera pushes in with small amplitude at slow speed"),dolly/
orbital 行业黑话服从性打折。与渲染端 manjuOfficializeCameraVerbs 同源(单一
事实源);渲染端在 finalize 链也会兜底归一,此处返正是源头落盘(技能侧产物
同水位)。幂等:官方句式不命中。crane rise 等名词形态(气闸句叙事引用)保留。

用法: python tools/rework_camera_officialize.py [--dry-run] [--book 书名]
"""
import argparse
import glob
import io
import json
import os
import re
import sys

NOVEL_ROOT = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "novel")

# 与 internal/manju/manju_pipeline.go manjuOfficialCameraRepl 逐条同源
CAM_OFFICIAL = [
    (re.compile(r"\bperforms a (\w+) cinematic dolly push-in with (\w+) amplitude\b"),
     r"pushes in with \2 amplitude at \1 speed"),
    (re.compile(r"\bperforms a cinematic dolly push-in with (\w+) amplitude\b"),
     r"pushes in with \1 amplitude"),
    (re.compile(r"\bperforms a (\w+) dolly pull-back with (\w+) amplitude\b"),
     r"pulls out with \2 amplitude at \1 speed"),
    (re.compile(r"\bperforms a dolly pull-back with (\w+) amplitude\b"),
     r"pulls out with \1 amplitude"),
    (re.compile(r"\bperforms a (\w+) orbital arc with (\w+) amplitude\b"),
     r"arcs with \2 amplitude at \1 speed"),
    (re.compile(r"\bperforms an orbital arc with (\w+) amplitude\b"),
     r"arcs with \1 amplitude"),
]

# 残留检测(返正后应为 0;crane rise 名词形态豁免)
JARGON_LEFT = re.compile(r"\bperforms (?:a|an) (?:\w+ )?(?:cinematic dolly push-in|dolly pull-back|orbital arc)")


def rework_text(hp):
    n = 0
    for rex, fmt in CAM_OFFICIAL:
        hp, k = rex.subn(fmt, hp)
        n += k
    return hp, n


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--book", default="")
    args = ap.parse_args()

    books = sorted(os.listdir(NOVEL_ROOT)) if not args.book else [args.book]
    tot_files = tot_repl = tot_left = 0
    for book in books:
        bdir = os.path.join(NOVEL_ROOT, book)
        sbs = sorted(glob.glob(os.path.join(bdir, "素材", "分镜脚本", "*.json")))
        b_repl = b_left = 0
        for sb in sbs:
            try:
                data = json.load(io.open(sb, encoding="utf-8"))
            except Exception as e:
                print("  [skip] %s 解析失败: %s" % (os.path.basename(sb), e))
                continue
            tot_files += 1
            dirty = False
            for s in data.get("shots", []):
                hp = s.get("h3_prompt") or ""
                new, n = rework_text(hp)
                if n:
                    s["h3_prompt"] = new
                    b_repl += n
                    dirty = True
                if JARGON_LEFT.search(new):
                    b_left += 1
            if dirty and not args.dry_run:
                if not os.path.exists(sb + ".bak"):
                    io.open(sb + ".bak", "w", encoding="utf-8", newline="").write(
                        io.open(sb, encoding="utf-8").read())
                io.open(sb, "w", encoding="utf-8", newline="").write(
                    json.dumps(data, ensure_ascii=False, indent=2))
        tot_repl += b_repl
        tot_left += b_left
        print("[%s] 黑话返正 %d 处 | 残留镜 %d" % (book, b_repl, b_left))
    mode = "(dry-run,未落盘)" if args.dry_run else "(已落盘)"
    print("合计: 文件 %d | 返正 %d 处 | 残留 %d 镜 %s" % (tot_files, tot_repl, tot_left, mode))
    return 0 if tot_left == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
