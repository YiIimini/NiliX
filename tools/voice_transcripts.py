# -*- coding: utf-8 -*-
"""音色库参考音频转写(GPT-SoVITS prompt_text 生成,2026-09-05 STEP3)。

GPT-SoVITS /tts 的参考音频需要配套 prompt_text(参考音频说的内容文字稿)才有
稳定克隆效果;voice_lib 的 .src 只存来源标注。本脚本对全部 lib_*.mp3 干声跑
faster-whisper(CPU)转写,落 lib_<key>.txt(UTF-8 一行)。幂等:已有 .txt 跳过。
用法: python tools/voice_transcripts.py [--force]
"""
import argparse
import glob
import io
import os
import sys

os.environ.setdefault("HF_HUB_OFFLINE", "1")
from faster_whisper import WhisperModel

AUDIO_DIR = r"D:/Ai/NiliX/asset_lib/voices/audio"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--force", action="store_true")
    args = ap.parse_args()
    model = WhisperModel("small", device="cpu", compute_type="int8")
    done = skip = empty = 0
    for mp3 in sorted(glob.glob(os.path.join(AUDIO_DIR, "lib_*.mp3"))):
        txt = mp3[:-4] + ".txt"
        if os.path.exists(txt) and not args.force:
            skip += 1
            continue
        segs, _ = model.transcribe(mp3, language="zh", vad_filter=False)
        text = "".join(s.text.strip() for s in segs).replace(" ", "")
        if not text:
            empty += 1
            print("[empty]", os.path.basename(mp3))
            continue
        io.open(txt, "w", encoding="utf-8").write(text)
        done += 1
        print("[ok]", os.path.basename(mp3), "->", text[:40])
    print(f"转写 {done} | 跳过 {skip} | 空 {empty}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
