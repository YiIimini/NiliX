# -*- coding: utf-8 -*-
"""GPT-SoVITS 整集换声(2026-09-05 STEP3 音画分离配音)。

原理:H3 渲染照常出声(口型/节奏由它定)→ 后期把台词人声换成 GPT-SoVITS 克隆声:
  ①读 plan:每镜 <d>[Chinese] 台词逐句 + 说话角色(音色绑定 voice_lib 档)
  ②ASR 原音频定位每句时间窗(faster-whisper,与 QC 同源)
  ③逐句调 GPT-SoVITS API(/tts,声库 gs 参考件+1.05 默认语速(2026-09-05 成片语境回退:1.25 单听合适但配画面偏快+口型赶))
  ④ffmpeg:原声在台词窗内压 -18dB(保环境音/配乐),克隆声按窗起点叠入
  ⑤输出 revoice/<EP>/NN.mp4(不动原 clips;合成时优先取 revoice 版)

用法: python tools/gptsovits_revoice.py <workdir> <EP> [--speed 1.25] [--api http://127.0.0.1:9880]
"""
import argparse
import glob
import io
import json
import os
import re
import subprocess
import sys
import urllib.request
import urllib.error

os.environ.setdefault("HF_HUB_OFFLINE", "1")

FFMPEG = r"C:/Mi/Apps/GPT-SoVITS/gsenv/Library/bin/ffmpeg.exe"
FFPROBE = r"C:/Mi/Apps/GPT-SoVITS/gsenv/Library/bin/ffprobe.exe"
GS_DIR = r"D:/Ai/NiliX/asset_lib/voices/gs"
D_LINE = re.compile(r"<d>\s*\[Chinese\]\s*(.*?)\s*</d>", re.S)
AUDIO_DEF = re.compile(r"<Audio (\d+)> is the voice-timbre reference for <Subject \d+> \((S\d+)\)")
SX_SAYS = re.compile(r"\(S(\d+)\)[^:<]{0,40}says", re.I)
from difflib import SequenceMatcher


def cjk(s):
    return re.sub(r"[^\u4e00-\u9fff]", "", s)


def plan_lines(plan_path, workdir):
    """镜号 → [(台词文本, voice_key|None)]。<d> 台词逐句 + 说话角色音色档。
    voice_lib 权威=小说素材/人物生成提示词.json 角色卡(plan 的 characters 不带
    该字段;渲染端 autoVoiceFor 同源读卡)。"""
    d = json.load(io.open(plan_path, encoding="utf-8"))
    vlib = {}
    cards = os.path.normpath(os.path.join(os.path.dirname(workdir.rstrip("/\\")),
                                          "..", "novel", os.path.basename(workdir.rstrip("/\\")),
                                          "素材", "人物生成提示词.json"))
    try:
        cs = json.load(io.open(cards, encoding="utf-8"))
        for c in cs:
            if c.get("voice_lib"):
                vlib[str(c.get("id"))] = str(c["voice_lib"])
    except Exception as e:
        print("[warn] 角色卡未读:", cards, e)
    out = {}
    for s in d.get("shots") or []:
        hp = s.get("h3_prompt") or ""
        lines = [re.sub(r"<[^>]+>", "", m).strip() for m in D_LINE.findall(hp)]
        if not lines:
            continue
        keys = [vlib.get(str(c)) for c in (s.get("characters") or [])]
        key = next((k for k in keys if k), None)
        out[int(s.get("shot_id") or 0)] = list(zip(lines, [key] * len(lines)))
    return out


def asr_segments(path, model):
    segs, _ = model.transcribe(path, language="zh", vad_filter=True)
    return [(s.start, s.end, s.text.strip()) for s in segs]


def match_window(line, segs):
    """台词 → ASR 段时间窗:字符多重集相似度最高的段(含相邻段拼接取最优)。"""
    best, best_r = None, 0.0
    want = cjk(line)
    for i in range(len(segs)):
        acc, t0 = "", segs[i][0]
        for j in range(i, min(i + 3, len(segs))):
            acc += cjk(segs[j][2])
            r = SequenceMatcher(None, want, acc).ratio()
            if r > best_r:
                best_r, best = r, (t0, segs[j][1])
    if best_r >= 0.5:
        return best
    # 短句(≤6 字,顿挫念白)ASR 常漏字:字符重叠比 0.22 兜底(镜10「我,反,悔,了。」只转出「我」)
    if len(want) <= 6:
        for t0, t1, txt in segs:
            got = cjk(txt)
            overlap = sum(1 for ch in want if ch in got) / max(len(want), 1)
            if overlap >= 0.22:
                return (t0, t1)
    return None


def synth(text, key, speed, api):
    wav = os.path.join(GS_DIR, key + ".wav")
    txt = os.path.join(GS_DIR, key + ".txt")
    if not (os.path.exists(wav) and os.path.exists(txt)):
        return None, "缺参考件 " + key
    prompt = io.open(txt, encoding="utf-8").read().strip()
    body = {"text": text, "text_lang": "zh", "ref_audio_path": wav,
            "prompt_text": prompt, "prompt_lang": "zh", "media_type": "wav",
            "streaming_mode": False, "speed_factor": speed}
    req = urllib.request.Request(api + "/tts", data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=900) as r:
            return r.read(), None
    except urllib.error.HTTPError as e:
        return None, "tts " + e.read().decode("utf-8", "replace")[:120]


def dur_of(path):
    r = subprocess.run([FFPROBE, "-v", "error", "-show_entries", "format=duration",
                        "-of", "csv=p=0", path], capture_output=True)
    try:
        return float(r.stdout.decode().strip())
    except Exception:
        return 0.0


def revoice_shot(src, dst, windows, api, speed, tmpdir, duck_spans):
    """windows=[(t0,t1,voice_key,text)] 克隆叠入窗;duck_spans=[(t0,t1)] 原声压低窗
    (=原片 ASR 全部语音段并集——原声台词窗常超出匹配窗(镜05 尾巴 4.8-7.2 残留
    实锤),凡 ASR 听到人声的位置一律压低,克隆按匹配窗起点叠入)。"""
    inputs = ["-i", src]
    filters = []
    mix_labels = []
    for i, (t0, t1, key, text) in enumerate(windows):
        win = max(t1 - t0, 0.5)
        data, err = synth(text, key, speed, api)
        if not data:
            print("    [tts-fail] 镜内句 %d: %s" % (i + 1, err))
            continue
        w = os.path.join(tmpdir, "line_%d.wav" % i)
        io.open(w, "wb").write(data)
        d = dur_of(w)
        # 口型对齐核心(2026-09-05 用户验收:语速快+对不上嘴型):H3 的口型动画
        # 跟着原声台词窗走,克隆声必须占满同一窗口——失配>8% 按窗口反推合成侧
        # speed_factor 重合成(音质优于 atempo),残余再 atempo 微调(夹 0.85~1.4)
        if abs(d - win) / win > 0.08:
            adj = max(0.8, min(2.0, speed * d / win))
            data2, err2 = synth(text, key, adj, api)
            if data2:
                io.open(w, "wb").write(data2)
                d = dur_of(w)
        if d > win * 1.02 or d < win * 0.98:
            tempo = max(0.85, min(1.4, d / win))
            w2 = w
            w = os.path.join(tmpdir, "line_%d_t.wav" % i)
            subprocess.run([FFMPEG, "-y", "-v", "error", "-i", w2, "-filter:a",
                            "atempo=%.3f" % tempo, w], capture_output=True)
        delay = int(t0 * 1000)
        # 克隆件输出后处理(与参考件预制同链,C 案):压残余混响与高频毛刺
        w3 = w
        w = os.path.join(tmpdir, "line_%d_clean.wav" % i)
        d = dur_of(w3)
        rc = subprocess.run([FFMPEG, "-y", "-v", "error", "-i", w3, "-af",
                        "highpass=f=140,lowpass=f=6800,afftdn=nr=12:nf=-25,"
                        "afade=t=in:st=0:d=0.005,afade=t=out:st=%.3f:d=0.005" % max(d - 0.01, 0),
                        w], capture_output=True)
        if rc.returncode != 0 or not os.path.exists(w) or os.path.getsize(w) == 0:
            print("    [clean-fail] %s -> %s" % (w3, rc.stderr.decode("utf-8", "replace")[-100:]))
            w = w3  # 后处理失败降级用未清理件(可听但不静默出坏档)
        filters.append("[%d:a]adelay=%d:all=1[v%d]" % (i + 1, delay, i))
        mix_labels.append("[v%d]" % i)
        inputs += ["-i", w]
    if not mix_labels:
        return False
    # 接缝伪影修复(2026-09-05 用户实锤"电子噪音只在成片"):帧级硬切在窗边界产生
    # 咔啦声——每窗边界 ±8ms 线性斜坡过渡(下坡 1→0,上坡 0→1)
    F = 0.008
    # 逐窗链式 volume(2026-09-05:多窗单表达式超 ffmpeg 解析长度;每窗一层
    # volume 滤镜做 8ms 梯形坡,链式相乘等效多窗同时压,无表达式长度限制)
    duck_expr = ""
    chain = "[0:a]"
    for t0, t1 in duck_spans:
        ramp = "max(0,min(1,(%.4f-t)/%.4f))+max(0,min(1,(t-%.4f)/%.4f))" % (t0, F, t1, F)
        chain += "volume='if(min(1,%s),0.0,1.0)':eval=frame," % ramp
    chain += "anull[base];"
    af = (chain + ";".join(filters) +
          ";[base]%samix=inputs=%d:duration=first:normalize=0[aout]"
          % ("".join(mix_labels), len(mix_labels) + 1))
    r = subprocess.run([FFMPEG, "-y", "-v", "error"] + inputs +
                       ["-filter_complex", af, "-map", "0:v", "-map", "[aout]",
                        "-c:v", "copy", "-c:a", "aac", "-b:a", "192k", dst],
                       capture_output=True)
    if r.returncode != 0:
        print("    [fferr]", r.stderr.decode("utf-8", "replace")[-200:])
        return False
    return True


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("workdir")
    ap.add_argument("episode")
    ap.add_argument("--speed", type=float, default=1.05)
    ap.add_argument("--api", default="http://127.0.0.1:9880")
    ap.add_argument("--shots", default="", help="只处理指定镜(逗号分隔)")
    ap.add_argument("--narrator-key", default="male_sun", help="旁白/无绑定角色兜底音色档")
    args = ap.parse_args()

    clips = os.path.join(args.workdir, "clips", args.episode)
    outdir = os.path.join(args.workdir, "revoice", args.episode)
    plan = os.path.join(args.workdir, "analysis", args.episode + "_direct_plan.json")
    os.makedirs(outdir, exist_ok=True)
    tmpdir = os.path.join(args.workdir, "revoice", "_tmp")
    os.makedirs(tmpdir, exist_ok=True)

    lines_of = plan_lines(plan, args.workdir)
    only = {int(x) for x in args.shots.split(",") if x.strip().isdigit()} if args.shots else None
    from faster_whisper import WhisperModel
    model = WhisperModel("small", device="cpu", compute_type="int8")

    n_ok = n_skip = n_fail = 0
    for mp4 in sorted(glob.glob(os.path.join(clips, "*.mp4"))):
        sid = int(os.path.splitext(os.path.basename(mp4))[0])
        if only and sid not in only:
            continue
        lines = lines_of.get(sid)
        if not lines:
            n_skip += 1
            continue
        segs = asr_segments(mp4, model)
        windows = []
        for text, key in lines:
            if not key:
                key = args.narrator_key  # 旁白/无绑定角色走叙述兜底档(镜1 纯旁白实锤)
            w = match_window(text, segs)
            if not w:
                print("  [win-miss] 镜%02d: ASR 未定位「%s」" % (sid, text[:12]))
                continue
            windows.append((w[0], w[1], key, text))
        if not windows:
            n_fail += 1
            continue
        spans = []
        for t0, t1, _ in segs:
            if spans and t0 <= spans[-1][1] + 0.3:
                spans[-1][1] = max(spans[-1][1], t1)
            else:
                spans.append([t0, t1])
        dst = os.path.join(outdir, "%02d.mp4" % sid)
        print("镜%02d: %d 句换声 | duck %d 段" % (sid, len(windows), len(spans)))
        if revoice_shot(mp4, dst, windows, args.api, args.speed, tmpdir,
                        [(a, b) for a, b in spans]):
            n_ok += 1
        else:
            n_fail += 1
    print("换声 %d | 无台词跳过 %d | 失败 %d -> %s" % (n_ok, n_skip, n_fail, outdir))
    return 0 if n_fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
