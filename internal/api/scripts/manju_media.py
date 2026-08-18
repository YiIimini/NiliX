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
    """读 direct_plan 的台词/旁白,按镜头顺序返回句子列表(与 NN.mp4 序号对齐)。

    台词去掉「角色:」前缀,旁白原文保留;多句用换行分隔的台词逐行拆成独立句子,
    同一镜头内每句按序均分字幕窗口(避免把换行文本整段传给 PIL 测量导致崩溃)。
    """
    import json
    import re
    if not plan_path or not os.path.exists(plan_path):
        return []
    with open(plan_path, encoding="utf-8") as f:
        plan = json.load(f)
    cues = []
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
        cues.append(lines)
    return cues


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


def cmd_assemble(args):
    import av
    import numpy as np
    from fractions import Fraction
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
    cues = _load_subtitle_cues(args.plan)
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
    print(f"🎬 合成 {len(files)} 个镜头 → {out} (crf18, {fps}fps, mosaic={args.mosaic}, 字幕{'on' if cues else 'off'})")

    o = av.open(out, "w")
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
    for name in files:
        p = os.path.join(args.clips_dir, name)
        i = av.open(p)
        v = i.streams.video[0]
        a = i.streams.audio[0] if i.streams.audio else None
        dur = float(v.duration * v.time_base)
        # 本镜头字幕窗口:镜头内 12%-92%,台词/旁白按字数占比分配窗口
        # (12% 起:台词开说即出字幕,避免延后;92% 止:给下一镜转场留白)
        subs = []
        if ci < len(cues):
            subs = _assign_subtitle_windows(cues[ci], dur, film_sec)
        ci += 1
        film_sec += dur
        # 视频+音频必须交错解码(PyAV 先解完视频再解音频会拿不到音频帧)
        streams = (v, a) if a is not None else (v,)
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
                    fr.pts = None
                    for pkt in as_.encode(fr):
                        o.mux(pkt)
                    total_a += 1
        # 冲洗该剪辑的音频重采样缓冲,再进下一个
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
    print(f"  ✅ 帧 {total_v} / 音频块 {total_a}，成片已写入")


def cmd_facecrop(args):
    """从定妆照切出完整正脸/头肩特写,放大到短边 768 作为 R2V 参考。

    H3 人脸 token 极少(视觉 VAE 32× 下采样),全身立绘脸占比小、锁定弱;
    用正脸特写可让脸部占满参考帧,身份锁定大幅增强。ref_image_size=match 只缩不放,
    所以这里主动放大。
    裁剪范围覆盖完整头部+肩部(垂直 0-55%,水平居中 70%),确保正脸完整——
    含发顶/额头/下巴/肩,避免只裁到"半张脸"导致与渲染视频对不上。
    """
    from PIL import Image
    im = Image.open(args.src).convert("RGB")
    w, h = im.size
    left, right = int(w * 0.15), int(w * 0.85)   # 水平居中 70%
    top, bot = int(h * 0.00), int(h * 0.55)      # 上部 55%:完整头部+肩部
    crop = im.crop((left, top, right, bot))
    short = min(crop.size)
    if short < 768:
        scale = 768 / short
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
        with open(args.plan, encoding="utf-8") as f:
            plan = json.load(f)
        import re
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
                lines.append(nar)
            sid = int(s.get("shot_id") or 0)
            if sid and lines:
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
    cues = _load_subtitle_cues(args.plan) if args.plan else []
    # 每镜掐头去尾取中段:总时长≈target;段长按 target/镜头数 clamp 到 [2, seg]
    seg = max(2.0, min(args.seg, args.target / max(len(picked), 1)))
    print(f"🎬 预告片: {len(picked)} 镜 × {seg:.1f}s ≈ {len(picked) * seg:.0f}s → {out}")

    o = av.open(out, "w")
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
        lines = cues[sid - 1] if 0 < sid <= len(cues) else []
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


def main():
    ap = argparse.ArgumentParser(description="manju media helper")
    sub = ap.add_subparsers(dest="cmd", required=True)
    q = sub.add_parser("qc")
    q.add_argument("--dir", required=True)
    q.add_argument("--threshold", type=float, default=0.5)
    q.add_argument("--json", default="")
    q.add_argument("--shots", default="", help="只质检指定镜头(如 3 或 1,3;空=全部)")
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
    f = sub.add_parser("facecrop")
    f.add_argument("--src", required=True)
    f.add_argument("--dst", required=True)
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
    elif args.cmd == "asr":
        cmd_asr(args)
    elif args.cmd == "trailer":
        cmd_trailer(args)


if __name__ == "__main__":
    main()
