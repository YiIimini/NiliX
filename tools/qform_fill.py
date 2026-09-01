#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""全角色 q_form 批量补写(2026-09-01 用户规则:全部角色 Q 版形象提示词技能侧统一输出):
基于每张角色卡的 image_prompt(正面定妆照)提炼 q_form——同一人同风格、chibi 短手
短脚、保留标志特征/年龄感、服装完整、纯白背景。断点续跑:已有 q_form 跳过。
用法: python qform_fill.py [--books 书1,书2] [--workers 4] [--limit N]
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

SYS = """你是漫剧角色美术师。为角色卡提炼【Q版形象提示词】(q_form 字段,内心独白/
情绪镜头渲染用,Q 版资产全角色生成)。
硬规范:①基于该角色正面定妆照 image_prompt 的风格与长相——同一人,禁止形象大变;
②chibi 短手短脚、大头呆萌比例(3 头身);③保留角色标志特征(发型/瞳色/服饰/印记/
年龄感——老人要有老相、有胡须者保留胡须、少年保持少年相);④服装完整覆盖;
⑤纯白背景;⑥物种区分:兽类=萌化小兽本体(禁人形),物品=物品本体萌化(禁人脸),
人类=人形 chibi。
输出严格 JSON: {"id": "角色名", "q_form": "英文Q版提示词"}"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--books", help="逗号分隔书名(默认全部)")
    ap.add_argument("--workers", type=int, default=4)
    ap.add_argument("--limit", type=int, default=0)
    args = ap.parse_args()
    books = [b.strip() for b in (args.books or "").split(",") if b.strip()]
    if not books:
        books = sorted(d for d in os.listdir(ROOT) if os.path.isdir(os.path.join(ROOT, d)))
    cfg = sr.load_api()
    done = fail = skip = 0
    for b in books:
        cp = os.path.join(ROOT, b, "素材", "人物生成提示词.json")
        if not os.path.exists(cp):
            continue
        cards = json.load(open(cp, encoding="utf-8"))
        todo = [c for c in cards if not (c.get("q_form") or "").strip()]
        if args.limit:
            todo = todo[: args.limit]
        if not todo:
            print(f"{b}: 全部已有 q_form ✓")
            continue
        print(f"{b}: 缺 q_form {len(todo)} 张,开始补写...")

        def work(c):
            ctx = json.dumps({k: v for k, v in c.items() if k != "q_form"},
                             ensure_ascii=False)[:800]
            user = f"【角色卡】{ctx}\n\n【输出 JSON】{{\"id\": \"{c['id']}\", \"q_form\": \"\"}}"
            try:
                out = sr.llm_json(cfg, SYS, user, temp=0.4)
                qf = (out.get("q_form") or "").strip()
                if len(qf) < 50:
                    raise RuntimeError("提示词过短")
                return ("ok", c["id"], qf)
            except Exception as e:
                return ("fail", c["id"], str(e)[:90])

        with ThreadPoolExecutor(max_workers=args.workers) as ex:
            for fut in as_completed([ex.submit(work, c) for c in todo]):
                kind, cid, data = fut.result()
                if kind == "ok":
                    for c in cards:
                        if c["id"] == cid:
                            c["q_form"] = data
                            break
                    done += 1
                else:
                    fail += 1
                    print(f"  ✗ {cid}: {data}")
        json.dump(cards, open(cp, "w", encoding="utf-8", newline="\n"),
                  ensure_ascii=False, indent=1)
        print(f"  {b}: 补 {done - skip} 张")
    print(f"完成: 补写 {done} / 失败 {fail}")


if __name__ == "__main__":
    main()
