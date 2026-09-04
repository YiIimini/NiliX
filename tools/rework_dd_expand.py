# -*- coding: utf-8 -*-
"""detailed_description 存量扩容(2026-09-04 画质升级的存量同步,根因⑤)。

官方 ref 指南要求正文 "as detailed and explicit as possible"(normally 350-500 词),
全库 99.8% 镜 ≤220 词(渲染端旧约束时代产物)——画面细节不足=成片空洞感来源。
逐镜 LLM 扩写到 250-350 词(对白密集镜以完整台词时间线优先可低至 200)。

安全设计(校验闭环,失败保留原文不落盘):
  ① <d> 台词块序列逐字一致  ② Subject/Picture/Audio/Shot/Sx 引用多重集合一致
  ③ detailed_description 之外全部逐字保留  ④ 词数 200-430
  ⑤ 剥 <d> 后无中文  ⑥ 气闸句/运镜句锚保留
断点续跑:tools/logs/dd_expand_progress.json 按 书|文件|镜号 记完成,重跑跳过。
.bak 保护(最早备份不覆盖)。用法:
  python tools/rework_dd_expand.py [--dry-run] [--book 书名] [--limit N] [--workers 8]
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
PROGRESS = os.path.join(LOG_DIR, "dd_expand_progress.json")

SYSTEM = """你是 MiniMax H3 视频提示词扩写师。输入是一镜的完整 H3 六段式提示词,你的任务是
【只重写 detailed_description 段】:把画面正文扩写到 250-350 英文词(对白密集镜以完整
台词时间线优先,可低至 200 词),显著提升画面细节密度。输出严格 JSON:
{"detailed_description": "扩写后的段落正文(不含段标题,不含其它段)"}

扩写方向(只加可见细节,禁止改既有事实——人物/动作/位置/光线/场景/道具/结局状态全部保持原意):
- 构图层次:前景/中景/背景各有什么,主体在画面哪三分之一、朝向;
- 材质与光影:服饰面料/皮肤/道具/地面材质,光源方向、光质(硬/软)、色温、阴影落点;
- 表演细节:微表情(眉/眼/嘴角层次)、肢体动作的起步-进行-完成节拍、呼吸与重量感;
- 环境动效:飘动的发丝/衣角、浮尘、水汽、火光摇曳等持续微动;
- 动作按时间节拍展开(起点状态→变化过程→完成态)。

铁律(违反=废片,输出前逐条自查):
1. <d>…</d> 台词块逐字保留:顺序不变、一个不增一个不减、中文原文一字不改;
2. <Subject N>/<Picture N>/<Audio N>/[Shot N]/(Sx) 引用标签全部原样保留,禁止新增或删除;
3. 开头 1-2 句风格句保持原意(可润色衔接,不换风格、不加逗号标签堆);
4. 运镜句保持官方动词句式与原位置(the camera pushes in with ... amplitude at ... speed),
   不换措辞不加新运镜;原正文里的 camera 句一句都不能丢;
5. 非首镜承接句(opens holding the previous shot's closing framing ...)保持原样;
6. 禁止新台词/新旁白/新镜头切换/新角色/新道具实体;
7. 画面可见文字保持英文双引号原文;原文已有的中文(角色名/可见文字)原样保留,禁止新增中文;
8. 扩写后正文【必须】达到 250-350 英文词——只润色不增细节=失败,禁止输出接近原文长度的结果。"""

DD_RE = re.compile(r"(detailed_description:\s*\n)(.*?)(?=\noverall_soundscape:|$)", re.S)
D_TAG = re.compile(r"<d>.*?</d>", re.S)
CJK = re.compile(r"[\u4e00-\u9fff]")
REF_TAGS = re.compile(r"<Subject \d+>|<Picture \d+>|<Audio \d+>|\[Shot \d+\]")
SX_TAGS = re.compile(r"\(S\d+(?:,\s*S?\d+)*\)")


def dd_words(dd):
    return len(dd.split())


def tags_multiset(txt, rex):
    out = {}
    for t in rex.findall(txt):
        out[t] = out.get(t, 0) + 1
    return out


def validate(old_dd, new_dd):
    """校验闭环:返回 None=通过,否则返回失败原因。"""
    if not new_dd or len(new_dd) < 120:
        return "过短"
    w = dd_words(new_dd)
    # 官方:对白密集镜优先完整台词时间线而非机械词数——<d> 台词字符占比高时下限放宽到 150
    dial_chars = sum(len(t) for t in D_TAG.findall(new_dd))
    floor = 150 if dial_chars >= 40 else 200
    if w < floor:
        return "词数不足%d(%d)" % (floor, w)
    if w > 430:
        return "词数超430(%d)" % w
    if D_TAG.findall(old_dd) != D_TAG.findall(new_dd):
        return "台词块变动"
    if tags_multiset(old_dd, REF_TAGS) != tags_multiset(new_dd, REF_TAGS):
        return "引用标签集合变动"
    if tags_multiset(old_dd, SX_TAGS) != tags_multiset(new_dd, SX_TAGS):
        return "说话者ID集合变动"
    body = D_TAG.sub("", new_dd)
    old_cjk = set(CJK.findall(D_TAG.sub("", old_dd)))
    if set(CJK.findall(body)) - old_cjk:
        return "正文新增中文"
    for anchor in ("opens holding", "stays locked off"):
        if anchor in old_dd and anchor not in new_dd:
            return "锚句丢失(%s)" % anchor
    if "camera" in old_dd and "camera" not in new_dd:
        return "运镜句丢失"
    return None


def expand_one(cfg, hp, sem):
    """LLM 扩写单镜 detailed_description;失败/校验不过返回 (None, 原因)。"""
    m = DD_RE.search(hp)
    if not m:
        return None, "无detailed_description段"
    old_dd = m.group(2)
    last_reason = "LLM异常"
    for attempt in range(2):
        try:
            with sem:
                user = hp + "\n\n【当前 detailed_description 词数: %d;扩写目标: 250-350 英文词(硬要求,低于 250=失败)】" % dd_words(old_dd)
                data = sr.llm_json(cfg, SYSTEM, user, temp=0.35 if attempt == 0 else 0.5)
        except Exception as e:
            last_reason = "LLM异常:" + str(e)[:60]
            time.sleep(2)
            continue
        new_dd = (data.get("detailed_description") or "").strip()
        new_dd = re.sub(r"^detailed_description:\s*\n?", "", new_dd).strip()
        if reason := validate(old_dd, new_dd):
            last_reason = reason
            continue
        return new_dd, None
    return None, last_reason


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true", help="只统计,不调用 LLM")
    ap.add_argument("--book", default="")
    ap.add_argument("--limit", type=int, default=0, help="最多处理镜数(试跑用)")
    ap.add_argument("--workers", type=int, default=8)
    ap.add_argument("--min-words", type=int, default=250, help="低于此词数才扩容")
    args = ap.parse_args()

    cfg = sr.load_api(None)
    done = set()
    if os.path.exists(PROGRESS):
        try:
            done = set(json.load(io.open(PROGRESS, encoding="utf-8")))
        except Exception:
            done = set()

    # 收集任务:文件 → [(shot 序号, shot_id, h3_prompt, key)]
    books = sorted(os.listdir(NOVEL_ROOT)) if not args.book else [args.book]
    tasks = []  # (file_path, book, shots_list_ref, idx_in_file, key)
    file_map = {}
    tot_below = 0
    for book in books:
        for sb in sorted(glob.glob(os.path.join(NOVEL_ROOT, book, "素材", "分镜脚本", "*.json"))):
            try:
                data = json.load(io.open(sb, encoding="utf-8"))
            except Exception as e:
                print("  [skip] %s 解析失败: %s" % (os.path.basename(sb), e))
                continue
            shots = data.get("shots", [])
            has_task = False
            for i, s in enumerate(shots):
                hp = s.get("h3_prompt") or ""
                m = DD_RE.search(hp)
                if not m:
                    continue
                w = dd_words(m.group(2))
                if w < args.min_words:
                    tot_below += 1
                key = "%s|%s|%s" % (book, os.path.basename(sb), s.get("shot_id", i))
                if w >= args.min_words or key in done:
                    continue
                tasks.append((sb, book, shots, i, key))
                has_task = True
            if has_task:
                file_map.setdefault(sb, {"book": book, "shots": shots, "pending": 0, "results": {}})
    for t in tasks:
        file_map[t[0]]["pending"] += 1
    if args.limit > 0:
        tasks = tasks[: args.limit]
        keep = set(t[0] for t in tasks)
        for sb in list(file_map):
            if sb not in keep:
                del file_map[sb]

    print("待扩容镜数: %d(全库低于 %d 词: %d,断点已完成: %d)" % (len(tasks), args.min_words, tot_below, len(done)))
    if args.dry_run or not tasks:
        return 0

    os.makedirs(LOG_DIR, exist_ok=True)
    sem = threading.Semaphore(args.workers)
    lock = threading.Lock()
    stats = {"ok": 0, "fail": 0}
    fail_log = []

    def persist():
        """把已完成镜的扩容结果合并写盘(重读磁盘保住 shots 之外的元数据字段)。"""
        for sb, info in file_map.items():
            if not info["results"]:
                continue
            for i, new_dd in info["results"].items():
                hp = info["shots"][i].get("h3_prompt") or ""
                m = DD_RE.search(hp)
                if m:
                    info["shots"][i]["h3_prompt"] = hp[: m.start(2)] + new_dd + hp[m.end(2):]
            data = json.load(io.open(sb, encoding="utf-8"))
            data["shots"] = info["shots"]
            if not os.path.exists(sb + ".bak"):
                io.open(sb + ".bak", "w", encoding="utf-8", newline="").write(
                    io.open(sb, encoding="utf-8").read())
            io.open(sb, "w", encoding="utf-8", newline="").write(
                json.dumps(data, ensure_ascii=False, indent=2))
            info["results"] = {}

    with ThreadPoolExecutor(max_workers=args.workers) as ex:
        futs = {}
        for sb, book, shots, i, key in tasks:
            futs[ex.submit(expand_one, cfg, shots[i].get("h3_prompt") or "", sem)] = (sb, i, key)
        for fut in as_completed(futs):
            sb, i, key = futs[fut]
            try:
                new_dd, err = fut.result()
            except Exception as e:
                new_dd, err = None, str(e)[:80]
            if new_dd:
                with lock:
                    file_map[sb]["results"][i] = new_dd
                    done.add(key)
                    stats["ok"] += 1
            else:
                with lock:
                    stats["fail"] += 1
                    fail_log.append("%s: %s" % (key, err))
            n = stats["ok"] + stats["fail"]
            if n % 50 == 0:
                with lock:
                    persist()
                    io.open(PROGRESS, "w", encoding="utf-8").write(json.dumps(sorted(done), ensure_ascii=False))
                print("进度 %d/%d ok=%d fail=%d" % (n, len(tasks), stats["ok"], stats["fail"]), flush=True)

    with lock:
        persist()
        io.open(PROGRESS, "w", encoding="utf-8").write(json.dumps(sorted(done), ensure_ascii=False))
    if fail_log:
        io.open(os.path.join(LOG_DIR, "dd_expand_failures.txt"), "w", encoding="utf-8").write("\n".join(fail_log))
    print("完成: ok=%d fail=%d(失败保留原文;清单 tools/logs/dd_expand_failures.txt)" % (stats["ok"], stats["fail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
