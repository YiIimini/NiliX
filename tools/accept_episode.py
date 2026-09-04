# -*- coding: utf-8 -*-
"""单集成品验收(2026-09-04 基线 STEP 1;渲染验收铁律的机器侧支撑)。

对 clips/<EP>/NN.mp4 逐镜:ffprobe 时长/画幅/音轨 + faster-whisper ASR 台词,
对照 plan(analysis/<EP>_direct_plan.json)的 <d> 台词与静音契约,输出:
  PASS-voiced   应有声且 ASR 命中至少一句(首 6 字探针)
  PASS-silent   应无声且 ASR 无语音段(幽灵人声反向核验)
  FAIL-漏句     应有声但 ASR 一句未中
  FAIL-幽灵     应无声但 ASR 检出人声(时间+置信度)
  WARN-置信     命中但平均 logprob < -1.0(听感存疑人工复核)
块模式:组头按 takes 并组内台词核对(manju_media qc 同口径的独立第三方复核)。
用法: python tools/accept_episode.py <项目config目录或workdir> <EP> [--plan 路径]
"""
import argparse
import glob
import io
import json
import os
import re
import subprocess
import sys

D_TAG = re.compile(r"<d>\s*\[Chinese\]\s*(.*?)\s*</d>", re.S)
FFPROBE = None


def find_ffprobe():
    global FFPROBE
    if FFPROBE:
        return FFPROBE
    for c in (r"D:/Ai/NiliX/comfyui/standalone-env/Lib/site-packages/ffmpeg/ffprobe.exe",
              "ffprobe"):
        try:
            subprocess.run([c, "-version"], capture_output=True, timeout=10)
            FFPROBE = c
            return c
        except Exception:
            continue
    return "ffprobe"


def probe(path):
    try:
        out = subprocess.run([find_ffprobe(), "-v", "error", "-show_entries",
                              "format=duration:stream=codec_type,width,height,avg_frame_rate",
                              "-of", "json", path], capture_output=True, timeout=30)
        d = json.loads(out.stdout.decode("utf-8", "replace"))
        dur = float(d.get("format", {}).get("duration") or 0)
        v = a = None
        for s in d.get("streams", []):
            if s.get("codec_type") == "video" and not v:
                v = s
            if s.get("codec_type") == "audio" and not a:
                a = s
        return dur, v, a
    except Exception as e:
        return 0, None, str(e)


def plan_expectations(plan_path):
    """镜号 → (期望台词[中文串], 组内镜号)。台词=plan h3_prompt <d> 中文。"""
    d = json.load(io.open(plan_path, encoding="utf-8"))
    takes = {}
    for grp in d.get("takes") or []:
        ids = [int(x) for x in (grp if isinstance(grp, list) else [])]
        for i in ids[1:]:
            takes[i] = ids[0]
    exp = {}
    for s in d.get("shots") or []:
        sid = int(s.get("shot_id") or 0)
        if sid in takes:
            continue  # 内镜:台词并组头
        lines = []
        for m in D_TAG.finditer(s.get("h3_prompt") or ""):
            txt = re.sub(r"<[^>]+>", "", m.group(1)).strip()
            if len(txt) >= 2:
                lines.append(txt)
        grp = [sid] + [i for i, h in takes.items() if h == sid]
        for i in takes:
            pass
        exp[sid] = (lines, sorted(set(grp + [i for i, h in takes.items() if h == sid])))
    return exp


def asr(path):
    from faster_whisper import WhisperModel
    m = asr.model
    segs, _ = m.transcribe(path, language="zh", vad_filter=True)
    return [(s.start, s.end, s.avg_logprob, s.text.strip()) for s in segs]


asr.model = None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("workdir")
    ap.add_argument("episode")
    ap.add_argument("--plan", default="")
    args = ap.parse_args()

    clips = os.path.join(args.workdir, "clips", args.episode)
    plan = args.plan or os.path.join(args.workdir, "analysis", args.episode + "_direct_plan.json")
    if not os.path.isdir(clips):
        print("无镜头目录:", clips)
        return 2
    exp = plan_expectations(plan) if os.path.exists(plan) else {}
    if not exp:
        print("⚠️ plan 缺失或无镜头:", plan)

    files = sorted(glob.glob(os.path.join(clips, "*.mp4")))
    if not files:
        print("无 mp4")
        return 2
    from faster_whisper import WhisperModel
    asr.model = WhisperModel("small", device="cpu", compute_type="int8")

    n_pass = n_fail = 0
    from difflib import SequenceMatcher

    def heard(hit_want, joined):
        """ASR 转写噪声容忍匹配:6 字精确子串 → 退字符多重集相似度 ≥0.6
        (small 模型对咬字/繁简变体误转写普遍,精确子串必误报,2026-09-05 实锤:
        生死簿→生死不化/寫回,台词实际念了)"""
        import re as _re
        probe = _re.sub(r"[^\u4e00-\u9fff]", "", hit_want)[:6]
        if probe and probe in _re.sub(r"[^\u4e00-\u9fff]", "", joined):
            return True
        a = sorted(_re.sub(r"[^\u4e00-\u9fff]", "", hit_want))
        b = sorted(_re.sub(r"[^\u4e00-\u9fff]", "", joined))
        return SequenceMatcher(None, "".join(a), "".join(b)).ratio() >= 0.6

    for f in files:
        sid = int(os.path.splitext(os.path.basename(f))[0])
        dur, v, a = probe(f)
        lines, grp = exp.get(sid, ([], [sid]))
        segs = asr(f)
        tag = ""
        if lines:
            hit = 0
            joined = "".join(x[3] for x in segs)
            for txt in lines:
                if heard(txt, joined):
                    hit += 1
            if hit == 0:
                tag = "FAIL-漏句"
            else:
                lp = min(x[2] for x in segs) if segs else 0
                tag = "WARN-置信(lp=%.2f)" % lp if lp < -1.0 else "PASS-voiced(%d/%d 句中)" % (hit, len(lines))
        else:
            if segs:
                tag = "FAIL-幽灵(%s)" % ";".join("%.1fs %s" % (x[0], x[3][:10]) for x in segs[:2])
            else:
                tag = "PASS-silent"
        bad = "FAIL" in tag
        n_fail += bad
        n_pass += not bad
        print("%s 镜%02d%s %5.1fs %sx%s %s | %s" % (
            "❌" if bad else "✅", sid, ("(块%s)" % grp if len(grp) > 1 else ""),
            dur, (v or {}).get("width", "?"), (v or {}).get("height", "?"),
            "音轨✓" if a and not isinstance(a, str) else "❌无音轨", tag))
        if os.environ.get("ACCEPT_VERBOSE"):
            for x in segs:
                print("      [%.1f-%.1f lp%.2f] %s" % x)
    print("结果: %d PASS / %d FAIL / 共 %d" % (n_pass, n_fail, len(files)))
    final = os.path.join(args.workdir, args.episode + "_成片.mp4")
    if os.path.exists(final):
        dur, v, a = probe(final)
        print("成片: %s %.1fs %sx%s %s" % (final, dur, (v or {}).get("width", "?"),
                                           (v or {}).get("height", "?"),
                                           "音轨✓" if a and not isinstance(a, str) else "❌"))
    return 1 if n_fail else 0


if __name__ == "__main__":
    sys.exit(main())
