# -*- coding: utf-8 -*-
"""解析 genshin-voice 分片:按角色统计并导出候选干声(供 voice_import.py 导入)。

用法:
  python tools/voice_src/extract_voices.py            # 统计角色分布
  python tools/voice_src/extract_voices.py --npc 派蒙 --out paimon   # 导出该角色全部台词
"""
import argparse
import io
import json
import os
import struct
import wave

import pyarrow.parquet as pq

HERE = os.path.dirname(os.path.abspath(__file__))


def wav_bytes_len(b):
    try:
        with wave.open(io.BytesIO(b)) as w:
            return w.getnframes() / w.getframerate()
    except Exception:
        return 0.0


def save_wav(b, path):
    with open(path, "wb") as f:
        f.write(b)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--npc", default="")
    ap.add_argument("--shard", default="shard0.parquet")
    ap.add_argument("--out", default="")
    ap.add_argument("--min-sec", type=float, default=6.0)
    ap.add_argument("--max-sec", type=float, default=18.0)
    ap.add_argument("--limit", type=int, default=6)
    args = ap.parse_args()

    t = pq.read_table(os.path.join(HERE, args.shard), columns=["npcName", "text", "audio"])
    d = t.to_pydict()
    n = len(d["npcName"])
    print("分片总条数:", n)

    if not args.npc:
        from collections import Counter
        c = Counter(d["npcName"])
        print("角色数:", len(c))
        for name, cnt in c.most_common(40):
            print("  %s: %d 条" % (name, cnt))
        return 0

    outdir = os.path.join(HERE, args.out or args.npc)
    os.makedirs(outdir, exist_ok=True)
    picked = 0
    manifest = []
    for i in range(n):
        if d["npcName"][i] != args.npc:
            continue
        audio = d["audio"][i]
        b = audio.get("bytes") if isinstance(audio, dict) else None
        if not b:
            continue
        sec = wav_bytes_len(b)
        text = (d["text"][i] or "").strip()
        if not (args.min_sec <= sec <= args.max_sec) or len(text) < 12:
            continue
        fn = "%s_%02d_%.1fs.wav" % (args.out or args.npc, picked + 1, sec)
        save_wav(b, os.path.join(outdir, fn))
        manifest.append({"file": fn, "sec": round(sec, 1), "text": text})
        picked += 1
        if picked >= args.limit:
            break
    io.open(os.path.join(outdir, "manifest.json"), "w", encoding="utf-8").write(
        json.dumps(manifest, ensure_ascii=False, indent=1))
    print("导出 %d 条(%s~%ss)→ %s(见 manifest.json 文本,人工挑情感丰富句)" % (
        picked, args.min_sec, args.max_sec, outdir))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
