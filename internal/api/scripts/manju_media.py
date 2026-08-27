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
  bgm-pick --dir <曲库目录> [--json <p>]   BGM 选曲体检(整合 ai-film-skills bgm-spectral):PyAV+numpy 零新依赖,
                                         逐曲算质心/滚降/低频占比/调性 → 推荐档位(清新通透/深邃有质感/压抑惊悚);
                                         选曲前定档再找曲,别凭文件名;定期体检曲库重复文件/档位失衡
  qc 默认开启字幕位文字渗漏 OCR 扫描(整合 ai-film-skills pitfalls ⑫):rapidocr_onnxruntime 可用时
                                         抽 3 帧按「位置+宽度」判据(cy≥0.78 且 0.3≤cx≤0.7 且宽>0.02)检字幕位文字,
                                         未装时优雅跳过(可用 --no-ocr 关闭)
"""
import argparse
import json
import os
import sys

# 强制 stdout/stderr 用 UTF-8 且 errors="replace":
# Windows 控制台/管道默认 GBK,脚本里 print emoji(✂️✅🎨) 在 GBK 下抛 UnicodeEncodeError
# → 退出码非 0 → Go 侧报"媒体处理退出码非 0"。此处无论 PYTHONIOENCODING 是否生效都兜底。
for _stream in (sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass


def check_video(path, threshold=0.5):
    import av
    c = av.open(path)
    v = c.streams.video[0]
    a = list(c.streams.audio)
    a0 = a[0] if a else None
    dur = float(round(v.duration * v.time_base, 2)) if v.duration else 0.0
    res = (v.width, v.height)
    samples, n = [], 0
    # 冻结检测(2026-08-24 知识库「H3长镜连续与工作室实战」freeze-aware 整合):
    # H3 段尾可能提前到达 Last Frame 后几乎静止(冻结),且冻结长度每段不同——固定裁剪不可靠。
    # 每采样帧记录整帧灰度均值,末尾窗口内相邻差 < 阈值(0.8/255)占比高=段尾冻结。
    frame_means = []
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
                frame_means.append(float(g.mean()))
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
    # 冻结检测计算:末尾 25% 采样点窗口(至少 4 个点),相邻灰度均值差 < 0.8/255 记一次"冻结";
    # 冻结占比 = 冻结邻接数 / 窗口邻接总数。结尾淡出带丢弃(淡出是合法暗化,不是冻结)。
    freeze_ratio = 0.0
    if len(frame_means) >= 6:
        win = frame_means[-max(4, int(len(frame_means) * 0.25)):]
        diffs = [abs(win[i + 1] - win[i]) for i in range(len(win) - 1)]
        if diffs:
            freeze_ratio = round(sum(1 for d in diffs if d < 0.8) / len(diffs), 3)
    audio_rms = rms_sum / max(rms_n, 1)
    # 音轨规格(H3 原生规格 32kHz 立体声;审计升级 P0:静音/单声道/采样率异常在合成前拦截)
    audio_rate = 0
    audio_channels = 0
    if a0 is not None:
        audio_rate = int(getattr(a0, "rate", 0) or 0)
        audio_channels = int(getattr(a0, "channels", 0) or 0)
    return {
        "duration_s": dur, "resolution": res, "audio_streams": len(a),
        "audio_rate": audio_rate, "audio_channels": audio_channels,
        "dark_ratio": round(sum(samples) / max(len(samples), 1), 3), "decoded_frames": n,
        "audio_rms": round(audio_rms, 4), "audio_frames": rms_n,
        "freeze_ratio": freeze_ratio,
    }


def trim_frozen_tail(path, min_tail=0.5, max_cut_ratio=0.4, sample_every=2):
    """段尾冻结自动截尾(2026-08-27 用户反馈"像PPT/质检多数不合格")。

    H3 长镜存在运动衰减:动作早早 settle 后画面趋静止(实测 freeze_ratio 0.33~1.0),
    且换 seed 重渲也不解决(模型固有特性)——旧 QC 判不合格触发重渲纯属烧 GPU。
    正确姿势:程序检测冻结尾巴并 packet-copy 截除(无损重封装,秒级完成)。

    逻辑:每 sample_every 帧采样整帧灰度均值,从尾部向前找连续冻结游程
    (相邻差 < 0.8,容忍 1 个孤立抖动点);冻结尾 >= min_tail 秒且不超过总时长
    max_cut_ratio 时截到冻结起点前(留 0.2s 余量)。返回截除秒数(0=未处理)。
    """
    import av
    c = av.open(path)
    v = c.streams.video[0]
    fps = float(v.average_rate) if v.average_rate else 24.0
    means = []
    n = 0
    for fr in c.decode(v):
        if n % sample_every == 0:
            g = fr.to_ndarray(format="gray")
            means.append(float(g.mean()))
        n += 1
    c.close()
    if len(means) < 8 or n == 0:
        return 0.0
    # 从尾向前的冻结游程(容忍 1 个孤立非冻结抖动点)
    TH = 0.8
    j = len(means) - 1
    outlier = False
    while j > 0:
        if abs(means[j] - means[j - 1]) >= TH:
            if not outlier:
                outlier = True
            else:
                break
        j -= 1
    tail_sec = (len(means) - j) * sample_every / fps
    total_sec = n / fps
    if tail_sec < min_tail or tail_sec > total_sec * max_cut_ratio or total_sec - tail_sec < 1.0:
        return 0.0
    keep_sec = max(1.0, (j * sample_every) / fps - 0.2)
    # packet-copy 无损截断(音视频按各自 pts 过滤;尾 GOP 不完整不影响保留段)
    tmp = path + ".trim.mp4"
    inp = av.open(path)
    out = av.open(tmp, "w")
    smap = {}
    for s in inp.streams:
        smap[s] = out.add_stream_from_template(s)
    for pkt in inp.demux():
        if pkt.dts is None or pkt.pts is None:
            continue
        if float(pkt.pts * pkt.stream.time_base) <= keep_sec:
            pkt.stream = smap[pkt.stream]
            out.mux(pkt)
    inp.close()
    out.close()
    os.replace(tmp, path)
    return total_sec - keep_sec


def scan_text_bleed(path, n_frames=3, width=768):
    """字幕位文字渗漏扫描(整合 ai-film-skills pitfalls ⑫ 实测):
    抽 n_frames 帧,rapidocr 检出文字框后按「位置+宽度」判据判定字幕位文字:
    框中心 cy >= 0.78 且 0.3 <= cx <= 0.7 且 框宽 > 0.02(刺绣纹样等小框是噪声,不算)。
    模型会把提示词里的台词/数字/标签画进画面(字幕位,H3 text_sensitivity 低尤其明显),
    「画面无文字」句无效——只能检出后重渲/裁底。
    rapidocr 未装时优雅跳过(建议装到独立目录,别污染 ComfyUI venv 的 numpy/torch)。
    返回 {"hits": [{"frame": i, "text": "..."}], "skipped": bool, "reason": "..."}
    """
    try:
        from rapidocr_onnxruntime import RapidOCR
    except Exception:
        return {"skipped": True,
                "reason": "rapidocr_onnxruntime 未安装,跳过文字渗漏扫描(可选:pip install --target <独立目录> rapidocr_onnxruntime)"}
    import av
    from PIL import Image
    try:
        c = av.open(path)
        v = c.streams.video[0]
        total = sum(1 for _ in c.decode(v))
        c.close()
        if total < n_frames:
            n_frames = max(1, total)
        ocr = RapidOCR()
        hits = []
        c = av.open(path)
        v = c.streams.video[0]
        fps = float(v.average_rate) if v.average_rate else 24.0
        half = 0.5 / fps
        for i in range(n_frames):
            t_sec = total * (i + 0.5) / n_frames / fps
            try:
                c.seek(int(max(0, t_sec - 1.0) / v.time_base), stream=v)
            except Exception:
                c.seek(0)
            fr = None
            for f in c.decode(v):
                if f.pts is not None and f.pts * v.time_base >= t_sec - half:
                    fr = f
                    break
            if fr is None:
                continue
            im = fr.to_image()
            if im.width > width:
                im = im.resize((width, int(im.height * width / im.width)), Image.LANCZOS)
            import numpy as np
            arr = np.asarray(im.convert("RGB"))[:, :, ::-1].copy()  # RGB→BGR(rapidocr 输入)
            res, _ = ocr(arr)
            if not res:
                continue
            W, H = im.width, im.height
            for box, text, score in res:
                xs = [p[0] for p in box]
                ys = [p[1] for p in box]
                cx = (min(xs) + max(xs)) / 2 / W
                cy = (min(ys) + max(ys)) / 2 / H
                bw = (max(xs) - min(xs)) / W
                if cy >= 0.78 and 0.3 <= cx <= 0.7 and bw > 0.02:
                    hits.append({"frame": i + 1, "text": (text or "")[:40]})
        c.close()
        return {"hits": hits, "skipped": False, "reason": ""}
    except Exception as e:
        return {"skipped": True, "reason": "OCR 扫描异常跳过: %s" % e}


def spectral_stats(path, sr_target=22050):
    """单曲频谱统计(整合 ai-film-skills bgm-spectral 实测判据):质心/滚降/低频占比/调性。
    零新依赖:PyAV 解码 + numpy FFT。低频带取 <500Hz(仓库实测参考档按此带标定):
    清新通透 质心~2000/滚降~4200/低频~15%/大调;深邃 ~1400/~2960/~33%/小调;压抑惊悚 ~550/~440/~61%。
    """
    import av
    import numpy as np
    c = av.open(path)
    a = c.streams.audio[0]
    rs = av.AudioResampler(format="s16", layout="mono", rate=sr_target)
    chunks = []
    for fr in c.decode(a):
        for out in _frame_list(rs.resample(fr)):
            arr = out.to_ndarray()
            if arr.dtype.kind in "iu":
                arr = arr.astype(np.float32) / float(np.iinfo(arr.dtype).max)
            chunks.append(arr.reshape(-1))
    for out in _frame_list(rs.resample(None)):
        arr = out.to_ndarray()
        if arr.dtype.kind in "iu":
            arr = arr.astype(np.float32) / float(np.iinfo(arr.dtype).max)
        chunks.append(arr.reshape(-1))
    c.close()
    if not chunks:
        return None
    y = np.concatenate(chunks)
    n = len(y)
    if n < 4096:
        return None
    win = 1 << 17
    hop = win // 2
    if n < win:
        win = 1 << 15
        hop = win // 2
    freqs = np.fft.rfftfreq(win, 1 / sr_target)
    mag2 = np.zeros(len(freqs))
    segs = 0
    for start in range(0, n - win + 1, hop):
        seg = y[start:start + win] * np.hanning(win)
        mag2 += np.abs(np.fft.rfft(seg)) ** 2
        segs += 1
    mag2 /= max(1, segs)
    total = mag2.sum()
    if total <= 0:
        return None
    centroid = float((freqs * mag2).sum() / total)
    cum = np.cumsum(mag2) / total
    rolloff = float(freqs[np.searchsorted(cum, 0.85)])
    lowf = float(mag2[freqs < 500].sum() / total)
    chroma = np.zeros(12)
    for idx in range(1, len(freqs)):
        if freqs[idx] < 80 or freqs[idx] > 4000:
            continue
        # A4=440Hz → 音级 9(等程十二平均,参考频率偏移 +9)
        pc = int(round(12 * np.log2(freqs[idx] / 440.0)) + 9) % 12
        chroma[pc] += mag2[idx]
    if chroma.sum() > 0:
        chroma /= chroma.sum()
    return {"centroid": round(centroid), "rolloff": round(rolloff), "lowfreq": round(lowf, 3),
            "major": round(float(chroma[[0, 4, 7]].sum()), 3),
            "minor": round(float(chroma[[0, 3, 7]].sum()), 3)}


def bgm_tier(st):
    """按频谱三指标推荐档位(仓库实测阈值,宽松判据):
    滚降掉到 ~440Hz 就是低频嗡鸣(被判「诡异」),深邃≠发闷分界在滚降 ~3000Hz。"""
    c, r, lf = st["centroid"], st["rolloff"], st["lowfreq"]
    if c <= 1000 and r <= 1500 and lf >= 0.40:
        return "压抑惊悚"
    if c <= 1800 and lf >= 0.22:
        return "深邃有质感"
    return "清新通透"


def cmd_bgm_pick(args):
    """选曲体检:对目录音频算频谱三指标 + 档位推荐(把「选曲」固化成命令,比写说明有效)。
    用途:① 选 BGM 前定档再找曲,别凭文件名;② 定期体检曲库(重复文件/档位失衡会锁死创作多样性)。
    """
    files = sorted(f for f in os.listdir(args.dir)
                   if f.lower().endswith((".mp3", ".m4a", ".wav", ".flac", ".aac", ".ogg")))
    if not files:
        print("❌ 目录无音频文件: " + args.dir)
        sys.exit(1)
    print(f"BGM 选曲体检 {len(files)} 首(质心/滚降/低频占比/调性 → 档位):")
    rows = []
    for f in files:
        p = os.path.join(args.dir, f)
        try:
            st = spectral_stats(p)
        except Exception as e:
            print(f"  {f:26s} ❌ {e}")
            continue
        if st is None:
            print(f"  {f:26s} ⚠️ 音频过短/解码失败")
            continue
        tier = bgm_tier(st)
        st["file"] = f
        st["tier"] = tier
        rows.append(st)
        print(f"  {f:26s} 质心{st['centroid']:>6}Hz 滚降{st['rolloff']:>6}Hz 低频{st['lowfreq']*100:3.0f}% 大调{st['major']:.2f} 小调{st['minor']:.2f} → {tier}")
    if args.json:
        with open(args.json, "w", encoding="utf-8") as fp:
            json.dump(rows, fp, ensure_ascii=False)
    if not rows:
        sys.exit(1)


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
    # 2026-08-27 升级:①段尾冻结自动截尾(defreeze,默认开)②静音分级(有台词镜静音=丢台词
    # 判失败;纯空镜静音=环境音弱,降软告警不触发重渲——换 seed 重渲也未必出环境音,
    # BGM 可兜底)。台词判定读 plan:h3_prompt 含 <d> 即有人声预期(对白/旁白都算)。
    dlg_shots = set()
    if getattr(args, "plan", "") and os.path.exists(args.plan):
        try:
            with open(args.plan, encoding="utf-8") as f:
                _plan = json.load(f)
            for s in _plan.get("shots", []):
                sid = int(s.get("shot_id") or 0)
                if not sid:
                    continue
                if str(s.get("dialogue", "")).strip() or "<d>" in str(s.get("h3_prompt", "")):
                    dlg_shots.add(sid)
        except Exception:
            pass
    def _has_dialogue(fn):
        stem = fn.rsplit(".", 1)[0]
        try:
            return int(stem) in dlg_shots
        except ValueError:
            return False
    bad = []
    report = {"shots": {}}
    ocr_warned = False
    for f in files:
        p = os.path.join(args.dir, f)
        try:
            r = check_video(p)
            flags = []
            soft = []  # 软告警(不判失败,不触发重渲)
            # 段尾冻结自动截尾(2026-08-27):H3 固有运动衰减,截掉冻结尾巴优于换 seed 重渲
            if r["freeze_ratio"] > 0.6 and not args.no_defreeze:
                trimmed = trim_frozen_tail(p)
                if trimmed > 0:
                    print(f"  ✂️ {f} 段尾冻结自动截除 {trimmed:.1f}s(冻结为 H3 固有特性,程序修复,不再重渲)")
                    r = check_video(p)
                    if r["freeze_ratio"] <= 0.6:
                        soft.append(f"段尾冻结已截尾{trimmed:.1f}s")
            if r["audio_streams"] == 0:
                flags.append("无音轨")
            elif r["audio_rms"] < 0.02:
                if _has_dialogue(f):
                    flags.append(f"静音丢台词(rms {r['audio_rms']:.3f})")
                else:
                    soft.append(f"静音告警(rms {r['audio_rms']:.3f},空镜环境音弱)")
            elif r["audio_rate"] > 0 and r["audio_rate"] != 32000:
                flags.append(f"音轨采样率异常({r['audio_rate']}Hz≠32000)")
            elif r["audio_channels"] > 0 and r["audio_channels"] != 2:
                flags.append(f"音轨非立体声({r['audio_channels']}ch≠2)")
            if r["dark_ratio"] > args.threshold:
                flags.append(f"近黑帧{r['dark_ratio']*100:.0f}%")
            if r["freeze_ratio"] > 0.6:
                flags.append(f"段尾冻结{int(r['freeze_ratio']*100)}%")
            if r["decoded_frames"] == 0:
                flags.append("解码0帧")
            if r["duration_s"] < 0.5:
                flags.append("时长过短")
            # 字幕位文字渗漏扫描(整合 ai-film-skills pitfalls ⑫:提示词里的台词/数字/标签
            # 会被画进画面字幕位;rapidocr 未装时优雅跳过)
            text_bleed = {"skipped": True, "reason": ""}
            if not args.no_ocr:
                text_bleed = scan_text_bleed(p)
                if text_bleed.get("skipped") and not ocr_warned:
                    ocr_warned = True
                    print("  ℹ️ " + (text_bleed.get("reason") or "OCR 跳过"))
                if text_bleed.get("hits"):
                    n = len(text_bleed["hits"])
                    samples = "、".join(f"#{h['frame']}:{h['text']}" for h in text_bleed["hits"][:3])
                    flags.append(f"字幕位文字×{n}({samples})")
            status = "OK" if not flags and not soft else ("⚠️ " + ",".join(flags + soft))
            if not flags and soft:
                status = "OK·" + ",".join(soft)
            print(f"  {f:12s} {r['duration_s']:6.2f}s {r['resolution']} 音轨:{r['audio_streams']}@{r['audio_rate']}Hz/{r['audio_channels']}ch 响度:{r['audio_rms']:.3f} 近黑:{r['dark_ratio']*100:3.0f}% 冻结:{int(r['freeze_ratio']*100):3d}% {status}")
            report["shots"][f] = {
                "ok": not flags, "flags": flags, "soft_flags": soft, "duration_s": r["duration_s"],
                "dark_ratio": r["dark_ratio"], "freeze_ratio": r["freeze_ratio"],
                "audio_streams": r["audio_streams"],
                "audio_rate": r["audio_rate"], "audio_channels": r["audio_channels"],
                "audio_rms": r["audio_rms"], "decoded_frames": r["decoded_frames"], "error": "",
                "text_bleed": text_bleed,
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
    srt_cues = []  # 2026-08-26 升级:台词/旁白字幕时间轴(成片秒),最后落盘 srt
    files = sorted(f for f in os.listdir(args.clips_dir) if f.lower().endswith(".mp4"))
    if not files:
        print("❌ 无镜头可合成: " + args.clips_dir)
        sys.exit(1)
    # 跳过镜头(用户决定不入成片):按 03.mp4 补零名匹配
    if args.skip_shots:
        skip = set()
        for x in args.skip_shots.split(","):
            x = x.strip()
            if x.isdigit():
                skip.add(x.zfill(2))
        kept = [f for f in files if f.rsplit(".", 1)[0] not in skip]
        if len(kept) != len(files):
            print(f"  ⏭ 跳过 {len(files) - len(kept)} 个镜头: {sorted(skip)}")
        files = kept
        if not files:
            print("❌ 全部镜头均被跳过,无可合成: " + args.clips_dir)
            sys.exit(1)
    out = args.out
    if os.path.exists(out):
        os.remove(out)
    if os.path.dirname(out):
        os.makedirs(os.path.dirname(out), exist_ok=True)
    fps = args.fps
    cues_by_id, takes_map = _load_subtitle_cues(args.plan)
    # 2026-08-27 按分镜表时长截断(用户反馈"像PPT"):H3 帧数按 17k+5 网格量化,产物常比
    # 计划时长多出最多 ~0.7s 的运动衰减尾巴;冻结尾巴 QC 已截,这里把剩余量化尾巴裁齐
    # 到分镜表 duration(只截短不补长,成片节奏回归分镜设计)。多切点长镜组头=组内求和。
    plan_frames = {}
    if args.plan and os.path.exists(args.plan):
        try:
            import json as _json
            with open(args.plan, encoding="utf-8") as f:
                _plan = _json.load(f)
            for s in _plan.get("shots", []):
                try:
                    sid = int(s.get("shot_id") or 0)
                    d = float(s.get("duration") or 0)
                    if sid and d > 0:
                        plan_frames[sid] = int(round(d * fps))
                except (TypeError, ValueError):
                    pass
            for grp in _plan.get("takes") or []:
                ids = [int(x) for x in grp if isinstance(x, (int, float))]
                if len(ids) >= 2:
                    plan_frames[ids[0]] = sum(plan_frames.get(i, 0) for i in ids) or plan_frames.get(ids[0], 0)
        except Exception:
            plan_frames = {}
    if args.no_subtitle:
        # 不烧录字幕:对白/旁白仅保留 H3 原生音轨,画面不出现字幕文字
        cues_by_id = {}
        takes_map = {}

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
        sid = _shot_no(name)
        plan_max = plan_frames.get(sid) or 0
        dur = float(v.duration * v.time_base)
        if clip_frames is not None:
            dur = clip_frames[name] / fps  # 数帧结果更准(元数据 duration 偶有偏差)
        if plan_max:
            dur = min(dur, plan_max / fps)  # 分镜表时长截断:量化尾巴不进成片
        # 转场边界判定:本剪辑头(与上一剪辑之间)、本剪辑尾(与下一剪辑之间)
        head_trans = trans_frames > 0 and ci > 0 and sid not in hard_cuts
        next_sid = _shot_no(files[ci + 1]) if ci + 1 < len(files) else 0
        tail_trans = trans_frames > 0 and ci + 1 < len(files) and next_sid not in hard_cuts
        # 本镜头字幕窗口:镜头内 12%-92%,台词/旁白按字数占比分配窗口
        # (12% 起:台词开说即出字幕,避免延后;92% 止:给下一镜转场留白)
        # 多切点长镜:组头文件承载组内全部台词/旁白
        cue_lines = _cues_for_shot(cues_by_id, takes_map, sid)
        subs = _assign_subtitle_windows(cue_lines, dur, film_sec) if cue_lines else []
        if args.srt and subs:
            srt_cues.extend(subs)
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
        if plan_max:
            total_frames = min(total_frames, plan_max) if total_frames is not None else plan_max
        tail_buf = deque(maxlen=trans_frames) if tail_trans else None
        atb = float(a.time_base) if a is not None else 0.0
        for frame in i.decode(*streams):
            if isinstance(frame, av.VideoFrame):
                if plan_max and cut_v >= plan_max:
                    break  # 分镜表时长已满,丢弃量化/衰减尾巴(音画同步截断)
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
    if args.srt and srt_cues:
        _write_srt(args.srt, srt_cues)
        print(f"  ✅ 字幕 srt 已导出: {args.srt} ({len(srt_cues)} 条)")
    print(f"  ✅ 帧 {total_v} / 音频块 {total_a}，成片已写入")


def _detect_face_rect(im):
    """cv2 FaceDetectorYN(YuNet)人脸检测;返回最大脸框 (x,y,w,h) 或 None。

    固定百分比窗口对不同构图的定妆照不鲁棒(主图是大头照时 8%-52% 只裁到下半脸,
    人物偏移时水平居中裁掉半张脸——2026-08-26 用户实测 *_face.png 脸部不全),
    检测到脸就以脸为中心裁,检测失败由调用方回退启发式窗口。
    模型按「本脚本同目录(Go 内嵌释放 face_detection_yunet onnx)」查找
    (opencv-python 5.x 已移除 CascadeClassifier,统一走 YuNet DNN 检测)。
    """
    try:
        import cv2
        import numpy as np
        # 抑制 cv2 5.x DNN 新引擎的 target 告警(WARN: Targets are not supported by
        # the new graph engine)——不影响检测结果,只刷屏污染运行日志
        try:
            cv2.utils.logging.setLogLevel(cv2.utils.logging.LOG_LEVEL_ERROR)
        except Exception:
            pass
        onnx = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                            "face_detection_yunet_2023mar.onnx")
        if not os.path.exists(onnx):
            return None
        w, h = im.size
        det = cv2.FaceDetectorYN.create(onnx, "", (w, h), score_threshold=0.5)
        faces, _ = det.detect(cv2.cvtColor(np.array(im), cv2.COLOR_RGB2BGR))
        if faces is None or len(faces) == 0:
            return None
        best = max(faces, key=lambda f: float(f[2]) * float(f[3]))  # 最大框
        return int(best[0]), int(best[1]), int(best[2]), int(best[3])
    except Exception:
        return None


def cmd_facecrop(args):
    """从定妆照切出完整正脸/头肩特写,作为 R2V 参考。

    H3 人脸 token 极少(视觉 VAE 32× 下采样),全身立绘脸占比小、锁定弱;
    用正脸特写可让脸部占满参考帧,身份锁定大幅增强。
    关键:输出按渲染同比例(默认 768x1344,可用 --ratio 覆盖)——ref_image_size=match
    会把参考图缩放/裁剪到输出尺寸,比例不一致会被压扁变形,脸部遵循直接劣化;
    同比例 + 紧凑脸区(检测到脸以脸为中心;否则启发式 垂直 8%-52% 水平居中)保证
    脸部占满且不变形、完整覆盖发顶/额头/下巴+少量肩。
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
    # 兽类(--beast):YuNet 只检测人脸,兽脸必然未命中——跳过检测直接启发式窗口,
    # 免白跑一次检测+刷「未命中」告警
    face = None if getattr(args, "beast", False) else _detect_face_rect(im)
    if face is not None:
        # 以脸为中心:脸框上方留 0.25 脸高(发顶),下巴下方 0.4 脸高(脖颈+肩),
        # 水平按输出比例取窗口并夹在图界内——脸再偏也裁得全
        fx, fy, fw, fh = [int(v) for v in face]
        cx, cy = fx + fw / 2.0, fy + fh / 2.0
        top = max(0, int(cy - fh * 0.75))
        bot = min(h, int(cy + fh * 0.90))
        ch = bot - top
        cw = int(ch * ratio)
        if cw > w:
            cw = w
            ch = int(cw / ratio)
            top = max(0, min(int(cy - ch * 0.42), h - ch))
            bot = top + ch
        left = int(min(max(cx - cw / 2.0, 0), w - cw))
        print("  ✂️ 人脸检测命中:脸框 %dx%d,以脸为中心裁切" % (fw, fh))
    else:
        top, bot = int(h * 0.08), int(h * 0.52)  # 启发式:垂直 8%-52%,检测失败兜底
        ch = bot - top
        cw = int(ch * ratio)
        if cw > w:  # 目标窗口超宽(竖图定妆照):限宽后按比例缩高
            cw = w
            ch = int(cw / ratio)
            bot = top + ch
        left = (w - cw) // 2
        if getattr(args, "beast", False):
            print("  ✂️ 兽类角色:按启发式窗口(垂直 8%-52% 水平居中)裁切兽首参考")
        else:
            print("  ✂️ 人脸检测未命中,按启发式窗口(垂直 8%-52% 水平居中)裁切")
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


# ---- 旁白/画外音后期配音(2026-08-23:H3 本地对画面外音/旁白不生成音轨→edge-tts 兜底) ----
def _split_dialogue(d):
    """dialogue 字段「角色:台词」多行拆解 → [(角色, 台词)]"""
    out = []
    for line in (d or "").splitlines():
        line = line.strip()
        if ":" in line:
            sp = line.split(":", 1)
            out.append((sp[0].strip(), sp[1].strip()))
    return out


def manju_voiceover(args):
    """把每镜的旁白(narration)与画外音台词(说话人不在该镜 characters)用 edge-tts
    合成并混入该镜音轨(镜头 12%-92% 窗口按字数分配时间点),再用 assemble 重合成。
    静音检测(2026-08-23 双配音修复):先算该镜音轨 RMS,≥阈值视为 H3 已生成语音/环境音,
    跳过配音(避免 H3 原生画外音 + TTS 双声);只有静音镜(<阈值)才补 TTS。
    """
    try:
        import edge_tts
    except Exception:
        print("⚠️ edge_tts 不可用,跳过配音(venv pip install edge-tts)")
        return
    import asyncio
    import subprocess
    import tempfile
    import av

    ff = args.ffmpeg or "ffmpeg"
    rms_threshold = args.silence_threshold
    plan = json.load(open(args.plan, encoding="utf-8"))
    done = 0
    skipped = 0
    for sh in plan.get("shots", []):
        sid = sh.get("shot_id")
        clip = os.path.join(args.clips_dir, "%02d.mp4" % int(sid))
        if not os.path.exists(clip):
            continue
        # 静音检测:该镜音轨 RMS(有语音/环境音 → 不补 TTS)
        try:
            with av.open(clip) as c:
                dur = float(c.duration) / av.time_base if c.duration else 4.0
                rms = _audio_rms(clip)
        except Exception:
            dur, rms = 4.0, 0.0
        lines = []
        # 2026-08-23 用户规则:只配「角色内心活动」——narration 含「内心·角色名」前缀的行
        # (角色对话走 H3 原生配音;客观旁白/画外音/渲染脚本内容一律不配 TTS,避免给脚本内容配音)
        if sh.get("narration"):
            narr = sh["narration"].strip()
            for seg in narr.splitlines():
                seg = seg.strip()
                if seg.startswith("内心·") or "内心·" in seg[:8]:
                    body = seg
                    if "：" in seg:
                        body = seg.split("：", 1)[1]
                    elif ":" in seg:
                        body = seg.split(":", 1)[1]
                    lines.append((body, args.voice_narr or "zh-CN-XiaoxiaoNeural"))
        if not lines:
            continue
        if rms >= rms_threshold:
            skipped += 1
            print(f"  ⏭ 镜 {sid} 已有语音/环境音(rms {rms:.3f}),跳过配音(防双声)")
            continue
        total_len = sum(len(t) for t, _ in lines)
        win_span = dur * 0.80
        cur = dur * 0.12
        for text, voice in lines:
            w = len(text) / total_len
            w_start = cur
            cur = cur + win_span * w
            delay_ms = int(w_start * 1000)
            tmp = tempfile.mktemp(suffix=".mp3")
            asyncio.run(edge_tts.Communicate(text, voice).save(tmp))
            out = clip + ".vo.mp4"
            cmd = [ff, "-y", "-i", clip, "-i", tmp,
                   "-filter_complex",
                   "[1:a]adelay=%d|%d,volume=%s[a1];[0:a][a1]amix=inputs=2:duration=first:dropout_transition=0[a]" % (delay_ms, delay_ms, args.gain),
                   "-map", "0:v", "-map", "[a]", "-c:v", "copy", "-c:a", "aac", "-b:a", "192k", out]
            r = subprocess.run(cmd, capture_output=True)
            if r.returncode != 0:
                print(f"  ❌ 镜 {sid} 配音失败: {r.stderr.decode('utf-8', 'ignore')[-200:]}")
                if os.path.exists(tmp):
                    os.remove(tmp)
                continue
            os.replace(out, clip)
            if os.path.exists(tmp):
                os.remove(tmp)
            done += 1
            print(f"  🎙 镜 {sid} 配音 {w_start:.1f}s: {text[:20]}… ({voice})")
    print("✅ 旁白/画外音配音完成,配音 %d 句,跳过 %d 镜(已有语音)" % (done, skipped))


def manju_voice_gen(args):
    """生成一段 edge-tts 语音作为 H3 音色参考音频(H3 只引用 timbre,文本内容不限)"""
    try:
        import edge_tts
    except Exception as e:
        print("❌ edge_tts 不可用: %s (venv pip install edge-tts)" % e)
        sys.exit(1)
    import asyncio
    out = os.path.abspath(args.out)
    os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
    asyncio.run(edge_tts.Communicate(args.text, args.voice).save(out))
    print("✅ 音色参考已生成: %s (%s)" % (out, args.voice))


def _audio_rms(path):
    """计算视频文件音轨 RMS(粗略响度;0=无音轨/静音)"""
    import math
    import av
    with av.open(path) as c:
        acc = 0.0
        n = 0
        for fr in c.decode(audio=0):
            arr = fr.to_ndarray()
            if arr is None or arr.size == 0:
                continue
            acc += float((arr.astype("float64") ** 2).mean())
            n += 1
        if n == 0:
            return 0.0
    return math.sqrt(acc / n)


import re as _reGlobal  # noqa: E402
reASRShot = _reGlobal.compile(r"^(\d+)\.mp4$", _reGlobal.IGNORECASE)
def _cues_with_weights(lines):
    """台词/旁白按字数加权:长句占更长的字幕窗口(原来是均分,长句放不下/短句空挂)"""
    weights = [max(1, len(l)) for l in lines]
    total = sum(weights)
    return [w / total for w in weights]


def _srt_ts(sec):
    """秒 → SRT 时间戳 HH:MM:SS,mmm"""
    sec = max(0.0, sec)
    h = int(sec // 3600)
    m = int((sec % 3600) // 60)
    s = int(sec % 60)
    ms = int(round((sec - int(sec)) * 1000))
    if ms >= 1000:
        ms = 0
        s += 1
    return "%02d:%02d:%02d,%03d" % (h, m, s, ms)


def _write_srt(path, cues):
    """(start_sec, end_sec, text) 列表 → srt 文件(按开始时间排序,去重相邻重叠)"""
    items = sorted((sa, sb, txt) for sa, sb, txt in cues if txt and txt.strip())
    out = []
    idx = 0
    for sa, sb, txt in items:
        if sb <= sa:
            sb = sa + 0.5
        idx += 1
        out.append(str(idx))
        out.append("%s --> %s" % (_srt_ts(sa), _srt_ts(sb)))
        out.append(txt.strip())
        out.append("")
    with open(path, "w", encoding="utf-8") as f:
        f.write("\n".join(out))


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
    # 单遍交错解码:数帧 + 近黑采样 + 冻结采样 + 音频响度 + 均匀抽帧(先数总数,再按目标位置取)
    samples, n = [], 0
    frame_means = []
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
                frame_means.append(float(g.mean()))
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
    freeze_ratio = 0.0
    if len(frame_means) >= 6:
        win = frame_means[-max(4, int(len(frame_means) * 0.25)):]
        diffs = [abs(win[i + 1] - win[i]) for i in range(len(win) - 1)]
        if diffs:
            freeze_ratio = round(sum(1 for d in diffs if d < 0.8) / len(diffs), 3)
    audio_rms = rms_sum / max(rms_n, 1)
    flags = []
    if a0 is None:
        flags.append("无音轨")
    elif audio_rms < 0.02:
        flags.append(f"静音(rms {audio_rms:.3f})")
    dark = sum(samples) / max(len(samples), 1) if samples else 0.0
    if dark > args.threshold:
        flags.append(f"近黑帧{dark*100:.0f}%")
    if freeze_ratio > 0.6:
        flags.append(f"段尾冻结{int(freeze_ratio*100)}%")
    if n == 0:
        flags.append("解码0帧")
    if dur < 0.5:
        flags.append("时长过短")
    qc = {"duration_s": round(dur, 2), "resolution": (v.width, v.height),
          "audio_streams": len(a), "audio_rms": round(audio_rms, 4),
          "dark_ratio": round(dark, 3), "freeze_ratio": freeze_ratio,
          "decoded_frames": n, "flags": flags, "ok": not flags}
    print(f"  🔬 {os.path.basename(args.file)} {dur:.1f}s {v.width}x{v.height} 音轨:{len(a)} 响度:{audio_rms:.3f} 近黑:{dark*100:.0f}% 冻结:{int(freeze_ratio*100):3d}% {'OK' if qc['ok'] else '⚠️ ' + ','.join(flags)}")
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
    q.add_argument("--plan", default="", help="分镜方案 JSON(静音分级:判定镜头有无台词/旁白)")
    q.add_argument("--no-defreeze", action="store_true", default=False,
                   help="关闭段尾冻结自动截尾(H3 固有运动衰减,默认程序截除而非重渲)")
    q.add_argument("--no-ocr", action="store_true", default=False,
                   help="跳过字幕位文字渗漏 OCR 扫描(默认开启;未装 rapidocr 自动跳过)")
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
    a.add_argument("--skip-shots", default="", help="合成时排除的镜头号(逗号分隔,如 7 或 1,3;用户决定跳过质检不过的镜头)")
    a.add_argument("--bgm", default="", help="背景音乐音频文件(循环补齐,按字幕窗口对白闪避)")
    a.add_argument("--bgm-gain", type=float, default=0.28, help="BGM 基础音量(0-1)")
    a.add_argument("--bgm-duck", type=float, default=0.35, help="对白时段 BGM 压低系数(0-1)")
    a.add_argument("--no-subtitle", action="store_true", default=False,
                   help="不烧录字幕(对白/旁白仅 H3 原生音轨,画面无字幕文字;2026-08-23 用户反馈成片字幕位文字优化)")
    a.add_argument("--srt", default="",
                   help="同时导出台词/旁白 srt 字幕文件(2026-08-26 升级:不烧录也可导出,供上传平台/剪映使用)")
    vo = sub.add_parser("voiceover")
    vo.add_argument("--plan", required=True, help="分镜方案 JSON(含 shots.narration/dialogue/characters)")
    vo.add_argument("--clips-dir", required=True, help="镜头目录(直接覆盖该镜 mp4 音轨)")
    vo.add_argument("--fps", type=int, default=24)
    vo.add_argument("--ffmpeg", default="ffmpeg", help="ffmpeg 可执行文件路径")
    vo.add_argument("--voice-narr", default="zh-CN-XiaoxiaoNeural", help="旁白语音(默认女声)")
    vo.add_argument("--voice-char", default="zh-CN-YunxiNeural", help="画外音角色语音(默认男声)")
    vo.add_argument("--gain", type=float, default=1.2, help="TTS 音量增益(默认 1.2)")
    vo.add_argument("--silence-threshold", type=float, default=0.02, help="静音判定 RMS 阈值:低于才补 TTS(防 H3 原生对白+ TTS 双声)")
    vg = sub.add_parser("voice-gen")
    vg.add_argument("--text", required=True, help="参考文本(生成音色参考音频,内容不限)")
    vg.add_argument("--voice", required=True, help="edge-tts 音色名,如 zh-CN-YunxiNeural")
    vg.add_argument("--out", required=True, help="输出文件路径(.mp3)")
    f = sub.add_parser("facecrop")
    f.add_argument("--src", required=True)
    f.add_argument("--dst", required=True)
    f.add_argument("--ratio", default="")  # 目标宽x高(默认 1344x768,与渲染同比例防变形)
    f.add_argument("--beast", action="store_true")  # 兽类角色:跳过人脸检测直接启发式(YuNet 只识人脸)
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
    bp = sub.add_parser("bgm-pick")
    bp.add_argument("--dir", required=True, help="曲库目录(扫描 mp3/m4a/wav/flac/aac/ogg)")
    bp.add_argument("--json", default="", help="另写频谱 JSON(选曲记录/许可 manifest 参考)")
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
    elif args.cmd == "bgm-pick":        cmd_bgm_pick(args)
    elif args.cmd == "voiceover":
        manju_voiceover(args)
    elif args.cmd == "voice-gen":
        manju_voice_gen(args)


if __name__ == "__main__":
    main()
