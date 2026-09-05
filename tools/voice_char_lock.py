# -*- coding: utf-8 -*-
"""角色音色锁(2026-09-06 用户规则:音色锁角色,所有集都需要锁)。

与 char_lib 锁脸同构的锁声:每个角色取一份「H3 实际渲染的该角色台词」作权威参考
音,落 <workdir>/voice_lock/<角色>.wav + .txt,全集所有集的补配永远用这份——
音色与 H3 渲染零色差、跨集零漂移。锁=稳定:已锁不再覆盖(--force 才换)。

选段标准:该角色独白/单角色镜(归属无歧义)的 H3 台词,ASR 置信最高、时长 3-9s;
不足 3s 用最长段+相邻静音垫。

用法: python tools/h3_microsurgery.py 的配套采集——
      python tools/voice_char_lock.py <workdir> [--episodes EP01,EP02] [--force]
"""
import argparse
import glob
import io
import json
import os
import re
import subprocess
import sys

os.environ.setdefault("HF_HUB_OFFLINE", "1")

FF = r"C:/Mi/Apps/GPT-SoVITS/gsenv/Library/bin/ffmpeg.exe"
FFPROBE = FF.replace("ffmpeg", "ffprobe")
D_LINE = re.compile(r"<d>\s*\[Chinese\]\s*(.*?)\s*</d>", re.S)


def dur_of(p):
    r = subprocess.run([FFPROBE, "-v", "error", "-show_entries", "format=duration",
                        "-of", "csv=p=0", p], capture_output=True, text=True)
    try:
        return float(r.stdout.strip())
    except Exception:
        return 0.0


def shot_audio_def_speakers(hp):
    """<Audio N> is the voice-timbre reference for <Subject M> (Sx) → 说者名(英文Subject 描述
    难以回指中文名,这里取登场角色序:Subject 序=登场序)。返回 [登场角色名](按 S 序)。"""
    chars = []
    for m in re.finditer(r"<Subject (\d+)> is ([^,\n]+)", hp):
        chars.append(int(m.group(1)))
    return chars


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("workdir")
    ap.add_argument("--episodes", default="", help="只扫指定集(逗号分隔;默认全部已渲染集)")
    ap.add_argument("--force", action="store_true")
    ap.add_argument("--min-sec", type=float, default=3.0)
    args = ap.parse_args()

    lock_dir = os.path.join(args.workdir, "voice_lock")
    os.makedirs(lock_dir, exist_ok=True)
    eps = [e.strip() for e in args.episodes.split(",") if e.strip()]
    if not eps:
        eps = sorted(os.path.basename(p) for p in glob.glob(os.path.join(args.workdir, "clips", "*")))

    from faster_whisper import WhisperModel
    model = WhisperModel("small", device="cpu", compute_type="int8")

    # 候选:角色 → [(片段路径, t0, t1, 置信, 台词)]
    cands = {}
    for ep in eps:
        plan = os.path.join(args.workdir, "analysis", ep + "_direct_plan.json")
        if not os.path.exists(plan):
            continue
        d = json.load(io.open(plan, encoding="utf-8"))
        for s in d.get("shots") or []:
            hp = s.get("h3_prompt") or ""
            lines = D_LINE.findall(hp)
            chars = s.get("characters") or []
            if not lines or not chars:
                continue
            mp4 = os.path.join(args.workdir, "clips", ep, "%02d.mp4" % int(s.get("shot_id") or 0))
            if not os.path.exists(mp4):
                continue
            # 单角色镜→归属明确;多角色镜跳过(说者歧义)
            if len(chars) != 1:
                continue
            cid = str(chars[0])
            segs, _ = model.transcribe(mp4, language="zh", vad_filter=True)
            segs = list(segs)
            for text in lines:
                want = re.sub(r"[^\u4e00-\u9fff]", "", re.sub(r"<[^>]+>", "", text))[:8]
                for i, sg in enumerate(segs):
                    got = re.sub(r"[^\u4e00-\u9fff]", "", sg.text)
                    if want[:5] and want[:5] in got:
                        t0, t1 = max(sg.start - 0.2, 0), min(sg.end + 0.2, dur_of(mp4))
                        cands.setdefault(cid, []).append((mp4, t0, t1, sg.avg_logprob,
                                                         re.sub(r"<[^>]+>", "", text)))
                        break
    # 每角色取置信最高且 ≥min-sec 的段;全不足取最长的
    locked, weak = [], []
    for cid, lst in cands.items():
        wav = os.path.join(lock_dir, cid + ".wav")
        txt = os.path.join(lock_dir, cid + ".txt")
        if os.path.exists(wav) and os.path.exists(txt) and not args.force:
            continue
        ok = [c for c in lst if c[2] - c[1] >= args.min_sec]
        pool = ok or lst
        if not pool:
            continue
        best = max(pool, key=lambda c: (c[2] - c[1] >= args.min_sec, c[3]))
        mp4, t0, t1, lp, text = best
        # 弱锁自动升级(2026-09-06):已锁段不足 min_sec 时,新段更长即可替换;
        # 足长锁=稳定,不覆盖(--force 强制)
        if os.path.exists(wav) and not args.force:
            meta_old = os.path.join(lock_dir, cid + ".json")
            try:
                old_sec = json.load(io.open(meta_old, encoding="utf-8")).get("seconds", 99)
            except Exception:
                old_sec = 99
            if old_sec >= args.min_sec or (t1 - t0) <= old_sec:
                continue
        r = subprocess.run([FF, "-y", "-v", "error", "-ss", "%.2f" % t0, "-t", "%.2f" % (t1 - t0),
                            "-i", mp4, "-vn", "-ac", "1", "-ar", "32000",
                            "-af", "highpass=f=140,lowpass=f=6800,afftdn=nr=12:nf=-25", wav],
                           capture_output=True)
        if r.returncode != 0 or not os.path.exists(wav) or os.path.getsize(wav) == 0:
            continue
        segs2, _ = model.transcribe(wav, language="zh", vad_filter=False)
        tr = "".join(x.text.strip() for x in segs2).replace(" ", "")
        if len(tr) < 4:
            os.remove(wav)
            continue
        io.open(txt, "w", encoding="utf-8").write(tr)
        meta = {"source": mp4, "window": [t0, t1], "asr_lp": round(lp, 3), "seconds": round(t1 - t0, 2)}
        io.open(os.path.join(lock_dir, cid + ".json"), "w", encoding="utf-8").write(
            json.dumps(meta, ensure_ascii=False, indent=2))
        (locked if (t1 - t0) >= args.min_sec else weak).append((cid, meta))
        print("[lock] %-12s %.1fs lp%.2f <- %s" % (cid, t1 - t0, lp, os.path.basename(mp4)))
    print("锁定 %d | 短段待更优 %d | 目录 %s" % (len(locked), len(weak), lock_dir))
    for cid, m in weak:
        print("  [weak] %s 仅 %.1fs(后续集有更长的会更好?锁已定,--force 才换)" % (cid, m["seconds"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
