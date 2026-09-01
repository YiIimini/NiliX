#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""分镜 JSON 质量机械修复(2026-09-01 全面复查配套):
①长句拆句:台词/旁白引号内汉字 >20 按标点断点拆成 ≤20 字多句(dialogue 列拆多行,
   h3_prompt 对应拆多条 says: 句,同断点同步)——H3 口型/节奏上限硬约束
②[Shot N] 标记修复:h3_prompt 内 [Shot N] 重复/错位按 shot_id 重写去重
   (渲染端/质检按镜序解析,重复标记会触发时间戳 FAIL)
确定性修复,幂等(已达标镜跳过),修复后 storyboard_check 复验。

用法: python fix_sb_quality.py --root <novel库根> [--books 书1,书2] [--dry-run]
"""
import argparse
import glob
import json
import os
import re
import sys

sys.stdout.reconfigure(encoding="utf-8")

MAX_LEN = 20  # 引号内汉字上限(H3 口型/节奏硬约束)

# dialogue 行: (S3)阿栓:"内容" 或 (S3)阿栓:“内容” 或 内心·/旁白 前缀
# 内容组用贪婪 .* 取最后一对引号(非贪婪会匹配到开头第一个引号,内容被吃空)
DIAL_RE = re.compile(
    r'^(\([^)]*\)[^:"：]*[:"：]\s*)([“"].*[”"])$', re.S)
INNER = re.compile(r'^[“"](.*)[”"]$', re.S)

# 断点优先级:句末强断点 > 逗号弱断点
HARD = "。！？!?…"
SOFT = "，,；;"


def han_len(s):
    return len(re.findall(r'[\u4e00-\u9fff]', s))


def split_at_breaks(text):
    """把超长文本按断点拆成 ≤MAX_LEN 汉字的多段。返回段列表(段与段拼接=原文去断点)。"""
    if han_len(text) <= MAX_LEN:
        return [text]
    segs, cur = [], ""
    # 先按强断点切
    parts = re.split('([' + HARD + '])', text)
    # parts: [段, 断点, 段, 断点...]
    i = 0
    buf = ""
    for p in parts:
        if not p:
            continue
        if p in HARD:
            buf += p
            segs.append(buf)
            buf = ""
            continue
        # 文本段:若 buf 加本段超限,先按弱断点拆 buf,再续
        if buf and han_len(buf) + han_len(p) > MAX_LEN:
            segs.append(buf)
            buf = p
        else:
            buf += p
    if buf:
        segs.append(buf)
    # 弱断点细化:仍超长的段按逗号拆
    out = []
    for seg in segs:
        if han_len(seg) <= MAX_LEN:
            out.append(seg)
            continue
        parts2 = re.split('([' + SOFT + '])', seg)
        buf = ""
        for p in parts2:
            if not p:
                continue
            if p in SOFT:
                buf += p
                if han_len(buf) >= 8:  # 弱断点段 ≥8 字才独立,太碎继续积攒
                    out.append(buf)
                    buf = ""
                continue
            if buf and han_len(buf) + han_len(p) > MAX_LEN:
                out.append(buf)
                buf = p
            else:
                buf += p
        if buf:
            out.append(buf)
    # 兜底:任何段仍 >MAX 就硬切(罕见,保完整)
    final = []
    for seg in out:
        if han_len(seg) <= MAX_LEN:
            final.append(seg)
            continue
        while han_len(seg) > MAX_LEN:
            cut = 0
            n = 0
            for idx, ch in enumerate(seg):
                if ch >= "\u4e00" and ch <= "\u9fff":
                    n += 1
                    if n == MAX_LEN:
                        cut = idx + 1
                        break
            final.append(seg[:cut])
            seg = seg[cut:]
        if seg:
            final.append(seg)
    return final


def fix_dialogue_line(ln):
    """拆 dialogue 单行,返回 [行, ...](不变则 [ln])。
    兼容两种多句格式:同行多句用 "" 相邻连接((S1)甲:"句1""句2"),先按引号对切分;
    单句仍 >20 再按标点断点拆。输出保留原引号样式(ASCII/中文)。
    2026-09-01 容错:引号未闭合的行(LLM 截断,第33章镜6 实锤)按已闭合段+尾部硬拆。"""
    m = DIAL_RE.match(ln.strip())
    if not m:
        # 内心/旁白行(无引号,`内心·角色名：内容` / `旁白：内容`)——按标点断点拆
        m3 = re.match(r'^((?:内心·|旁白[:：]?)[^:：]{0,12}?[:：]?\s*)(.+)$', ln.strip(), re.S)
        if m3 and han_len(m3.group(2)) > MAX_LEN:
            segs = split_at_breaks(m3.group(2).strip())
            if len(segs) > 1:
                return [f'{m3.group(1)}{s.strip()}' for s in segs]
        # 容错:有 (Sx)前缀且内容引号未闭合但超长 → 按引号切段硬拆
        m2 = re.match(r'^(\([^)]*\)[^:"：]*[:：]?\s*)(.+)$', ln.strip(), re.S)
        if not m2:
            return [ln]
        body = m2.group(2).strip()
        if body.count('"') + body.count('“') + body.count('”') < 2:
            return [ln]
        # 按引号对切:先完整对,剩余尾部
        parts = re.findall(r'“[^”]*”|"[^"]*"', body)
        tails = re.split(r'“[^”]*”|"[^"]*"', body)
        out = []
        for pi, part in enumerate(parts):
            inner = part.strip('“”"')
            if han_len(inner) <= MAX_LEN:
                out.append(f'{m2.group(1)}"{inner}"')
            else:
                for seg in split_at_breaks(inner):
                    out.append(f'{m2.group(1)}"{seg.strip()}"')
        if tails and tails[-1].strip():
            last = tails[-1].strip()
            if han_len(last) > 2:
                out.append(f'{m2.group(1)}"{last}"')
        return out if len(out) != len([ln]) or True else [ln]
    prefix, quoted = m.group(1), m.group(2)
    im = INNER.match(quoted)
    if not im:
        return [ln]
    content = im.group(1)
    quote = '"' if quoted.startswith('"') else '“'
    # ①同行多句:按 相邻引号对("") 切分
    raw_parts = [p for p in re.split(r'""|“”', content) if p.strip()]
    out = []
    for part in raw_parts:
        inner = part.strip('“”"')
        if han_len(inner) <= MAX_LEN:
            out.append(f'{prefix}{quote}{part.strip()}{quote}')
        else:
            segs = split_at_breaks(inner)
            if len(segs) <= 1:
                out.append(f'{prefix}{quote}{part.strip()}{quote}')
            else:
                for seg in segs:
                    out.append(f'{prefix}{quote}{seg.strip()}{quote}')
    return out if len(out) != 1 or out[0] != ln.strip() else [ln]


def fix_h3_says(hp, plan):
    """h3_prompt 内超长 <d> 拆多条 says/adds 句。返回 (新hp, 是否变)"""
    changed = False
    # 匹配 says 句(adds: 变体统一保留原动词);内容剥 [中文]/[Chinese] 标签后按断点拆
    pat = re.compile(r'(<Subject\s+\d+>\s*\((S\d+)\)\s*(says|adds):\s*<d>\s*(?:\[(?:中文|Chinese)\]\s*)?([^<]+)</d>)')
    out = []
    pos = 0
    for m in pat.finditer(hp):
        out.append(hp[pos:m.start()])
        full, sx, verb, content = m.group(1), m.group(2), m.group(3), m.group(4).strip()
        content2 = content.strip('“”"')
        if han_len(content2) <= MAX_LEN:
            out.append(full)
        else:
            segs = split_at_breaks(content2)
            if len(segs) <= 1:
                out.append(full)
            else:
                subj = re.match(r'(<Subject\s+\d+>)', full).group(1)
                for k, seg in enumerate(segs):
                    v = "says" if k == 0 else "adds"
                    out.append(f'{subj} ({sx}) {v}: <d>[Chinese] {seg.strip()}</d>')
                changed = True
        pos = m.end()
    out.append(hp[pos:])
    return "".join(out), changed


# At 时码:MM:SS.mmm(渲染端契约)或 LLM 漂移出的 HH:MM:SS.mmm(换算回 MM:SS.mmm)
AT_RE = re.compile(r'At\s+(?:(?P<hh>\d{1,2}):)?(?P<mm>\d{1,2}):(?P<ss>\d{2})\.(?P<ms>\d{3})')


def norm_at(txt):
    """At 时码归一:HH:MM:SS.mmm → MM:SS.mmm(总秒换算,MM 可超 59)"""
    m = AT_RE.match(txt)
    if not m:
        return txt, False
    total = int(m.group("mm")) * 60 + int(m.group("ss"))
    if m.group("hh"):
        total += int(m.group("hh")) * 3600
    mm, ss = divmod(total, 60)
    return f'At {mm:02d}:{ss:02d}.{m.group("ms")}', True


def fix_shot_marks(hp, shot_id):
    """[Shot N] 处理:首个标记改写为 shot_id(乱序章根因:shot_id 字段错乱,
    标记跟着错号走),其 At 归一为 MM:SS.mmm;后续重复标记删除(连同 At)——LLM
    曾在 detailed_description 尾重复 [Shot N](第1章镜17 双 [Shot 17] 实锤)。
    全库实测 0 处 retention 引用,全重写安全。"""
    marks = list(re.finditer(r'\[Shot\s+(\d+)\]', hp))
    if not marks:
        return hp, False
    changed = False
    out = []
    pos = 0
    for idx, m in enumerate(marks):
        out.append(hp[pos:m.start()])
        if idx > 0:
            # 重复标记:整段删除(含紧跟 At)
            tail = hp[m.end():]
            am = AT_RE.match(tail.lstrip())
            end = m.end() + (am.end() if am else 0)
            pos = end
            changed = True
            continue
        if int(m.group(1)) != shot_id:
            changed = True
        out.append(f'[Shot {shot_id}]')
        tail = hp[m.end():]
        # 消费紧跟的 At(保留第一个,归一格式)
        lead = len(tail) - len(tail.lstrip())
        stripped = tail.lstrip()
        am = AT_RE.match(stripped)
        if am:
            at_txt, ch = norm_at(am.group(0))
            out.append(tail[:lead] + at_txt)
            changed = changed or ch
            rest = stripped[am.end():]
            am2 = AT_RE.match(rest.lstrip())
            if am2:
                changed = True
                rest = rest.lstrip()[am2.end():]
            tail = rest
        pos = m.end() + (len(hp[m.end():]) - len(tail))
    out.append(hp[pos:])
    return "".join(out), changed


def fix_at_axis(shots):
    """按 duration 累进重算每镜 At 时码(真实时间轴,MM:SS.mmm):
    LLM 曾写错时码(第50章 15:00→00:00 递减)导致 check At 递增 FAIL;
    渲染端 clip-local 化(At≥本镜时长自动改写),重算后不越界不递减。"""
    t = 0
    for s in shots:
        hp = s.get("h3_prompt", "")
        if not hp:
            continue
        mm, ss = divmod(t, 60)
        at = f'At {mm:02d}:{ss:02d}.000'
        hp2, n = AT_RE.subn(at, hp, count=1)  # 只改第一个 At(标记后的本镜时码)
        if n and hp2 != hp:
            s["h3_prompt"] = hp2
        t += int(s.get("duration", 0) or 0)
    return shots


def fix_file(path, dry):
    j = json.load(open(path, encoding="utf-8"))
    shots = j.get("shots", [])
    nd = nm = 0
    # ①shot_id 按数组顺序重排(乱序根因:LLM 输出错号,第104章镜11 曾 shot_id=29)
    for idx, s in enumerate(shots, 1):
        if int(s.get("shot_id", 0)) != idx:
            s["shot_id"] = idx
            nm += 1
    # ①z At 时码递减检测:LLM 曾写错(第50章 15:00→00:00),按 duration 重算真实时间轴
    seq = re.findall(r'At\s+(\d+):(\d+)\.\d+', "\n".join(s.get("h3_prompt", "") for s in shots))
    times = [int(m) * 60 + int(s) for m, s in seq]
    if any(b <= a for a, b in zip(times, times[1:])):
        fix_at_axis(shots)
        nm += 1
    for s in shots:
        sid = int(s.get("shot_id", 0))
        # ①长句拆句
        dlines = (s.get("dialogue", "") or "").split("\n")
        newlines = []
        for ln in dlines:
            fixed = fix_dialogue_line(ln)
            newlines.extend(fixed)
            if len(fixed) > 1:
                nd += 1
        if newlines != dlines:
            s["dialogue"] = "\n".join(newlines)
        # ①b h3_prompt 同步拆 <d>(与 dialogue 同断点)
        hp = s.get("h3_prompt", "") or ""
        if hp:
            hp2, chh = fix_h3_says(hp, None)
            if chh:
                s["h3_prompt"] = hp2
                nd += 1
        # ②[Shot N] 重写/补齐
        hp = s.get("h3_prompt", "") or ""
        if hp:
            hp2, ch = fix_shot_marks(hp, sid)
            if '[Shot' not in hp2:
                # 完全漏写标记(LLM 生成遗漏):补到 detailed_description 风格句后
                dd = hp2.find('detailed_description:')
                if dd >= 0:
                    rest = hp2[dd + len('detailed_description:'):]
                    lead = rest[:len(rest) - len(rest.lstrip())]
                    hp2 = hp2[:dd + len('detailed_description:')] + lead + \
                          f'[Shot {sid}] At 00:00.000 ' + rest.lstrip()
                    ch = True
            if ch:
                s["h3_prompt"] = hp2
                nm += 1
    if (nd or nm) and not dry:
        json.dump(j, open(path, "w", encoding="utf-8", newline="\n"),
                  ensure_ascii=False, indent=1)
    return nd, nm

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default=r"D:\Ai\NiliX\novel")
    ap.add_argument("--books", help="逗号分隔书名(默认全部)")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()
    books = [b.strip() for b in (args.books or "").split(",") if b.strip()]
    if not books:
        books = sorted(d for d in os.listdir(args.root)
                       if os.path.isdir(os.path.join(args.root, d)))
    tot_d = tot_m = 0
    for b in books:
        bd, bm = 0, 0
        for f in sorted(glob.glob(os.path.join(args.root, b, "素材", "分镜脚本", "*.json"))):
            nd, nm = fix_file(f, args.dry_run)
            bd += nd
            bm += nm
        tot_d += bd
        tot_m += bm
        print(f"{b}: 拆句 {bd} 镜 / Shot重写 {bm} 镜" + (" [dry]" if args.dry_run else ""))
    print(f"合计: 拆句 {tot_d} 镜 / Shot重写 {tot_m} 镜")


if __name__ == "__main__":
    main()
