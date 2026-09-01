#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""分镜对白覆盖缺失 LLM 定点补全(2026-09-01 全面复查配套):
storyboard_check 覆盖 FAIL 的缺失对白(正文有、分镜没有)——成片丢对白硬伤。
流程:①逐章提取缺失对白(与 storyboard_check 同口径)②LLM 定位插入镜(读本章
分镜精简+正文上下文)③程序执行插入:shots[N-1].dialogue 追加行
`(Sx)角色:"原文"`(既有格式)+ h3_prompt 同步 <Subject M> (Sx) says: <d>[Chinese]
原文</d>(M=该镜最后 Subject 号,无则 1)④插入后复验覆盖,不达标该章重试 3 轮。
确定性校验:插入后 norm 匹配才算成功。

用法: python fix_sb_coverage.py --root <novel库根> [--books 书1,书2] [--limit N]
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

SYS = """你是漫剧分镜编辑。正文对白里有若干句没进分镜脚本(成片会丢对白),
请把每句对白安排进分镜:读本章分镜摘要找到语义最贴合的镜(说话人/场景/情节
匹配),把该句追加进该镜台词。
输出严格 JSON: {"inserts": [{"dialogue": "原文逐字", "shot_id": 目标镜号,
"speaker": "(Sx)角色名", "new_shot": false, "action": ""}]}
约束:①speaker 填完整前缀如 (S3)金珠——若该角色在本章已出现复用其编号,新角色
用本章最大号+1;②dialogue 只写引号内原文,不加引号;③若本章无合适镜可
new_shot=true 并给 shot_id=插入位置(该镜前插入)与 action(中文画面列,以
【场景名】开头);④禁止输出 JSON 以外内容。"""


def run_check(sc, bdir, f, n):
    """与 storyboard_check 同口径的缺失对白提取"""
    body = glob.glob(os.path.join(bdir, "正文", "*", f"第{n}章*.md"))
    if not body:
        return [], None
    text = open(body[0], encoding="utf-8").read()
    dialogs = sc.extract_dialogues(text)
    if not dialogs:
        return [], None
    j = json.load(open(f, encoding="utf-8"))
    sb = sc.json_to_md(json.dumps(j, ensure_ascii=False))
    sb_norm = sc.norm(sb)
    speech = sc.cell_by_header(*sc.parse_shot_table(sb)[:2], "台词") or []
    joined = sc.dial_joined_norm(speech)
    joined_d = sc.d_block_joined_norm(sb)
    missing = [d for d in dialogs if d not in sb and sc.norm(d) not in sb_norm
               and sc.norm(d) not in joined and sc.norm(d) not in joined_d]
    return missing, text


def apply_insert(j, ins):
    """插入一句对白到目标镜(dialogue 行 + h3_prompt <d> 同步)。返回 (新shots, ok)"""
    dial = (ins.get("dialogue") or "").strip().strip('“”"')
    spk = (ins.get("speaker") or "").strip()
    sid = int(ins.get("shot_id") or 0)
    if not dial or not spk or sid < 1:
        return None, False
    shots = j.get("shots", [])
    line = f'{spk}:"{dial}"'
    # 去重:该句已在任意镜 dialogue/h3 中则不重复插入(LLM 多轮重插事故 2026-09-01)
    alltxt = json.dumps(shots, ensure_ascii=False)
    if dial in alltxt:
        return shots, False
    if ins.get("new_shot"):
        act = ins.get("action", "") or f"【场景】{dial[:24]}"
        new = {"shot_id": sid, "shot_size": "中景", "camera": "固定",
               "action": act, "dialogue": line, "characters": [spk.split(")", 1)[-1]],
               "light": "", "sound": "", "duration": 5,
               "h3_prompt": f"subject_definitions:\n<Subject 1> is {spk.split(')',1)[-1]}, a character in this story.\n\ndetailed_description:\nThe camera holds a medium shot of the scene. {act}\n\n<Subject 1> ({spk.split('(',1)[1]} says: <d>[Chinese] {dial}</d>"}
        shots.insert(sid - 1, new)
    else:
        idx = sid - 1
        if not (0 <= idx < len(shots)):
            return None, False
        s = shots[idx]
        d0 = s.get("dialogue", "") or ""
        s["dialogue"] = (d0 + "\n" if d0 else "") + line
        # h3 同步:Subject 号 = 该镜最后出现的 Subject;无则 1
        hp = s.get("h3_prompt", "") or ""
        subjs = re.findall(r'<Subject\s+(\d+)>', hp)
        m = int(subjs[-1]) if subjs else 1
        sx = spk.split("(", 1)[1].split(")", 1)[0]
        tail = re.search(r'\nnon_diegetic_music:', hp)
        if tail:
            hp = hp[:tail.start()] + f'\n<Subject {m}> ({sx}) says: <d>[Chinese] {dial}</d>' + hp[tail.start():]
        else:
            hp = hp.rstrip() + f'\n<Subject {m}> ({sx}) says: <d>[Chinese] {dial}</d>\n'
        s["h3_prompt"] = hp
    # 重排 shot_id
    for i, s in enumerate(shots, 1):
        s["shot_id"] = i
    j["shots"] = shots
    return shots, True


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default=ROOT)
    ap.add_argument("--books", help="逗号分隔书名(默认全部)")
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()
    books = [b.strip() for b in (args.books or "").split(",") if b.strip()]
    if not books:
        books = sorted(d for d in os.listdir(args.root)
                       if os.path.isdir(os.path.join(args.root, d)))
    skills = os.path.join(os.path.expanduser("~"), ".agents", "skills", "shuangwen-novel", "scripts")
    sys.path.insert(0, skills)
    import storyboard_check as sc
    cfg = sr.load_api()
    done = fail = 0
    for b in books:
        bdir = os.path.join(args.root, b)
        for f in sorted(glob.glob(os.path.join(bdir, "素材", "分镜脚本", "*.json"))):
            n = sc.chapter_num(f)
            missing, text = run_check(sc, bdir, f, n)
            if not missing:
                continue
            if args.limit and done + fail >= args.limit:
                break
            j = json.load(open(f, encoding="utf-8"))
            brief = []
            for s in j.get("shots", []):
                act = (s.get("action") or "").strip().replace("\n", " ")[:50]
                dl = (s.get("dialogue") or "").strip().replace("\n", "；")[:40]
                brief.append(f"{s.get('shot_id')}|{act}|{dl}")
            ctx = ""
            for d in missing:
                i = text.find(d) if text else -1
                if i >= 0:
                    ctx += f"…{text[max(0, i - 60):i + len(d) + 60]}…\n"
            user = (f"【书名】{b} 第{n}章\n【缺失对白】\n" + "\n".join(
                f"{i + 1}. {d[:80]}" for i, d in enumerate(missing)) +
                f"\n\n【正文上下文】\n{ctx[:1500]}\n\n【本章分镜摘要】\n" +
                "\n".join(brief)[:3000] + f"\n\n【输出 JSON】{{\"inserts\": []}}")
            # 3 轮重试:插入后复验,仍有缺失则带缺失清单重试
            for rnd in range(3):
                try:
                    data = sr.llm_json(cfg, SYS, user, temp=0.3)
                except Exception as e:
                    print(f"  ✗ {b} 第{n}章 LLM 失败: {str(e)[:80]}")
                    break
                inserts = data.get("inserts") or []
                if not inserts:
                    print(f"  - {b} 第{n}章: LLM 未返回插入({len(missing)} 句缺失)")
                    break
                ok = 0
                for ins in inserts:
                    shots, succ = apply_insert(j, ins)
                    if succ:
                        ok += 1
                if args.dry_run:
                    print(f"  [dry] {b} 第{n}章: 第{rnd + 1}轮插入 {ok} 句")
                    break
                json.dump(j, open(f, "w", encoding="utf-8", newline="\n"),
                          ensure_ascii=False, indent=1)
                # 复验
                missing2, _ = run_check(sc, bdir, f, n)
                if not missing2:
                    print(f"  ✓ {b} 第{n}章: 插入 {ok} 句,覆盖 100%")
                    done += 1
                    break
                print(f"  ↻ {b} 第{n}章: 插入 {ok} 句,仍缺 {len(missing2)} 句,重试")
                missing = missing2
                user = user.replace("【缺失对白】", "【缺失对白(更新)】") if "【缺失对白(更新)】" in user else user
                user = re.sub(r'【缺失对白】\n(.+?)\n\n【正文上下文】',
                              lambda mm: '【缺失对白(更新)】\n' + "\n".join(
                                  f"{i + 1}. {d[:80]}" for i, d in enumerate(missing2)) + "\n\n【正文上下文】",
                              user, flags=re.S)
            else:
                fail += 1
                print(f"  ✗ {b} 第{n}章: 3 轮后仍缺失 {len(missing)} 句")
    print(f"完成: 补全 {done} 章 / 失败 {fail}")


if __name__ == "__main__":
    main()
