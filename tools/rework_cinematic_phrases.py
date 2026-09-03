# -*- coding: utf-8 -*-
"""电影级措辞源头返工(2026-09-03,渲染端升级的存量分镜同步)。

两项机械替换(全库 3 本书分镜 JSON 的 h3_prompt,幂等,.bak 保护最早备份):

① 风格句写实化——与渲染端 manjuRealizeStyle 替换表完全同款(单一事实源):
    subtly anime-stylized semi-realistic characters → photorealistic cinematic characters and subjects with natural detailed textures
    subtly anime-stylized characters / anime-stylized / semi-realistic → photorealistic / realistic
  源头写对后渲染端替换幂等不命中;CINEMATOGRAPHY 电影级纪律由渲染端按写实锚注入(权威)。

② 运镜句电影化——与渲染端 manjuCameraPhrase 新短语同源(方向性/电影术语):
    the camera pushes in with {幅} amplitude at {速} speed → the camera performs a {速} cinematic dolly push-in with {幅} amplitude
    the camera pulls back with …           → the camera performs a {速} dolly pull-back with {幅} amplitude
    the camera orbits with … around        → the camera performs a {速} orbital arc with {幅} amplitude around
  pan/track/holds 等已是电影语言,不动。

用法:python tools/rework_cinematic_phrases.py [--dry-run] [--book 书名]
"""
import argparse
import glob
import io
import json
import os
import re
import sys

NOVEL_ROOT = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "novel")

# ① 风格句(与 internal/api/manju_llm.go manjuRealizeStyle 同款;顺序敏感:长词在前)
STYLE_REPL = [
    ("subtly anime-stylized semi-realistic characters",
     "photorealistic cinematic characters and subjects with natural detailed textures"),
    ("subtly anime-stylized semi-realized characters",
     "photorealistic cinematic characters and subjects with natural detailed textures"),
    ("anime-stylized semi-realistic characters",
     "photorealistic cinematic characters and subjects with natural detailed textures"),
    ("subtly anime-stylized", "photorealistic"),
    ("anime-stylized", "photorealistic"),
    ("semi-realistic", "realistic"),
]

# ② 运镜句(保留大小写引导词;后续 toward/as/to/around 等原样衔接)
CAM_REPL = [
    (re.compile(r"\b([Tt]he camera) pushes in with (\w+) amplitude at (\w+) speed\b"),
     r"\1 performs a \3 cinematic dolly push-in with \2 amplitude"),
    (re.compile(r"\b([Tt]he camera) pushes in with (\w+) amplitude\b"),
     r"\1 performs a cinematic dolly push-in with \2 amplitude"),
    (re.compile(r"\b([Tt]he camera) pulls back with (\w+) amplitude at (\w+) speed\b"),
     r"\1 performs a \3 dolly pull-back with \2 amplitude"),
    (re.compile(r"\b([Tt]he camera) pulls back with (\w+) amplitude\b"),
     r"\1 performs a dolly pull-back with \2 amplitude"),
    (re.compile(r"\b([Tt]he camera) orbits with (\w+) amplitude at (\w+) speed\b"),
     r"\1 performs a \3 orbital arc with \2 amplitude"),
    (re.compile(r"\b([Tt]he camera) orbits with (\w+) amplitude\b"),
     r"\1 performs an orbital arc with \2 amplitude"),
]


def rework_text(hp):
    style_n = cam_n = 0
    for old, new in STYLE_REPL:
        c = hp.count(old)
        if c:
            hp = hp.replace(old, new)
            style_n += c
    for rex, fmt in CAM_REPL:
        hp, n = rex.subn(fmt, hp)
        cam_n += n
    return hp, style_n, cam_n


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--book", default="")
    args = ap.parse_args()

    books = sorted(os.listdir(NOVEL_ROOT)) if not args.book else [args.book]
    tot_files = tot_style = tot_cam = 0
    for book in books:
        bdir = os.path.join(NOVEL_ROOT, book)
        sbs = sorted(glob.glob(os.path.join(bdir, "素材", "分镜脚本", "*.json")))
        b_style = b_cam = 0
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
                new, ns, nc = rework_text(hp)
                if ns or nc:
                    s["h3_prompt"] = new
                    b_style += ns
                    b_cam += nc
                    dirty = True
            if dirty and not args.dry_run:
                # .bak 保护:已存在(前次返工的最早备份)则不覆盖
                if not os.path.exists(sb + ".bak"):
                    io.open(sb + ".bak", "w", encoding="utf-8", newline="").write(
                        io.open(sb, encoding="utf-8").read())
                io.open(sb, "w", encoding="utf-8", newline="").write(
                    json.dumps(data, ensure_ascii=False, indent=2))
        tot_style += b_style
        tot_cam += b_cam
        print("[%s] 风格句写实化 %d 处 | 运镜句电影化 %d 处" % (book, b_style, b_cam))
    mode = "(dry-run,未落盘)" if args.dry_run else "(已落盘)"
    print("合计: 文件 %d | 风格句 %d | 运镜句 %d %s" % (tot_files, tot_style, tot_cam, mode))
    return 0


if __name__ == "__main__":
    sys.exit(main())
