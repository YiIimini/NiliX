# -*- coding: utf-8 -*-
"""内心独白源头返工(2026-09-03,渲染漂移根治的源头侧同步升级)。

背景:分镜师技能生成的 h3_prompt 把内心独白镜(「内心·角色名(情绪):"…"」)的画面段
写成 "The narrator says in an off-screen voiceover, in a {情绪} inward voice: <d>…</d>"——
角色内心话被旁白叙述者音色念出,是用户感知「镜头内容跑偏」的来源之一。渲染端
fixInnerVoiceDefLine 已兜底存量;本工具把源头分镜 JSON 一并返工为角色内心音写法:

    The narrator says in an off-screen voiceover…
        → The quiet inner voice of {角色id} says in an off-screen voiceover…

- 只处理内心独白镜(narration/says/dialogue 含「内心·」);客观旁白镜的 narrator
  句式语义正确,一概不动。
- 幂等:改写后句式不再命中;重复运行零改动。
- 备份:首次改动前存 .bak(同目录);--dry-run 只统计不落盘。

用法:
    python tools/rework_inner_voice.py                # 全库返工
    python tools/rework_inner_voice.py --dry-run      # 只统计
    python tools/rework_inner_voice.py --book 书名    # 只返工某本书
"""
import argparse
import glob
import io
import json
import os
import re
import sys

NOVEL_ROOT = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "novel")

# 内心独白镜判定 + 角色id提取:「内心·小满(愁):"…"」/「内心·阿影: …」
RE_INNER = re.compile(r"内心·([^（(：:，,\n]{1,12})[（(]?[^：:\n]*[）)]?[：:]")
# narrator 画外句主语:主语后 140 字符内(不跨句)出现 off-screen/inward/voiceover 锚词
# 才改写——覆盖 says/adds/speaks 及 "in an sorrowful ... inward off-screen voiceover"
# 等语序变体;summary 里的 "narrated by" 类描述不命中。re.I 容忍 The/the。
RE_NARR_SUBJ = re.compile(
    r"\b([Tt]he) narrator\b(?=[^.:]{0,140}?(?:off-?screen|inward|voiceover))",
    re.I | re.S)


def shot_is_inner(shot):
    for k in ("narration", "says", "dialogue"):
        v = shot.get(k) or ""
        if "内心·" in v:
            m = RE_INNER.search(v)
            if m:
                return m.group(1).strip()
    return None


def rework_text(hp, cid):
    """返回(新文本, 改写次数)。只换 narrator 主语为内心音,情绪/闭唇措辞原样保留。"""
    n = 0

    def sub_says(m):
        nonlocal n
        n += 1
        lead = "The" if m.group(1).istitle() else "the"
        return "%s quiet inner voice of %s" % (lead, cid)

    hp = RE_NARR_SUBJ.sub(sub_says, hp)
    return hp, n


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--book", default="")
    args = ap.parse_args()

    books = sorted(os.listdir(NOVEL_ROOT)) if not args.book else [args.book]
    total_files = total_shots = total_inner = total_reworked = 0
    for book in books:
        bdir = os.path.join(NOVEL_ROOT, book)
        sbs = sorted(glob.glob(os.path.join(bdir, "素材", "分镜脚本", "*.json")))
        b_inner = b_rw = 0
        for sb in sbs:
            try:
                data = json.load(io.open(sb, encoding="utf-8"))
            except Exception as e:
                print("  [skip] %s 解析失败: %s" % (os.path.basename(sb), e))
                continue
            total_files += 1
            shots = data.get("shots", [])
            file_dirty = False
            for s in shots:
                total_shots += 1
                cid = shot_is_inner(s)
                if not cid:
                    continue
                total_inner += 1
                b_inner += 1
                hp = s.get("h3_prompt") or ""
                new_hp, n = rework_text(hp, cid)
                if n > 0:
                    s["h3_prompt"] = new_hp
                    total_reworked += n
                    b_rw += n
                    file_dirty = True
            if file_dirty and not args.dry_run:
                io.open(sb + ".bak", "w", encoding="utf-8", newline="").write(
                    io.open(sb, encoding="utf-8").read())
                io.open(sb, "w", encoding="utf-8", newline="").write(
                    json.dumps(data, ensure_ascii=False, indent=2))
        print("[%s] 分镜文件 %d | 总镜 %s | 内心独白镜 %d | narrator 句改写 %d" % (
            book, len(sbs), sum(len(json.load(io.open(f, encoding='utf-8')).get('shots', [])) for f in sbs) if False else "-",
            b_inner, b_rw))
    mode = "(dry-run,未落盘)" if args.dry_run else "(已落盘,.bak 备份)"
    print("合计: 文件 %d | 镜 %d | 内心独白镜 %d | narrator→内心音改写 %d 处 %s" % (
        total_files, total_shots, total_inner, total_reworked, mode))
    return 0


if __name__ == "__main__":
    sys.exit(main())
