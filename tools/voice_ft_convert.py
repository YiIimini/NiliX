# -*- coding: utf-8 -*-
"""SoVITS v4 LoRA 原始 ckpt → 推理权重(GPT-SoVITS savee 等价,config 纯 dict)。
用法: python tools/voice_ft_convert.py <key> [...](无参=全部 logs/ 下已训练档)
"""
import glob
import io
import json
import os
import sys

import torch

GS = r"C:/Mi/Apps/GPT-SoVITS"
sys.path.insert(0, os.path.join(GS, "GPT_SoVITS"))
import process_ckpt  # noqa: E402

OUT = r"C:/Mi/Apps/GPT-SoVITS/SoVITS_weights_v4"


def convert(key):
    raw = os.path.join(GS, "logs", key, "logs_s2_v4_lora_32", "G_233333333333.pth")
    cfgp = os.path.join(GS, "logs", key, "tmp_s2.json")
    if not (os.path.exists(raw) and os.path.exists(cfgp)):
        print("[skip]", key, "(ckpt/config 缺)")
        return
    ckpt = torch.load(raw, map_location="cpu", weights_only=False)
    hps = json.load(io.open(cfgp, encoding="utf-8"))
    rank = hps["train"]["lora_rank"]
    step = int(ckpt.get("iteration", 0))
    name = "%s_e8_s%d_l%d" % (key, step, rank)
    opt = {}
    opt["weight"] = {k: v.half() for k, v in ckpt["model"].items() if "enc_q" not in k}
    opt["config"] = hps
    opt["info"] = "8epoch_%siteration" % step
    opt["lora_rank"] = rank
    bio = io.BytesIO()
    torch.save(opt, bio)
    bio.seek(0)
    data = bio.getvalue()
    os.makedirs(OUT, exist_ok=True)
    with open(os.path.join(OUT, name + ".pth"), "wb") as f:
        f.write(process_ckpt.model_version2byte["v4"] + data[2:])
    print("[ok]", name)


if __name__ == "__main__":
    keys = sys.argv[1:] or [os.path.basename(p) for p in glob.glob(os.path.join(GS, "logs", "*")) ]
    for k in keys:
        convert(k)
