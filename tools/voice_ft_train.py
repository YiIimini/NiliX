# -*- coding: utf-8 -*-
"""GPT-SoVITS 微调一键训练(2026-09-05 STEP3③)。webui 同款 env+tmp 配置直驱:

  1a get-text(bert)   → env: inp_text/opt_dir/bert_pretrained_dir/i_part/all_parts/_CUDA_VISIBLE_DEVICES/is_half
  1b hubert+wav32k    → env: + cnhubert_base_dir/sv_path
  1c semantic         → env: + pretrained_s2G/s2config_path
  SoVITS v4 = s2_train_v3_lora.py --config tmp_s2.json(lora 微调)
  GPT       = s1_train.py --config_file tmp_s1.yaml(s1longer-v2.yaml 派生)

产物: logs/<exp>/logs_s2_v4/*.pth + logs_s1_v4/*_ep*.ckpt
     → 复制 asset_lib/voices/ft/<key>/
用法: python tools/voice_ft_train.py --key boy_teen --epochs 8 --bs 6
"""
import argparse
import io
import json
import os
import re
import shutil
import subprocess
import sys

import yaml

GS_ROOT = r"C:/Mi/Apps/GPT-SoVITS"
PY = os.path.join(GS_ROOT, "gsenv", "python.exe")
FT_ROOT = r"D:/Ai/NiliX/tools/logs/ft"
OUT_ROOT = r"D:/Ai/NiliX/asset_lib/voices/ft"
PM = os.path.join(GS_ROOT, "GPT_SoVITS", "pretrained_models")

BERT = os.path.join(PM, "chinese-roberta-wwm-ext-large")
SSL = os.path.join(PM, "chinese-hubert-base")
SV = os.path.join(PM, "sv")
S2G_V4 = os.path.join(PM, "gsv-v4-pretrained/s2Gv4.pth")
S2D_V4 = os.path.join(PM, "gsv-v4-pretrained/s2D488k.pth")
S1_V2 = os.path.join(PM, "gsv-v2final-pretrained/s1bert25hz-5kh-longer-epoch=12-step=369668.ckpt")

ENV_BASE = {
    "PYTHONIOENCODING": "utf-8",
    "inp_text": "",
    "inp_wav_dir": "",
    "exp_name": "",
    "opt_dir": "",
    "i_part": "0",
    "all_parts": "1",
    "_CUDA_VISIBLE_DEVICES": "0",
    "is_half": "True",
    "bert_pretrained_dir": BERT,
    "cnhubert_base_dir": SSL,
    "sv_path": SV,
    "pretrained_s2G": S2G_V4,
    "s2config_path": os.path.join(GS_ROOT, "GPT_SoVITS/configs/s2.json"),
}


def run(cmd, env, tail=5):
    print("  $", " ".join(cmd[:4]), "...")
    r = subprocess.run(cmd, env=env, cwd=GS_ROOT, capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    if r.returncode != 0:
        print(r.stdout[-1200:])
        print(r.stderr[-1200:])
        raise SystemExit("step failed: %s" % " ".join(cmd[:3]))
    if r.stdout:
        print("   " + "\n   ".join(r.stdout.strip().splitlines()[-tail:]))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--key", required=True)
    ap.add_argument("--epochs", type=int, default=8)
    ap.add_argument("--bs", type=int, default=6)
    ap.add_argument("--version", default="v4")
    args = ap.parse_args()

    list_path = os.path.join(FT_ROOT, args.key, "asr_opt", "speech.list")
    if not os.path.exists(list_path):
        raise SystemExit("缺 speech.list: " + list_path)
    exp = args.key
    opt_dir = os.path.join(GS_ROOT, "logs", exp)
    os.makedirs(opt_dir, exist_ok=True)

    env = dict(os.environ)
    env["PYTHONPATH"] = os.pathsep.join([os.path.join(GS_ROOT, "GPT_SoVITS"), GS_ROOT])
    env.update(ENV_BASE)
    env.update({"inp_text": list_path, "inp_wav_dir": "", "exp_name": exp, "opt_dir": opt_dir})

    print("[1/5] get-text")
    run([PY, "-s", "GPT_SoVITS/prepare_datasets/1-get-text.py"], env)
    # 分片汇总(webui 同款):2-name2text-0.txt → 2-name2text.txt
    part = os.path.join(opt_dir, "2-name2text-0.txt")
    if os.path.exists(part):
        shutil.copy(part, os.path.join(opt_dir, "2-name2text.txt"))
    print("[2/5] hubert+wav32k")
    run([PY, "-s", "GPT_SoVITS/prepare_datasets/2-get-hubert-wav32k.py"], env)
    print("[3/5] semantic")
    run([PY, "-s", "GPT_SoVITS/prepare_datasets/3-get-semantic.py"], env)
    # semantic 汇总:带表头 item_name	semantic_audio
    ppart = os.path.join(opt_dir, "6-name2semantic-0.tsv")
    pfull = os.path.join(opt_dir, "6-name2semantic.tsv")
    if os.path.exists(ppart):
        with io.open(ppart, encoding="utf-8") as f:
            rows = f.read().replace("\r\n", "\n").strip("\n").split("\n")
        with io.open(pfull, "w", encoding="utf-8", newline="\n") as f:
            f.write("item_name\tsemantic_audio\n" + "\n".join(rows) + "\n")

    # SoVITS v4(lora):tmp_s2.json = s2.json 派生
    print("[4/5] SoVITS v4 lora 微调 (%d epochs)" % args.epochs)
    with open(os.path.join(GS_ROOT, "GPT_SoVITS/configs/s2.json")) as f:
        cfg = json.load(f)
    s2_dir = opt_dir
    os.makedirs(os.path.join(s2_dir, "logs_s2_%s" % args.version), exist_ok=True)
    cfg["train"]["batch_size"] = args.bs
    cfg["train"]["epochs"] = args.epochs
    cfg["train"]["text_low_lr_rate"] = 0.2
    cfg["train"]["pretrained_s2G"] = S2G_V4
    cfg["train"]["pretrained_s2D"] = S2D_V4
    cfg["train"]["if_save_latest"] = True
    cfg["train"]["if_save_every_weights"] = True
    cfg["train"]["save_every_epoch"] = args.epochs
    cfg["train"]["gpu_numbers"] = "0"
    cfg["train"]["grad_ckpt"] = True
    cfg["train"]["lora_rank"] = 32
    cfg["model"]["version"] = args.version
    cfg["data"]["exp_dir"] = cfg["s2_ckpt_dir"] = s2_dir
    cfg["save_weight_dir"] = os.path.join(GS_ROOT, "SoVITS_weights_v4")
    cfg["name"] = exp
    cfg["version"] = args.version
    tmp_s2 = os.path.join(opt_dir, "tmp_s2.json")
    with open(tmp_s2, "w") as f:
        json.dump(cfg, f)
    run([PY, "-s", "GPT_SoVITS/s2_train_v3_lora.py", "--config", tmp_s2], env, tail=8)

    # GPT:s1longer-v2.yaml 派生 tmp_s1.yaml
    print("[5/5] GPT 微调")
    s1_dir = opt_dir
    os.makedirs(os.path.join(s1_dir, "logs_s1"), exist_ok=True)
    with open(os.path.join(GS_ROOT, "GPT_SoVITS/configs/s1longer-v2.yaml")) as f:
        data = yaml.load(f, Loader=yaml.FullLoader)
    data["train"]["batch_size"] = args.bs
    data["train"]["epochs"] = args.epochs
    data["pretrained_s1"] = S1_V2
    data["train"]["save_every_n_epoch"] = args.epochs
    data["train"]["if_save_every_weights"] = True
    data["train"]["if_save_latest"] = True
    data["train"]["if_dpo"] = False
    data["train"]["half_weights_save_dir"] = os.path.join(GS_ROOT, "GPT_weights_v4")
    data["train"]["exp_name"] = exp
    data["train_semantic_path"] = os.path.join(s1_dir, "6-name2semantic.tsv")
    data["train_phoneme_path"] = os.path.join(s1_dir, "2-name2text.txt")
    data["output_dir"] = os.path.join(s1_dir, "logs_s1_%s" % args.version)
    tmp_s1 = os.path.join(opt_dir, "tmp_s1.yaml")
    with open(tmp_s1, "w") as f:
        yaml.dump(data, f, default_flow_style=False)
    env["_CUDA_VISIBLE_DEVICES"] = "0"
    env["hz"] = "25hz"
    run([PY, "-s", "GPT_SoVITS/s1_train.py", "--config_file", tmp_s1], env, tail=8)

    # 收割
    out = os.path.join(OUT_ROOT, args.key)
    os.makedirs(out, exist_ok=True)
    got = []
    for d, pat in ((os.path.join(s2_dir, "logs_s2_%s" % args.version), r".*\.pth$"),
                   (os.path.join(s1_dir, "logs_s1_%s" % args.version), r".*_ep\d+\.ckpt$"),
                   (os.path.join(GS_ROOT, "SoVITS_weights_v4"), r".*%s.*\.pth$" % exp),
                   (os.path.join(GS_ROOT, "GPT_weights_v4"), r".*%s.*_ep\d+\.ckpt$" % exp)):
        if not os.path.isdir(d):
            continue
        for f in os.listdir(d):
            if re.match(pat, f):
                shutil.copy(os.path.join(d, f), os.path.join(out, f))
                got.append(f)
    print("产物 ->", out)
    for g in sorted(set(got)):
        print("  ", g)
    return 0


if __name__ == "__main__":
    sys.exit(main())
