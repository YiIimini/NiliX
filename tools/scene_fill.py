#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""缺失场景补卡工具(2026-09-01 场景渲染趋同根治的第二层):
分镜脚本引用了场景卡里没有的场景(406 种)——无场景图镜 H3 自由发挥导致趋同。
从分镜脚本收集每个缺失场景的画面列+六段式场景描述,LLM 生成英文场景提示词,
追加进场景提示词.json。断点续跑:已存在的场景跳过。

用法: python tools/scene_fill.py [--books 书1,书2] [--workers 4] [--limit N]
"""
import argparse
import glob
import json
import os
import re
import sys
import threading
from concurrent.futures import ThreadPoolExecutor, as_completed

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import storyboard_regen as sr

BOOKS = ["人算不如天算，天算不如算盘", "全小区就我一个活人", "废铁按斤卖，雷劫排队充",
         "我的影子会咬人", "轮回欠费九世", "金丹一万重", "杂毛神兽"]

SYSTEM = """你是漫剧场景美术师。基于分镜脚本对某场景的多次描述,提炼该场景的
【英文生图提示词】(用于场景参考图定妆)。输出严格 JSON:
{"id": "场景名", "description": "中文一句话用途", "image_prompt": "英文场景提示词"}

英文提示词规范:场景主体/空间结构/光线色调/氛围(100-160 英文词);
只写场景与环境,禁止任何人物/角色/人群/个体存在(禁止 person/people/crowd/
disciples/students/figures/man/woman 等一切人物词汇,场景必须空无一人);
禁止台词/镜头语言(推拉摇移/特写等);
以场景名能对应的实体描述开头(如 an ancient palace exterior staircase);
逗号分隔的自然英文,末尾可附质量词(photorealistic, ultra detailed, 8k);
禁止输出 JSON 以外的任何内容。"""

CLEAN_SYSTEM = """你是漫剧场景美术师。把英文场景提示词里的所有人物元素删除,
只保留纯环境(建筑/地形/植被/光线/氛围)。输出严格 JSON:
{"id": "场景名", "description": "不变", "image_prompt": "清洗后英文提示词"}
保持提示词连贯自然,逗号分隔,长度 80-150 英文词。禁止输出 JSON 以外内容。"""

PERSON_RE = re.compile(
    r"(?<!no )(?<!without )(?<!devoid of )(?<!empty of )(?<!free of )"
    r"\b(?:person|people|crowd|disciples|students|figures|figure|man|men|woman|women|"
    r"children|onlookers|spectators|audience|warriors|guards|cultivators|"
    r"a man|a woman|someone|pedestrians|bystanders|servants|attendants)\b",
    re.I)


def collect_scene_shots(book_dir):
    """分镜脚本:场景名 → 该场景镜的画面列+六段式场景句"""
    out = {}
    for f in glob.glob(os.path.join(book_dir, "素材", "分镜脚本", "*.json")):
        try:
            j = json.load(open(f, encoding="utf-8"))
        except Exception:
            continue
        for s in j.get("shots", []):
            m = re.match(r"【([^】]+)】", s.get("action", "") or "")
            if not m:
                continue
            name = m.group(1).strip()
            seg = s.get("action", "")[:120]
            hp = s.get("h3_prompt", "") or ""
            dd = hp.split("detailed_description:", 1)[-1].split("overall_soundscape:", 1)[0] if "detailed_description:" in hp else ""
            # 场景句:detailed_description 前 200 词(去掉 [Shot N] 与人物动作段)
            sc = dd[:260]
            out.setdefault(name, []).append((seg, sc))
    return out


def gen_scene(cfg, name, samples):
    """LLM 生成场景卡"""
    ctx_txt = "\n".join(
        f"- 画面列: {a[:90]}\n- 六段式场景句: {d[:150]}" for a, d in samples[:6])
    user = f"【场景名】{name}\n【分镜脚本中的该场景描述】\n{ctx_txt}\n\n【输出 JSON】{{\"id\": \"{name}\", \"description\": \"\", \"image_prompt\": \"\"}}"
    data = sr.llm_json(cfg, SYSTEM, user, temp=0.4)
    ip = (data.get("image_prompt") or "").strip()
    if len(ip) < 40:
        raise RuntimeError("场景提示词过短")
    data["id"] = name
    if not data.get("description"):
        data["description"] = ""
    return data


def clean_person_words(cfg, data):
    """image_prompt 含人物词 → LLM 清洗为纯环境"""
    ip = data.get("image_prompt", "")
    if not PERSON_RE.search(ip):
        return data, False
    user = f"【原始提示词】\n{ip}\n\n【输出 JSON】{{\"id\": \"{data['id']}\", \"description\": \"\", \"image_prompt\": \"\"}}"
    try:
        out = sr.llm_json(cfg, CLEAN_SYSTEM, user, temp=0.3)
        if len(out.get("image_prompt", "")) >= 40:
            out["id"] = data["id"]
            out.setdefault("description", data.get("description", ""))
            return out, True
    except Exception:
        pass
    return data, False


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--books", help="逗号分隔书名(默认全部)")
    ap.add_argument("--workers", type=int, default=4)
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--resume", action="store_true", help="跳过已存在场景")
    args = ap.parse_args()
    books = [b.strip() for b in (args.books or "").split(",") if b.strip()] or BOOKS
    cfg = sr.load_api()
    done = fail = skip = cleaned = 0
    for b in books:
        book_dir = os.path.join(sr.ROOT, "novel", b)
        cards_path = os.path.join(book_dir, "素材", "场景提示词.json")
        if not os.path.exists(cards_path):
            print(f"跳过: 无场景卡 {b}")
            continue
        cards = json.load(open(cards_path, encoding="utf-8"))
        have = {c.get("id") for c in cards}
        shots = collect_scene_shots(book_dir)
        missing = [(n, v) for n, v in shots.items() if n not in have]
        if args.limit:
            missing = missing[:args.limit]
        if not missing:
            print(f"{b}: 无缺失场景 ✓")
            continue
        print(f"{b}: 缺失 {len(missing)} 场景,开始补卡...")

        def work(item):
            name, samples = item
            if args.resume and name in have:
                return ("skip", name, None, False)
            try:
                data = gen_scene(cfg, name, samples)
                data, cleaned = clean_person_words(cfg, data)
                return ("ok", name, data, cleaned)
            except Exception as e:
                return ("fail", name, str(e)[:120], False)

        with ThreadPoolExecutor(max_workers=args.workers) as ex:
            for fut in as_completed([ex.submit(work, it) for it in missing]):
                kind, name, data, cln = fut.result()
                if kind == "skip":
                    skip += 1
                elif kind == "ok":
                    cards.append(data)
                    done += 1
                    if cln:
                        cleaned += 1
                    print(f"  ✓ {name} ({len(data.get('image_prompt',''))} 字)"
                          + (" [清洗过人物]" if cln else ""))
                else:
                    fail += 1
                    print(f"  ✗ {name}: {data}")
        json.dump(cards, open(cards_path, "w", encoding="utf-8", newline="\n"),
                  ensure_ascii=False, indent=1)
    print(f"完成: 补卡 {done} / 清洗 {cleaned} / 跳过 {skip} / 失败 {fail}")


if __name__ == "__main__":
    main()
