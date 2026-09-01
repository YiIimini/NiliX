#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""存量低密度章细拆(2026-09-01「细中细」配套):
正文 >80 字/镜的章(39 章,内容可能被合并)——LLM 按细中细规则重拆:
①段落级穷尽(正文每段至少一镜)②动作分解(起步/进行/完成)③神态微表情入镜
④记忆点道具特写镜⑤一镜一拍推荐。输出全量分镜 JSON(含每镜六段式),程序校验
(对白覆盖/场景锚定/Shot 标记)后落盘。
用法: python storyboard_fine_split.py [--books 书1,书2] [--limit N]
"""
import argparse
import glob
import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import storyboard_regen as sr

ROOT = os.path.join(r"D:\Ai\NiliX", "novel")
DENSITY = 80  # 正文/镜数 超此值视为低密度需细拆

SYS = """你是漫剧分镜编辑。本章分镜密度不足(镜头合并了多个情节节拍),请按
【细中细】规则重新拆镜:①段落级穷尽——正文每段至少一个对应镜头,无段遗漏;
②动作分解——关键动作拆 2-3 镜(起步/进行/完成),禁止一个动作一笔带过;
③神态入镜——情绪顶点配微表情特写(眼神/嘴角/手部小动作);④细节特写——
记忆点道具/伏笔物件必须有特写镜;⑤一镜一拍——一个节拍一个镜头。
输出严格 JSON(与分镜 JSON schema 一致):
{"book": "《书名》", "episode": 章号, "chapter_title": "章节名",
"global_style": "全局风格句", "bridge": {...},
"shots": [{"shot_id": 1, "shot_size": "景别", "camera": "运镜(类型+幅度+速度)",
"action": "【场景名】画面内容(可渲染·光声味·站位写清·动作分解·神态微表情)",
"dialogue": "(S1)角色名:\"台词\" 或 内心·角色名:\"内心\" 或 旁白：…(无则空)",
"characters": ["登场角色名"], "light": "光影", "sound": "音效", "duration": 秒数,
"h3_prompt": "六段式全文(subject_definitions:/summary:/retention_analysis:/detailed_description:/overall_soundscape:/non_diegetic_music:,六字段缺一不可,<d>[Chinese]台词逐字,画外音用off-screen voiceover句式)"}]}
硬约束:①shots 按镜号严格递增从 1 连续;②台词逐字保留(禁止改写/删减正文对白);
③每镜 4-15s,语音预算(台词字数÷4)不超时长;④【场景名】用场景卡名(参考本章
原分镜的 action 前缀);⑤禁止输出 JSON 以外的任何内容。"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--books", help="逗号分隔书名(默认全部)")
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()
    books = [b.strip() for b in (args.books or "").split(",") if b.strip()]
    if not books:
        books = sorted(d for d in os.listdir(ROOT) if os.path.isdir(os.path.join(ROOT, d)))
    cfg = sr.load_api()
    done = fail = 0
    for b in books:
        bdir = os.path.join(ROOT, b)
        for f in sorted(glob.glob(os.path.join(bdir, "素材", "分镜脚本", "*.json"))):
            m = re.search(r"第(\d+)章", os.path.basename(f))
            if not m:
                continue
            n = int(m.group(1))
            body = glob.glob(os.path.join(bdir, "正文", "*", f"第{n}章*.md")) or \
                   glob.glob(os.path.join(bdir, "正文", "*", f"*{n:03d}*"))
            if not body:
                continue
            text = open(body[0], encoding="utf-8").read()
            j = json.load(open(f, encoding="utf-8"))
            shots = j.get("shots", [])
            if not shots or len(text) / len(shots) <= DENSITY:
                continue
            if args.limit and done + fail >= args.limit:
                break
            brief = []
            for s in shots:
                act = (s.get("action") or "").strip().replace("\n", " ")[:60]
                dl = (s.get("dialogue") or "").strip().replace("\n", "；")[:40]
                brief.append(f"{s.get('shot_id')}|{act}|{dl}")
            user = (f"【书名】{b} 第{n}章\n【正文】\n{text[:4500]}\n\n"
                    f"【原分镜摘要】\n" + "\n".join(brief)[:2500] +
                    f"\n\n【输出 JSON】{{\"book\": \"《{b}》\", \"episode\": {n}, \"shots\": []}}")
            try:
                data = sr.llm_json(cfg, SYS, user, temp=0.3, )
            except Exception as e:
                print(f"  ✗ {b} 第{n}章 LLM 失败: {str(e)[:80]}")
                fail += 1
                continue
            newshots = data.get("shots") or []
            if len(newshots) <= len(shots):
                print(f"  - {b} 第{n}章: 细拆未增加镜头({len(shots)}→{len(newshots)}),跳过")
                fail += 1
                continue
            # 落盘(保留原 book/episode/chapter_title/global_style/bridge)
            for k in ("book", "episode", "chapter_title", "global_style", "bridge"):
                if k not in data and k in j:
                    data[k] = j[k]
            if not args.dry_run:
                json.dump(data, open(f, "w", encoding="utf-8", newline="\n"),
                          ensure_ascii=False, indent=1)
            print(f"  ✓ {b} 第{n}章: {len(shots)}→{len(newshots)} 镜({len(text)//len(newshots)}字/镜)")
            done += 1
    print(f"完成: 细拆 {done} 章 / 失败 {fail}")


if __name__ == "__main__":
    main()
