# -*- coding: utf-8 -*-
"""H3 原生微创成片管线(2026-09-06 用户拍板路线:H3 原生为主,微创修复)。

H3 配音质量本身是好的,只有低频缺陷。本管线 95% 保留 H3 原声,只做两道微创:
  ①幽灵静音:无台词镜检出 H3 自发人声段(双轮 ASR 复检,任一轮听到即静音)——
    只删幻声段,环境音/配乐全保留
  ②漏句补配:plan <d> 台词在成片里 ASR 找不到(或只中一半)→ GPT-SoVITS 按音色锁
    (voice_lock/<角色>)补配,时间轴叠入;音色锁缺失回落 voice_lib gs 参考件

产物: <workdir>/<EP>_成片_微创版.mp4(修复镜落 revoice/<EP>_micro/,其余原样)
用法: python tools/h3_microsurgery.py <workdir> <EP> [--api http://127.0.0.1:9880]
"""
import argparse
import glob
import io
import json
import os
import re
import subprocess
import sys
import urllib.parse
import urllib.request
import urllib.error
from difflib import SequenceMatcher

os.environ.setdefault("HF_HUB_OFFLINE", "1")

FF = r"C:/Mi/Apps/GPT-SoVITS/gsenv/Library/bin/ffmpeg.exe"
FFPROBE = FF.replace("ffmpeg", "ffprobe")
GS_DIR = r"D:/Ai/NiliX/asset_lib/voices/gs"
D_LINE = re.compile(r"<d>\s*\[Chinese\]\s*(.*?)\s*</d>", re.S)


def cjk(s):
    return re.sub(r"[^\u4e00-\u9fff]", "", s)


def dur_of(p):
    r = subprocess.run([FFPROBE, "-v", "error", "-show_entries", "format=duration",
                        "-of", "csv=p=0", p], capture_output=True, text=True)
    try:
        return float(r.stdout.strip())
    except Exception:
        return 0.0


def plan_shots(workdir, ep):
    d = json.load(io.open(os.path.join(workdir, "analysis", ep + "_direct_plan.json"), encoding="utf-8"))
    vlib = {}
    cards = os.path.normpath(os.path.join(os.path.dirname(workdir.rstrip("/\\")), "..", "novel",
                                          os.path.basename(workdir.rstrip("/\\")), "素材", "人物生成提示词.json"))
    try:
        for c in json.load(io.open(cards, encoding="utf-8")):
            if c.get("voice_lib"):
                vlib[str(c.get("id"))] = str(c["voice_lib"])
    except Exception:
        pass
    out = {}
    for s in d.get("shots") or []:
        hp = s.get("h3_prompt") or ""
        lines = [re.sub(r"<[^>]+>", "", m).strip() for m in D_LINE.findall(hp)]
        out[int(s.get("shot_id") or 0)] = {"lines": lines, "chars": s.get("characters") or [], "vlib": vlib}
    return out


def synth(text, ref_wav, ref_text, speed, api):
    body = {"text": text, "text_lang": "zh", "ref_audio_path": ref_wav,
            "prompt_text": ref_text, "prompt_lang": "zh", "media_type": "wav",
            "streaming_mode": False, "speed_factor": speed}
    req = urllib.request.Request(api + "/tts", data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=900) as r:
        return r.read()


def lock_for(workdir, cid):
    d = os.path.join(workdir, "voice_lock")
    wav = os.path.join(d, cid + ".wav")
    txt = os.path.join(d, cid + ".txt")
    if os.path.exists(wav) and os.path.exists(txt):
        return wav, io.open(txt, encoding="utf-8").read().strip()
    return None, None


def duck_expr(spans):
    return "".join("between(t,%.2f,%.2f)+" % sp for sp in spans).rstrip("+")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("workdir")
    ap.add_argument("episode")
    ap.add_argument("--api", default="http://127.0.0.1:9880")
    ap.add_argument("--speed", type=float, default=1.05)
    args = ap.parse_args()

    cl = os.path.join(args.workdir, "clips", args.episode)
    outdir = os.path.join(args.workdir, "revoice", args.episode + "_micro")
    tmpdir = os.path.join(args.workdir, "revoice", "_tmp")
    os.makedirs(outdir, exist_ok=True)
    os.makedirs(tmpdir, exist_ok=True)

    from faster_whisper import WhisperModel
    m = WhisperModel("small", device="cpu", compute_type="int8")
    shots = plan_shots(args.workdir, args.episode)

    n_mute = n_fill = 0
    fixed = {}
    for mp4 in sorted(glob.glob(os.path.join(cl, "*.mp4"))):
        sid = int(os.path.splitext(os.path.basename(mp4))[0])
        info = shots.get(sid, {})
        lines, chars = info.get("lines", []), info.get("chars", [])
        # 双轮 ASR(VAD 开/关各一轮——单轮口径抖动是镜06/11 漏网根源)
        a1, _ = m.transcribe(mp4, language="zh", vad_filter=True)
        a2, _ = m.transcribe(mp4, language="zh", vad_filter=False)
        segs1, segs2 = list(a1), list(a2)
        if not lines:
            # 无台词镜:任一轮听到人声→该段静音
            spans = [(s.start, s.end) for s in segs1 if s.text.strip()]
            spans += [(s.start, s.end) for s in segs2 if s.text.strip()]
            spans = [(round(a, 2), round(b, 2)) for a, b in spans if b - a >= 0.4]
            if not spans:
                continue
            dst = os.path.join(outdir, "%02d.mp4" % sid)
            r = subprocess.run([FF, "-y", "-v", "error", "-i", mp4,
                                "-af", "volume='if(%s,0.0,1.0)':eval=frame" % duck_expr(spans),
                                "-c:v", "copy", "-c:a", "aac", "-b:a", "192k", dst],
                               capture_output=True)
            if r.returncode == 0:
                fixed[sid] = dst
                n_mute += 1
                print("镜%02d 幽灵静音 %d 段" % (sid, len(spans)))
            continue
        # 有台词镜:漏句检测(只用 VAD 开的轮——VAD 关轮 whisper 会把短句自动
        # 补全成惯用语,镜10 只念「我」被补成「我反悔了」=假命中实锤);判定=
        # 探针子串 或 整镜联合文本相似度(逐段比会误杀转写噪声句,镜01 实锤)
        joined = cjk("".join(s.text for s in segs1))
        missing = []
        for text in lines:
            want = cjk(text)
            if not want:
                continue
            if want[:5] and want[:5] in joined:
                continue
            if SequenceMatcher(None, want, joined).ratio() >= 0.6:
                continue
            missing.append(text)
        if not missing:
            continue
        # 补配:该镜角色的音色锁→GPT-SoVITS 合成→压掉 H3 对应残句段→叠入
        cid = next((c for c in chars), None)
        ref_wav, ref_txt = (lock_for(args.workdir, cid) if cid else (None, None))
        # 锁段不足 3s(GPT-SoVITS 硬下限)回落 voice_lib 参考件;弱锁待后续集采集
        # 更长段自动升级后自然接管
        if ref_wav and dur_of(ref_wav) < 3.0:
            ref_wav, ref_txt = None, None
        if not ref_wav and cid and info.get("vlib", {}).get(cid):
            k = info["vlib"][cid]
            w, t = os.path.join(GS_DIR, k + ".wav"), os.path.join(GS_DIR, k + ".txt")
            if os.path.exists(w) and os.path.exists(t):
                ref_wav, ref_txt = w, io.open(t, encoding="utf-8").read().strip()
        if not ref_wav:
            print("镜%02d 漏句但无参考音,跳过补配: %s" % (sid, [t[:10] for t in missing]))
            continue
        # H3 残句段(该镜全部人声段)静音+克隆按窗叠
        spans = [(s.start, s.end) for s in segs1 if s.text.strip()] or [(0.2, min(2.5, dur_of(mp4)))]
        inputs = ["-i", mp4]
        filters = []
        labels = []
        for j, text in enumerate(missing):
            try:
                data = synth(text, ref_wav, ref_txt, args.speed, args.api)
            except Exception as e:
                print("镜%02d 补配失败: %s" % (sid, str(e)[:80]))
                continue
            w = os.path.join(tmpdir, "fill_%d_%d.wav" % (sid, j))
            io.open(w, "wb").write(data)
            d = dur_of(w)
            win = max(spans[0][1] - spans[0][0], 0.8)
            if d > win * 1.05:
                w2 = w
                w = os.path.join(tmpdir, "fill_%d_%d_t.wav" % (sid, j))
                subprocess.run([FF, "-y", "-v", "error", "-i", w2,
                                "-af", "atempo=%.3f" % min(d / win, 1.5), w], capture_output=True)
            filters.append("[%d:a]adelay=%d:all=1[v%d]" % (j + 1, int(spans[0][0] * 1000), j))
            labels.append("[v%d]" % j)
            inputs += ["-i", w]
        if not labels:
            continue
        af = ("[0:a]volume='if(%s,0.0,1.0)':eval=frame[base];" % duck_expr(spans) +
              ";".join(filters) + ";[base]%samix=inputs=%d:duration=first:normalize=0[aout]"
              % ("".join(labels), len(labels) + 1))
        dst = os.path.join(outdir, "%02d.mp4" % sid)
        r = subprocess.run([FF, "-y", "-v", "error"] + inputs +
                           ["-filter_complex", af, "-map", "0:v", "-map", "[aout]",
                            "-c:v", "copy", "-c:a", "aac", "-b:a", "192k", dst], capture_output=True)
        if r.returncode == 0:
            fixed[sid] = dst
            n_fill += 1
            print("镜%02d 漏句补配 %d 句(音色锁:%s)" % (sid, len(missing), os.path.basename(ref_wav)))
        else:
            print("镜%02d 混音失败: %s" % (sid, r.stderr.decode('utf-8', 'replace')[-120:]))

    # 拼装
    lst = os.path.join(outdir, "concat.txt")
    with io.open(lst, "w", encoding="utf-8") as f:
        for mp4 in sorted(glob.glob(os.path.join(cl, "*.mp4"))):
            sid = int(os.path.splitext(os.path.basename(mp4))[0])
            f.write("file '%s'\n" % fixed.get(sid, mp4))
    out = os.path.join(args.workdir, args.episode + "_成片_微创版.mp4")
    r = subprocess.run([FF, "-y", "-v", "error", "-f", "concat", "-safe", "0", "-i", lst,
                        "-c", "copy", out], capture_output=True)
    print("微创成片: 静音 %d 镜 + 补配 %d 镜 -> %s %s"
          % (n_mute, n_fill, out, "OK" if r.returncode == 0 else "FAIL"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
