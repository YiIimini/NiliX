# -*- coding: utf-8 -*-
"""声库 GPT-SoVITS 参考件批量预制(2026-09-05 STEP3)。

GPT-SoVITS 克隆要求参考音频 3~10 秒且必须给段内文字稿(prompt_text);voice_lib
的 lib_*.mp3 干声普遍超长、.txt 是全文转写(与段不匹配)。本脚本为每档预制:
  asset_lib/voices/gs/<key>.wav   —— 干声前 8 秒(32k 单声道,GPT-SoVITS 直用)
  asset_lib/voices/gs/<key>.txt   —— 该 8 秒段的 faster-whisper 转写
幂等:两文件都在则跳过。--force 重做。
用法: python tools/voice_gs_refs.py [--force]
"""
import argparse
import glob
import io
import os
import subprocess
import sys

os.environ.setdefault("HF_HUB_OFFLINE", "1")
from faster_whisper import WhisperModel

AUDIO_DIR = r"D:/Ai/NiliX/asset_lib/voices/audio"
GS_DIR = r"D:/Ai/NiliX/asset_lib/voices/gs"
FFMPEG = r"C:/Mi/Apps/GPT-SoVITS/gsenv/Library/bin/ffmpeg.exe"
SEG_SEC = 8


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--force", action="store_true")
    args = ap.parse_args()
    os.makedirs(GS_DIR, exist_ok=True)
    model = WhisperModel("small", device="cpu", compute_type="int8")
    done = skip = empty = 0
    for mp3 in sorted(glob.glob(os.path.join(AUDIO_DIR, "lib_*.mp3"))):
        key = os.path.splitext(os.path.basename(mp3))[0][4:]  # lib_ 前缀剥掉
        wav = os.path.join(GS_DIR, key + ".wav")
        txt = os.path.join(GS_DIR, key + ".txt")
        if os.path.exists(wav) and os.path.exists(txt) and not args.force:
            skip += 1
            continue
        # 2026-09-05 音源去混响(用户验收 C 案):原神游戏语音带引擎混响,克隆会
        # 连混响一起学走=「电子回音」;参考件统一过高通切低频+anlmdn 降噪收窄,
        # 与换声输出后处理同链(cmp_C 拍板)
        af = "highpass=f=140,lowpass=f=6800,afftdn=nr=12:nf=-25"
        r = subprocess.run([FFMPEG, "-y", "-v", "error", "-i", mp3, "-ss", "0",
                            "-t", str(SEG_SEC), "-ac", "1", "-ar", "32000",
                            "-af", af, wav],
                           capture_output=True)
        if r.returncode != 0 or not os.path.exists(wav):
            print("[fferr]", key, r.stderr.decode("utf-8", "replace")[:80])
            continue
        segs, _ = model.transcribe(wav, language="zh", vad_filter=False)
        text = "".join(s.text.strip() for s in segs).replace(" ", "")
        if len(text) < 6:
            empty += 1
            print("[empty]", key, repr(text))
            continue
        io.open(txt, "w", encoding="utf-8").write(text)
        done += 1
        print("[ok]", key, "->", text[:36])
    print(f"预制 {done} | 跳过 {skip} | 空 {empty}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
