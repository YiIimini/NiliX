#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""缺卡角色 LLM 批量补群演卡(2026-09-01 主次混乱/无参考图根治配套):
分镜 characters 声明/说话人不在角色卡的角色(变体归一后仍缺,全库 439 角色/
1733 处)——无参考图 → H3 自由发挥 → 形象漂移/与主角混淆。从该角色首次出现镜的
六段式 subject 描述提炼英文生图提示词建卡,追加进人物生成提示词.json。
断点续跑:已存在跳过。用法: python char_fill.py [--books 书1,书2] [--min-n 3]
"""
import argparse
import glob
import json
import os
import re
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import storyboard_regen as sr

ROOT = os.path.join(r"D:\Ai\NiliX", "novel")

SYS = """你是漫剧角色美术师。分镜脚本里有角色没有角色卡(无参考图,渲染会形象漂移),
请为该角色建一张群演卡。素材:该角色在分镜六段式里的主体描述(英文)。提炼英文
生图提示词(定妆用):人物以 a N-year-old male/female Chinese ... 开头(年龄性别从
描述推断),写外貌/发型/服装/神态,禁止其他人物与场景叙事,纯白背景,写实电影级。
输出严格 JSON: {"id": "角色名", "role": "群演", "age": "年龄段或空",
"appearance": "中文外貌", "costume": "中文服装", "image_prompt": "英文提示词"}
禁止输出 JSON 以外内容。"""


def resolve(name, ids):
    if name in ids:
        return name
    base = name.split("·")[-1].strip()
    if base in ids:
        return base
    best, bl = "", 0
    for c in ids:
        n = len(c)
        if n >= 2 and len(base) >= 2 and (c.endswith(base) or base.endswith(c) or (n >= len(base) and base in c)):
            if best == "" or n < bl:
                best, bl = c, n
    return best


def collect_missing(book_dir, ids):
    """缺卡角色 → 该角色首次出现镜的 subject 描述"""
    miss = {}
    for f in sorted(glob.glob(os.path.join(book_dir, "素材", "分镜脚本", "*.json"))):
        j = json.load(open(f, encoding="utf-8"))
        for s in j.get("shots", []):
            hp = s.get("h3_prompt", "") or ""
            names = []
            for c in s.get("characters") or []:
                c2 = (c or "").strip()
                if c2 and not c2.startswith(("旁白", "画外", "内心", "群杂")):
                    names.append(c2)
            for ln in (s.get("dialogue", "") or "").splitlines():
                m = re.match(r"^\(([^)]*)\)\s*([^:：]{1,10})[:：]", ln)
                if m:
                    names.append(m.group(2).strip())
            for n in names:
                if "画外" in n or "（画外" in n or "(画外" in n:
                    continue  # 画外说话者不入画,不需要定妆卡
                if resolve(n, ids):
                    continue
                if n.startswith(("旁白", "画外", "内心", "群杂")):
                    continue
                miss.setdefault(n, {"n": 0, "subj": "", "act": ""})
                miss[n]["n"] += 1
                if not miss[n]["subj"]:
                    miss[n]["subj"] = find_subject_for(hp, s, n)
                    miss[n]["act"] = (s.get("action") or "").strip().replace("\n", " ")[:80]
    return miss


def find_subject_for(hp, s, name):
    """按台词 (Sx) 号定位该角色的 subject 定义段(2026-09-01:首个 subject 常是
    主角,取错会串形象;「大黄」的 (S3) → 找含 (S3) 的 subject 段)"""
    sx = ""
    for ln in (s.get("dialogue", "") or "").splitlines():
        m = re.match(r"^\(([^)]*)\)\s*([^:：]{1,10})[:：]", ln)
        if m and (m.group(2).strip() == name or m.group(2).strip().split("·")[-1].strip() == name):
            sx = m.group(1).strip()
            break
    if sx:
        # (Sx) 定位:定义段 = 行首 <Subject N> is ... 直到下一个行首 <Subject/summary
        # (Audio 行在同一段内,含 (Sx));「大黄」的 (S2) 在 <Subject 2> is Da Huang 段
        # 注意 sx 已含 'S' 前缀,正则不能再拼 S(曾拼成 (SS2) 全部失配)
        for mm in re.finditer(r"(?m)^<Subject (\d+)> is (.+?)(?=^<Subject \d+> is|^summary:)", hp, re.S):
            seg = mm.group(2)
            if re.search(r"\(" + re.escape(sx) + r"\)", seg):
                seg = re.sub(r"in <Picture \d+>|\(S\d+\)[^,]*|, with a .*? voice[^,]*|, containing a spoken voiceover|; <Audio \d+> is the voice-timbre reference for <Subject \d+>", "", seg)
                seg = re.sub(r"\s+", " ", seg).strip(" ,;")
                if len(seg) > 8:
                    return seg[:400]
    # 退回:含角色名的 subject 段
    for m2 in re.finditer(r"<Subject \d+> is (.+?)(?=\n<Subject|\n<Audio|$)", hp, re.S):
        seg = re.sub(r"in <Picture \d+>|\(S\d+\)[^,]*|, with a .*? voice[^,]*|, containing a spoken voiceover|; <Audio \d+> is the voice-timbre reference for <Subject \d+>", "", m2.group(1))
        seg = re.sub(r"\s+", " ", seg).strip(" ,;")
        if name in seg or name.split("·")[-1].strip() in seg:
            return seg[:400]
    # 最后:第一个 subject 段
    m2 = re.search(r"<Subject \d+> is (.+?)(?=\n<Subject|\n<Audio|$)", hp, re.S)
    if m2:
        seg = re.sub(r"in <Picture \d+>|\(S\d+\)[^,]*|, with a .*? voice[^,]*|, containing a spoken voiceover|; <Audio \d+> is the voice-timbre reference for <Subject \d+>", "", m2.group(1))
        seg = re.sub(r"\s+", " ", seg).strip(" ,;")
        return seg[:400]
    return ""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--books", help="逗号分隔书名(默认全部)")
    ap.add_argument("--workers", type=int, default=4)
    ap.add_argument("--min-n", type=int, default=2, help="引用次数低于此值跳过(龙套不补)")
    ap.add_argument("--limit", type=int, default=0)
    args = ap.parse_args()
    books = [b.strip() for b in (args.books or "").split(",") if b.strip()]
    if not books:
        books = sorted(d for d in os.listdir(ROOT) if os.path.isdir(os.path.join(ROOT, d)))
    cfg = sr.load_api()
    done = fail = skip = 0
    for b in books:
        bdir = os.path.join(ROOT, b)
        cp = os.path.join(bdir, "素材", "人物生成提示词.json")
        if not os.path.exists(cp):
            continue
        cards = json.load(open(cp, encoding="utf-8"))
        ids = [c["id"] for c in cards]
        miss = collect_missing(bdir, ids)
        miss = {n: v for n, v in miss.items() if v["n"] >= args.min_n}
        if args.limit:
            miss = dict(list(miss.items())[: args.limit])
        if not miss:
            print(f"{b}: 无缺卡 ✓")
            continue
        print(f"{b}: 缺卡 {len(miss)} 角色(按频次 {sum(v['n'] for v in miss.values())} 处),开始补卡...")

        def work(item):
            name, v = item
            act = f"\n【画面上下文】{v['act']}" if v.get("act") else ""
            user = f"【角色名】{name}\n【主体描述】{v['subj'] or '(无,按常见群演推断)'}{act}\n\n【输出 JSON】{{\"id\": \"{name}\", \"role\": \"群演\", \"age\": \"\", \"appearance\": \"\", \"costume\": \"\", \"image_prompt\": \"\"}}"
            try:
                out = sr.llm_json(cfg, SYS, user, temp=0.4)
                ip = (out.get("image_prompt") or "").strip()
                if len(ip) < 50:
                    raise RuntimeError("提示词过短")
                out["id"] = name
                out.setdefault("role", "群演")
                return ("ok", name, out)
            except Exception as e:
                return ("fail", name, str(e)[:100])

        with ThreadPoolExecutor(max_workers=args.workers) as ex:
            for fut in as_completed([ex.submit(work, it) for it in miss.items()]):
                kind, name, data = fut.result()
                if kind == "ok":
                    cards.append(data)
                    done += 1
                    print(f"  ✓ {name} (×{miss[name]['n']})")
                else:
                    fail += 1
                    print(f"  ✗ {name}: {data}")
        json.dump(cards, open(cp, "w", encoding="utf-8", newline="\n"),
                  ensure_ascii=False, indent=1)
    print(f"完成: 补卡 {done} / 失败 {fail} / 跳过 {skip}")


if __name__ == "__main__":
    main()
