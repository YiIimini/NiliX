# -*- coding: utf-8 -*-
"""GPT-SoVITS 微调语料制备(2026-09-05 STEP3③:零射击→每档专属声模)。

从原神 parquet 分片为指定角色抽 3-8s 干声(限单说话人铁律),faster-whisper 批量
转写,落 GPT-SoVITS 训练目录约定:
  tools/logs/ft/<档名>/wav32k/<序号>.wav   32k 单声道
  tools/logs/ft/<档名>/asr_opt/speech.list  "wav路径|音色名|语言|转写" 每行一条

用法: python tools/voice_ft_prepare.py --char 温迪 --key boy_teen --minutes 5
"""
import argparse
import glob
import io
import os
import subprocess
import sys
import wave

import pyarrow.parquet as pq

os.environ.setdefault("HF_HUB_OFFLINE", "1")

FFMPEG = r"C:/Mi/Apps/GPT-SoVITS/gsenv/Library/bin/ffmpeg.exe"
SRC_GLOB = r"D:/Ai/NiliX/tools/voice_src/shard*.parquet"
OUT_ROOT = r"D:/Ai/NiliX/tools/logs/ft"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--char", required=True, help="分片内角色名(如 温迪)")
    ap.add_argument("--key", required=True, help="声库档名(如 boy_teen)")
    ap.add_argument("--minutes", type=float, default=5.0, help="目标语料分钟数")
    ap.add_argument("--min-sec", type=float, default=3.0)
    ap.add_argument("--max-sec", type=float, default=12.0)
    args = ap.parse_args()

    out_dir = os.path.join(OUT_ROOT, args.key)
    wav_dir = os.path.join(out_dir, "wav32k")
    asr_dir = os.path.join(out_dir, "asr_opt")
    os.makedirs(wav_dir, exist_ok=True)
    os.makedirs(asr_dir, exist_ok=True)

    target = args.minutes * 60
    n = 0
    total = 0.0
    list_lines = []
    for shard in sorted(glob.glob(SRC_GLOB)):
        if total >= target:
            break
        t = pq.read_table(shard, columns=["npcName", "audio"])
        for name, aud in zip(t.column("npcName").to_pylist(), t.column("audio").to_pylist()):
            if name != args.char or total >= target:
                continue
            raw = os.path.join(wav_dir, "raw_%04d.wav" % n)
            with open(raw, "wb") as f:
                f.write(aud["bytes"])
            try:
                with wave.open(raw) as w:
                    d = w.getnframes() / w.getframerate()
            except Exception:
                os.remove(raw)
                continue
            if not (args.min_sec <= d <= args.max_sec):
                os.remove(raw)
                continue
            # 32k 单声道 + 干声链(高通/低通/afftdn 降噪,与推理参考件同链)
            out = os.path.join(wav_dir, "%s_%04d.wav" % (args.key, n))
            r = subprocess.run([FFMPEG, "-y", "-v", "error", "-i", raw, "-ac", "1", "-ar", "32000",
                                "-af", "highpass=f=140,lowpass=f=6800,afftdn=nr=12:nf=-25", out],
                               capture_output=True)
            os.remove(raw)
            if r.returncode != 0 or not os.path.exists(out) or os.path.getsize(out) == 0:
                continue
            n += 1
            total += d
        print("  [%s] 已抽 %d 条 / %.0fs" % (args.char, n, total))

    # 批量转写(小模型够用;语料转写错几个字影响可忽略,训练对齐以音频为主)
    from faster_whisper import WhisperModel
    m = WhisperModel("small", device="cpu", compute_type="int8")
    list_path = os.path.join(asr_dir, "speech.list")
    with io.open(list_path, "w", encoding="utf-8") as lf:
        for i in range(n):
            wav = os.path.join(wav_dir, "%s_%04d.wav" % (args.key, i))
            segs, _ = m.transcribe(wav, language="zh", vad_filter=False)
            text = "".join(s.text.strip() for s in segs).replace(" ", "")
            if len(text) < 2:
                continue
            lf.write("%s|%s|ZH|%s\n" % (wav, args.key, text))
    print("完成: %d 条 / %.0fs -> %s" % (n, total, list_path))
    return 0


if __name__ == "__main__":
    sys.exit(main())
