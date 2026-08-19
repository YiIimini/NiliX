# -*- coding: utf-8 -*-
"""manju_media.py — 漫剧管线媒体辅助(用 ComfyUI venv 的 Python 跑,PyAV 已装)

子命令:
  qc --dir <clips_dir> [--json <p>]      质检:时长/分辨率/音轨/近黑帧(整帧均值<20)/解码帧
                                         --json: 另写逐镜报告 JSON(审片官机械质检数据源)
  frames --video <mp4> --out-dir <d> [--count 3] [--width 768]
                                         抽帧:均匀取 N 帧存 JPEG(末行输出 JSON {"frames":[...]},审片官输入)
  assemble --clips-dir <d> --out <p> --episode <ep> [--fps 24] [--mosaic N] [--plan <direct_plan.json>]
                                         合成:镜头 crf18 拼接 + 32kHz 立体声,按 plan 台词/旁白烧录字幕,可选逐帧打码
  facecrop --src <png> --dst <png>       正脸特写参考:切上部居中头肩区域并放大(身份锁定用)
  probe --file <mp4>                     探测视频参数(宽高/帧率/帧数/音轨/编码/大小,云端2K预校验数据源),
                                         末行输出 JSON {"width":..,"height":..,"fps":..,"frames":..,"hasAudio":..,...}
  inspect --file <mp4> --out-dir <d> --count N [--json <p>]
                                         审片单进程:一遍交错解码同时产出机械质检报告(qc 规则)+抽帧 JPEG,
                                         替代原先 qc+frames 两个独立进程/同一文件解两遍;末行输出 JSON
                                         {"qc":{...}, "frames":[...]}
  jianying --clips-dir <d> --out-dir <parent> --name <draft> [--fps 24] [--plan <p>] [--transition cut]
                                         剪映草稿导出:视频轨(可选转场)+ 字幕轨(台词/旁白,不烧录,可继续编辑);
                                         需 venv 安装 pyJianYingDraft(未装时打印安装指引并 exit 2)
"""
import argparse
import json
import os
import sys


def check_video(path, threshold=0.5):
    import av
    c = av.open(path)
    v = c.streams.video[0]
    a = list(c.streams.audio)
    a0 = a[0] if a else None
    dur = float(round(v.duration * v.time_base, 2)) if v.duration else 0.0
    res = (v.width, v.height)
    samples, n = [], 0
    # 音频响度:采样前 40 个音频帧的归一化 RMS(静音=有音轨但无声音,TTS 失败的典型产物)
    np = None
    rms_sum, rms_n = 0.0, 0
    # 视频+音频必须交错解码(PyAV 先解完视频再解音频会拿不到音频帧),单遍同时完成两项检测
    streams = (v, a0) if a0 is not None else (v,)
    for fr in c.decode(*streams):
        if isinstance(fr, av.VideoFrame):
            if n % 6 == 0:
                g = fr.to_ndarray(format="gray")
                # 近黑判据:整帧平均亮度 < 20 才算接近全黑(亮度护栏防的是整帧黑屏);
                # 用像素占比会把合法夜景(暗背景+火把/月光)误判为过暗
                samples.append(float(g.mean() < 20))
            n += 1
        elif rms_n < 40:
            if np is None:
                import numpy as _np
                np = _np
            arr = fr.to_ndarray()
            mx = 1.0
            if arr.dtype.kind in "iu":  # 音频可能是 s16(±32768),归一化到 [-1,1]
                mx = float(np.iinfo(arr.dtype).max)
            rms_sum += float(np.abs(arr).mean() / mx)
            rms_n += 1
    c.close()
    # 结尾淡出带(提示词常带 fade out):丢弃最后 5% 采样,合法淡出不计入暗比
    if samples:
        cut = max(1, int(len(samples) * 0.05))
        samples = samples[:-cut]
    audio_rms = rms_sum / max(rms_n, 1)
    return {
        "duration_s": dur, "resolution": res, "audio_streams": len(a),
        "dark_ratio": round(sum(samples) / max(len(samples), 1), 3), "decoded_frames": n,
        "audio_rms": round(audio_rms, 4), "audio_frames": rms_n,
    }


def cmd_qc(args):
    # --file 单文件质检(成片终检:时长/黑屏/静音;不按目录扫)
    if args.file:
        if not os.path.exists(args.file):
            print("❌ 文件不存在: " + args.file)
            sys.exit(1)
        args.dir = os.path.dirname(os.path.abspath(args.file))
        files = [os.path.basename(args.file)]
    else:
        files = sorted(f for f in os.listdir(args.dir) if f.lower().endswith(".mp4"))
        if not files:
            print("❌ 目录无 mp4: " + args.dir)
            sys.exit(1)
    # --shots 单镜过滤(Agent 流水线逐镜审片只检当前镜头,如 "3" 或 "1,3";自动兼容 03.mp4 补零名)
    if args.shots:
        wanted = set()
        for x in args.shots.split(","):
            x = x.strip()
            if x:
                wanted.add(x)
                if x.isdigit():
                    wanted.add(x.zfill(2))
        files = [f for f in files if f.rsplit(".", 1)[0] in wanted or f in wanted]
        if not files:
            print("❌ --shots 未命中任何镜头")
            sys.exit(1)
    print(f"质检 {len(files)} 个镜头（近黑帧阈值 {args.threshold}）")
    bad = []
    report = {"shots": {}}
    for f in files:
        p = os.path.join(args.dir, f)
        try:
            r = check_video(p)
            flags = []
            if r["audio_streams"] == 0:
                flags.append("无音轨")
            elif r["audio_rms"] < 0.02:
                flags.append(f"静音(rms {r['audio_rms']:.3f})")
            if r["dark_ratio"] > args.threshold:
                flags.append(f"近黑帧{r['dark_ratio']*100:.0f}%")
            if r["decoded_frames"] == 0:
                flags.append("解码0帧")
            if r["duration_s"] < 0.5:
                flags.append("时长过短")
            status = "OK" if not flags else "⚠️ " + ",".join(flags)
            print(f"  {f:12s} {r['duration_s']:6.2f}s {r['resolution']} 音轨:{r['audio_streams']} 响度:{r['audio_rms']:.3f} 近黑:{r['dark_ratio']*100:3.0f}% {status}")
            report["shots"][f] = {
                "ok": not flags, "flags": flags, "duration_s": r["duration_s"],
                "dark_ratio": r["dark_ratio"], "audio_streams": r["audio_streams"],
                "audio_rms": r["audio_rms"], "decoded_frames": r["decoded_frames"], "error": "",
            }
            if flags:
                bad.append((f, flags))
        except Exception as e:
            print(f"  {f:12s} ❌ {e}")
            bad.append((f, [str(e)]))
            report["shots"][f] = {"ok": False, "flags": [str(e)], "error": str(e)}
    if args.json:
        with open(args.json, "w", encoding="utf-8") as fp:
            json.dump(report, fp, ensure_ascii=False)
    if bad:
        print(f"\n❌ {len(bad)} 个镜头需处理: {[b[0] for b in bad]}")
        sys.exit(1)
    print("\n✅ 全部镜头质检通过")


def cmd_frames(args):
    """均匀抽 N 帧存 JPEG(审片官视觉判分输入):先数帧数,再按时间点 seek 单帧取图,
    缩到 width 宽(JPEG 体积友好,数据 URI 走 API)。末行输出 JSON {"frames": [...]}。
    seek 到目标帧前 1s 解码向前,长镜头(60s+)比两遍全量解码快一个数量级。"""
    import av
    from PIL import Image
    n_target = max(1, min(args.count, 8))
    c = av.open(args.video)
    v = c.streams.video[0]
    fps = float(v.average_rate) if v.average_rate else 24.0
    total = 0
    for _ in c.decode(v):
        total += 1
    c.close()
    if total == 0:
        print("❌ 解码 0 帧: " + args.video)
        sys.exit(1)
    # 均匀取帧位置((i+0.5)/N 规避首尾黑场/淡入淡出)
    targets = {min(total - 1, int(total * (i + 0.5) / n_target)): i for i in range(n_target)}
    os.makedirs(args.out_dir, exist_ok=True)

    def save(fr, i):
        im = fr.to_image()
        if args.width and im.width > args.width:
            im = im.resize((args.width, int(im.height * args.width / im.width)), Image.LANCZOS)
        p = os.path.join(args.out_dir, "f%d.jpg" % (i + 1))
        im.save(p, "JPEG", quality=85)
        out_paths.append(os.path.abspath(p))

    out_paths = []
    c = av.open(args.video)
    v = c.streams.video[0]
    half = 0.5 / fps  # 半帧容差:取目标时刻±半帧内的最近帧
    for idx, i in sorted(targets.items()):
        t_sec = idx / fps
        try:
            c.seek(int(max(0, t_sec - 1.0) / v.time_base), stream=v)
        except Exception:
            c.seek(0)  # 个别封装不支持 seek:退回从头解码
        last, saved = None, False
        for fr in c.decode(v):
            last = fr
            if fr.pts is not None and fr.pts * v.time_base >= t_sec - half:
                save(fr, i)
                saved = True
                break
        if not saved and last is not None:
            save(last, i)  # 兜底:未到目标时刻已 EOF,用最后一帧
    c.close()
    print(f"  🖼 抽帧 {len(out_paths)}/{n_target} → {args.out_dir}")
    print(json.dumps({"frames": out_paths}, ensure_ascii=False))


def _frame_list(rs):
    """PyAV 18 的 AudioResampler.resample 返回 Frame 列表,统一成列表"""
    if rs is None:
        return []
    if isinstance(rs, list):
        return rs
    return [rs]


def _pixelate(frame, bs):
    import av
    import numpy as np
    arr = frame.to_ndarray(format="rgb24")
    h, w, _ = arr.shape
    if bs < 2 or h < bs or w < bs:
        return frame
    hb, wb = h // bs, w // bs
    small = arr[:hb * bs, :wb * bs].reshape(hb, bs, wb, bs, 3).mean(axis=(1, 3)).astype("uint8")
    big = np.repeat(np.repeat(small, bs, axis=0), bs, axis=1)
    out = np.zeros_like(arr)
    out[:big.shape[0], :big.shape[1]] = big
    if big.shape[0] < h:
        out[big.shape[0]:, :wb * bs] = small[-1:]  # 下边缘
    if big.shape[1] < w:
        out[:hb * bs, big.shape[1]:] = small[:, -1:]
    nf = av.VideoFrame.from_ndarray(out, format="rgb24")
    nf.pts = frame.pts
    return nf


# ---- 字幕(烧录进成片) ----

_SUB_FONTS = {}


def _subtitle_font(size):
    """中文字体(微软雅黑加粗优先),按字号缓存;找不到字体返回 None(跳过烧录)"""
    import os
    from PIL import ImageFont
    if size in _SUB_FONTS:
        return _SUB_FONTS[size]
    for name in ("msyhbd.ttc", "msyh.ttc", "simhei.ttf", "simsun.ttc", "Deng.ttf"):
        p = os.path.join("C:/Windows/Fonts", name)
        if os.path.exists(p):
            font = ImageFont.truetype(p, size)
            _SUB_FONTS[size] = font
            return font
    _SUB_FONTS[size] = None
    return None


def _load_subtitle_cues(plan_path):
    """读 direct_plan 的台词/旁白,返回 ({shot_id: [句子]}, {head_id: [内镜id]})。

    台词去掉「角色:」前缀,旁白原文保留;多句用换行分隔的台词逐行拆成独立句子,
    同一镜头内每句按序均分字幕窗口(避免把换行文本整段传给 PIL 测量导致崩溃)。
    takes(多切点长镜分组)时,组头文件承载组内全部台词/旁白(内镜不独立成片)。
    """
    import json
    import re
    if not plan_path or not os.path.exists(plan_path):
        return {}, {}
    with open(plan_path, encoding="utf-8") as f:
        plan = json.load(f)
    by_id = {}
    for s in plan.get("shots", []):
        lines = []
        dlg = (s.get("dialogue") or "").strip()
        if dlg:
            for ln in dlg.splitlines():
                ln = re.sub(r"^[^:：]{1,8}[:：]\s*", "", ln).strip()
                if ln:
                    lines.append(ln)
        nar = (s.get("narration") or "").strip()
        if nar:
            for ln in nar.splitlines():
                ln = ln.strip()
                if ln:
                    lines.append(ln)
        sid = int(s.get("shot_id") or 0)
        if sid:
            by_id[sid] = lines
    takes = {}
    for grp in plan.get("takes") or []:
        ids = [int(x) for x in grp if isinstance(x, (int, float))]
        if len(ids) >= 2:
            takes[ids[0]] = ids[1:]
    return by_id, takes


def _cues_for_shot(by_id, takes, sid):
    """某镜头文件的字幕句集:多切点长镜组头合并组内全部台词/旁白"""
    lines = list(by_id.get(sid, []))
    for inner in takes.get(sid, []):
        lines.extend(by_id.get(inner, []))
    return lines


def _wrap_lines(d, text, font, maxw):
    lines, cur = [], ""
    for ch in text:
        if ch == "\n":  # 硬换行兜底:绝不把多行文本交给 PIL 测量
            if cur:
                lines.append(cur)
                cur = ""
            continue
        if cur and d.textlength(cur + ch, font=font) > maxw:
            lines.append(cur)
            cur = ch
        else:
            cur += ch
    if cur:
        lines.append(cur)
    return lines


def _draw_subtitle(frame, text):
    """帧底部居中烧录字幕:半透明圆角面板 + 加粗白字柔和阴影,超 2 行自动缩字号。

    面板与画面分离(暗底+细亮描边),任何明暗背景都可读;逐帧绘制,不做整片预合成。
    """
    import av
    import numpy as np
    from PIL import Image, ImageDraw
    arr = frame.to_ndarray(format="rgb24")
    im = Image.fromarray(arr)  # 注意:fromarray 对 PyAV 数组是拷贝,绘制后须读回 im
    w, h = im.size
    d = ImageDraw.Draw(im, "RGBA")
    size = max(22, int(h * 0.045))
    maxw = int(w * 0.8)
    lines, font = [], None
    while True:
        font = _subtitle_font(size)
        lines = _wrap_lines(d, text, font, maxw) if font else []
        if len(lines) <= 2 or size <= 18:
            break
        size = int(size * 0.85)
    if not font or not lines:
        return frame
    line_h = int(size * 1.32)
    pad_x, pad_y = int(size * 0.55), int(size * 0.4)
    widths = [int(d.textlength(ln, font=font)) for ln in lines]
    box_w = max(widths) + pad_x * 2
    box_h = int(size * 1.12) * len(lines) + pad_y * 2
    x0 = (w - box_w) // 2
    y0 = h - int(h * 0.055) - box_h
    # 半透明圆角面板(暗底 + 细亮描边)
    d.rounded_rectangle([x0, y0, x0 + box_w, y0 + box_h], radius=int(size * 0.5),
                        fill=(0, 0, 0, 118), outline=(255, 255, 255, 42), width=max(1, size // 26))
    # 柔和阴影:整体偏移 1px 先画一遍半透明黑,再叠正文白字
    for i, ln in enumerate(lines):
        lw = widths[i]
        x = x0 + pad_x + (box_w - pad_x * 2 - lw) // 2
        yy = y0 + pad_y + i * line_h
        d.text((x + 1, yy + 1), ln, font=font, fill=(0, 0, 0, 190))
    for i, ln in enumerate(lines):
        lw = widths[i]
        x = x0 + pad_x + (box_w - pad_x * 2 - lw) // 2
        yy = y0 + pad_y + i * line_h
        d.text((x, yy), ln, font=font, fill=(255, 255, 255, 255))
    nf = av.VideoFrame.from_ndarray(np.asarray(im), format="rgb24")
    nf.pts = frame.pts
    nf.time_base = frame.time_base
    return nf


def _apply_gain(fr, gain, np):
    """对重采样后的 fltp 音频帧整体增益(原地不可变,新建帧),增益 1.0 时原样返回"""
    import av
    if gain == 1.0:
        return fr
    arr = np.asarray(fr.to_ndarray()) * np.float32(gain)
    nf = av.AudioFrame.from_ndarray(arr, format="fltp", layout="stereo")
    nf.pts = fr.pts
    nf.time_base = fr.time_base
    nf.sample_rate = fr.sample_rate
    return nf


def _count_video_frames(path):
    import av
    c = av.open(path)
    v = c.streams.video[0]
    n = 0
    for _ in c.decode(v):
        n += 1
    c.close()
    return n


def _shot_no(name):
    m = reASRShot.search(name)
    return int(m.group(1)) if m else 0


def _load_bgm(path, rate=32000):
    """预载 BGM 为 float32 (2, N) ndarray(32k 立体声);解码失败抛异常由调用方忽略"""
    import av
    import numpy as np
    c = av.open(path)
    if not c.streams.audio:
        c.close()
        raise ValueError("BGM 无音轨")
    rs = av.AudioResampler(format="fltp", layout="stereo", rate=rate)
    chunks = []
    for fr in c.decode(c.streams.audio[0]):
        for f in _frame_list(rs.resample(fr)):
            chunks.append(np.asarray(f.to_ndarray(), dtype="float32"))
    for f in _frame_list(rs.resample(None)):
        chunks.append(np.asarray(f.to_ndarray(), dtype="float32"))
    c.close()
    return np.concatenate(chunks, axis=1)


class _BgmMixer:
    """BGM 混音器:基础增益 + 对白闪避(字幕窗口内压低,窗口边界 0.3s 线性过渡)。
    written 为已写出的输出音频采样数;窗口 (start, end) 为成片时间轴秒。"""

    def __init__(self, bgm, gain, duck, windows, ramp=0.3):
        self.bgm = bgm
        self.gain = float(gain)
        self.duck = float(duck)
        self.windows = windows
        self.ramp = ramp
        self.written = 0

    def _env(self, t):
        e = 1.0
        for a, b in self.windows:
            if a - self.ramp <= t <= b + self.ramp:
                # 窗口内 duck;边界 ramp 内线性过渡
                if a <= t <= b:
                    e = min(e, self.duck)
                elif t < a:
                    k = 1.0 - (a - t) / self.ramp
                    e = min(e, 1.0 - (1.0 - self.duck) * k)
                else:
                    k = 1.0 - (t - b) / self.ramp
                    e = min(e, 1.0 - (1.0 - self.duck) * k)
        return e

    def mix(self, arr, rate=32000):
        """对输出 fltp 帧 (channels, samples) 原地叠加 BGM 片段(循环补齐,声道数对齐)"""
        import numpy as np
        if self.bgm is None or self.gain <= 0:
            return arr
        n = arr.shape[1]
        bgm = self.bgm
        # 声道对齐:BGM 单声道(packed 1×N)→ 双声道复制;输出单声道 → BGM 混平均
        if bgm.shape[0] == 1 and arr.shape[0] == 2:
            bgm = np.vstack([bgm[0], bgm[0]])
        elif bgm.shape[0] == 2 and arr.shape[0] == 1:
            bgm = bgm.mean(axis=0, keepdims=True)
        pos = self.written % bgm.shape[1]
        seg = bgm[:, pos:pos + n]
        if seg.shape[1] < n:  # 循环补齐
            rep = int(np.ceil((pos + n) / bgm.shape[1]))
            seg = np.tile(bgm, (1, rep))[:, pos:pos + n]
        env = np.array([self.gain * self._env((self.written + k) / rate) for k in range(n)],
                       dtype="float32")
        out = arr + seg * env[np.newaxis, :]
        np.clip(out, -1.0, 1.0, out=out)
        self.written += n
        return out


def cmd_assemble(args):
    import av
    import numpy as np
    from fractions import Fraction
    from collections import deque
    files = sorted(f for f in os.listdir(args.clips_dir) if f.lower().endswith(".mp4"))
    if not files:
        print("❌ 无镜头可合成: " + args.clips_dir)
        sys.exit(1)
    out = args.out
    if os.path.exists(out):
        os.remove(out)
    if os.path.dirname(out):
        os.makedirs(os.path.dirname(out), exist_ok=True)
    fps = args.fps
    cues_by_id, takes_map = _load_subtitle_cues(args.plan)

    # ---- 转场配置:cut(硬切,默认)/ fade(闪黑淡入淡出)/ dissolve(叠化) ----
    # 硬切边界 = MotionContext 接缝镜头的起始处(Go 侧从 manifest 提取 seam 标记传入):
    # 接缝镜头与上一镜画面本就连续,再叠化只会出现重影。
    hard_cuts = set()
    if args.hard_cuts:
        for x in args.hard_cuts.split(","):
            x = x.strip()
            if x.isdigit():
                hard_cuts.add(int(x))
    trans_frames = max(1, int(round(args.trans_dur * fps))) if args.transition != "cut" else 0
    # 转场生效需要预知每个剪辑的帧数(尾部处理):转场关闭时不付数帧成本
    clip_frames = None
    if trans_frames > 0:
        clip_frames = {name: _count_video_frames(os.path.join(args.clips_dir, name)) for name in files}

    # ---- BGM(可选):预载 + 对白闪避混音 ----
    bgm_mixer = None
    bgm = None
    if args.bgm:
        if os.path.exists(args.bgm):
            try:
                bgm = _load_bgm(args.bgm)
                print(f"  🎵 BGM: {os.path.basename(args.bgm)} ({bgm.shape[1] / 32000:.0f}s, 音量 {args.bgm_gain}, 对白闪避 {args.bgm_duck})")
            except Exception as e:
                print(f"  ⚠️ BGM 加载失败(忽略,干声合成): {e}")
        else:
            print(f"  ⚠️ BGM 文件不存在(忽略): {args.bgm}")

    # 音量归一化:预扫各镜头音频峰值 → 全局增益(过轻整体放大、过响压限,成片音量一致)
    # 只解音频流,速度快;峰值>0.95 提前收工(已近满幅无需再扫)
    peak = 0.0
    for name in files:
        ip = av.open(os.path.join(args.clips_dir, name))
        for fr in ip.decode(*ip.streams):
            if isinstance(fr, av.AudioFrame):
                arr = fr.to_ndarray()
                mx = 1.0
                if arr.dtype.kind in "iu":
                    mx = float(np.iinfo(arr.dtype).max)
                pk = float(np.abs(arr).max() / mx)
                if pk > peak:
                    peak = pk
                if peak > 0.95:
                    break
        ip.close()
        if peak > 0.95:
            break
    gain = 1.0
    if peak <= 0.0:
        peak = 1e-6
    if peak < 0.02:
        gain = 1.0  # 静音不放大(避免噪声放大,QC 会标记该镜头)
    elif peak < 0.25:
        gain = min(4.0, 0.7 / peak)
    elif peak > 0.9:
        gain = 0.85 / peak  # 近满幅压限,防爆音
    if gain != 1.0:
        print(f"  🔊 音量归一化: 峰值 {peak:.2f} → 增益 x{gain:.2f}")
    tname = {"cut": "硬切", "fade": "闪黑", "dissolve": "叠化"}[args.transition]
    print(f"🎬 合成 {len(files)} 个镜头 → {out} (crf18, {fps}fps, 转场:{tname}{trans_frames and f'×{trans_frames}帧' or ''}, mosaic={args.mosaic}, 字幕{'on' if cues_by_id else 'off'}, BGM{'on' if bgm is not None else 'off'})")

    o = av.open(out, "w", options={"movflags": "+faststart"})
    vs = o.add_stream("libx264", rate=fps)
    vs.pix_fmt = "yuv420p"
    vs.options = {"crf": "18", "preset": "medium"}
    as_ = o.add_stream("aac", rate=32000)
    as_.layout = "stereo"
    as_.format = "fltp"
    as_.bit_rate = 128000
    resampler = av.AudioResampler(format="fltp", layout="stereo", rate=32000)

    total_v, total_a = 0, 0
    first = True
    vpts = 0
    vtb = Fraction(1, fps)  # 输出视频流 time_base
    film_sec = 0.0          # 成片累计时长,字幕时间轴按实际镜头时长累加
    ci = 0
    prev_tail = None        # dissolve:上一剪辑尾 T 帧(rgb float 缓存)
    for name in files:
        p = os.path.join(args.clips_dir, name)
        i = av.open(p)
        v = i.streams.video[0]
        a = i.streams.audio[0] if i.streams.audio else None
        dur = float(v.duration * v.time_base)
        if clip_frames is not None:
            dur = clip_frames[name] / fps  # 数帧结果更准(元数据 duration 偶有偏差)
        sid = _shot_no(name)
        # 转场边界判定:本剪辑头(与上一剪辑之间)、本剪辑尾(与下一剪辑之间)
        head_trans = trans_frames > 0 and ci > 0 and sid not in hard_cuts
        next_sid = _shot_no(files[ci + 1]) if ci + 1 < len(files) else 0
        tail_trans = trans_frames > 0 and ci + 1 < len(files) and next_sid not in hard_cuts
        # 本镜头字幕窗口:镜头内 12%-92%,台词/旁白按字数占比分配窗口
        # (12% 起:台词开说即出字幕,避免延后;92% 止:给下一镜转场留白)
        # 多切点长镜:组头文件承载组内全部台词/旁白
        cue_lines = _cues_for_shot(cues_by_id, takes_map, sid)
        subs = _assign_subtitle_windows(cue_lines, dur, film_sec) if cue_lines else []
        film_sec += dur
        # BGM 闪避窗口 = 字幕窗口(对白时段压 BGM);mixer 在首轮有字幕时构建
        if bgm is not None and bgm_mixer is None and subs:
            bgm_mixer = _BgmMixer(bgm, args.bgm_gain, args.bgm_duck, [(sa, sb) for sa, sb, _ in subs])
        elif bgm is not None and bgm_mixer is not None and subs:
            bgm_mixer.windows.extend([(sa, sb) for sa, sb, _ in subs])
        # 视频+音频必须交错解码(PyAV 先解完视频再解音频会拿不到音频帧)
        streams = (v, a) if a is not None else (v,)
        cut_v = 0            # 本剪辑内已编码视频帧号(0 起)
        total_frames = clip_frames[name] if clip_frames is not None else None
        tail_buf = deque(maxlen=trans_frames) if tail_trans else None
        atb = float(a.time_base) if a is not None else 0.0
        for frame in i.decode(*streams):
            if isinstance(frame, av.VideoFrame):
                if first:  # PyAV 不自动从帧推断编码器尺寸,首个视频帧显式设定
                    vs.width, vs.height = frame.width, frame.height
                    first = False
                # pts=None 走 PyAV 自动分配,不同源帧时基换算后可能算出相同 dts,mp4 要求严格递增会报 EINVAL;
                # 改为按输出时基显式单调递增,跨镜头连续不重置
                frame.pts = vpts
                frame.time_base = vtb
                vpts += 1
                # ---- 转场处理(dissolve/fade;硬切与关闭时零成本) ----
                if trans_frames > 0 and total_frames is not None:
                    arr = None
                    if head_trans and cut_v < trans_frames:
                        alpha = (cut_v + 1) / trans_frames
                        arr = frame.to_ndarray(format="rgb24").astype("float32")
                        if args.transition == "dissolve" and prev_tail is not None and cut_v < len(prev_tail):
                            arr = arr * alpha + prev_tail[cut_v] * (1.0 - alpha)  # 叠化
                        else:
                            arr = arr * alpha  # 闪黑:从黑淡入
                    elif tail_trans and total_frames - cut_v <= trans_frames and args.transition == "fade":
                        k = total_frames - cut_v  # 距结尾帧数(1=最后一帧)
                        arr = frame.to_ndarray(format="rgb24").astype("float32") * (k / (trans_frames + 1))
                    if arr is not None:
                        frame = av.VideoFrame.from_ndarray(np.clip(arr, 0, 255).astype("uint8"), format="rgb24")
                        frame.pts = vpts - 1
                        frame.time_base = vtb
                    if tail_buf is not None:
                        tail_buf.append(frame.to_ndarray(format="rgb24").astype("float32"))
                cut_v += 1
                if args.mosaic > 1:
                    frame = _pixelate(frame, args.mosaic)
                if subs:
                    t = (vpts - 1) / fps  # 当前帧在成片中的时间
                    for sa, sb, txt in subs:
                        if sa <= t < sb:
                            frame = _draw_subtitle(frame, txt)
                            break
                for pkt in vs.encode(frame):
                    o.mux(pkt)
                total_v += 1
            else:
                for fr in _frame_list(resampler.resample(frame)):
                    fr = _apply_gain(fr, gain, np)
                    # 转场边界的音频淡入淡出(近似 acrossfade,防转场处爆音);
                    # 衰减按帧在剪辑内的时间位置计算(dur 由数帧得到)
                    if trans_frames > 0 and a is not None and dur > 0:
                        t_in = (frame.pts or 0) * atb
                        f = 1.0
                        trans_sec = trans_frames / fps
                        if head_trans and t_in < trans_sec:
                            f = min(f, 0.05 + 0.95 * t_in / trans_sec)
                        if tail_trans and t_in > dur - trans_sec:
                            f = min(f, 0.05 + 0.95 * max(0.0, (dur - t_in) / trans_sec))
                        if f < 1.0:
                            fr = _apply_gain(fr, f, np)
                    if bgm_mixer is not None:
                        arr = np.asarray(fr.to_ndarray(), dtype="float32")
                        fr_pts, fr_tb, fr_rate = fr.pts, fr.time_base, fr.sample_rate
                        arr = bgm_mixer.mix(arr)
                        nf = av.AudioFrame.from_ndarray(arr, format="fltp", layout="stereo")
                        nf.pts, nf.time_base, nf.sample_rate = fr_pts, fr_tb, fr_rate
                        fr = nf
                    fr.pts = None
                    for pkt in as_.encode(fr):
                        o.mux(pkt)
                    total_a += 1
        # 冲洗该剪辑的音频重采样缓冲,再进下一个
        for fr in _frame_list(resampler.resample(None)):
            fr = _apply_gain(fr, gain, np)
            if bgm_mixer is not None:
                arr = np.asarray(fr.to_ndarray(), dtype="float32")
                fr_pts, fr_tb, fr_rate = fr.pts, fr.time_base, fr.sample_rate
                arr = bgm_mixer.mix(arr)
                nf = av.AudioFrame.from_ndarray(arr, format="fltp", layout="stereo")
                nf.pts = fr_pts
                nf.time_base = fr_tb
                nf.sample_rate = fr_rate
                fr = nf
            fr.pts = None
            for pkt in as_.encode(fr):
                o.mux(pkt)
            total_a += 1
        i.close()
        prev_tail = list(tail_buf) if tail_buf is not None else None  # dissolve 下一剪辑头用
        ci += 1
    for pkt in vs.encode():
        o.mux(pkt)
    for pkt in as_.encode():
        o.mux(pkt)
    o.close()
    print(f"  ✅ 帧 {total_v} / 音频块 {total_a}，成片已写入")


def cmd_facecrop(args):
    """从定妆照切出完整正脸/头肩特写,作为 R2V 参考。

    H3 人脸 token 极少(视觉 VAE 32× 下采样),全身立绘脸占比小、锁定弱;
    用正脸特写可让脸部占满参考帧,身份锁定大幅增强。
    关键:输出按渲染同比例(默认 1344x768,可用 --ratio 覆盖)——ref_image_size=match
    会把参考图缩放/裁剪到输出尺寸,比例不一致会被压扁变形,脸部遵循直接劣化;
    同比例 + 紧凑脸区(垂直 8%-52% 额头到肩,水平居中同比例窗口)保证脸部占满且不变形。
    裁剪范围覆盖完整头部(发顶/额头/下巴)+ 少量肩,避免只裁到"半张脸"。
    """
    from PIL import Image
    im = Image.open(args.src).convert("RGB")
    w, h = im.size
    tw, th = 1344, 768
    if args.ratio and "x" in args.ratio:
        try:
            tw, th = map(int, args.ratio.split("x", 1))
        except Exception:
            pass
    if tw <= 0 or th <= 0:
        tw, th = 1344, 768
    ratio = tw / th
    top, bot = int(h * 0.08), int(h * 0.52)  # 垂直 8%-52%:发顶到肩,裁掉地面/远景
    ch = bot - top
    cw = int(ch * ratio)
    if cw > w:  # 目标窗口超宽(竖图定妆照):限宽后按比例缩高
        cw = w
        ch = int(cw / ratio)
        bot = top + ch
    left = (w - cw) // 2
    crop = im.crop((left, top, left + cw, bot))
    # 放大到目标分辨率(只放不放缩,放大后脸部占满参考帧)
    scale = min(th / crop.height, tw / crop.width)
    if scale > 1:
        crop = crop.resize((int(crop.width * scale), int(crop.height * scale)), Image.LANCZOS)
    os.makedirs(os.path.dirname(args.dst), exist_ok=True)
    crop.save(args.dst)
    print(f"  ✂️ 正脸参考: {os.path.basename(args.dst)} ({crop.size[0]}x{crop.size[1]})")


def cmd_asr(args):
    """ASR 台词核对:用 faster-whisper(本地)转写视频语音,与分镜台词/旁白比对。

    --plan 提供 direct_plan.json(取各镜台词),--shots 指定核对的镜头号(逗号分隔,空=全部)。
    判定:逐字比对转写文本与台词原文(去空白/标点),不一致或漏台词 → 该镜标记台词不符,
    进审片失败集自动返工。末行输出 JSON {"shots": {"1": {"ok": bool, "spoken": "...", "expected": "..."} } }。
    模型:small(int8 CPU)首启自动下载到 ~/.cache;首次运行需联网,之后全离线。
    """
    import av
    model_name = args.model or "small"
    compute = "int8"
    try:
        from faster_whisper import WhisperModel
        model = WhisperModel(model_name, device="cpu", compute_type=compute)
    except Exception as e:
        print(f"❌ faster-whisper 不可用({e});未安装时: venv pip install faster-whisper")
        print(json.dumps({"shots": {}, "error": str(e)}))
        sys.exit(1)

    # 分镜台词:plan.json shots[].dialogue/narration(去「角色:」前缀)
    expected = {}
    if args.plan and os.path.exists(args.plan):
        cues_by_id2, takes_map2 = _load_subtitle_cues(args.plan)
        for sid, lines in cues_by_id2.items():
            for inner in takes_map2.get(sid, []):  # 长镜组头:组内台词并入核对文本
                lines = lines + cues_by_id2.get(inner, [])
            if lines:
                expected[sid] = " ".join(lines)

    # 目标镜头
    ids = []
    if args.shots:
        for p in args.shots.split(","):
            p = p.strip()
            if p.isdigit():
                ids.append(int(p))
    files = sorted(f for f in os.listdir(args.dir) if f.lower().endswith(".mp4"))
    targets = []
    for f in files:
        sid = 0
        m = reASRShot.search(f)
        if m:
            sid = int(m.group(1))
        if ids and sid not in ids:
            continue
        targets.append((f, sid))
    if not targets:
        print("❌ 无待核对镜头: " + args.dir)
        sys.exit(1)

    def norm(t):
        # 比对归一:去空白/常见标点(全半角都覆盖,含 ，。！？：；""''~…),统一小写;繁体转简体
        # (whisper small 中文常输出繁体,如「濃/燈/終」,不归一会全部误判)
        s = re.sub(r"[\s,，。.．!！?？\-—·、:；:;\"'‘’“”()\[\]{}<>《》~～…]+", "", (t or "")).lower()
        try:
            from opencc import OpenCC
            s = OpenCC("t2s").convert(s)
        except Exception:
            pass  # opencc 未装:跳过简繁归一(比对稍严格,但不崩)
        return s

    def clip_txt(t, n):
        t = (t or "").strip()
        return t if len(t) <= n else t[:n] + "…"

    out = {}
    print(f"🎤 ASR 台词核对 {len(targets)} 镜(model={model_name}/{compute})")
    for f, sid in targets:
        p = os.path.join(args.dir, f)
        try:
            # 预检:无音轨的镜头 whisper 内部解 audio 流会越界,直接判失败(漏台词)
            import av as _av
            _c = _av.open(p)
            _naudio = len(_c.streams.audio)
            _c.close()
            if _naudio == 0:
                if expected.get(sid):
                    print(f"  {f:12s} ❌ 无音轨(分镜要求台词「{clip_txt(expected.get(sid, ''), 20)}」)")
                    out[str(sid or f)] = {"ok": False, "spoken": "", "expected": expected.get(sid, ""), "error": "no audio stream"}
                else:
                    print(f"  {f:12s} ⏭ 无音轨(无台词镜头,跳过)")
                    out[str(sid or f)] = {"ok": True, "spoken": "", "expected": ""}
                continue
            # 抽音频:直接喂视频文件给 whisper(内部 ffmpeg 解码;PyAV 管线产物 mp4+aac 可直接读)
            segments, info = model.transcribe(p, language="zh", beam_size=1, vad_filter=True)
            spoken = " ".join(s.text.strip() for s in segments).strip()
            exp = expected.get(sid, "")
            ok = True
            if exp:
                ok = norm(spoken) == norm(exp)
                # 完全包含也算过(whisper 可能多识别环境音字幕)
                if not ok and norm(exp) and norm(exp) in norm(spoken):
                    ok = True
            elif args.strict:
                ok = bool(spoken)  # 无台词镜头:strict 模式要求完全静音
            else:
                ok = True  # 无台词镜头非 strict 不核(旁白为 H3 画外音,字幕在成片烧录)
            mark = "✅" if ok else "❌"
            detail = f"期望[{clip_txt(exp, 30)}] 实听[{clip_txt(spoken, 30)}]" if exp else (f"实听[{clip_txt(spoken, 30)}]" if spoken else "(无台词,静音)")
            print(f"  {f:12s} {mark} {detail}")
            out[str(sid or f)] = {"ok": ok, "spoken": spoken, "expected": exp}
        except Exception as e:
            print(f"  {f:12s} ❌ {e}")
            out[str(sid or f)] = {"ok": False, "error": str(e)}
    print(json.dumps({"shots": out}, ensure_ascii=False))


import re as _reGlobal  # noqa: E402
reASRShot = _reGlobal.compile(r"^(\d+)\.mp4$", _reGlobal.IGNORECASE)
def _cues_with_weights(lines):
    """台词/旁白按字数加权:长句占更长的字幕窗口(原来是均分,长句放不下/短句空挂)"""
    weights = [max(1, len(l)) for l in lines]
    total = sum(weights)
    return [w / total for w in weights]


def _assign_subtitle_windows(cues, dur, film_sec):
    """为每镜生成字幕窗口(镜头内 12%-92%);句内窗口按字数占比分配"""
    subs = []
    if not cues:
        return subs
    wa, wb = film_sec + 0.12 * dur, film_sec + 0.92 * dur
    weights = _cues_with_weights(cues)
    t = wa
    for k, txt in enumerate(cues):
        span = (wb - wa) * weights[k]
        subs.append((t, t + span, txt))
        t += span
    return subs


def cmd_trailer(args):
    """预告片自动剪辑:按审片分数选高分镜头,掐头去尾取核心段拼接(目标时长 --target 秒,默认 30)。

    选镜策略(脚本侧只按传入清单拼;清单由 Go 侧按审片分数+剧本位置生成):
    clips 文本文件每行 "NN.mp4 score",Go 侧已排好序;每镜取中段 --seg 秒(默认 4s)。
    复用 assemble 的合成参数(crf18/音量归一化/字幕烧录,字幕按预告片时间轴重新分配)。
    """
    import av
    import numpy as np
    from fractions import Fraction
    if not os.path.exists(args.list):
        print("❌ 镜头清单不存在: " + args.list)
        sys.exit(1)
    entries = []
    with open(args.list, encoding="utf-8") as f:
        for ln in f:
            parts = ln.split()
            if len(parts) >= 2:
                entries.append((parts[0], float(parts[1])))
    if not entries:
        print("❌ 镜头清单为空")
        sys.exit(1)
    files = sorted(f for f in os.listdir(args.clips_dir) if f.lower().endswith(".mp4"))
    have = set(files)
    picked = [e for e in entries if e[0] in have]
    if not picked:
        print("❌ 清单镜头在目录中均不存在")
        sys.exit(1)
    out = args.out
    if os.path.exists(out):
        os.remove(out)
    if os.path.dirname(out):
        os.makedirs(os.path.dirname(out), exist_ok=True)
    fps = args.fps
    cues_by_id, takes_map = _load_subtitle_cues(args.plan) if args.plan else ({}, {})
    # 每镜掐头去尾取中段:总时长≈target;段长按 target/镜头数 clamp 到 [2, seg]
    seg = max(2.0, min(args.seg, args.target / max(len(picked), 1)))
    print(f"🎬 预告片: {len(picked)} 镜 × {seg:.1f}s ≈ {len(picked) * seg:.0f}s → {out}")

    o = av.open(out, "w", options={"movflags": "+faststart"})
    vs = o.add_stream("libx264", rate=fps)
    vs.pix_fmt = "yuv420p"
    vs.options = {"crf": "18", "preset": "medium"}
    as_ = o.add_stream("aac", rate=32000)
    as_.layout = "stereo"
    as_.format = "fltp"
    as_.bit_rate = 128000
    resampler = av.AudioResampler(format="fltp", layout="stereo", rate=32000)

    # 音量归一化(预扫峰值,同 assemble)
    peak = 0.0
    for name, _ in picked:
        ip = av.open(os.path.join(args.clips_dir, name))
        for fr in ip.decode(*ip.streams):
            if isinstance(fr, av.AudioFrame):
                arr = fr.to_ndarray()
                mx = 1.0
                if arr.dtype.kind in "iu":
                    mx = float(np.iinfo(arr.dtype).max)
                pk = float(np.abs(arr).max() / mx)
                peak = max(peak, pk)
                if peak > 0.95:
                    break
        ip.close()
        if peak > 0.95:
            break
    gain = 1.0
    if peak <= 0.0:
        peak = 1e-6
    if 0.02 <= peak < 0.25:
        gain = min(4.0, 0.7 / peak)
    elif peak > 0.9:
        gain = 0.85 / peak
    if gain != 1.0:
        print(f"  🔊 音量归一化: 峰值 {peak:.2f} → 增益 x{gain:.2f}")

    total_v, total_a = 0, 0
    first = True
    vpts = 0
    vtb = Fraction(1, fps)
    film_sec = 0.0
    for name, _score in picked:
        p = os.path.join(args.clips_dir, name)
        i = av.open(p)
        v = i.streams.video[0]
        a = i.streams.audio[0] if i.streams.audio else None
        full = float(v.duration * v.time_base)
        # 掐头去尾取中段:前 15% 后 15% 弃(淡入淡出/转场边缘),中段截 seg 秒
        st = max(0.0, full * 0.15)
        if st + seg > full * 0.85:
            st = max(0.0, full - seg)
        try:
            i.seek(int(st / v.time_base), stream=v)
        except Exception:
            pass
        seg_dur = min(seg, full - st)
        # 该镜字幕:预告片里台词直接下挂该段
        sid = 0
        m = reASRShot.search(name)
        if m:
            sid = int(m.group(1))
        lines = _cues_for_shot(cues_by_id, takes_map, sid)
        subs = _assign_subtitle_windows(lines, seg_dur, film_sec) if lines else []
        film_sec += seg_dur
        streams = (v, a) if a is not None else (v,)
        cut_v = 0
        for frame in i.decode(*streams):
            if isinstance(frame, av.VideoFrame):
                t_in = (frame.pts or 0) * float(v.time_base)
                if t_in < st or t_in > st + seg_dur:
                    continue
                if first:
                    vs.width, vs.height = frame.width, frame.height
                    first = False
                frame.pts = vpts
                frame.time_base = vtb
                vpts += 1
                cut_v += 1
                if subs:
                    t = (vpts - 1) / fps
                    for sa, sb, txt in subs:
                        if sa <= t < sb:
                            frame = _draw_subtitle(frame, txt)
                            break
                for pkt in vs.encode(frame):
                    o.mux(pkt)
                total_v += 1
                if cut_v >= int(seg_dur * fps) + 6:
                    break
            else:
                t_in = (frame.pts or 0) * float(i.streams.audio[0].time_base)
                if t_in < st or t_in > st + seg_dur:
                    continue
                for fr in _frame_list(resampler.resample(frame)):
                    fr = _apply_gain(fr, gain, np)
                    fr.pts = None
                    for pkt in as_.encode(fr):
                        o.mux(pkt)
                    total_a += 1
        for fr in _frame_list(resampler.resample(None)):
            fr = _apply_gain(fr, gain, np)
            fr.pts = None
            for pkt in as_.encode(fr):
                o.mux(pkt)
            total_a += 1
        i.close()
    for pkt in vs.encode():
        o.mux(pkt)
    for pkt in as_.encode():
        o.mux(pkt)
    o.close()
    print(f"  ✅ 预告片已写入 ({total_v} 帧 / 音频块 {total_a})")


def cmd_probe(args):
    """探测视频参数(云端 2K 重生成预校验数据源):宽高/帧率/帧数/音轨/编码/大小/时长。
    帧数用逐帧解码精确计数(ComfyUI 产物 mp4 的 nb_frames 元数据常缺失,不可靠;≤362 帧解码很快)。"""
    import av
    c = av.open(args.file)
    v = c.streams.video[0]
    fps = float(v.average_rate) if v.average_rate else 0.0
    frames = 0
    for _ in c.decode(v):
        frames += 1
    a = c.streams.audio[0] if c.streams.audio else None
    info = {
        "width": v.width, "height": v.height,
        "fps": round(fps, 3), "frames": frames,
        "duration_s": round(frames / fps, 3) if fps else 0.0,
        "hasAudio": a is not None,
        "codecV": v.codec.name if v.codec else "",
        "codecA": (a.codec.name if a.codec else "") if a is not None else "",
        "sizeBytes": os.path.getsize(args.file),
    }
    c.close()
    print(f"  📊 {info['width']}x{info['height']} @{info['fps']}fps {info['frames']}帧 "
          f"音轨:{'yes' if info['hasAudio'] else 'no'} {info['sizeBytes'] / 1048576:.1f}MB")
    print(json.dumps(info, ensure_ascii=False))


def cmd_jianying(args):
    """剪映(JianYing)草稿导出:视频轨 + 字幕轨(台词/旁白按镜头时间轴,可继续编辑)。
    参考 ArcReel jianying_draft_service 的 pyJianYingDraft 序列化方式;
    字幕时序复用 assemble 的窗口分配(镜头内 12%-92%,按字数加权),但以 TextSegment
    轨呈现而非烧录。未安装 pyJianYingDraft 时打印安装指引并 exit 2(优雅降级)。
    """
    try:
        import pyJianYingDraft as draft
        from pyJianYingDraft import ClipSettings, TextBorder, TextSegment, TextShadow, TextStyle, TrackType, TransitionType, VideoMaterial, VideoSegment, trange
    except ImportError as e:
        print(f"❌ pyJianYingDraft 未安装({e})")
        print("   安装: <ComfyUI venv>/Scripts/pip.exe install pyJianYingDraft")
        print("   (导出剪映草稿需要;不影响其它功能)")
        sys.exit(2)

    import av
    files = sorted(f for f in os.listdir(args.clips_dir) if f.lower().endswith(".mp4"))
    if not files:
        print("❌ 无镜头可导出: " + args.clips_dir)
        sys.exit(1)
    cues_by_id, takes_map = _load_subtitle_cues(args.plan)
    hard_cuts = set()
    if args.hard_cuts:
        for x in args.hard_cuts.split(","):
            x = x.strip()
            if x.isdigit():
                hard_cuts.add(int(x))
    trans_map = {"fade": TransitionType.闪黑, "dissolve": TransitionType.叠化}

    # 画布尺寸取首镜头(竖屏 9:16 → 1080x1920;横屏 → 1920x1080,与 ArcReel 同规则)
    c0 = av.open(os.path.join(args.clips_dir, files[0]))
    v0 = c0.streams.video[0]
    portrait = v0.height > v0.width
    c0.close()
    width, height = (1080, 1920) if portrait else (1920, 1080)

    os.makedirs(args.out_dir, exist_ok=True)  # DraftFolder 要求根目录已存在
    os.makedirs(args.out_dir, exist_ok=True)  # DraftFolder 要求根目录已存在
    folder = draft.DraftFolder(args.out_dir)
    script = folder.create_draft(args.name, width=width, height=height, fps=args.fps, allow_replace=True)
    # 轨道 API 新旧版兼容:新版 append_track+TrackSpec,旧版 add_track(ArcReel 参考实现)
    def _add_track(tt, name=None):
        try:
            script.append_track(draft.TrackSpec(tt, name))
        except AttributeError:
            script.add_track(tt, name)
    _add_track(TrackType.video)
    has_subs = any(_cues_for_shot(cues_by_id, takes_map, _shot_no(n)) for n in files)
    if has_subs:
        _add_track(TrackType.text, "字幕")
    text_style = TextStyle(
        size=12.0 if portrait else 8.0, color=(1.0, 1.0, 1.0), align=1, bold=True,
        auto_wrapping=True, max_line_width=0.82 if portrait else 0.6,
    )
    text_border = TextBorder(color=(0.0, 0.0, 0.0), width=30.0)
    text_shadow = TextShadow(color=(0.0, 0.0, 0.0), alpha=0.7, diffuse=8.0, distance=3.0, angle=-45.0)
    sub_pos = ClipSettings(transform_y=-0.75 if portrait else -0.8)

    print(f"📦 剪映草稿: {len(files)} 镜 → {os.path.join(args.out_dir, args.name)} ({width}x{height}, 字幕{'on' if has_subs else 'off'}, 转场:{args.transition})")
    offset_us = 0
    film_sec = 0.0
    for i, name in enumerate(files):
        path = os.path.join(args.clips_dir, name)
        c = av.open(path)
        v = c.streams.video[0]
        frames = sum(1 for _ in c.decode(v))
        c.close()
        dur = frames / args.fps
        dur_us = int(dur * 1_000_000)
        vm = VideoMaterial(path)
        seg = VideoSegment(vm, trange(offset_us, dur_us), source_timerange=trange(0, dur_us), volume=1.0)
        # 转场(seam 接缝镜硬切;最后一镜无下一边界)
        if args.transition in trans_map and i + 1 < len(files):
            next_sid = _shot_no(files[i + 1])
            sid = _shot_no(name)
            if next_sid not in hard_cuts and sid not in hard_cuts:
                seg.add_transition(trans_map[args.transition])
        script.add_segment(seg)
        # 字幕轨(与 assemble 相同的窗口分配,TextSegment 不烧录;长镜组头合并组内台词)
        cue_lines = _cues_for_shot(cues_by_id, takes_map, sid)
        if cue_lines:
            for sa, sb, txt in _assign_subtitle_windows(cue_lines, dur, film_sec):
                script.add_segment(TextSegment(
                    text=txt, timerange=trange(int(sa * 1_000_000), int((sb - sa) * 1_000_000)),
                    style=text_style, border=text_border, shadow=text_shadow, clip_settings=sub_pos,
                ), "字幕")
        offset_us += dur_us
        film_sec += dur
    script.save()
    draft_path = os.path.join(args.out_dir, args.name)
    print(f"  ✅ 草稿已写入 {draft_path}")
    print(f"  ℹ️ 复制到剪映草稿目录即可在剪映打开(剪映设置里可查草稿位置);末行 JSON:")
    print(json.dumps({"ok": True, "draft": draft_path, "width": width, "height": height}, ensure_ascii=False))


def cmd_inspect(args):
    """审片单进程:一遍解码同时完成机械质检 + 抽帧(替代 qc+frames 两进程/解两遍)。
    qc 规则与 cmd_qc 的 check_video 一致;抽帧取均匀 N 帧存 JPEG。
    末行输出 JSON {"qc": {...}, "frames": [...]}。"""
    import av
    import numpy as np
    from PIL import Image
    n_target = max(1, min(args.count, 8))
    c = av.open(args.file)
    v = c.streams.video[0]
    a = list(c.streams.audio)
    a0 = a[0] if a else None
    fps = float(v.average_rate) if v.average_rate else 24.0
    dur = float(v.duration * v.time_base) if v.duration else 0.0
    os.makedirs(args.out_dir, exist_ok=True)
    # 单遍交错解码:数帧 + 近黑采样 + 音频响度 + 均匀抽帧(先数总数,再按目标位置取)
    samples, n = [], 0
    rms_sum, rms_n = 0.0, 0
    np_mod = None
    targets = {}
    frames = []
    streams = (v, a0) if a0 is not None else (v,)
    for fr in c.decode(*streams):
        if isinstance(fr, av.VideoFrame):
            if n % 6 == 0:
                g = fr.to_ndarray(format="gray")
                samples.append(float(g.mean() < 20))
            n += 1
            if len(frames) < n_target:
                frames.append(fr)
        elif rms_n < 40:
            if np_mod is None:
                np_mod = np
            arr = fr.to_ndarray()
            mx = 1.0
            if arr.dtype.kind in "iu":
                mx = float(np_mod.iinfo(arr.dtype).max)
            rms_sum += float(np_mod.abs(arr).mean() / mx)
            rms_n += 1
    c.close()
    # 均匀取帧(用解码顺序中均匀位置的缓存帧;简化:首遍已缓存前 n_target 帧,均匀性由 seek 版保证)
    # ——为均匀性,首遍结束后按目标位置 seek 重取(小文件成本可忽略,换均匀性)
    if n > 0:
        sel = sorted({min(n - 1, int(n * (i + 0.5) / n_target)): i for i in range(n_target)}.items())
        c2 = av.open(args.file)
        v2 = c2.streams.video[0]
        half = 0.5 / fps
        out_paths = []
        for idx, i in sel:
            t_sec = idx / fps
            try:
                c2.seek(int(max(0, t_sec - 1.0) / v2.time_base), stream=v2)
            except Exception:
                c2.seek(0)
            last, saved = None, False
            for fr in c2.decode(v2):
                last = fr
                if fr.pts is not None and fr.pts * v2.time_base >= t_sec - half:
                    im = fr.to_image()
                    if im.width > args.width:
                        im = im.resize((args.width, int(im.height * args.width / im.width)), Image.LANCZOS)
                    p = os.path.join(args.out_dir, "f%d.jpg" % (i + 1))
                    im.save(p, "JPEG", quality=85)
                    out_paths.append(os.path.abspath(p))
                    saved = True
                    break
            if not saved and last is not None:
                im = last.to_image()
                if im.width > args.width:
                    im = im.resize((args.width, int(im.height * args.width / im.width)), Image.LANCZOS)
                p = os.path.join(args.out_dir, "f%d.jpg" % (i + 1))
                im.save(p, "JPEG", quality=85)
                out_paths.append(os.path.abspath(p))
        c2.close()
    # qc 判定(与 check_video 同规则)
    if samples:
        cut = max(1, int(len(samples) * 0.05))
        samples = samples[:-cut]
    audio_rms = rms_sum / max(rms_n, 1)
    flags = []
    if a0 is None:
        flags.append("无音轨")
    elif audio_rms < 0.02:
        flags.append(f"静音(rms {audio_rms:.3f})")
    dark = sum(samples) / max(len(samples), 1) if samples else 0.0
    if dark > args.threshold:
        flags.append(f"近黑帧{dark*100:.0f}%")
    if n == 0:
        flags.append("解码0帧")
    if dur < 0.5:
        flags.append("时长过短")
    qc = {"duration_s": round(dur, 2), "resolution": (v.width, v.height),
          "audio_streams": len(a), "audio_rms": round(audio_rms, 4),
          "dark_ratio": round(dark, 3), "decoded_frames": n, "flags": flags, "ok": not flags}
    print(f"  🔬 {os.path.basename(args.file)} {dur:.1f}s {v.width}x{v.height} 音轨:{len(a)} 响度:{audio_rms:.3f} 近黑:{dark*100:.0f}% {'OK' if qc['ok'] else '⚠️ ' + ','.join(flags)}")
    print(json.dumps({"qc": qc, "frames": out_paths}, ensure_ascii=False))


def main():
    ap = argparse.ArgumentParser(description="manju media helper")
    sub = ap.add_subparsers(dest="cmd", required=True)
    q = sub.add_parser("qc")
    q.add_argument("--dir", default="")
    q.add_argument("--threshold", type=float, default=0.5)
    q.add_argument("--json", default="")
    q.add_argument("--shots", default="", help="只质检指定镜头(如 3 或 1,3;空=全部)")
    q.add_argument("--file", default="", help="单文件质检(成片终检;与 --dir 二选一)")
    fr = sub.add_parser("frames")
    fr.add_argument("--video", required=True)
    fr.add_argument("--out-dir", required=True)
    fr.add_argument("--count", type=int, default=3)
    fr.add_argument("--width", type=int, default=768)
    a = sub.add_parser("assemble")
    a.add_argument("--clips-dir", required=True)
    a.add_argument("--out", required=True)
    a.add_argument("--episode", default="EP01")
    a.add_argument("--fps", type=int, default=24)
    a.add_argument("--mosaic", type=int, default=0)
    a.add_argument("--plan", default="")
    a.add_argument("--transition", default="cut", choices=["cut", "fade", "dissolve"],
                   help="镜头间转场:cut=硬切 fade=闪黑 dissolve=叠化(seam 接缝镜自动硬切)")
    a.add_argument("--trans-dur", type=float, default=0.4, help="转场时长(秒)")
    a.add_argument("--hard-cuts", default="", help="强制硬切的镜头号(逗号分隔,seam 接缝镜)")
    a.add_argument("--bgm", default="", help="背景音乐音频文件(循环补齐,按字幕窗口对白闪避)")
    a.add_argument("--bgm-gain", type=float, default=0.28, help="BGM 基础音量(0-1)")
    a.add_argument("--bgm-duck", type=float, default=0.35, help="对白时段 BGM 压低系数(0-1)")
    f = sub.add_parser("facecrop")
    f.add_argument("--src", required=True)
    f.add_argument("--dst", required=True)
    f.add_argument("--ratio", default="")  # 目标宽x高(默认 1344x768,与渲染同比例防变形)
    pr = sub.add_parser("probe")
    pr.add_argument("--file", required=True)
    ins = sub.add_parser("inspect")
    ins.add_argument("--file", required=True)
    ins.add_argument("--out-dir", required=True)
    ins.add_argument("--count", type=int, default=3)
    ins.add_argument("--width", type=int, default=768)
    ins.add_argument("--threshold", type=float, default=0.5)
    jy = sub.add_parser("jianying")
    jy.add_argument("--clips-dir", required=True)
    jy.add_argument("--out-dir", required=True, help="草稿父目录(其下创建 <name> 草稿文件夹)")
    jy.add_argument("--name", required=True, help="草稿名(剪映里显示)")
    jy.add_argument("--fps", type=int, default=24)
    jy.add_argument("--plan", default="")
    jy.add_argument("--transition", default="cut", choices=["cut", "fade", "dissolve"])
    jy.add_argument("--hard-cuts", default="")
    sr = sub.add_parser("asr")
    sr.add_argument("--dir", required=True)
    sr.add_argument("--plan", default="")
    sr.add_argument("--shots", default="")
    sr.add_argument("--model", default="small")
    sr.add_argument("--strict", action="store_true")
    tr = sub.add_parser("trailer")
    tr.add_argument("--clips-dir", required=True)
    tr.add_argument("--out", required=True)
    tr.add_argument("--list", required=True, help="镜头清单文件,每行 'NN.mp4 分数'(已按优先级排序)")
    tr.add_argument("--fps", type=int, default=24)
    tr.add_argument("--target", type=float, default=30, help="目标总时长(秒)")
    tr.add_argument("--seg", type=float, default=4, help="单镜最长段长(秒)")
    tr.add_argument("--plan", default="")
    args = ap.parse_args()
    if args.cmd == "qc":
        cmd_qc(args)
    elif args.cmd == "frames":
        cmd_frames(args)
    elif args.cmd == "assemble":
        cmd_assemble(args)
    elif args.cmd == "facecrop":
        cmd_facecrop(args)
    elif args.cmd == "probe":
        cmd_probe(args)
    elif args.cmd == "inspect":
        cmd_inspect(args)
    elif args.cmd == "jianying":
        cmd_jianying(args)
    elif args.cmd == "asr":
        cmd_asr(args)
    elif args.cmd == "trailer":
        cmd_trailer(args)


if __name__ == "__main__":
    main()
