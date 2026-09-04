# -*- coding: utf-8 -*-
"""叙事块(narrative_blocks)存量返工(2026-09-04 STEP 2 多镜合渲转正)。

背景:分镜逐镜独立渲染=切镜发生在视频之外,成片是硬拼感;H3 官方支持单次生成内
[Shot N] At MM:SS.mmm 多切点——把 2-3 个同场景连续镜合成一个叙事块、块级六段式
一次生成,叙事衔接由模型在视频内原生完成。

两阶段:
  ①确定性分组(零 LLM):同场景相邻 2-3 镜/时长和 ≤15s/说话人并集 ≤3,对白交锋链
    优先,氛围空镜组≤2;
  ②LLM 写块级六段式:输入=组内各镜全字段+各自六段式(素材),输出=块级六段式
    (官方切点语法,切点时码=累计时长直供)。

校验闭环(失败该块丢弃,逐镜独立渲染兜底,绝不丢镜):
  W1 镜号连续/不重叠  W2 时长和≤15  W3 [Shot N] 数=镜数且首段无时码
  W4 切点时码=累计(±0.001)  W5 <d> 台词块多重集=组内并集(逐字)
  + 六字段齐 + dd 词数 250-500(对白密集≥200) + 不新增中文 + 说话者标签并集不变

幂等:文件已有 narrative_blocks 则跳过(除非 --force)。
.bak 保护(最早备份不覆盖);断点续跑 tools/logs/narrative_blocks_progress.json。
用法:
  python tools/rework_narrative_blocks.py [--dry-run] [--book 书名] [--chapter N] [--limit N] [--workers 8] [--force]
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
PROGRESS = os.path.join(LOG_DIR, "narrative_blocks_progress.json")

MAX_BLOCK_SEC = 15
MAX_BLOCK_SHOTS = 3
MAX_SPEAKERS = 3

SYSTEM = """你是 MiniMax H3 视频提示词合成师。输入是 2-3 个连续分镜(每镜全字段 + 各自的
单镜六段式提示词),它们将合并为【一个叙事块】单次生成——你的任务是写出块级六段式提示词,
在单次生成内用官方多切点语法切镜。输出严格 JSON: {"h3_prompt": "块级六段式全文"}

结构(与单镜六段式同字段):
- subject_definitions: 组内全部登场主体各一行 <Subject N>(编号=块内首次登场序),
  角色行带 <Picture N> 参考(Picture 编号按【Picture 挂载表】直供,逐字用,禁自编号);
  场景行同样按挂载表;说话角色各配一行 <Audio N> 音色定义(编号按【Audio 挂载表】);
- summary: 一句块级摘要;
- retention_analysis: 每主体一行 (appears in [Shot N]);
- detailed_description: 开头 1 句风格句(照抄组内单镜六段式的风格句);随后按切点分段:
  [Shot 1](首段,无时码)→ 段正文;[Shot 2] At MM:SS.mmm(时码照抄【切点表】,一字不改)
  → 段正文……每段完整交代:景别/机位/主体屏幕位置与朝向/动作节拍/运镜句(官方动词句式,
  照抄该镜原句)/台词块;
- overall_soundscape / non_diegetic_music: 合并组内各镜,各 1-2 句。

铁律(违反=废片):
0. 切点形态唯一合法写法:首段 `[Shot 1]` 后【直接接段正文,绝不带 At 时码】;自第 2 段起
   `[Shot N] At MM:SS.mmm` 时码逐字照抄【切点表】。组内单镜六段式里的 At 时码是逐镜
   独立渲染时代的全片轴旧形态,【一律丢弃重写】,禁止照抄;
1. 台词块 <d>…</d> 逐字照抄组内各镜六段式的原文:一个不增一个不减一个不改,顺序按镜序;
   画外音句式(The narrator says in an off-screen voiceover / The quiet inner voice of 角色名
   says in an off-screen voiceover, in a 情绪 inward voice)照抄原句式;
2. 切点处画面与声音同步切换、干净利落;【块内段间禁写承接句】(opens holding the previous
   shot's closing framing 是跨视频接缝专用,块内切点是模型原生切镜,写了=矛盾);
3. 每段主体屏幕位置/朝向必须写(主体首次出现处);组内各镜的画面事实(人物/动作/光线/
   道具/结局状态)保持原意,禁止新增实体;
4. 说话者 (Sx) 编号与组内单镜六段式一致,禁重排;可见中文只在 <d> 内与角色名;
5. detailed_description 正文(不含台词)250-500 英文词,对白密集(≥3 句台词)可至 200 词;
6. <Picture N>/<Audio N> 只用挂载表给的编号,禁自创。"""


SCENE_RE = re.compile(r"【([^】]+)】")
D_TAG = re.compile(r"<d>.*?</d>", re.S)
CJK = re.compile(r"[\u4e00-\u9fff]")


def scene_of(shot):
    m = SCENE_RE.search(shot.get("action") or "")
    return m.group(1) if m else ""


def speakers_of(shot):
    """该镜说话人集合(台词说话者+旁白/内心不计——旁白音色独立不占绑定位)。"""
    out = set()
    for line in (shot.get("dialogue") or "").splitlines():
        line = line.strip()
        if not line or line.startswith("旁白"):
            continue
        m = re.match(r"[\(（]S\d+[\)）]", line)
        if m:
            line = line[m.end():]
        m = re.match(r"画外·?", line)
        if m:
            continue  # 画外无名者走声线描述
        m = re.match(r"([^：:]{1,12})[：:]", line)
        if m and not re.match(r"^\d+$", m.group(1)):
            out.add(m.group(1).strip())
    return out


def dur_of(shot):
    try:
        return int(shot.get("duration") or 5)
    except Exception:
        return 5


def propose_blocks(shots):
    """确定性分组:返回 [[id,...], ...](仅多镜组;单镜不列)。
    登场 characters 并集 ≤3(NiliX 渲染端 Picture/音色绑定挂载上限,口径与
    storyboard_check 契约 W6 一致——比「说话人」更严,说话人不登场不占槽)。"""
    blocks = []
    cur = []
    cur_sec = 0
    cur_chars = set()

    def flush():
        nonlocal cur, cur_sec, cur_chars
        if len(cur) >= 2:
            blocks.append(list(cur))
        cur, cur_sec, cur_chars = [], 0, set()

    for s in shots:
        d = dur_of(s)
        sc = scene_of(s)
        ch = set(s.get("characters") or [])
        ok = (
            cur
            and scene_of(cur[-1]) == sc
            and sc != ""
            and len(cur) < MAX_BLOCK_SHOTS
            and cur_sec + d <= MAX_BLOCK_SEC
            and len(cur_chars | ch) <= MAX_SPEAKERS
        )
        if not ok:
            flush()
        cur.append(s)
        cur_sec += d
        cur_chars |= ch
    flush()
    # 全静音氛围组只留 2 镜(3 镜纯空镜信息量低还占时长上限)
    out = []
    for b in blocks:
        has_speech = any((s.get("dialogue") or "").strip() or (s.get("narration") or "").strip() for s in b)
        if not has_speech and len(b) > 2:
            out.append(b[:2])
            out.append(b[2:])
        else:
            out.append(b)
    return [b for b in out if len(b) >= 2]


def cut_table(block):
    """切点表:镜号→累计时长时码(第 2 镜起)。"""
    rows = []
    cum = 0
    for i, s in enumerate(block):
        if i > 0:
            rows.append((s["shot_id"], "%02d:%02d.%03d" % (cum // 60, cum % 60, round((cum % 1) * 1000))))
        cum += dur_of(s)
    return rows, cum


def mount_table(block, cfg_unused=None):
    """Picture/Audio 挂载表:角色(登场并集,组头 Characters 优先)在前、场景在后——
    与 NiliX 渲染端 charRefNames→sceneRefName 挂载顺序一致。"""
    chars = []
    seen = set()
    for s in block:
        for c in (s.get("characters") or []):
            if c not in seen:
                seen.add(c)
                chars.append(c)
    scene = scene_of(block[0]) or ""
    rows = []
    for i, c in enumerate(chars[:3], 1):
        rows.append("<Picture %d> = 角色「%s」参考图" % (i, c))
    if scene:
        rows.append("<Picture %d> = 场景「%s」参考图" % (len(chars[:3]) + 1, scene))
    spk = []
    for s in block:
        for c in sorted(speakers_of(s)):
            if c not in spk:
                spk.append(c)
    arows = []
    for i, c in enumerate(spk[:3], 1):
        arows.append("<Audio %d> = 角色「%s」音色参考" % (i, c))
    return "\n".join(rows), "\n".join(arows) if arows else "(组内无具名说话角色,不写 Audio 定义)"


def validate(block, hp):
    ids = [s["shot_id"] for s in block]
    if len(ids) < 2:
        return "单镜非块"
    if sum(dur_of(s) for s in block) > MAX_BLOCK_SEC:
        return "时长和超15"
    for field in ("subject_definitions:", "summary:", "retention_analysis:",
                  "detailed_description:", "overall_soundscape:", "non_diegetic_music:"):
        if field not in hp:
            return "缺字段" + field
    dd = hp.split("detailed_description:", 1)[-1].split("overall_soundscape:", 1)[0]
    shot_marks = re.findall(r"\[Shot \d+\]", dd)
    if len(shot_marks) != len(ids):
        return "切点标记数 %d≠%d" % (len(shot_marks), len(ids))
    # 首段无时码
    if re.search(r"\[Shot 1\] At", dd):
        return "首段带时码"
    ats = re.findall(r"\[Shot (\d+)\] At (\d+):(\d+)\.(\d+)", dd)
    cum = 0
    want = []
    for i in range(1, len(ids)):
        cum += dur_of(block[i - 1])
        want.append(cum)
    if len(ats) != len(ids) - 1:
        return "At 时码数 %d≠%d" % (len(ats), len(ids) - 1)
    for i, (n, mm, ss, mmm) in enumerate(ats):
        t = int(mm) * 60 + int(ss) + int(mmm) / 1000
        if abs(t - want[i]) > 0.001:
            return "切点%d时码%.3f≠累计%.3f" % (i + 2, t, want[i])
    # 台词块多重集=组内单镜六段式 <d> 并集(逐字)
    want_ds = []
    for s in block:
        want_ds += D_TAG.findall(s.get("h3_prompt") or "")
    got_ds = D_TAG.findall(hp)
    if sorted(want_ds) != sorted(got_ds):
        return "台词块集合不一致(want %d got %d)" % (len(want_ds), len(got_ds))
    # 台词列契约(与 storyboard_check W5 同口径):组内分镜表 dialogue 列每句
    # 引号段必须在块内(单镜 <d> 偶有漏句,台词列才是分镜契约权威)
    for s in block:
        for line in ((s.get("dialogue") or "") + "\n" + (s.get("narration") or "")).splitlines():
            line = line.strip()
            if not line:
                continue
            head = re.sub(r"^(内心·[^：:]+|旁白|[\(（]?S\d+[\)）]?[^：:]*)[：:]", "", line).strip()
            for seg in [x.strip() for x in re.split(r'["“”]', head) if len(x.strip()) >= 4]:
                if seg[:6] not in hp:
                    return "台词列句「%s」未进块" % seg[:12]
    # 词数:下限=信息密度(对白密集放宽),上限按块规模分级(单镜 dd 已 250-350 词,
    # 2/3 镜合块自然翻倍;整体 prompt 仍受 ≤950 词总闸约束 ≈2200 tokens 官方上限)
    body_words = len(D_TAG.sub("", dd).split())
    dial_chars = sum(len(t) for t in got_ds)
    floor = 200 if dial_chars >= 40 else 250
    ceil = 600 if len(ids) <= 2 else 780
    if body_words < floor:
        return "dd 词数 %d<%d" % (body_words, floor)
    if body_words > ceil:
        return "dd 词数 %d>%d" % (body_words, ceil)
    if len(hp.split()) > 1250:
        return "整体词数 %d>1250(token 上限风险)" % len(hp.split())
    # 不新增中文(组内已有中文集合之外;情绪模板占位词泄漏也在此暴露)
    old_cjk = set(CJK.findall("".join((s.get("h3_prompt") or "") + (s.get("dialogue") or "") + (s.get("narration") or "") for s in block)))
    new_cjk = set(CJK.findall(hp)) - old_cjk
    if new_cjk:
        return "新增中文(%s;英文语境写英文,模板占位词「情绪」必须替换为具体英文情绪词)" % "".join(sorted(new_cjk))
    # 说话者标签并集不减
    old_sx = set(re.findall(r"\(S\d+\)", "".join(s.get("h3_prompt") or "" for s in block)))
    if old_sx - set(re.findall(r"\(S\d+\)", hp)):
        return "说话者标签丢失"
    return None


def build_user(block):
    pics, auds = mount_table(block)
    cuts, total = cut_table(block)
    cut_lines = "\n".join("镜 %d → 切点时码 %s" % (sid, tc) for sid, tc in cuts)
    parts = ["【组内镜头(镜序即段序)】"]
    for s in block:
        parts.append(json.dumps({k: s.get(k) for k in
                                 ("shot_id", "shot_size", "camera", "action", "dialogue",
                                  "narration", "characters", "light", "sound", "duration")},
                                ensure_ascii=False))
        parts.append("该镜单镜六段式(素材,风格句/台词块/运镜句从这里逐字取):\n" + (s.get("h3_prompt") or ""))
    parts.append("【Picture 挂载表(编号逐字用)】\n" + pics)
    parts.append("【Audio 挂载表(编号逐字用)】\n" + auds)
    parts.append("【切点表(时码一字不改)】\n" + cut_lines + "\n块总时长 %ds" % total)
    target = "380-550" if len(block) == 2 else "450-720"
    parts.append("【detailed_description 词数硬目标: %s 英文词(不含 <d> 台词块);"
                 "低于或超出该区间=废稿,程序会驳回;素材句可压缩合并,禁铺陈注水】" % target)
    return "\n\n".join(parts)


def normalize_hp(hp):
    """LLM 输出形态归一:六段式正文或嵌套 JSON(实测 deepseek 会输出
    {"subject_definitions":"...",...})都收,JSON 展平为标准六段文本。"""
    hp = hp.strip()
    if not hp.startswith("{"):
        return hp
    try:
        d = json.loads(hp)
    except Exception:
        return hp
    if not isinstance(d, dict) or "detailed_description" not in d:
        return hp
    order = ["subject_definitions", "summary", "retention_analysis",
             "detailed_description", "overall_soundscape", "non_diegetic_music"]
    out = []
    for k in order:
        if d.get(k):
            out.append(k + ":\n" + str(d[k]).strip())
    extra = {k: v for k, v in d.items() if k not in order}
    if extra:
        return hp  # 未知字段=形态不可靠,退回原文让校验驳回
    return "\n".join(out)


def gen_block(cfg, block, sem, cached=None):
    """LLM 生成一个块级六段式;cached=断点续跑已存结果直接复用。
    返回 (h3_prompt|None, 原因)。校验失败原因回灌重试一轮。"""
    if isinstance(cached, str) and cached:
        bad = validate(block, cached)
        if bad is None:
            return cached, None
    last = "LLM异常"
    user = build_user(block)
    for attempt in range(3):
        try:
            with sem:
                data = sr.llm_json(cfg, SYSTEM, user + ("" if attempt == 0 else
                           "\n\n【上一稿被程序校验驳回,原因: %s;针对性修正后重写】" % last),
                           temp=0.3 if attempt < 2 else 0.5)
        except Exception as e:
            last = "LLM异常:" + str(e)[:60]
            time.sleep(2)
            continue
        hp = normalize_hp((data.get("h3_prompt") or "").strip())
        if not hp:
            last = "空输出"
            continue
        bad = validate(block, hp)
        if bad is None:
            return hp, None
        last = bad
    return None, last


_progress_lock = threading.Lock()


def load_progress():
    try:
        return json.load(io.open(PROGRESS, encoding="utf-8"))
    except Exception:
        return {}


def save_progress(p):
    with _progress_lock:
        io.open(PROGRESS, "w", encoding="utf-8").write(json.dumps(p, ensure_ascii=False))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true", help="只统计分组,不调 LLM")
    ap.add_argument("--book", default="")
    ap.add_argument("--chapter", type=int, default=0, help="只处理指定章号")
    ap.add_argument("--limit", type=int, default=0, help="最多处理文件数(试点用)")
    ap.add_argument("--workers", type=int, default=8)
    ap.add_argument("--force", action="store_true", help="文件已有 narrative_blocks 也重做")
    args = ap.parse_args()

    cfg = sr.load_api(None)
    books = sorted(os.listdir(NOVEL_ROOT)) if not args.book else [args.book]
    progress = load_progress()
    tot_files = tot_blocks = tot_shots = tot_covered = 0
    done_files = 0
    for book in books:
        bdir = os.path.join(NOVEL_ROOT, book)
        sbs = sorted(glob.glob(os.path.join(bdir, "素材", "分镜脚本", "*.json")))
        if args.chapter:
            sbs = [f for f in sbs if re.search(r"第0*%d章" % args.chapter, os.path.basename(f))]
        b_blocks = b_cover = 0
        for sb in sbs:
            if args.limit and done_files >= args.limit:
                break
            if sb.endswith(".bak"):
                continue
            try:
                data = json.load(io.open(sb, encoding="utf-8"))
            except Exception as e:
                print("  [skip] %s 解析失败: %s" % (os.path.basename(sb), e))
                continue
            if data.get("narrative_blocks") and not args.force:
                continue
            shots = data.get("shots") or []
            blocks = propose_blocks(shots)
            cov = sum(len(b) for b in blocks)
            tot_files += 1
            tot_blocks += len(blocks)
            tot_covered += cov
            tot_shots += len(shots)
            b_blocks += len(blocks)
            b_cover += cov
            if args.dry_run:
                continue
            done_files += 1
            key = os.path.join(book, os.path.basename(sb))
            pdone = progress.get(key, {})
            results = {}
            fails = []
            sem = threading.Semaphore(args.workers)
            with ThreadPoolExecutor(max_workers=args.workers) as ex:
                futs = {ex.submit(gen_block, cfg, b, sem, pdone.get(str(i))): i for i, b in enumerate(blocks)}
                for fu in as_completed(futs):
                    i = futs[fu]
                    hp, why = fu.result()
                    if hp:
                        results[i] = hp
                        pdone[str(i)] = hp  # 断点续跑:存结果(成功块重跑零 LLM)
                    else:
                        fails.append((blocks[i][0]["shot_id"], why))
                        pdone.pop(str(i), None)
            # 部分成功也落盘(块相互独立,失败块对应镜自动逐镜渲染兜底);
            # 成功块全部通过双重校验(工具 W1-W5+台词列 / 检查器 W 契约同口径)
            if not results:
                print("  [keep-solo] %s: %d/%d 块全败,保留逐镜(失败: %s)" %
                      (os.path.basename(sb), len(blocks), len(blocks),
                       "; ".join("%d镜:%s" % x for x in fails[:4])))
                progress[key] = pdone
                save_progress(progress)
                continue
            data["narrative_blocks"] = [
                {"shots": [s["shot_id"] for s in blocks[i]], "h3_prompt": results[i]}
                for i in sorted(results)
            ]
            cov = sum(len(blocks[i]) for i in results)
            if not os.path.exists(sb + ".bak"):
                io.open(sb + ".bak", "w", encoding="utf-8", newline="").write(
                    io.open(sb, encoding="utf-8").read())
            io.open(sb, "w", encoding="utf-8", newline="").write(
                json.dumps(data, ensure_ascii=False, indent=2))
            progress[key] = pdone if len(results) < len(blocks) else {"all": True}
            save_progress(progress)
            print("  [done] %s: %d/%d 块覆盖 %d/%d 镜%s" %
                  (os.path.basename(sb), len(results), len(blocks), cov, len(shots),
                   "" if not fails else "(失败块逐镜:" + ";".join("%d" % f[0] for f in fails) + ")"))
        if tot_files:
            print("[%s] 候选文件 %d | 块 %d | 覆盖 %d 镜" % (book, tot_files, b_blocks, b_cover))
    mode = "(dry-run,未落盘)" if args.dry_run else "(已执行)"
    if tot_shots:
        print("合计: 文件 %d | 块 %d | 覆盖 %d/%d 镜(%.0f%%) %s" %
              (tot_files, tot_blocks, tot_covered, tot_shots, tot_covered * 100.0 / tot_shots, mode))
    return 0


if __name__ == "__main__":
    sys.exit(main())
