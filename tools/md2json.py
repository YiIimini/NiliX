#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""分镜脚本 md → JSON 转换器(2026-08-31 技能侧新格式)。

背景:md 表格格式脆弱——LLM 在画面/台词列写入换行时表格断行,
Go 解析器 reScriptTableRow 丢镜(实测 EP01 25 镜解析成 21 镜,541 行异常)。
JSON 结构化输出彻底消除断行问题。

转换逻辑:
1. 分镜表按行扫描:以 `| N |` 开头的行,若列数 < 8 且后续行不以 `|` 开头,
   把后续行内容合并回该行(修复断列)直到列数达标或遇到新表行;
2. 六段式代码块 ### Shot N 原样提取;
3. 组装 JSON(shots[]: shot_id/shot_size/camera/action/dialogue/light/sound/duration/style/h3_prompt;
   dialogue 多行用 \n 连接,保留 内心·/旁白 前缀);
4. 输出同名 .json,删除原 .md(旧格式弃用)。

用法: python tools/md2json.py [--books 书1,书2] [--dry-run]
"""
import argparse
import glob
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BOOKS = ["人算不如天算，天算不如算盘", "全小区就我一个活人", "废铁按斤卖，雷劫排队充",
         "我的影子会咬人", "轮回欠费九世", "金丹一万重", "杂毛神兽"]

RE_ROW = re.compile(r"^\|\s*(\d+)\s*\|")
RE_DUR = re.compile(r"(\d+(?:\.\d+)?)\s*s")


def parse_table(lines):
    """分镜表 → [{shot_id, shot_size, camera, action, dialogue, light, sound, duration, style}]"""
    shots = []
    i = 0
    while i < len(lines):
        ln = lines[i]
        if not RE_ROW.match(ln):
            i += 1
            continue
        # 收集该行(处理断列:后续非 | 开头的行并入)
        buf = ln
        j = i + 1
        while j < len(lines):
            nxt = lines[j]
            if nxt.startswith("|"):
                break
            buf += "\n" + nxt
            j += 1
        i = j
        cells = [c.strip() for c in buf.strip("|").split("|")]
        if len(cells) < 5:
            continue
        sid = int(cells[0])
        # 列映射:8列=镜号|景别|运镜|画面|台词|光影|音效|时长;9列=…|风格|时长
        def cell(n):
            return cells[n] if n < len(cells) else ""
        if len(cells) >= 8 and RE_DUR.search(cells[-1]):
            shot_size, camera, action = cell(1), cell(2), cell(3)
            dialogue, light, sound = cell(4), cell(5), cell(6)
            style, duration = "", cell(7)
        else:
            shot_size, camera, action = cell(1), cell(2), cell(3)
            dialogue, light, sound = cell(4), cell(5), cell(6)
            style, duration = "", cell(7)
        # 9 列检测:末列为时长,倒数第 2 列非时长(风格列)
        if len(cells) >= 9 and RE_DUR.search(cells[-1]) and not RE_DUR.search(cells[-2]):
            style = cell(7)
            duration = cell(8)
        m = RE_DUR.search(duration)
        dur = int(float(m.group(1))) if m else 5
        shots.append({
            "shot_id": sid,
            "shot_size": shot_size, "camera": camera, "action": action,
            "dialogue": dialogue, "light": light, "sound": sound,
            "style": style, "duration": dur,
        })
    return shots


def parse_prompts(text):
    """### Shot N 六段式代码块 → {n: h3_prompt}"""
    out = {}
    for m in re.finditer(r"###\s*Shot\s+(\d+)[^\n]*\n```[^\n]*\n(.*?)\n```\s*", text, re.S):
        out[int(m.group(1))] = m.group(2).strip()
    return out


def convert(md_path, dry_run=False):
    text = open(md_path, encoding="utf-8").read()
    lines = text.splitlines()
    shots = parse_table(lines)
    prompts = parse_prompts(text)
    if not shots:
        print(f"  ✗ 无分镜表: {md_path}")
        return False
    # 六段式挂载
    missing = 0
    for s in shots:
        s["h3_prompt"] = prompts.get(s["shot_id"], "")
        if not s["h3_prompt"]:
            missing += 1
        # 去掉空字段
        for k in ("style", "light", "sound"):
            if not s.get(k):
                s.pop(k, None)
        if s.get("dialogue") == "无" or not s.get("dialogue"):
            s.pop("dialogue", None)
    # 头部元数据
    head = {}
    m = re.search(r"# 《(.+?)》分镜脚本\s*·\s*第(\d+)章_([^（(]+)", text)
    if m:
        head = {"book": "《%s》" % m.group(1), "episode": int(m.group(2)),
                "chapter_title": m.group(3).strip()}
    m = re.search(r"全局风格句[:：]\s*(.+)", text)
    if m:
        head["global_style"] = m.group(1).strip()
    bridge = {}
    for key, label in (("prev_ending", "上一章收尾"), ("opening_beat", "本章开场承接"),
                       ("position", "本章在全书的推进位"), ("closing_hook", "本章章尾钩子"),
                       ("next_entry", "下一章入口")):
        m = re.search(r">\s*-\s*" + label + r"[:：]\s*(.+)", text)
        if m:
            bridge[key] = m.group(1).strip()
    if bridge:
        head["bridge"] = bridge
    data = dict(head)
    data["shots"] = shots
    out_path = re.sub(r"\.md$", ".json", md_path)
    with open(out_path, "w", encoding="utf-8", newline="\n") as f:
        json.dump(data, f, ensure_ascii=False, indent=1)
    if not dry_run:
        os.remove(md_path)
    print(f"  ✓ {os.path.basename(md_path)} → {os.path.basename(out_path)} "
          f"({len(shots)} 镜{'，缺六段式 %d' % missing if missing else ''})")
    return True


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--books", help="逗号分隔书名(默认全部)")
    ap.add_argument("--dry-run", action="store_true", help="只转换不删 md")
    args = ap.parse_args()
    books = [b.strip() for b in (args.books or "").split(",") if b.strip()] or BOOKS
    total = ok = 0
    for b in books:
        files = sorted(glob.glob(os.path.join(ROOT, "novel", b, "素材", "分镜脚本", "*.md")))
        for f in files:
            total += 1
            if convert(f, args.dry_run):
                ok += 1
    print(f"完成: {ok}/{total} 转换成功")
    return 0 if ok == total else 1


if __name__ == "__main__":
    sys.exit(main())


# ---------------- 素材卡 md → JSON(2026-08-31 角色/场景提示词 JSON 化) ----------------

GLOBAL_SECS = ("统一风格", "全局", "通用", "负向", "风格说明", "质量")


def convert_char_cards(md_path, dry_run=False):
    """人物生成提示词.md → .json(角色数组:id/gender/age/role/species/voice/
    memories/appearance/image_prompt/q_form/second_form)"""
    text = open(md_path, encoding="utf-8").read()
    cards, cur = [], None
    lines = text.splitlines()
    i = 0
    while i < len(lines):
        line = lines[i]
        m = re.match(r"^##\s*(\d+(?:\.\d+)?)[.、．]?\s*(.*?)\s*$", line.strip())
        if m:
            head = m.group(2)
            if any(g in head for g in GLOBAL_SECS):
                cur = None
                i += 1
                continue
            # 名字(描述):括号前=名字(可能带身份词·前缀),括号内=描述。
            # 注意:species 描述里也可能含「·」(如 影灵·影子精灵),必须先按括号切,
            # 再在括号前找身份词分隔符——顺序反了会把 species 当名字
            mm2 = re.match(r"^([^（(]+?)(?:[（(](.*?)[）)])?$", head)
            name = mm2.group(1).strip() if mm2 else head.strip()
            desc = mm2.group(2).strip() if mm2 and mm2.group(2) else ""
            role_word = ""
            if "·" in name:
                rw, nm = name.rsplit("·", 1)
                if nm.strip():
                    role_word, name = rw.strip(), nm.strip()
            if not name:
                cur = None
                i += 1
                continue
            cur = {"id": name, "appearance": desc}
            # 身份词 → role/species(身份词可能在名字前「主角 · 顾烬」,也可能在
            # 描述首段「(女主·现代注会…)」「(人类·男,…)」——从全串提取)
            blob = role_word + " " + desc
            for w in ("灵宠", "萌宠", "妖兽", "神兽", "精怪", "鬼物", "机械", "灵植", "器灵", "影灵", "精灵"):
                if w in blob:
                    cur["species"] = w
                    break
            if "species" not in cur:
                for w in ("女主", "主角", "反派", "正派", "助攻", "群演", "配角"):
                    if w in blob:
                        cur["role"] = w
                        break
            # gender/age 从括号描述提取
            gm = re.search(r"(男|女)", desc)
            if gm:
                cur["gender"] = gm.group(1)
            am = re.search(r"(\d+)\s*岁|(老年|中年|青年|少年|儿童|幼年|孩童)", desc)
            if am:
                cur["age"] = (am.group(1) + "岁") if am.group(1) else am.group(2)
            cards.append(cur)
            i += 1
            # 收集该卡内容(到下一个 ## 为止):代码块按形态标记分配——
            # 【Q版/Q 版】→ q_form;【真身/化形/第二形态】→ second_form;
            # 无标记/【主形象】→ image_prompt(首个非标记块)
            cur_mark = ""
            while i < len(lines) and not re.match(r"^##\s*\d", lines[i].strip()):
                ln = lines[i].strip()
                if ln.startswith("```"):
                    blk = []
                    i += 1
                    while i < len(lines) and not lines[i].strip().startswith("```"):
                        blk.append(lines[i])
                        i += 1
                    i += 1
                    if blk and cur is not None:
                        content = "\n".join(blk).strip()
                        if "Q版" in cur_mark or "Q 版" in cur_mark:
                            cur["q_form"] = content
                        elif "真身" in cur_mark or "化形" in cur_mark or "第二形态" in cur_mark:
                            cur["second_form"] = content
                        else:
                            cur.setdefault("image_prompt", content)  # 主形象=首个非标记块
                    cur_mark = ""
                    continue
                # 形态标记行(**【Q版·…】** / **【主形象·…】** / **化形提示词…**)
                if ("Q版" in ln or "Q 版" in ln or "真身" in ln or "化形" in ln
                        or "主形象" in ln or "第二形态" in ln):
                    cur_mark = ln
                elif ln.startswith("- 记忆点") or ln.startswith("- 记忆"):
                    if cur is not None:
                        cur["memories"] = ln.split("：", 1)[-1].split(":", 1)[-1].strip()
                elif ln.startswith("- 音色") or ln.startswith("- 声线") or ln.startswith("- 方言"):
                    if cur is not None:
                        cur["voice"] = ln.split("：", 1)[-1].split(":", 1)[-1].strip()
                i += 1
            # age 兜底:image_prompt 里的 "N-year-old"(2026-09-01:括号描述常无年龄数字,
            # 如「(人类·男,星陨组织头目)」,而 image_prompt 首词带 45-year-old)
            if not cur.get("age"):
                ip = cur.get("image_prompt", "") or ""
                ym = re.search(r"(\d+)-year-old|(\d+)\s*岁", ip)
                if ym:
                    cur["age"] = (ym.group(1) + "岁") if ym.group(1) else ym.group(2)
            continue
        i += 1
    if not cards:
        print(f"  ✗ 角色卡为空: {md_path}")
        return False
    out_path = re.sub(r"\.md$", ".json", md_path)
    with open(out_path, "w", encoding="utf-8", newline="\n") as f:
        json.dump(cards, f, ensure_ascii=False, indent=1)
    print(f"  ✓ {os.path.basename(md_path)} → {os.path.basename(out_path)} ({len(cards)} 角色)")
    return True


def convert_scene_cards(md_path, dry_run=False):
    """场景提示词.md → .json(场景数组:id/description/image_prompt)。
    2026-09-01 重写(旧版错乱实锤:全局风格块被当场景提示词、各场景 image_prompt
    全是同一段全局前缀→场景渲染趋同):结构=「## 场景名」二级标题 + 紧随的
    ```代码块``` 为该场景 image_prompt;一级标题(统一风格/场景卡/书名标题)跳过;
    代码块后的 > 引用行为该场景 description。"""
    text = open(md_path, encoding="utf-8").read()
    scenes, cur = [], None
    lines = text.splitlines()
    i = 0
    while i < len(lines):
        ln = lines[i].strip()
        # 场景卡:二级标题 ## 场景名(可选编号/描述括号)
        m = re.match(r"^##\s*(?:场景\s*)?(?:[一二三四五六七八九十\d]+[、.．]?\s*)?(.+?)\s*$", ln)
        if m and not any(g in m.group(1) for g in GLOBAL_SECS):
            mm = re.match(r"^(.+?)(?:[（(](.*?)[）)])?$", m.group(1))
            name = mm.group(1).strip() if mm else m.group(1).strip()
            desc = mm.group(2).strip() if mm and mm.group(2) else ""
            cur = {"id": name, "description": desc}
            scenes.append(cur)
            i += 1
            # 该场景的代码块(紧随标题后的第一个 ```)
            while i < len(lines) and not lines[i].strip().startswith("```"):
                i += 1
            if i < len(lines):
                blk = []
                i += 1
                while i < len(lines) and not lines[i].strip().startswith("```"):
                    blk.append(lines[i])
                    i += 1
                i += 1
                if blk:
                    cur["image_prompt"] = "\n".join(blk).strip()
                # 代码块后的 > 引用行 = 用途备注(description 为空时补)
                while i < len(lines) and lines[i].strip().startswith(">"):
                    if cur is not None and not cur.get("description"):
                        cur["description"] = lines[i].strip().lstrip("> ").strip()
                    i += 1
            continue
        i += 1
    if not scenes:
        print(f"  ✗ 场景卡为空: {md_path}")
        return False
    out_path = re.sub(r"\.md$", ".json", md_path)
    with open(out_path, "w", encoding="utf-8", newline="\n") as f:
        json.dump(scenes, f, ensure_ascii=False, indent=1)
    print(f"  ✓ {os.path.basename(md_path)} → {os.path.basename(out_path)} ({len(scenes)} 场景)")
    return True
