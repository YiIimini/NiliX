#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""分镜脚本全面重写工具(2026-08-30 五问整改·用户规则:按新规则重构全部分镜脚本)

按新拆镜密度(每 80-130 字一镜,内容完整优先,拆镜数不设上限)+ 全部新规则
(位置锚定/动作必达/收尾动作/防重复/内心Q版动作演绎/景别运镜匹配/音色身份短语/
链式衔接/全书视角)逐章重新生成分镜脚本。

两阶段:
  ① LLM 生成分镜表(JSON:episode_bridge + shots[])
  ② 逐镜生成六段式 H3 提示词(上镜收尾链式承接,章内串行)
章间并发(默认 6 workers);resume 跳过已完成章;每镜程序化校验
(六段式六字段完整/<d> 台词同步/时间戳递增),不合格自动重试一次。

用法:
  python tools/storyboard_regen.py [--books 书1,书2] [--limit N] [--workers 6]
      [--resume] [--chapter N] [--logdir tools/logs]
"""
import argparse
import base64
import glob
import json
import os
import re
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed

try:
    import requests
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM
except ImportError:
    print("需要 requests + cryptography: pip install requests cryptography")
    sys.exit(1)

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SETTINGS = os.path.join(ROOT, "settings.json")
SECRET = os.path.join(ROOT, ".secret.key")
SKILL_REF = r"C:\Users\Administrator\.agents\skills\shuangwen-novel\references"
SKILL_SCRIPTS = r"C:\Users\Administrator\.agents\skills\shuangwen-novel\scripts"

BOOKS = ["人算不如天算，天算不如算盘", "全小区就我一个活人", "废铁按斤卖，雷劫排队充",
         "我的影子会咬人", "轮回欠费九世", "金丹一万重"]
# 排除《杂毛神兽》(用户点名先不管)

_lock = threading.Lock()
LOG_BUF = []


def log(msg):
    ts = time.strftime("%H:%M:%S")
    with _lock:
        LOG_BUF.append(f"[{ts}] {msg}")
        print(f"[{ts}] {msg}", flush=True)


# ---------------- 配置与 LLM ----------------

def load_api(model_override=None):
    """解密 settings.json 的 llm.api_key(AES-GCM, .secret.key 为密钥)"""
    key = open(SECRET, "rb").read()
    d = json.load(open(SETTINGS, encoding="utf-8"))
    llm = d.get("llm", {})
    enc = llm.get("api_key", "")
    if enc.startswith("enc:"):
        raw = base64.b64decode(enc[4:])
        nonce, ct = raw[:12], raw[12:]
        api_key = AESGCM(key).decrypt(nonce, ct, None).decode()
    else:
        api_key = enc
    # 模型:--model 优先;默认 deepseek-chat(2026-08-30 实测:settings 的
    # deepseek-v4-flash 是推理模型,长输入推理吃 17k-20k tokens、单次 2-5 分钟
    # 且 content 常空返回——批量任务不可用;deepseek-chat 19s/次稳定)
    return {
        "api_key": api_key,
        "base_url": (llm.get("base_url") or "https://api.deepseek.com").rstrip("/"),
        "model": model_override or "deepseek-chat",
        "max_tokens": 16384,
        "temperature": float(llm.get("temperature") or 0.4),
    }


def llm_chat(cfg, system, user, temp=None, max_tokens=None):
    """单次补全,JSON 模式,指数退避重试(429/5xx/网络抖动)"""
    body = {
        "model": cfg["model"],
        "temperature": temp if temp is not None else cfg["temperature"],
        "max_tokens": max_tokens or cfg["max_tokens"],
        "messages": [{"role": "system", "content": system},
                     {"role": "user", "content": user}],
        "response_format": {"type": "json_object"},
        "stream": False,
    }
    last = None
    for attempt in range(1, 4):
        try:
            r = requests.post(cfg["base_url"] + "/chat/completions",
                              headers={"Authorization": "Bearer " + cfg["api_key"]},
                              json=body, timeout=600)
            if r.status_code == 429 or r.status_code >= 500:
                last = f"HTTP {r.status_code}: {r.text[:120]}"
                time.sleep(5 * attempt)
                continue
            if r.status_code != 200:
                last = f"HTTP {r.status_code}: {r.text[:200]}"
                time.sleep(3)
                continue
            data = r.json()
            content = data["choices"][0]["message"]["content"]
            return content
        except Exception as e:
            last = str(e)
            time.sleep(5 * attempt)
    raise RuntimeError(f"LLM 调用失败: {last}")


def llm_json(cfg, system, user, temp=None):
    """LLM 输出解析为 JSON(剥代码块围栏/前后杂文)"""
    text = llm_chat(cfg, system, user, temp=temp)
    text = text.strip()
    text = re.sub(r"^```(?:json)?\s*|\s*```$", "", text)
    try:
        return json.loads(text)
    except Exception:
        # 容错:截取首个 { 到最后一个 }
        i, j = text.find("{"), text.rfind("}")
        if i >= 0 and j > i:
            return json.loads(text[i:j + 1])
        raise


# ---------------- 上下文读取 ----------------

def read_utf8(p):
    try:
        return open(p, encoding="utf-8").read()
    except UnicodeDecodeError:
        return open(p, encoding="gbk", errors="replace").read()


def find_chapters(book_dir):
    """扫描 正文/<卷>/第N章_*.md → [(章号, 路径)] 按章号排序"""
    out = []
    for v in glob.glob(os.path.join(book_dir, "正文", "*", "第*章_*.md")):
        m = re.search(r"第(\d+)章_", os.path.basename(v))
        if m:
            out.append((int(m.group(1)), v))
    out.sort()
    return out


def find_storyboard(book_dir, ch):
    """找该章现有分镜脚本(JSON 优先,md 兼容),作为衔接输入"""
    for f in glob.glob(os.path.join(book_dir, "素材", "分镜脚本", f"第{ch}章_*.json")):
        return f
    for f in glob.glob(os.path.join(book_dir, "素材", "分镜脚本", f"第{ch}章_*.md")):
        return f
    return None


def parse_char_cards(book_dir):
    """人物生成提示词.md → {角色名: {mem: 记忆点, voice: 音色行, prompt: 英文提示词}}"""
    cards = {}
    f = os.path.join(book_dir, "素材", "人物生成提示词.md")
    if not os.path.exists(f):
        return cards
    text = read_utf8(f)
    cur = None
    for line in text.splitlines():
        m = re.match(r"^##\s*\d+\.\s*(.+?)\s*$", line.strip())
        if m:
            cur = m.group(1).strip()
            cards[cur] = {"mem": "", "voice": "", "prompt": ""}
            continue
        if cur is None:
            continue
        ls = line.strip()
        if ls.startswith("- 记忆点") or ls.startswith("- 记忆"):
            cards[cur]["mem"] = ls.split("：", 1)[-1].split(":", 1)[-1].strip()
        elif ls.startswith("- 音色") or ls.startswith("- 声线") or ls.startswith("- 方言"):
            cards[cur]["voice"] = ls.split("：", 1)[-1].split(":", 1)[-1].strip()
        elif ls.startswith("```"):
            # 代码块内的英文提示词
            inblock = True
            blk = []
            for l2 in text.splitlines()[text.splitlines().index(line) + 1:]:
                if l2.strip().startswith("```"):
                    break
                blk.append(l2)
            if blk:
                cards[cur]["prompt"] = "\n".join(blk).strip()
    return cards


def parse_scene_cards(book_dir):
    """场景提示词.md → {场景名: 描述首行}"""
    scenes = {}
    f = os.path.join(book_dir, "素材", "场景提示词.md")
    if not os.path.exists(f):
        return scenes
    text = read_utf8(f)
    cur = None
    for line in text.splitlines():
        m = re.match(r"^#{1,3}\s*(.+)", line.strip())
        if m and not m.group(1).startswith("场景"):
            cur = m.group(1).strip()
            scenes[cur] = ""
            continue
        if cur and not scenes[cur] and line.strip():
            scenes[cur] = line.strip()[:120]
    return scenes


def extract_style_sentence(book_dir):
    """全局风格句:优先取旧脚本头部「> 全局风格句:」,回退渲染提示词总集"""
    for f in glob.glob(os.path.join(book_dir, "素材", "分镜脚本", "*.md")):
        for line in read_utf8(f).splitlines():
            m = re.search(r"全局风格句[:：]\s*(.+)", line)
            if m:
                return m.group(1).strip()
    f = os.path.join(book_dir, "素材", "渲染提示词总集.md")
    if os.path.exists(f):
        for line in read_utf8(f).splitlines():
            if "The target video uses" in line:
                return line.strip()
    return "The target video uses a realistic live-action film style set in a modern Chinese city, with photorealistic human characters, natural skin texture, dramatic volumetric lighting, and cinematic camera language."


def extract_plot_outline(book_dir):
    """设定集大纲节选(全书视角,截断 3000 字)"""
    for f in glob.glob(os.path.join(book_dir, "设定集", "*.md")):
        t = read_utf8(f)
        # 优先大纲/主线段落
        i = t.find("大纲")
        seg = t[i:i + 3000] if i >= 0 else t[:3000]
        return re.sub(r"\n{3,}", "\n\n", seg)
    return ""


def old_chapter_ending(sb_path):
    """上一章脚本收尾 1-2 镜(画面+台词+六段式收尾句),供本章首镜链式承接。
    2026-08-31 JSON 格式:shots[] 直接读;md 兼容走旧解析"""
    if not sb_path:
        return ""
    t = read_utf8(sb_path)
    if sb_path.endswith(".json"):
        try:
            d = json.loads(t)
            shots = d.get("shots") or []
            part = []
            for s in shots[-2:]:
                part.append(f"镜{s.get('shot_id')} 画面:{str(s.get('action',''))[:150]} 台词:{str(s.get('dialogue',''))[:80]}")
                hp = s.get("h3_prompt") or ""
                m = re.search(r"detailed_description:\s*(.*?)(?:\noverall_soundscape:|\Z)", hp, re.S)
                if m:
                    part.append("六段式收尾:" + m.group(1).strip()[-260:])
            return "\n".join(part)
        except Exception:
            return ""
    # md 兼容(旧格式)
    rows = re.findall(r"^\|\s*(\d+)\s*\|[^|]*\|[^|]*\|([^|]*)\|([^|]*)\|", t, re.M)
    part = []
    for r in rows[-2:]:
        part.append(f"镜{r[0]} 画面:{r[1].strip()[:150]} 台词:{r[2].strip()[:80]}")
    blocks = re.findall(r"### Shot \d+[^\n]*\n```(.*?)```", t, re.S)
    for b in blocks[-2:]:
        m = re.search(r"detailed_description:\s*(.*?)(?:\noverall_soundscape:|\Z)", b, re.S)
        if m:
            part.append("六段式收尾:" + m.group(1).strip()[-260:])
    return "\n".join(part)


def next_chapter_opening(book_dir, ch):
    """下一章正文开头 300 字(章尾钩子)"""
    chapters = find_chapters(book_dir)
    for n, p in chapters:
        if n == ch + 1:
            t = read_utf8(p)
            t = re.sub(r"^#.*\n", "", t)
            return t[:300]
    return ""


# ---------------- 规则模板 ----------------

# 精简 system(2026-08-30 实测:完整三件套模板 27KB 触发 deepseek-v4-flash 空返回
# 抖动,精简核心规则 2.5k 字 + max_tokens=16384 稳定 3/3;规则全部来自
# 分镜派发模板/H3分镜脚本文档模板/漫剧H3渲染规范卡,含 2026-08-30 全部新规则)
COMPACT_SYSTEM = """你是资深漫剧分镜师。为小说章节生成 H3 分镜脚本(漫剧分镜,每章=1集)。

【拆镜硬性规范】
1. 完整拆镜不设上限(细中细·2026-09-01 用户规则升级):逐句/逐段拆镜,覆盖全文情节、对白、动作、情绪;每 50-80 字一镜(正文/镜数 ≤80),内容表达完整优先,宁多镜不压缩,拆镜数不设上限;一个节拍放不下就拆两镜,长对白拆多句、多动作拆多镜;动作分解(关键动作起步/进行/完成 2-3 镜)+神态微表情特写入镜+记忆点道具特写镜;段落级穷尽(正文每段至少一镜);单镜 ≤15s,时长按内容定(对白镜字数÷5 取整,4-15s;动作/空镜 4-8s),禁止全部同一时长。
2. 对白逐字入镜:正文每个引号对白必须进台词列 (S#)角色名:"原文",原词原标点(半角/全角与正文逐字一致,禁止统一转全角),单句 ≤20 字(超长拆多句,只按语义断句增删改写零容忍);说话人 ID 按发声顺序编号且跨镜稳定。
3. 叙述五级漏斗:①画面能表达→旁白留空(不复述画面) ②背景/设定→画外群众议论(台词列写 (S#)画外·路人:"…",≤2人≤2句每句≤20字) ③角色心理→对白化或内心·角色名(正角Q版) ④时空跳跃→画面文字卡 ⑤兜底单句旁白(≤15字,每集≤3句);100+字整段叙述塞一镜禁止。
4. 画面列=可渲染视觉句:谁在哪/做什么/光从哪来/色调;开头必标【场景名】(与场景卡逐字一致);在场角色写角色卡名(首次登场可括注),禁纯代称;站位写清(谁左谁右/距离层次)。
5. 每镜六段式完整(Ref2VA 官方格式,全英文,仅<d>内与可见文字中文):
   subject_definitions(每个登场角色一行 <Subject N> is ... in <Picture N>;<Audio N> is the voice-timbre reference for <Subject M> (Sx), with 音色身份短语, containing a spoken voiceover)
   summary / retention_analysis / detailed_description / overall_soundscape / non_diegetic_music
   detailed_description:自然风格句开头(用输入的全局风格句)→[Shot 1]开场承接(上镜收尾构图约1秒,continues seamlessly from the previous shot)→主体外观位置(每个角色首次清晰出现写屏幕位置:左/中/右+前景/中景/背景+朝向 facing camera/left/right,背对写 turned away)→动作(每镜≥1个明确动作动词幅度 clearly visible;爆发镜大幅快速带惯性、常态镜中幅自然、情绪镜微表情三层物理拆解;结尾落完成态动作禁静止定格收尾)→运镜三要素自然英语(Push In/Pull Out/Pan/Track/Tilt/Pedestal/Arc/Static/POV 等+with small/large amplitude+at slow/fast speed)→光影→台词 <d>[Chinese] 中文原文</d>。
6. 台词/旁白写法:画面角色 <Subject N> (Sx) says: <d>[Chinese] 原文</d>;旁白/内心 The narrator says in an off-screen voiceover: <d>[Chinese] 原文</d> while the on-screen characters' lips remain completely closed.;d 标签内禁止带「内心·/旁白:」前缀与中文引号,同一句台词/旁白/内心在整条提示词中只能出现一次;说话者首次出现带音色身份短语(年龄段+性别+音色质感+语速,如 a young man with a clear steady voice),同一角色跨镜复用同一短语。
7. 内心独白:正角写 内心·角色名:内容(画面=原角色静止+Q版小人动作演绎内心语义:数数掰指头/思考托腮/惊讶瞪眼捂嘴/担忧抱膝揪衣角/得意叉腰晃头);反派/功能配角=写实镜头+画外音;六段式 Q 版单独一行 Subject 并引用其 Q 版参考图。
8. 登场角色 ≤3/镜(超3人拆镜分摊;群像用画外议论/背影剪影消化);运镜与景别匹配(特写近景→推/摇/固定小幅;中景→推拉横移跟移;全景→横移跟移升降;远景→升降缓推航拍;对话对峙→固定+缓慢微摇禁纯静止);状态变化镜写起点状态→变化过程;画面禁数字(年龄/数量/年份会被画成画面文字);缺席否定句列全。
9. 链式衔接:非首镜 detailed_description 开头承接上镜收尾构图约1秒,人物安排=上镜收尾(凭空换构图会 union 多出人脸),换位先写动作过渡(无过渡禁止左右翻转);静止 hold 段写微小动作(a breath/a weight shift/an eyeline change/fabric or hair movement);接缝时间预算:动作节拍按早 0.92s 预算。
10. 电影化拆镜:镜头分建立/动作/反应/细节四类搭配,对白戏≥2种景别交替(说话人近景+听者反应特写),情绪戏/哭戏/内心挣扎近景特写,过肩=对峙/低机位=威压/高机位=弱势;全章疏密(连续3镜同节奏必须调整);画面叙事优先(道具/动作/环境能表达的别写对白旁白)。【动作拆小·2026-09-01 知识库五步导演法】复杂动作拆成 3-6 个连续可观察子动作(如 转向→按剑→拔剑→出鞘→收势),禁止转身拔剑战斗一笔带过;动作越复杂越减少同镜叠加——大幅移动+说话+复杂运镜+场景变化同镜=难度爆炸,拆镜或改画外旁白。【单一运镜·2026-09-01】每镜只安排一种主要镜头运动(推/拉/摇/移/跟/环绕/固定选一),禁止堆叠互相冲突的运动关键词。【配乐避让·2026-09-01】有对白镜 non_diegetic_music 写对白出现时降低音量,对白结束后短暂增强(稀疏低音弦乐铺开→对白时压低→结束后回升);配乐禁止写情绪形容词(悲伤/史诗感),写乐器+速度+节奏+动态变化;对白只进 detailed_description 的 <d>,不写进 overall_soundscape。
11. 六段式 [Shot N] At MM:SS.mmm 时间戳按镜头时长累进严格递增,首镜无时间戳。
12. 章头写「本章衔接」元数据:上一章收尾/本章开场承接/全书推进位(铺垫/回收/转折/高潮)/章尾钩子/下一章入口;伏笔必上镜(道具/台词/表情特写),回收章必回收(同名道具/同款机位/同句台词回响)。

【输出】严格 JSON,不许输出 JSON 以外的任何内容。"""


def build_system():
    """组装 system:精简核心规则(实测稳定;完整三件套触发 API 空返回抖动)"""
    return COMPACT_SYSTEM


# ---------------- 生成 ----------------

TABLE_SCHEMA = """{"episode_bridge": {"prev_ending": "上一章收尾状态(结尾画面/人物位置/悬念)", "opening_beat": "本章开场承接(首镜从它延续或显式转场)", "closing_hook": "本章章尾钩子(悬念/未竟动作/情绪余韵)", "position": "本章在全书的推进位(铺垫/回收/转折/高潮)", "next_entry": "下一章入口"}, "shots": [{"shot_id": 1, "shot_size": "景别", "camera": "运镜(类型+幅度+速度,如 缓推（Push In, small, slow）/固定)", "action": "【场景名】画面内容(可渲染·光声味,站位写清谁左谁右/谁对谁)", "dialogue": "台词/旁白(带 ID,如 (S1)角色名:\"...\"/内心·角色名:\"...\"/旁白：.../无;弹幕/评论只写内容如 (S2)画外·路人:\"我押程野，躺赢\",昵称禁止写进台词列)", "characters": ["登场角色名"], "light": "光影", "sound": "音效", "duration": 5}...]}"""


def gen_table(cfg, sys_text, book, ch, ch_title, chapter_text, cards, scenes,
              style, outline, prev_ending, next_open, temp=0.3, feedback=""):
    """阶段①:LLM 生成分镜表 JSON"""
    card_summary = []
    for name, c in cards.items():
        card_summary.append(f"- {name}: 记忆点[{c['mem']}] 音色[{c['voice']}]")
    scene_summary = []
    for name, desc in scenes.items():
        scene_summary.append(f"- {name}: {desc}")
    user = f"""【任务】为《{book}》第{ch}章《{ch_title}》生成分镜表 JSON(第{ch}集)。

【本章正文(全文,拆镜唯一依据)】
{chapter_text}

【全书大纲(节选,带着全书视角切本章)】
{outline[:3000]}

【角色卡(全部,含音色行)】
{chr(10).join(card_summary)}

【场景卡(全部)】
{chr(10).join(scene_summary)}

【全局渲染风格句】
{style}

【上一章收尾(本章首镜必须从它延续或显式转场)】
{prev_ending or "(全书首章,直接建立世界)"}

【下一章正文开头(本章收尾要为它留钩子)】
{next_open or "(全书末章)"}

【输出 JSON(严格按此结构,不许增删字段)】
{TABLE_SCHEMA}"""
    # 正文对白单元列表直接供 LLM 照抄(2026-08-30:LLM 自读正文常把半角标点转
    # 全角,check 覆盖比对(只剥全角标点)即失败——列表直供,原词原标点零改动)
    dlg_list = extract_dialogues_like_check(chapter_text)
    if dlg_list:
        user += "\n\n【本章全部正文对白(必须逐字进入台词列,原词原标点,禁止任何改动/转全角)】\n" + \
                "\n".join("「%s」" % d for d in dlg_list)
    if feedback:
        user += "\n\n【上一版质检反馈·必须修正】以下正文对白未逐字进入台词列(漏挂或标点不一致)。本次生成必须逐字包含(从上方对白列表复制,原词原标点):\n" + feedback
    data = llm_json(cfg, sys_text, user, temp=temp)
    shots = data.get("shots") or []
    if not shots:
        raise RuntimeError("分镜表为空")
    return data


def gen_shot(cfg, sys_text, book, ch, ch_title, shot, all_shots, cards, scenes,
             style, prev_prompt_tail, temp=0.3, feedback=""):
    """阶段②:LLM 生成单镜六段式 H3 提示词"""
    # 登场角色完整卡
    char_full = []
    for name in shot.get("characters") or []:
        c = cards.get(name, {})
        char_full.append(f"## {name}\n记忆点:{c.get('mem','')}\n音色:{c.get('voice','')}\n提示词:{c.get('prompt','')[:600]}")
    scene_name = ""
    m = re.match(r"【([^】]+)】", shot.get("action", ""))
    if m:
        scene_name = m.group(1)
    scene_desc = scenes.get(scene_name, "")
    shot_json = json.dumps(shot, ensure_ascii=False)
    ctx_json = json.dumps([{k: s.get(k) for k in ("shot_id", "shot_size", "camera", "action", "dialogue", "duration")} for s in all_shots], ensure_ascii=False)
    user = f"""【任务】为《{book}》第{ch}章《{ch_title}》第 {shot.get('shot_id')} 镜生成【完整六段式 H3 提示词】(Ref2VA 官方格式)。
【本镜分镜表行】
{shot_json}

【本章分镜表(上下文,助你理解场景/人物连续性,只写本镜六段式)】
{ctx_json}

【本镜登场角色卡】
{chr(10).join(char_full) or "(无角色卡,按 action 描述自拟)"}

【本镜场景卡】
{scene_name}: {scene_desc}

【全局渲染风格句】(detailed_description 开头用)
{style}

【链式衔接(本镜开头承接它约 1 秒;首镜=上一章收尾)】
{prev_prompt_tail or "(全书/本章首镜,直接建立开场构图)"}

【六段式硬规范】subject_definitions(每个登场角色一行,<Subject N> is ... in <Picture N>,音色定义行 <Audio N> is the voice-timbre reference for <Subject M> (Sx), with 音色身份短语, containing a spoken voiceover)/summary(必须以 [reference generation] 开头)/retention_analysis/detailed_description(自然风格句开头→[Shot {shot.get('shot_id')}]开场承接(首镜无时间戳,后续镜写 [Shot N] At MM:SS.mmm)→人物屏幕位置+朝向(左中右+前景中景背景)→动作幅度分级(爆发镜大幅/常态中幅/情绪镜微表情)→运镜三要素自然英语→光影→台词 <d>[Chinese] 中文原文</d>→收尾完成态动作,禁止静止定格收尾)/overall_soundscape/non_diegetic_music 六字段完整;台词逐字入 <d>(禁止带「内心·/旁白:」前缀与引号,同一句只出现一次);旁白/内心用 The narrator says in an off-screen voiceover: <d>…</d> while the on-screen characters' lips remain completely closed.;画面角色写 <Subject N> (Sx) says: <d>…</d>;六段全英文(subject_definitions/summary/retention_analysis/overall_soundscape/non_diegetic_music 禁止任何中文字符,仅 <d> 内与画面可见文字可中文);【内心戏镜·Q版强制】本镜台词含「内心·」时,subject_definitions 必须为 Q 版/迷你形象单独定义一行 Subject(写 chibi/miniature/Q-version 并引用其 Q 版参考图 <Picture>),detailed_description 画面主体=Q 版形象动作演绎内心语义。

【输出格式·硬约束】h3_prompt 必须包含且只包含以下 6 个字段名(小写英文+冒号,每字段一段,禁止折叠成一行):
subject_definitions:
summary:
retention_analysis:
detailed_description:
overall_soundscape:
non_diegetic_music:
只输出这 6 个字段,禁止只写 detailed_description 单段、禁止输出 JSON 以外的任何内容。

【输出 JSON】{{"h3_prompt": "六段式全文(不含```围栏)"}}"""
    if feedback:
        user += "\n\n【上一版质检反馈·必须修正】上一版输出存在以下问题,本次输出必须全部修正:\n" + feedback
    data = llm_json(cfg, sys_text, user, temp=temp)
    hp = (data.get("h3_prompt") or "").strip()
    if not hp:
        raise RuntimeError("六段式为空")
    return hp


# ---------------- 校验 ----------------

REQ_SECTIONS = ["subject_definitions:", "summary:", "retention_analysis:",
                "detailed_description:", "overall_soundscape:", "non_diegetic_music:"]


def extract_dialogues_like_check(text):
    """仿 storyboard_check.extract_dialogues:中文弯引号或 ASCII 直引号成对提取"""
    out = re.findall(r"“[^”]{2,}”", text)
    for ln in text.splitlines():
        parts = ln.split('"')
        for i in range(1, len(parts) - 1, 2):
            seg = parts[i].strip()
            if len(re.findall(r"[\u4e00-\u9fff]", seg)) >= 2:
                out.append(seg)
    return out


def is_speech_context(text, d):
    """正文引号单元是否为真对白:单元后 20 字内出现言语动词(说/喊/问/道/嚷/
    叫/念/答/吼/叹)即对白;「的"黑色剪纸"」「叫"预报中的全城雷雨"」这类
    强调词/专名豁免;「还想写句"…",想想,没写」这类未发生的心理台词豁免
    (LLM 判断正确,check 误报边界)"""
    i = text.find(d)
    if i < 0:
        return True  # 找不到上下文,保守按对白处理
    tail = text[i + len(d):i + len(d) + 20]
    if re.search(r"没写|没念|没喊|没说|没叫|没嚷|没答|没吼|没叹|没讲|想写|想喊|想说|想叫|没来得及|咽了回去|吞了回去|想想，没|想想,没", tail):
        return False
    return bool(re.search(r"说|喊|问|道|嚷|叫|念|答|吼|叹|讲|嘀咕|嘟囔", tail))


def validate_table_dialogues(chapter_text, shots):
    """分镜表台词列 vs 正文对白覆盖(2026-08-30:对齐 storyboard_check 口径——
    check 的缺失判定是「正文对白单元不在全文(sb)也不在台词列也不在 <d>」,
    名词强调/画面词(如「黑色剪纸」)出现在画面列即视为覆盖;只查台词列会
    误判漏挂导致分镜表反复重试失败。返回缺失列表(非空=须重试)"""
    dialogs = extract_dialogues_like_check(chapter_text)
    if not dialogs:
        return []
    col = norm_text("".join(re.findall(r'"[^"]+"|“[^”]+”', "\n".join(s.get("dialogue") or "" for s in shots))))
    full = norm_text("\n".join(((s.get("dialogue") or "") + " " + (s.get("action") or "")) for s in shots))
    missing = []
    for d in dialogs:
        if len([c for c in d if not c.isspace()]) < 4:
            continue  # 2-3 字文学性引号/强调词豁免(check 子串命中兜底)
        dn = norm_text(d)
        if dn in col or dn in full:
            continue
        subs = [norm_text(x) for x in re.split(r"(?<=[。！？])", d)]
        subs = [x for x in subs if len(x) >= 4]
        if subs and all(x in col or x in full for x in subs):
            continue
        if not is_speech_context(chapter_text, d):
            continue  # 非对白引号词(「的"黑色剪纸"」)豁免,LLM 无义务保留
        missing.append(d[:20])
    return missing


def fix_dialogue_punct(dial, dialogs):
    """台词列引号单元标点修复(2026-08-30:正文半角/全角标点混合,check 的 norm
    只剥全角标点——LLM 转全角即覆盖比对失败。引号单元与正文对白按内容
    (全剥)匹配,命中则替换为正文原文,标点以正文为准。拆句后的子句同样匹配"""
    if not dialogs:
        return dial

    def repl(m):
        inner = m.group(2) or m.group(3)
        nin = norm_text(inner)
        if len(nin) < 2:
            return m.group(0)
        for d in dialogs:
            if nin == norm_text(d):
                return m.group(1) + '"' + d + '"'
        # 拆句子句匹配:内容为某正文单元的子句 → 取该单元对应子句原文
        for d in dialogs:
            dn = norm_text(d)
            subs = [x.strip() for x in re.split(r"(?<=[。！？])", d)]
            for sub in subs:
                if len(norm_text(sub)) >= 4 and nin == norm_text(sub):
                    return m.group(1) + '"' + sub + '"'
        return m.group(0)

    return re.sub(r'((?:\(S\d+[^：:]*|内心·[^：:]*|旁白)[：:]?)(?:"([^"]+)"|“([^”]+)”)', repl, dial)


def fix_d_punct(hp, dialogs):
    """六段式 <d> 内容标点修复(与 fix_dialogue_punct 同源,正文标点为准)"""
    if not dialogs or "<d>" not in hp:
        return hp

    def repl(m):
        inner = m.group(1)
        nin = norm_text(inner)
        if len(nin) < 2:
            return m.group(0)
        for d in dialogs:
            if nin == norm_text(d):
                return "<d>" + d + "</d>"
        for d in dialogs:
            for sub in [x.strip() for x in re.split(r"(?<=[。！？])", d)]:
                if len(norm_text(sub)) >= 4 and nin == norm_text(sub):
                    return "<d>" + sub + "</d>"
        return m.group(0)

    return re.sub(r"<d>(?:\[Chinese\]|\[中文\])?([^<]+)</d>", repl, hp)


def norm_text(t):
    """台词/提示词归一:去空白与全部常见标点(全半角),比对用"""
    return re.sub(r"[\s,，。.．!！?？\-—·、:；:;\"'‘’“”()\[\]{}<>《》【】~～…]+", "", t or "")


def norm_check(t):
    """storyboard_check.norm 同口径:只剥全角标点(半角保留)——正文半角标点
    的对白必须保持半角,否则 check 对白覆盖比对失败(2026-08-30 实锤)"""
    return re.sub(r"[\s，。！？；：、—…·“”‘’'\"()\[\]（）｛｝]+", "", t or "")


def split_oversized_shots(shots):
    """语音预算兜底拆镜(2026-08-31:LLM 对长叙述对白反复不拆——反馈 3 次只把
    台词挪到别的镜。程序按引号单元把超 75 字台词列的镜拆成多镜,新增镜画面=
    角色继续说/听者反应,保证每镜 ≤75 字(15s 上限)。与技能「禁止机械拆镜」
    不冲突:这是对 LLM 已拆镜结果的预算修正,不改台词内容"""
    out = []
    nxt_id = max((int(s.get("shot_id") or 0) for s in shots), default=0) + 1
    for s in shots:
        dial = s.get("dialogue") or ""
        n = len(re.findall(r"[\u4e00-\u9fff\u3000-\u303f\uff00-\uffef]", dial))
        if n <= 75:
            out.append(s)
            continue
        blocks = re.findall(r'(?:\(S\d+[^：:]*|内心·[^：:]*|旁白)[：:]?(?:"[^"]+"|“[^”]+”)', dial)
        if len(blocks) < 2:
            # 无引号/单块(朗读型对白如账本):按句末标点切,重建「前缀+句组」
            m = re.match(r'((?:\(S\d+[^：:]*|内心·[^：:]*|旁白)[：:]?)', dial)
            pre = m.group(1) if m else ""
            body = dial[len(pre):] if m else dial
            segs = [x for x in re.split(r"(?<=[。！？])", body) if x.strip()]
            if len(segs) < 2:
                # 无句末标点的超长句(LLM 一口气写完):按逗号切分兜底
                segs = [x for x in re.split(r"(?<=[,，])", body) if x.strip()]
            if len(segs) < 2:
                out.append(s)
                continue
            groups = []
            cur, cur_n = [], 0
            for seg in segs:
                sn = len(re.findall(r"[\u4e00-\u9fff\u3000-\u303f\uff00-\uffef]", seg))
                if cur and cur_n + sn > 74:
                    groups.append("".join(cur))
                    cur, cur_n = [seg], sn
                else:
                    cur.append(seg)
                    cur_n += sn
            if cur:
                groups.append("".join(cur))
            blocks = [pre + g for g in groups]
        groups = []
        cur, cur_n = [], 0
        for b in blocks:
            bn = len(re.findall(r"[\u4e00-\u9fff\u3000-\u303f\uff00-\uffef]", b))
            if cur and cur_n + bn > 74:
                groups.append("".join(cur))
                cur, cur_n = [b], bn
            else:
                cur.append(b)
                cur_n += bn
        if cur:
            groups.append("".join(cur))
        # 第 1 组留原镜,其余组建新镜(时长按组字数 ÷5 计算,防拆后仍超预算)
        s["dialogue"] = "".join(groups[0])
        out.append(s)
        for g in groups[1:]:
            gn = len(re.findall(r"[\u4e00-\u9fff\u3000-\u303f\uff00-\uffef]", g))
            out.append({
                "shot_id": nxt_id, "shot_size": "近景", "camera": "固定（Static）",
                "action": "【同上一镜场景】角色继续说,听者表情反应",
                "dialogue": "".join(g),
                "characters": list(s.get("characters") or []),
                "light": s.get("light", ""), "sound": s.get("sound", ""),
                "duration": max(4, min(15, -(-gn // 5))),
            })
            nxt_id += 1
    return out


def fix_shot_metadata(shots, dialogs=None):
    """确定性后处理(2026-08-30 storyboard_check 首检暴露):
    ① 语音预算时长修正——台词+内心+旁白字数 ÷5 向上取整(4-15s),防语音超标;
    ② 说话人 Sx 按全集首次发言序统一——修复跨镜 ID 翻转(主持人镜18 S5 vs 镜6 S1);
    ③ 长句引号单元 >20 字按句末标点拆多引号连写;
    ④ 台词列标点修复(与正文对白匹配的单元替换为正文原文标点,check 覆盖口径)。
    返回修正后的 shots。"""
    # ①时长——口径与 storyboard_check 一致(cjk_count:中文字符+全角标点,
    # 含「内心·角色名:」前缀,「（接）」接续标记剥除;2026-08-31 对齐)
    for s in shots:
        dial = s.get("dialogue") or ""
        if dial and dial != "无":
            n = len(re.findall(r"[\u4e00-\u9fff\u3000-\u303f\uff00-\uffef]",
                               re.sub(r"（接）|\(接\)", "", dial)))
            need = max(4, min(15, -(-n // 5)))
            if need > int(s.get("duration") or 5):
                s["duration"] = need
    # ②Sx 统一(按镜序,说话人首次出现分配 S1..)
    order = {}
    nxt = [1]

    def repl(m):
        name = m.group(1)
        if name not in order:
            order[name] = nxt[0]
            nxt[0] += 1
        return f"(S{order[name]}){name}:"

    for s in shots:
        dial = s.get("dialogue") or ""
        s["dialogue"] = re.sub(r"\(S\d+\)([^：:]+?)[：:]", repl, dial)
    # ③ 长句拆分(2026-08-30 check 长句 WARN):引号内容中文字数 >20 的按
    # 「。！？」拆成多引号连写(「(S1)角色:"句1""句2"」,check/渲染口型预算同口径)
    for s in shots:
        dial = s.get("dialogue") or ""

        def split_q(m):
            inner = m.group(2) or m.group(3)
            if len(re.findall(r"[\u4e00-\u9fff]", inner or "")) <= 20:
                return m.group(0)
            segs = [x.strip() for x in re.split(r"(?<=[。！？])", inner) if x.strip()]
            return m.group(1) + "".join('"%s"' % x for x in segs)

        s["dialogue"] = re.sub(r'((?:\(S\d+[^：:]*|内心·[^：:]*|旁白)[：:]?)(?:"([^"]+)"|“([^”]+)”)', split_q, dial)
    # ④ 台词列标点修复(与正文对白匹配的单元替换为正文原文标点)
    if dialogs:
        for s in shots:
            s["dialogue"] = fix_dialogue_punct(s.get("dialogue") or "", dialogs)
    return shots


def fix_summary_prefix(hp):
    """summary 段必须以 [reference generation] 开头(官方格式④)"""
    i = hp.find("summary:")
    if i >= 0:
        rest = hp[i + len("summary:"):].lstrip()
        if not rest.startswith("[reference generation]"):
            hp = hp[:i + len("summary:")] + " [reference generation] " + rest
    return hp


def fix_timestamps(h3s, shots):
    """按分镜表时长累进重写 [Shot N] At MM:SS.mmm(2026-08-30:LLM 直出全写
    [Shot 1] 或错码,程序确定性重写;首镜 [Shot 1] 无时间戳,后续镜严格递增)"""
    out = []
    cum = 0.0
    for i, hp in enumerate(h3s):
        if i == 0:
            hp = re.sub(r"\[Shot \d+\](?:\s+At\s+\d{1,2}:\d{2}[.,]\d{1,3})?", "[Shot 1]", hp, count=1)
        else:
            cum += float(int(shots[i - 1].get("duration") or 5))
            ts = "[Shot %d] At %02d:%02d.%03d" % (i + 1, int(cum // 60), int(cum % 60), int((cum % 1) * 1000))
            hp = re.sub(r"\[Shot \d+\](?:\s+At\s+\d{1,2}:\d{2}[.,]\d{1,3})?", ts, hp, count=1)
        out.append(hp)
    return out


def ensure_d_tags(hp, dial):
    """<d> 补写兜底(2026-08-30:LLM 六段式漏写 <d> 标签是主要失败源——弹幕/长句
    常被写进叙述裸句或直接遗漏;与渲染端 Go 侧 scriptValidateShots 同款机制:
    台词列有、<d> 没有 → 程序补写,成片必有此句配音。对白补裸 <d>,内心/旁白
    补 narrator 画外音句式。"""
    if not dial or dial == "无":
        return hp
    d_norm = norm_text("".join(re.findall(r"<d>(?:\[Chinese\]|\[中文\])?([^<]+)</d>", hp, re.S)))
    patch = []
    parts = re.split(r"(\(S\d+\)[^：:]*[：:]|内心·[^：:]*[：:]|旁白[：:])", dial)
    for i in range(1, len(parts), 2):
        pre = parts[i]
        content = parts[i + 1] if i + 1 < len(parts) else ""
        inner = content.strip().strip('"“”').strip()
        if len([c for c in inner if not c.isspace()]) < 2:
            continue
        if norm_text(inner) in d_norm:
            continue
        if pre.startswith("(S"):
            patch.append("<d>" + inner + "</d>")
        else:
            patch.append("The narrator says in an off-screen voiceover: <d>" + inner + "</d> while the on-screen characters' lips remain completely closed.")
    if not patch:
        return hp
    k = hp.rfind("</d>")
    add = "\n" + "\n".join(patch)
    if k >= 0:
        hp = hp[:k + len("</d>")] + add + hp[k + len("</d>"):]
    else:
        hp = hp + add
    return hp


def validate_shot(hp, shot):
    """程序化轻校验:返回 (errs 硬错误列表, warns 软警告列表)。
    硬错误(重试):六段式缺失/台词未同步/时间戳异常/语音预算超/内心镜缺 Q 版;
    软警告(放行,仅日志):六段非 <d> 区混中文(官方格式⑤ WARN,不阻断)"""
    errs, warns = [], []
    for sec in REQ_SECTIONS:
        if sec not in hp:
            errs.append(f"缺字段 {sec}")
    # 台词同步:该镜台词列每句须在 <d> 内——缺失由 ensure_d_tags 程序补写兜底
    # (2026-08-30:LLM 漏写 <d> 是主要失败源,补写后必然满足,只记软警告)
    dial = (shot.get("dialogue") or "").strip()
    if dial and dial != "无":
        d_norm = norm_text("".join(re.findall(r"<d>(?:\[Chinese\]|\[中文\])?([^<]+)</d>", hp, re.S)))
        for seg in re.split(r"\(S\d+\)[^：:]*[：:]|内心·[^：:]*[：:]|旁白[：:]", dial):
            if len([c for c in seg if not c.isspace()]) < 4:
                continue  # 2-3 字文学性引号豁免
            seg_n = norm_text(seg)
            if seg_n in d_norm:
                continue
            subs = [norm_text(x) for x in re.split(r"(?<=[。！？])", seg)]
            subs = [x for x in subs if len(x) >= 4]
            if subs and all(x in d_norm for x in subs):
                continue
            warns.append(f"台词将由程序补写进d:{seg[:14]}")
    # 时间戳递增
    marks = [int(a) * 60000 + int(b) * 1000 + int(c.ljust(3, "0"))
             for a, b, c in re.findall(r"At\s+(\d{1,2}):(\d{2})[.,](\d{1,3})", hp)]
    for i in range(1, len(marks)):
        if marks[i] <= marks[i - 1]:
            errs.append("时间戳未递增")
            break
    # 语音预算:与 storyboard_check 同口径 cjk_count(中文字符+全角标点,含
    # 「内心·」前缀);「（接）」接续标记剥除不计;÷5 ≤ 时长+1(容差)
    if dial and dial != "无":
        n = len(re.findall(r"[\u4e00-\u9fff\u3000-\u303f\uff00-\uffef]",
                           re.sub(r"（接）|\(接\)", "", dial)))
        if n and n / 5 > int(shot.get("duration") or 5) + 1:
            errs.append(f"语音预算超:{n}字/{shot.get('duration')}s")
    # 六段非 <d> 区混中文(软警告:check 官方格式⑤为 WARN)
    for sec in ("subject_definitions:", "summary:", "retention_analysis:"):
        if sec not in hp:
            continue
        seg = hp.split(sec, 1)[1]
        for nxt in REQ_SECTIONS:
            if nxt != sec and nxt in seg:
                seg = seg.split(nxt, 1)[0]
                break
        if re.search(r"[\u4e00-\u9fff]", seg):
            warns.append(f"{sec.rstrip(':')}含中文")
    # 内心戏镜须定义 Q 版主体(官方格式⑥)
    if "内心·" in dial:
        subj = hp.split("subject_definitions:", 1)[1] if "subject_definitions:" in hp else ""
        if not re.search(r"(?i)chibi|miniature|Q-version|Q version", subj):
            errs.append("内心镜缺Q版主体")
    return errs, warns


# ---------------- 组装 md ----------------

def assemble_json(book, ch, ch_title, style, bridge, shots, h3_prompts):
    """组装 JSON 分镜脚本(2026-08-31 技能侧新格式:结构化输出,零表格断行风险)"""
    out_shots = []
    for i, s in enumerate(shots):
        sh = {
            "shot_id": int(s.get("shot_id") or i + 1),
            "shot_size": s.get("shot_size", ""),
            "camera": s.get("camera", ""),
            "action": s.get("action", ""),
        }
        d = (s.get("dialogue") or "").strip()
        if d and d != "无":
            sh["dialogue"] = d
        if s.get("characters"):
            sh["characters"] = list(s.get("characters"))
        for k in ("light", "sound", "style"):
            if s.get(k):
                sh[k] = s[k]
        sh["duration"] = int(s.get("duration") or 5)
        sh["h3_prompt"] = h3_prompts[i]
        out_shots.append(sh)
    data = {
        "book": "《%s》" % book,
        "episode": ch,
        "chapter_title": ch_title,
        "generated": "2026-08-31 新规则全面重写",
    }
    if style:
        data["global_style"] = style
    if bridge:
        data["bridge"] = bridge
    data["shots"] = out_shots
    return data


# ---------------- 主流程 ----------------

def regen_chapter(cfg, sys_text, book, ch, ch_title, book_dir, out_dir, force):
    """重写一章(整章重试至多 3 轮——LLM 单镜随机失败率 ~4%,一章 30 镜全过
    概率仅 ~36%,重跑一轮即可消除大部分随机失败)"""
    last_err = ""
    for rnd in range(3):
        try:
            return _regen_chapter_once(cfg, sys_text, book, ch, ch_title, book_dir, out_dir, force)
        except Exception as e:
            last_err = str(e)[:150]
            if rnd < 2:
                log(f"  ↻ 第{ch}章 第{rnd+1}轮失败({last_err[:80]}),整章重试(第{rnd+2}轮)")
    return False, 0, last_err


def _regen_chapter_once(cfg, sys_text, book, ch, ch_title, book_dir, out_dir, force):
    """单轮重写:分镜表 → 逐镜六段式 → 组装落盘。返回 (ok, 镜数, 错误)"""
    chap_path = None
    for n, p in find_chapters(book_dir):
        if n == ch:
            chap_path = p
            break
    if not chap_path:
        return False, 0, f"正文缺失 第{ch}章"
    chapter_text = read_utf8(chap_path)
    chapter_text = re.sub(r"^#.*\n", "", chapter_text, count=1)

    cards = parse_char_cards(book_dir)
    scenes = parse_scene_cards(book_dir)
    style = extract_style_sentence(book_dir)
    outline = extract_plot_outline(book_dir)
    prev_ending = old_chapter_ending(find_storyboard(book_dir, ch - 1)) if ch > 1 else ""
    next_open = next_chapter_opening(book_dir, ch)
    old_sb = find_storyboard(book_dir, ch)
    old_head = ""
    if old_sb:
        t = read_utf8(old_sb)
        first = re.search(r"^#.*$", t, re.M)
        if first:
            old_head = first.group(0).strip()

    # 阶段① 分镜表(最多重试 3 次,temp 交替;生成后校验台词列对正文逐字覆盖
    # ——漏挂/标点不一致时把缺失清单反馈给 LLM 重试)
    table = None
    fb = ""
    for attempt in range(3):
        try:
            table = gen_table(cfg, sys_text, book, ch, ch_title, chapter_text,
                              cards, scenes, style, outline, prev_ending, next_open,
                              temp=0.3 if attempt % 2 == 0 else 0.5, feedback=fb)
            miss = validate_table_dialogues(chapter_text, table.get("shots") or [])
            if miss:
                # 反馈带正文上下文,帮助 LLM 定位漏挂对白所在场景
                ctx_lines = []
                for m0 in miss[:8]:
                    i = chapter_text.find(m0)
                    c = chapter_text[max(0, i - 40):i + len(m0) + 40].replace("\n", " ") if i >= 0 else ""
                    ctx_lines.append(f"- 「{m0}」(正文上下文: …{c}…)")
                fb = "\n".join(ctx_lines)
                table = None
                log(f"  ⚠️ 第{ch}章 分镜表台词漏挂/标点不符 {len(miss)} 条(首:{miss[0]}),第{attempt+2}次重试")
                raise RuntimeError("台词漏挂:" + ";".join(miss[:3]))
            break
        except Exception as e:
            log(f"  ⚠️ 第{ch}章 分镜表第{attempt+1}次失败: {str(e)[:120]}")
    if table is None:
        return False, 0, "分镜表生成失败"
    shots = table.get("shots") or []
    dialogs = extract_dialogues_like_check(chapter_text)
    # 确定性后处理:语音预算时长修正 + 说话人 Sx 跨镜统一 + 长句拆分 + 标点修复
    shots = fix_shot_metadata(shots, dialogs)
    # 语音预算兜底拆镜(2026-08-31:LLM 反复不拆 80-125 字长对白镜,程序按引号
    # 单元拆成多镜,保证每镜 ≤75 字;先拆后验,不再因预算重试分镜表)
    shots = split_oversized_shots(shots)

    # 阶段② 逐镜六段式(章内串行,链式承接)
    h3 = []
    prev_tail = ""
    fb = ""
    for i, s in enumerate(shots):
        s.setdefault("shot_id", i + 1)
        ok = False
        hp = ""
        for attempt in range(4):
            try:
                hp = gen_shot(cfg, sys_text, book, ch, ch_title, s, shots,
                              cards, scenes, style, prev_tail,
                              temp=0.3 if attempt % 2 == 0 else 0.5,
                              feedback=fb)
                # <d> 补写兜底先于校验(LLM 漏写 <d> 由程序补齐,不再重试死磕)
                hp = ensure_d_tags(hp, s.get("dialogue") or "")
                errs, warns = validate_shot(hp, s)
                if warns:
                    log(f"  ⚠️ 第{ch}章 镜{s.get('shot_id')} 软警告: {'; '.join(warns)[:70]}")
                if errs:
                    fb = "\n".join("- " + e for e in errs[:5])
                    if attempt < 3:
                        log(f"  ⚠️ 第{ch}章 镜{s.get('shot_id')} 校验未过({'; '.join(errs)[:70]}),第{attempt+2}次重试")
                    else:
                        dtags = [d[:40] for d in re.findall(r"<d>(?:\[Chinese\]|\[中文\])?([^<]+)</d>", hp, re.S)]
                        log(f"  ⚠️ 第{ch}章 镜{s.get('shot_id')} 4 次重试仍缺: {'; '.join(errs)[:70]} | <d>列表: {dtags[:4]}")
                    raise RuntimeError(";".join(errs))
                ok = True
                break
            except Exception as e:
                if attempt == 3:
                    log(f"  ⚠️ 第{ch}章 镜{s.get('shot_id')} 失败: {str(e)[:100]}")
        if not ok:
            return False, len(shots), f"镜{s.get('shot_id')} 六段式生成失败"
        h3.append(hp)
        # 链式承接:上镜 detailed_description 收尾 2 句
        m = re.search(r"detailed_description:\s*(.*?)(?:\noverall_soundscape:|\Z)", hp, re.S)
        if m:
            d = m.group(1).strip()
            prev_tail = d[-300:]
        else:
            prev_tail = ""

    # 确定性收尾:summary 前缀 + <d> 补写兜底 + 时间戳按时长累进重写 + <d> 标点修复
    h3 = [fix_summary_prefix(h) for h in h3]
    h3 = [ensure_d_tags(h, s.get("dialogue") or "") for h, s in zip(h3, shots)]
    h3 = [fix_d_punct(h, dialogs) for h in h3]
    h3 = fix_timestamps(h3, shots)

    data = assemble_json(book, ch, ch_title, style, table.get("episode_bridge"), shots, h3)
    out = os.path.join(out_dir, f"第{ch}章_{ch_title}_分镜脚本.json")
    with open(out, "w", encoding="utf-8", newline="\n") as f:
        json.dump(data, f, ensure_ascii=False, indent=1)
    return True, len(shots), ""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--books", help="逗号分隔书名(默认全部 6 本)")
    ap.add_argument("--chapters", default="", help="仅处理指定章号(逗号分隔,如 7,8,15;配合 --books 补跑失败章)")
    ap.add_argument("--limit", type=int, default=0, help="最多处理章数(调试用)")
    ap.add_argument("--workers", type=int, default=6)
    ap.add_argument("--resume", action="store_true", help="跳过已生成文件")
    ap.add_argument("--force", action="store_true", help="覆盖已生成文件")
    ap.add_argument("--model", default="", help="LLM 模型(默认 deepseek-chat,settings 的 v4-flash 是推理模型不适用于批量)")
    ap.add_argument("--logdir", default=os.path.join(ROOT, "tools", "logs"))
    args = ap.parse_args()

    books = [b.strip() for b in (args.books or "").split(",") if b.strip()] or BOOKS
    only_chs = {int(x) for x in (args.chapters or "").split(",") if x.strip().isdigit()}
    os.makedirs(args.logdir, exist_ok=True)
    cfg = load_api(args.model or None)
    sys_text = build_system()
    log(f"LLM: {cfg['model']} | books: {books} | workers={args.workers} | resume={args.resume}")

    tasks = []
    for book in books:
        book_dir = os.path.join(ROOT, "novel", book)
        out_dir = os.path.join(book_dir, "素材", "分镜脚本")
        if not os.path.isdir(book_dir):
            log(f"跳过: 无目录 {book}")
            continue
        chapters = find_chapters(book_dir)
        for ch, chap_path in chapters:
            if only_chs and ch not in only_chs:
                continue
            m = re.search(r"第\d+章_(.+)\.md$", os.path.basename(chap_path))
            ch_title = m.group(1) if m else str(ch)
            tasks.append((book, ch, ch_title, book_dir, out_dir))
    if args.limit:
        tasks = tasks[:args.limit]
    log(f"共 {len(tasks)} 章待处理")

    done = fail = skip = 0
    failed = []
    results = []

    def work(task):
        book, ch, ch_title, book_dir, out_dir = task
        out = os.path.join(out_dir, f"第{ch}章_{ch_title}_分镜脚本.json")
        # resume 只跳过「新规则重写版」JSON(旧脚本存在但无重写标记 → 照常重写)
        if not args.force and args.resume and os.path.exists(out):
            try:
                if "新规则全面重写" in open(out, encoding="utf-8").read(2000):
                    return ("skip", book, ch, ch_title, 0, "")
            except Exception:
                pass
        try:
            ok, n, err = regen_chapter(cfg, sys_text, book, ch, ch_title, book_dir, out_dir, args.force)
            return ("ok" if ok else "fail", book, ch, ch_title, n, err)
        except Exception as e:
            return ("fail", book, ch, ch_title, 0, str(e)[:150])

    with ThreadPoolExecutor(max_workers=args.workers) as ex:
        futs = {ex.submit(work, t): t for t in tasks}
        for fut in as_completed(futs):
            kind, book, ch, ch_title, n, err = fut.result()
            if kind == "skip":
                skip += 1
            elif kind == "ok":
                done += 1
                log(f"✅ {book} 第{ch}章 {ch_title}: {n} 镜 ({done+skip+fail}/{len(tasks)})")
            else:
                fail += 1
                failed.append(f"{book} 第{ch}章 {ch_title}: {err}")
                log(f"❌ {book} 第{ch}章 {ch_title}: {err}")

    log(f"完成: 成功 {done} / 跳过 {skip} / 失败 {fail}")
    if failed:
        fp = os.path.join(args.logdir, "storyboard_regen_failed.txt")
        open(fp, "w", encoding="utf-8").write("\n".join(failed))
        log(f"失败清单 -> {fp}")
    # 汇总 JSON
    summary = os.path.join(args.logdir, "storyboard_regen_summary.json")
    json.dump({"done": done, "skip": skip, "fail": fail, "failed": failed},
              open(summary, "w", encoding="utf-8"), ensure_ascii=False, indent=1)
    log(f"汇总 -> {summary}")


if __name__ == "__main__":
    main()
