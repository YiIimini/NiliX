# -*- coding: utf-8 -*-
"""音色库外部音源导入工具(2026-09-04 配音去 AI 味升级)。

背景:音色库 39 个档位音色此前全部由 edge-tts 合成(约 2s 机械念白),H3 拿它当
音色参考,平板韵律被继承放大=AI 味根源。官方音色克隆最佳实践:干净干声(无混响/
无背景噪音/单说话人)8-15 秒。本工具把真实人声音源(动漫角色台词干声/真人录音/
GPT-SoVITS 克隆产物)导入为指定档位音色——Go 侧 genVoiceLibAudio 检测 .src 标记
即跳过 edge-tts 兜底(手工音源永不被合成版覆盖)。

⚠️ 版权提示:动漫角色台词的声线权益归声优/版权方所有。个人研究/自用通常无碍,
公开发布/商用作品使用克隆声线有声音权与著作权风险(民法典声音权益+深度合成
管理规定),商用请选用可商用声源(Fish/CosyVoice 官方声线/开源多说话人声库/自录)。

用法:
  python tools/voice_import.py --list                       # 列出档位与当前来源
  python tools/voice_import.py --src 台词.mp3 --key lib_male_deep \
      [--ss 12.5] [--t 12]                                  # 截取导入(默认取前 15s)
"""
import argparse
import glob
import io
import json
import os
import shutil
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
VOICE_LIB = os.path.join(ROOT, "asset_lib", "voices")
AUDIO_DIR = os.path.join(VOICE_LIB, "audio")
COMFY_INPUT_AUDIO = os.path.join(ROOT, "comfyui", "shared", "input", "audio")
INDEX = os.path.join(VOICE_LIB, "index.json")


def find_ffmpeg():
    for c in ("ffmpeg", shutil.which("ffmpeg")):
        if c and shutil.which(c):
            return c
    venv_ff = os.path.join(ROOT, "comfyui", "ComfyUI", ".venv", "Scripts", "ffmpeg.exe")
    return venv_ff if os.path.exists(venv_ff) else None


def load_keys():
    keys = []
    for f in sorted(glob.glob(os.path.join(AUDIO_DIR, "lib_*.mp3"))):
        base = os.path.basename(f)
        key = base[:-4]
        keys.append((key, os.path.exists(f[:-4] + ".src"), os.path.getsize(f)))
    return keys


def update_index(key, size):
    try:
        d = json.load(io.open(INDEX, encoding="utf-8"))
    except Exception:
        d = {"count": 0, "voices": []}
    import time
    voices = d.get("voices") or []
    entry = {"file": "audio/%s.mp3" % key, "key": key, "size_bytes": size,
             "updated_at": int(time.time()), "source": "external"}
    d["voices"] = [v for v in voices if v.get("key") != key] + [entry]
    d["count"] = len(d["voices"])
    io.open(INDEX, "w", encoding="utf-8", newline="").write(
        json.dumps(d, ensure_ascii=False, indent=1))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--list", action="store_true", help="列出全部档位与来源")
    ap.add_argument("--src", default="", help="源音频文件(干声)")
    ap.add_argument("--key", default="", help="目标档位 key(如 lib_male_deep)")
    ap.add_argument("--ss", default="", help="截取起点秒(默认 0)")
    ap.add_argument("--t", default="15", help="截取时长秒(默认 15,官方 10s+)")
    ap.add_argument("--note", default="", help="来源说明(写入 .src 标记)")
    args = ap.parse_args()

    if args.list or not args.src:
        keys = load_keys()
        ext = sum(1 for _, is_ext, _ in keys if is_ext)
        print("音色档位 %d 个,外部音源 %d 个" % (len(keys), ext))
        for key, is_ext, size in keys:
            print("  %s%-24s %6.1f KB" % ("🔗" if is_ext else "🤖", key, size / 1024))
        print("🤖=edge-tts 合成(AI 味) 🔗=外部真实音源;导入: --src 干声.mp3 --key <档位>")
        return 0

    if not args.key.startswith("lib_"):
        print("key 须为既有档位(先 --list 查看),如 lib_male_deep")
        return 2
    dst = os.path.join(AUDIO_DIR, args.key + ".mp3")
    if not os.path.exists(dst):
        print("未知档位 key(不在音色库清单):", args.key)
        return 2
    ff = find_ffmpeg()
    if not ff:
        print("未找到 ffmpeg(PATH 或 ComfyUI venv)")
        return 2
    # 截取 + 单声道 44.1k mp3 + 响度归一(EBU R128,参考电平 -16LUFS 口播标准)
    tmp = dst + ".tmp.mp3"
    cmd = [ff, "-y", "-i", args.src]
    if args.ss:
        cmd += ["-ss", args.ss]
    cmd += ["-t", args.t, "-ac", "1", "-ar", "44100", "-b:a", "128k",
            "-af", "loudnorm=I=-16:TP=-1.5:LRA=11", tmp]
    r = subprocess.run(cmd, capture_output=True)
    if r.returncode != 0 or not os.path.exists(tmp) or os.path.getsize(tmp) < 20000:
        print("ffmpeg 处理失败:", r.stderr.decode("utf-8", "ignore")[-400:])
        if os.path.exists(tmp):
            os.remove(tmp)
        return 2
    # 落盘:mp3 + .src 标记(Go 侧据此跳过 edge-tts 覆盖) + 同步 Comfy input
    if not os.path.exists(dst + ".bak") and os.path.exists(dst):
        shutil.copy2(dst, dst + ".bak")
    os.replace(tmp, dst)
    io.open(dst[:-4] + ".src", "w", encoding="utf-8").write(
        args.note or ("imported from %s" % os.path.basename(args.src)))
    os.makedirs(COMFY_INPUT_AUDIO, exist_ok=True)
    shutil.copy2(dst, os.path.join(COMFY_INPUT_AUDIO, args.key + ".mp3"))
    update_index(args.key, os.path.getsize(dst))
    print("✅ 已导入 %s ← %s(%s 秒起,截 %s 秒;旧文件备份 .bak;.src 标记已写)" % (
        args.key, os.path.basename(args.src), args.ss or "0", args.t))
    print("   渲染时该档位角色自动使用真实音源;镜头指纹含音频 mtime,已渲镜头自动 stale 重出")
    return 0


if __name__ == "__main__":
    sys.exit(main())
