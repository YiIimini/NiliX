# -*- coding: utf-8 -*-
"""拼接候选干声为组合参考(多句→8-17s,官方 10s+ 建议)。"""
import glob
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
FF = "ffmpeg"


def combo(name, out):
    wavs = sorted(glob.glob(os.path.join(HERE, name, name + "_*.wav")))
    if not wavs:
        print(name, "无候选")
        return False
    n = len(wavs)
    dst = os.path.join(HERE, out + ".wav")
    cmd = [FF, "-y"]
    for w in wavs:
        cmd += ["-i", w]
    streams = "".join("[%d:a]" % i for i in range(n))
    cmd += ["-filter_complex", "%sconcat=n=%d:v=0:a=1[out]" % (streams, n), "-map", "[out]", dst]
    r = subprocess.run(cmd, capture_output=True)
    ok = r.returncode == 0 and os.path.exists(dst) and os.path.getsize(dst) > 100000
    print(name, "->", out + ".wav", "OK" if ok else "FAIL", n, "句")
    return ok


if __name__ == "__main__":
    for name, out in [a.split(":") for a in sys.argv[1:]]:
        combo(name, out)
