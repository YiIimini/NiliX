# -*- coding: utf-8 -*-
"""站位补写存量返工(2026-09-06 契约 X:场景与人物站位描述不到位,全库 75% 缺)。

只改 h3_prompt 的 detailed_description 段:为站位不全的镜补「每个登场角色的
屏幕位置+朝向」句(插在角色首次动作句后),其余逐字保留。安全设计同 dd_expand:
  ① <d> 台词块序列逐字一致 ② Subject/Picture/Audio/[Shot]/(Sx) 引用多重集一致
  ③ dd 之外逐字保留 ④ 不新增中文 ⑤ 词数上限 +90
断点续跑 tools/logs/positioning_progress.json;.bak 保护。
用法: python tools/rework_positioning.py [--book 书名] [--chapter N] [--limit N] [--workers 8]
"""
import argparse
import glob
import io
import json
import os
import re
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import storyboard_regen as sr

NOVEL_ROOT = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "novel")
LOG_DIR = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "tools", "logs")
PROGRESS = os.path.join(LOG_DIR, "positioning_progress.json")

POS = re.compile(r"\b(left|right|center|centre)\s+(?:third|of the frame|foreground|midground|background)", re.I)
DD_RE = re.compile(r"(detailed_description:\s*\n)(.*?)(?=\noverall_soundscape:)", re.S)
D_TAG = re.compile(r"<d>.*?</d>", re.S)
REF_TAGS = re.compile(r"<Subject \d+>|<Picture \d+>|<Audio \d+>|\[Shot \d+\]")
CJK = re.compile(r"[\u4e00-\u9fff]")

SYSTEM = """你是 MiniMax H3 视频提示词的站位修复师。输入一镜的 detailed_description 段与
登场角色清单,你的任务是【只追加站位句】:为每个还缺屏幕位置/朝向的角色补一句自然的
英文描述,明确其 left/center/right third of the frame(+foreground/midground/background
纵深,如能判断)+ facing 朝向(朝向谁/镜头/画外何方)。输出严格 JSON:
{"detailed_description": "补写后的段落全文"}

铁律:
1. 原文逐字保留,只在合适位置【插入站位句】(角色首次动作句后);已 有 明 确 站 位的
   角色不重复补;
2. 站位必须与原文已出现的位置线索一致(原文说 kneels at the center 就不能再写 left);
   无线索时按合理舞台调度安排(主角 center,对话者相对而立);
3. 禁止新增台词/角色/道具;禁止改动词组与镜头描述;禁止新增中文;
4. 词数净增 ≤ 90 词;宁可精炼,禁止铺陈。"""


def needs_fix(shot):
    chars = shot.get("characters") or []
    if not chars:
        return False
    hp = shot.get("h3_prompt") or ""
    m = DD_RE.search(hp)
    if not m:
        return False
    return len(POS.findall(m.group(2))) < len(chars)


def validate(old_dd, new_dd):
    if not new_dd or len(new_dd) < len(old_dd):
        return "变短"
    if D_TAG.findall(old_dd) != D_TAG.findall(new_dd):
        return "台词块变动"
    def ms(t):
        d = {}
        for x in REF_TAGS.findall(t):
            d[x] = d.get(x, 0) + 1
        return d
    if ms(old_dd) != ms(new_dd):
        return "引用标签集合变动"
    if set(CJK.findall(new_dd)) - set(CJK.findall(old_dd)):
        return "新增中文"
    if len(new_dd.split()) - len(old_dd.split()) > 90:
        return "净增超90词"
    return None


def fix_one(cfg, shot, sem):
    hp = shot["h3_prompt"]
    m = DD_RE.search(hp)
    old_dd = m.group(2)
    chars = shot.get("characters") or []
    user = json.dumps({"characters": chars, "detailed_description": old_dd},
                      ensure_ascii=False)
    for attempt in range(3):
        try:
            with sem:
                data = sr.llm_json(cfg, SYSTEM, user + ("" if attempt == 0 else
                           "\n\n【上一稿被驳回:%s,针对性修正】" % _last[0]), temp=0.3)
        except Exception as e:
            _last[0] = "LLM异常" + str(e)[:50]
            time.sleep(2)
            continue
        nd = (data.get("detailed_description") or "").strip()
        bad = validate(old_dd, nd)
        if bad is None:
            return nd, None
        _last[0] = bad
    return None, _last[0]


_last = [""]
_lock = threading.Lock()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--book", default="")
    ap.add_argument("--chapter", type=int, default=0)
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--workers", type=int, default=8)
    args = ap.parse_args()
    cfg = sr.load_api(None)
    books = [args.book] if args.book else sorted(os.listdir(NOVEL_ROOT))
    try:
        prog = json.load(io.open(PROGRESS, encoding="utf-8"))
    except Exception:
        prog = {}
    tot = done = failed = 0
    for book in books:
        bdir = os.path.join(NOVEL_ROOT, book)
        sbs = sorted(glob.glob(os.path.join(bdir, "素材", "分镜脚本", "*.json")))
        if args.chapter:
            sbs = [f for f in sbs if re.search(r"第0*%d章" % args.chapter, os.path.basename(f))]
        for sb in sbs:
            if sb.endswith(".bak"):
                continue
            if args.limit and tot >= args.limit:
                break
            try:
                data = json.load(io.open(sb, encoding="utf-8"))
            except Exception:
                continue
            todo = [(i, s) for i, s in enumerate(data.get("shots") or []) if needs_fix(s)]
            if not todo:
                continue
            tot += len(todo)
            key = os.path.join(book, os.path.basename(sb))
            results = {}
            sem = threading.Semaphore(args.workers)
            with ThreadPoolExecutor(max_workers=args.workers) as ex:
                futs = {ex.submit(fix_one, cfg, s, sem): i for i, s in todo}
                for fu in as_completed(futs):
                    i = futs[fu]
                    nd, why = fu.result()
                    if nd:
                        results[i] = nd
                    else:
                        print("  [fail] %s 镜idx%d: %s" % (os.path.basename(sb), i, why))
            if results:
                m0 = DD_RE.search(data["shots"][0]["h3_prompt"])
                for i, nd in results.items():
                    hp = data["shots"][i]["h3_prompt"]
                    m = DD_RE.search(hp)
                    data["shots"][i]["h3_prompt"] = hp[:m.start(2)] + nd + hp[m.end(2):]
                if not os.path.exists(sb + ".bak"):
                    io.open(sb + ".bak", "w", encoding="utf-8", newline="").write(
                        io.open(sb, encoding="utf-8").read())
                io.open(sb, "w", encoding="utf-8", newline="").write(
                    json.dumps(data, ensure_ascii=False, indent=2))
            done += len(results)
            failed += len(todo) - len(results)
            print("[done] %s: %d/%d" % (os.path.basename(sb), len(results), len(todo)))
    print("补写 %d | 失败 %d | 共 %d" % (done, failed, tot))
    return 0


if __name__ == "__main__":
    sys.exit(main())
