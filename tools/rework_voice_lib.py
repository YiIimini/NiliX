# -*- coding: utf-8 -*-
"""角色卡 voice_lib 存量补写(2026-09-04,创作侧音色确认的存量层)。

三部书主要人类角色按人设(LLM)分配 NiliX 音色档位:正角好听系/反派不好听系/
性别年龄对位/同剧互异(_2 变体)。json 写 voice_lib 字段,md 卡节内加
「配音档位:key」行(渲染端 reCharVoiceLibLine 解析,autoVoiceFor 最优先)。

硬校验(不过则规则兜底):合法 key/性别匹配/反派必 deep 系/同剧同性龄互异。
用法: python tools/rework_voice_lib.py [--dry-run] [--book 书名] [--workers 4]
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

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import storyboard_regen as sr

NOVEL_ROOT = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "novel")
LOG_DIR = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "tools", "logs")
PROGRESS = os.path.join(LOG_DIR, "voice_lib_progress.json")

# 39 档位语义表(与 NiliX manjuVoiceLib 同源;★=已配真实音源)
LIB = [
    ("male_sun", "男", "青年", "阳光温暖(★空·游戏男主)"),
    ("male_sun_2", "男", "青年", "清爽可靠(★托马)"),
    ("boy_teen", "男", "少年", "元气清亮(★温迪)"),
    ("boy_teen_2", "男", "少年", "野性直觉(★雷泽)"),
    ("male_mag", "男", "中年", "磁性温润(★白术)"),
    ("male_mag_2", "男", "中年", "沉稳智性(★艾尔海森)"),
    ("male_mag_3", "男", "中年", "粗犷锐利(★赛诺)"),
    ("male_elder", "男", "老年", "沧桑厚重(★钟离·帝王)"),
    ("male_elder_2", "男", "老年", "浑厚大嗓(★荒泷一斗)"),
    ("child_boy", "童", "儿童", "明亮少年(★五郎)"),
    ("child_boy_2", "童", "儿童", "机智元气(★鹿野院平藏)"),
    ("female_warm", "女", "青年", "温柔文雅(★神里绫华)"),
    ("female_warm_2", "女", "青年", "知性沉稳(★荧·游戏女主)"),
    ("girl_lively", "女", "少女", "活泼明亮(★芭芭拉)"),
    ("girl_lively_2", "女", "少女", "甜美元气(★安柏)"),
    ("female_mature", "女", "中年", "清冷知性(★申鹤)"),
    ("female_mature_2", "女", "中年", "温和守护(★坎蒂丝)"),
    ("female_elder", "女", "老年", "沉稳(edge-tts)"),
    ("female_elder_2", "女", "老年", "温和(edge-tts)"),
    ("child_girl", "童", "儿童", "咕咕高亢(★派蒙)"),
    ("child_girl_2", "童", "儿童", "呆萌平直(★七七)"),
    ("male_deep", "男", "反派", "威压阴森(★深渊法师)"),
    ("male_deep_2", "男", "反派", "沙哑尖刻(★散兵)"),
    ("female_deep", "女", "反派", "冷冽暗黑(★罗莎莉亚)"),
    ("female_deep_2", "女", "反派", "肃杀威压(★雷电将军)"),
    ("beast_cute", "通", "萌宠", "困倦哼唧(★早柚)"),
    ("beast_cute_2", "通", "萌宠", "怯生生轻软(★砂糖)"),
    ("male_narrator", "男", "叙述", "沧桑旁白(★戴因斯雷布)"),
    ("female_narrator", "女", "叙述", "庄重清晰(★琴)"),
    ("cn_dongbei", "女", "方言", "东北爽朗(★TELEVAL)"),
    ("cn_shaanxi", "女", "方言", "陕西亮堂"),
    ("cn_sichuan", "男", "方言", "四川麻辣(★TELEVAL)"),
    ("cn_henan", "男", "方言", "河南质朴(★TELEVAL)"),
    ("cn_guangxi", "男", "方言", "广西温吞"),
    ("cn_hunan", "男", "方言", "湖南热辣"),
    ("hk_female", "女", "粤语", "港风(★TELEVAL)"),
    ("hk_male", "男", "粤语", "港风(★TELEVAL)"),
    ("tw_female", "女", "台普", "台普"),
    ("tw_male", "男", "台普", "台普"),
]
LIB_BY_KEY = {k: (g, a, d) for k, g, a, d in LIB}
LIB_TABLE = "\n".join("- %s | %s | %s | %s" % t for t in LIB)

SYSTEM = """你是配音导演。为小说角色从 NiliX 音色档位表中选定配音档位(voice_lib)。
输出严格 JSON:{"assign": [{"id": "角色名", "voice_lib": "档位key"}, ...]}

选档铁律:
1. 主角/正角选好听系,反派(role=反派)必选不好听系(male_deep/male_deep_2/female_deep/female_deep_2);
2. 性别年龄对位(男角色选男声档,老年选 elder 系,儿童选 child 系,少女选 girl 系);
3. 同剧同性龄角色互异:两人都是青年男则一个 male_sun 一个 male_sun_2(用变体区分),禁止全剧共用一档;
4. 气质贴合角色卡的音色行与 appearance(软糯少女→girl_lively;训话敲钟的老者→male_elder;清冷→female_mature);
5. 旁白/画外专职角色才配 narrator 系;非人种(species≠人)不输出(渲染端固定 beast 系);
6. 方言档位仅在角色有明确地缘背景(音色行写明东北/四川/粤等)时选用;
7. 每个角色只输出一个档位 key,必须来自表内。

档位表(key|性别|年龄段|气质):
""" + LIB_TABLE


def age_band(age):
    m = re.search(r"(\d+)", age or "")
    if m:
        n = int(m.group(1))
        if n >= 50: return "老年"
        if n >= 40: return "中年"
        if n < 13: return "儿童"
        if n < 20: return "少年"
        return "青年"
    for k in ("老年", "中年", "少年", "儿童"):
        if k in (age or ""):
            return k
    return "青年"


def validate(cid, key, card, used):
    g, a = LIB_BY_KEY.get(key, ("?", "?", ""))[:2]
    if key not in LIB_BY_KEY:
        return "非法key"
    gender = card.get("gender") or ""
    if g in ("男", "女") and gender and g != gender:
        return "性别不符(%s→%s)" % (gender, key)
    role = card.get("role") or ""
    if role == "反派" and "deep" not in key:
        return "反派未配不好听系"
    if role != "反派" and "deep" in key and "narrator" not in key:
        return "正角配了反派档"
    # 同剧互异:同 key 已被别人用(且不是自己的)→ 除非无变体可用
    prev = used.get(key)
    if prev and prev != cid:
        base = key[:-2] if key.endswith("_2") else key
        alt = key + "_2" if not key.endswith("_2") else base
        if alt in LIB_BY_KEY and alt not in used:
            return "RETRY:" + alt
    return ""


def rule_assign(card, used):
    gender = card.get("gender") or "男"
    band = age_band(card.get("age") or "")
    role = card.get("role") or ""
    if role == "反派":
        key = "female_deep" if gender == "女" else "male_deep"
    elif band == "儿童":
        key = "child_girl" if gender == "女" else "child_boy"
    elif band == "少年":
        key = "girl_lively" if gender == "女" else "boy_teen"
    elif band == "中年":
        key = "female_mature" if gender == "女" else "male_mag"
    elif band == "老年":
        key = "female_elder" if gender == "女" else "male_elder"
    else:
        key = "female_warm" if gender == "女" else "male_sun"
    for _ in range(3):
        if used.get(key) is None:
            return key
        alt = key + "_2" if not key.endswith("_2") else key[:-2]
        if alt in LIB_BY_KEY:
            key = alt
        else:
            break
    return key


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--book", default="")
    ap.add_argument("--workers", type=int, default=4)
    args = ap.parse_args()
    cfg = sr.load_api(None)

    books = sorted(os.listdir(NOVEL_ROOT)) if not args.book else [args.book]
    for book in books:
        for jp in sorted(glob.glob(os.path.join(NOVEL_ROOT, book, "素材", "人物生成提示词.json"))):
            mp = jp[:-5] + ".md"
            data = json.load(io.open(jp, encoding="utf-8"))
            chars = data if isinstance(data, list) else data.get("characters", [])
            mains = [c for c in chars if c.get("id") and not c.get("minor")
                     and "影灵" not in c["id"] and "影子" not in c["id"]
                     and (c.get("species") in (None, "", "人"))
                     and not c.get("voice_lib")]
            if not mains:
                print("[%s] 全部已绑定" % book)
                continue
            roster = "\n".join("- %s | %s | %s | %s | 音色行: %s" % (
                c.get("id"), c.get("gender"), c.get("age"), (c.get("role") or "")[:10],
                (c.get("voice") or "")[:50]) for c in mains)
            user = "【待分配角色】\n%s\n\n【输出 JSON】{\"assign\":[...]}" % roster
            assign = {}
            try:
                out = sr.llm_json(cfg, SYSTEM, user, temp=0.3)
                for it in out.get("assign", []):
                    k = it.get("voice_lib") or ""
                    if k in LIB_BY_KEY:
                        assign[it.get("id") or ""] = k
            except Exception as e:
                print("  LLM 失败(规则兜底): %s" % str(e)[:60])
            used = {}
            results = {}
            for c in mains:
                cid = c["id"]
                key = assign.get(cid, "")
                why = validate(cid, key, c, used) if key else "缺失"
                for _ in range(2):
                    if why.startswith("RETRY:"):
                        key = why[6:]
                        why = validate(cid, key, c, used)
                    else:
                        break
                if why:
                    key = rule_assign(c, used)
                used[key] = cid
                results[cid] = key
            print("[%s] %d 个角色:" % (book, len(results)))
            for cid, key in results.items():
                print("   %s → %s(%s)" % (cid, key, LIB_BY_KEY[key][2][:24]))
            if args.dry_run:
                continue
            # 落盘 json + md(「配音档位:key」行)
            if not os.path.exists(jp + ".bak"):
                io.open(jp + ".bak", "w", encoding="utf-8", newline="").write(io.open(jp, encoding="utf-8").read())
                if os.path.exists(mp):
                    io.open(mp + ".bak", "w", encoding="utf-8", newline="").write(io.open(mp, encoding="utf-8").read())
            for c in chars:
                cid = c.get("id") or ""
                if cid in results:
                    c["voice_lib"] = results[cid]
            io.open(jp, "w", encoding="utf-8", newline="").write(json.dumps(data, ensure_ascii=False, indent=2))
            if os.path.exists(mp):
                md = io.open(mp, encoding="utf-8").read()
                for cid, key in results.items():
                    if "配音档位:" in md.split(cid, 1)[-1][:2000] if cid in md else False:
                        continue
                    # 在该角色音色行后插入;无音色行则加在节首
                    pat = re.compile(r"(#\s*[%s][^\n]*\n)" % re.escape(cid))
                    if pat.search(md):
                        md = pat.sub(lambda m: m.group(1) + "\n配音档位:%s\n" % key, md, count=1)
                io.open(mp, "w", encoding="utf-8", newline="").write(md)
    return 0


if __name__ == "__main__":
    sys.exit(main())
