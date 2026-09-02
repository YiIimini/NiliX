# -*- coding: utf-8 -*-
"""存量分镜按新契约返工(2026-09-02):运镜重分配 固定≤50% + 气闸 + 置首。

流程(LLM 出补丁 + 程序手术 + 校验兜底):
  1. 每章凝练上下文(镜号/景别/运镜/画面/ dd 头部)→ deepseek 选出需转运动镜的镜头
     并为每镜写贴合画面的运镜句(输出小 JSON 补丁);
  2. 程序应用:camera 列替换 + dd 静态镜头句替换为运镜句 + 气闸句补写/运镜句置首
     (复用全库调优的 transform 逻辑);
  3. 契约后检(复用 storyboard_check.check_contracts):H 仍超 → 按优先级程序化
     兜底转换(静态长镜→近景特写→全景远景)直到达标;
  4. 落盘(UTF-8),<d>/六段/时码零改动。

用法: python rework_camera_contract.py --root <书根> [--chapter N] [--dry-run] [--workers 4]
"""
import argparse, glob, json, os, re, sys, time, base64, urllib.request
from concurrent.futures import ThreadPoolExecutor

SKILL_SCRIPTS = r'C:\Users\Administrator\.agents\skills\NiliX-Novel\scripts'
sys.path.insert(0, os.path.normpath(SKILL_SCRIPTS))
import storyboard_check as sbc  # noqa: E402  复用 check_contracts/_cam_is_static

# 运镜类型白名单(与库内既有命名一致;渲染端按括号内英文三要素解析)
MOTION_TYPES = {
    '缓推（Push In, small, slow）', '急推（Push In, large, fast）',
    '缓拉（Pull Back, small, slow）', '拉（Pull Back, medium, slow）',
    '横移（Pan, medium, slow）', '缓摇（Pan, small, slow）',
    '跟移（Track, medium, slow）', '环绕（Orbit, small, slow）',
    '慢升（Rise, small, slow）',
}
GENERIC_SENT = 'The camera pushes in with small amplitude at slow speed, tightening the framing on the subject at the center of the frame.'
# 运镜列英文三要素 → 通用运镜句(K 兜底:存量镜运镜列动了但 dd 无运镜句时插入)
def generic_from_column(cam):
    m = re.search(r'（([^）]*)）', cam or '')
    if not m:
        return GENERIC_SENT
    parts = [x.strip() for x in m.group(1).split(',')]
    verb_en = parts[0] if parts else 'Push In'
    amp = parts[1].lower() if len(parts) > 1 else 'small'
    spd = parts[1 + 1].lower() if len(parts) > 2 else 'slow'
    tbl = {
        'push in': f'The camera pushes in with {amp} amplitude at {spd} speed.',
        'pull back': f'The camera pulls back with {amp} amplitude at {spd} speed.',
        'pan': f'The camera pans with {amp} amplitude at {spd} speed.',
        'track': f'The camera tracks alongside with {amp} amplitude at {spd} speed.',
        'orbit': f'The camera orbits the subject with {amp} amplitude at {spd} speed.',
        'rise': f'The camera rises with {amp} amplitude at {spd} speed.',
        'static': 'The camera holds still.',
    }
    low = verb_en.lower()
    for k, v in tbl.items():
        if k in low:
            return v
    return f'The camera moves with {amp} amplitude at {spd} speed.'
STATIC_SENT = re.compile(r'[Tt]he camera\s+(?:holds|remains|is|stays|keeps)[^.]{0,160}\.')
AIRLOCK_MARK = sbc.AIRLOCK_MARK
CAM_SENT = re.compile(r'(?:^|(?<=[.\s]))[Tt]he camera (?:pushes|pulls|pans|tracks|trucks|orbits|arcs|cranes|rises|drops|dollies|zooms|sweeps|tilts|follows)[^.]*\.')
CONTAM = re.compile(r'<d>|<Subject|says|voiceover')


def load_api():
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM
    key = open('.secret.key', 'rb').read()
    d = json.load(open('settings.json', encoding='utf-8'))
    enc = d['llm']['api_key']
    if enc.startswith('enc:'):
        raw = base64.b64decode(enc[4:])
        enc = AESGCM(key).decrypt(raw[:12], raw[12:], None).decode()
    return enc


API = None


def call_llm(prompt, max_tokens=4000):
    body = json.dumps({'model': 'deepseek-chat', 'messages': [{'role': 'user', 'content': prompt}],
                       'max_tokens': max_tokens, 'temperature': 0.3}).encode()
    req = urllib.request.Request('https://api.deepseek.com/chat/completions', data=body,
                                 headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + API})
    r = json.load(urllib.request.urlopen(req, timeout=240))
    return r['choices'][0]['message']['content'].strip()


# ---------------- 补丁应用(手术) ----------------

def apply_shot_patch(sh, cam, sent):
    """单镜手术:运镜列 + dd 静态句→运镜句;返回是否改动。"""
    sh['camera'] = cam
    hp = sh.get('h3_prompt') or ''
    di = hp.find('detailed_description:')
    if di < 0:
        return False
    os_i = hp.find('overall_soundscape:', di)
    if os_i < 0:
        os_i = len(hp)
    dd = hp[di:os_i]
    sent = sent.strip()
    sent = sent[0].upper() + sent[1:]
    if not sent.endswith('.'):
        sent += '.'
    m = STATIC_SENT.search(dd)
    if m and not CONTAM.search(m.group(0)):
        dd = dd[:m.start()] + sent + ' ' + dd[m.end():]
    else:
        # 无静态镜头句:运镜句插到 [Shot N] At 行之后
        blk = re.search(r'\[Shot \d+\](?:\s*At [\d:.,]+)?\s*', dd)
        if blk:
            dd = dd[:blk.end()] + sent + ' ' + dd[blk.end():]
        else:
            dd = dd.rstrip() + '\n' + sent
    sh['h3_prompt'] = hp[:di] + dd + hp[os_i:]
    return True


def transform_position(hp, shot_idx):
    """运动镜运镜句置首+缺气闸补写(与全库调优版同逻辑,纯位置手术)。"""
    di = hp.find('detailed_description:')
    if di < 0:
        return hp
    os_i = hp.find('overall_soundscape:', di)
    if os_i < 0:
        os_i = len(hp)
    dd = hp[di:os_i]
    m = re.search(r'\[Shot \d+\](?:\s*At [\d:.,]+)?\s*', dd)
    if not m:
        return hp
    body_ = dd[m.end():]
    cm = CAM_SENT.search(body_)
    if not cm:
        return hp
    cam_sent = cm.group(0).strip()
    if CONTAM.search(cam_sent):
        return hp
    pre = body_[:cm.start()]
    pre2 = re.sub(r'[\s,]*\b(?:then|and|as|while)\s*$', '', pre)
    pre2 = re.sub(r'[\s,;]+$', '', pre2)
    if pre2 and pre2[-1] not in '.!?' and pre2 != pre.rstrip():
        pre2 += '.'
    elif not pre2:
        pre2 = pre.rstrip()
    rest = (pre2 + ' ' + body_[cm.end():]).strip()
    rest = re.sub(r'\s{2,}', ' ', rest)
    first_m = re.match(r'[^.]*\.', rest)
    first = first_m.group(0) if first_m else ''
    first_air = bool(AIRLOCK_MARK.search(first))
    if rest.startswith(cam_sent):
        return hp
    if first_air:
        after = rest[first_m.end():].lstrip()
        if after.startswith(('The camera', 'the camera')):
            return hp
    moved = cam_sent[0].upper() + cam_sent[1:]
    if shot_idx > 0 and not first_air and not AIRLOCK_MARK.search(body_[:cm.start()]):
        nb = f"the clip opens holding the previous shot's closing framing for about one second. {moved} {rest}"
    elif first_m:
        cut = first_m.end()
        nb = rest[:cut].rstrip() + ' ' + moved + ' ' + rest[cut:].strip()
    else:
        nb = moved + ' ' + rest
    return hp[:di] + dd[:m.end()] + nb + hp[os_i:]


# ---------------- LLM 选镜 ----------------

def build_prompt(book, fn, shots, need):
    n = len(shots)
    static = sum(1 for s in shots if sbc._cam_is_static(s.get('camera')))
    quota = n // 2
    lines = []
    for i, s in enumerate(shots):
        hp = s.get('h3_prompt') or ''
        di = hp.find('detailed_description:')
        head = re.sub(r'\s+', ' ', hp[di:di + 220])[len('detailed_description:'):].strip() if di >= 0 else ''
        lines.append(f"镜{s.get('shot_id')} [{s.get('shot_size','')}] [{s.get('camera','')}] {s.get('duration','')}s | 画面:{(s.get('action') or '')[:60]} | 开头:{head[:120]}")
    return f"""你是 NiliX 漫剧分镜师的运镜重分配引擎。下列是《{book}》{fn} 的镜头清单(当前固定镜超标:固定 {static} 镜/共 {n} 镜)。

契约:本章固定镜必须 ≤{quota} 镜——**恰好选出 {need} 个固定镜转为运动镜,宁少勿多**(少转会被程序兜底,多转破坏节奏);连续固定 ≤3;静态镜 ≥8s 优先转。
选镜优先级(镜头设计方法论):①情绪顶点/神态特写(近景/特写+固定)→缓推;②人物走动/奔跑/穿越(画面含移动动作)→跟移;③全景/远景建立镜→横移或缓推;④揭晓/揭示内容→缓拉;⑤分散选取打破连续固定段,禁集中一窝蜂。
运镜句要求:贴合该镜画面主体(把镜头推向谁/扫过什么),自然英文句,以 "The camera" 开头、句号结尾,只写运镜不写剧情。

镜头清单:
{chr(10).join(lines)}

输出(纯 JSON,无围栏无解释):
[{{"id":镜号,"cam":"缓推（Push In, small, slow）","sent":"The camera pushes in with small amplitude at slow speed toward ..."}}, ...]
**恰好 {need} 项**;cam 只能用:缓推（Push In, small, slow）/急推（Push In, large, fast）/缓拉（Pull Back, small, slow）/拉（Pull Back, medium, slow）/横移（Pan, medium, slow）/缓摇（Pan, small, slow）/跟移（Track, medium, slow）/环绕（Orbit, small, slow）/慢升（Rise, small, slow）。"""


def pick_fallback(shots, need):
    """程序化兜底选镜:静态长镜→近景/特写→全景/远景→其余;分散取。"""
    static_idx = [i for i, s in enumerate(shots) if sbc._cam_is_static(s.get('camera'))]
    def prio(i):
        s = shots[i]
        sz = (s.get('shot_size') or '')
        if (s.get('duration') or 0) >= 8:
            return 0
        if '特写' in sz or '近景' in sz:
            return 1
        if '全景' in sz or '远景' in sz:
            return 2
        return 3
    cand = sorted(static_idx, key=prio)
    # 分散:按序轮转取,避免集中
    out, pools = [], {k: [i for i in cand if prio(i) == k] for k in range(4)}
    while len(out) < need and any(pools.values()):
        for k in range(4):
            if pools[k]:
                out.append(pools[k].pop(0))
            if len(out) >= need:
                break
    return out


def motion_for(shots, i):
    """兜底镜的运动类型:近景/特写→缓推;含走跑→跟移;全景远景→横移;默认缓推。"""
    s = shots[i]
    sz = (s.get('shot_size') or '')
    act = (s.get('action') or '')
    if re.search(r'走|跑|冲|穿|追|奔', act):
        return '跟移（Track, medium, slow）', 'The camera tracks alongside with medium amplitude at slow speed, following the movement through the frame.'
    if '全景' in sz or '远景' in sz:
        return '横移（Pan, medium, slow）', 'The camera pans slowly from left to right with medium amplitude across the scene.'
    return '缓推（Push In, small, slow）', GENERIC_SENT


# ---------------- 章级返工 ----------------

def rework_chapter(path, dry=False):
    book = os.path.basename(os.path.normpath(os.path.dirname(os.path.dirname(os.path.dirname(path)))))
    fn = os.path.basename(path)
    d = json.load(open(path, encoding='utf-8'))
    shots = d.get('shots') if isinstance(d, dict) else d
    n = len(shots)
    if n < 8:
        return None
    static = sum(1 for s in shots if sbc._cam_is_static(s.get('camera')))
    quota = n // 2  # 固定 ≤ floor(n/2)
    # need = 超配额数 + 静态长镜数(≥8s 一并转运动,治契约 N);
    # H 达标但 K/N/L 有残留的章仍走 ③/③b 清理相(不再整章跳过)
    long_static = sum(1 for s in shots
                      if sbc._cam_is_static(s.get('camera')) and (s.get('duration') or 0) >= 8)
    need = max(0, static - quota) + long_static
    pre_fails, pre_warns = sbc.check_contracts(shots)
    if need <= 0 and not pre_fails and not pre_warns:
        return None
    d_before = json.dumps(d, ensure_ascii=False).count('<d>')
    # ① LLM 选镜(need>0 才转)
    patch = []
    if need > 0:
        for attempt in range(2):
            try:
                out = call_llm(build_prompt(book, fn, shots, need))
                out = re.sub(r'^```(?:json)?|```$', '', out.strip(), flags=re.M).strip()
                patch = json.loads(out)
                break
            except Exception as e:
                print(f'    LLM {attempt + 1} 失败: {e}', flush=True)
                time.sleep(3)
    applied_llm = 0
    by_id = {}
    for p in patch if isinstance(patch, list) else []:
        if not isinstance(p, dict):
            continue
        cam = str(p.get('cam', ''))
        sent = str(p.get('sent', ''))
        if cam not in MOTION_TYPES:
            continue
        if not re.match(r'The camera [a-z]+', sent):
            continue
        by_id[int(p.get('id', 0))] = (cam, sent)
    # 只应用前 need 个有效补丁(按 LLM 优先序),宁少勿多——少的部分程序兜底
    for i, s in enumerate(shots):
        if applied_llm >= need:
            break
        sid = s.get('shot_id')
        if sid in by_id and sbc._cam_is_static(s.get('camera')):
            cam, sent = by_id[sid]
            apply_shot_patch(s, cam, sent)
            applied_llm += 1
    # ② 契约后检:①H 仍超 → 程序兜底转换;②静态长镜(≥8s)仍有剩余 → 强制转换
    #   (契约 N:LLM 未必按优先选长静态镜,程序保底)
    fails, _ = sbc.check_contracts(shots)
    hf = [f for f in fails if f.startswith('契约H')]
    applied_fb = 0
    if hf:
        static_now = sum(1 for s in shots if sbc._cam_is_static(s.get('camera')))
        need2 = static_now - quota
        for i in pick_fallback(shots, need2):
            cam, sent = motion_for(shots, i)
            apply_shot_patch(shots[i], cam, sent)
            applied_fb += 1
    for i, s in enumerate(shots):
        if sbc._cam_is_static(s.get('camera')) and (s.get('duration') or 0) >= 8:
            cam, sent = motion_for(shots, i)
            apply_shot_patch(s, cam, sent)
            applied_fb += 1
    # ③ 运镜句置首+气闸(全部运动镜统一过一遍位置手术)
    for i, s in enumerate(shots):
        if not sbc._cam_is_static(s.get('camera')):
            s['h3_prompt'] = transform_position(s.get('h3_prompt') or '', i)
    # ③b K 兜底:运镜列已动但 dd 无运镜句的存量镜(transform 无句可搬)——
    #     按运镜列生成通用运镜句,插到承接句/首句之后
    for i, s in enumerate(shots):
        if sbc._cam_is_static(s.get('camera')):
            continue
        hp = s.get('h3_prompt') or ''
        di = hp.find('detailed_description:')
        if di < 0:
            continue
        dd = hp[di:]
        m = re.search(r'\[Shot \d+\]', dd)
        blk = dd[m.start():m.start() + 800] if m else dd[:800]
        if CAM_SENT.search(blk):
            continue
        sent = generic_from_column(s.get('camera'))
        first_m = re.match(r'[\s\S]*?\.\s', blk[len('[Shot N]'):] if False else blk)
        # 插到 [Shot N] At 行之后(承接句在首句,插其后:找第一个句号)
        mm = re.search(r'\[Shot \d+\](?:\s*At [\d:.,]+)?\s*', dd)
        if mm:
            rest = dd[mm.end():]
            fm = re.match(r'[^.]*\.', rest)
            cut = mm.end() + (fm.end() if fm else 0)
            dd2 = dd[:cut].rstrip() + ' ' + sent + ' ' + dd[cut:].lstrip()
            s['h3_prompt'] = hp[:di] + re.sub(r'\s{2,}', ' ', dd2)
    # ④ 终检
    fails, warns = sbc.check_contracts(shots)
    hf = [f for f in fails if f.startswith('契约H')]
    d_after = json.dumps(d, ensure_ascii=False).count('<d>')
    assert d_before == d_after, f'{fn} <d> 数变化!'
    for s in shots:
        assert f"[Shot {s.get('shot_id')}]" in (s.get('h3_prompt') or ''), f'{fn} 镜{s.get("shot_id")} 丢 [Shot N]'
    ok = not hf
    if not dry:
        open(path, 'w', encoding='utf-8').write(json.dumps(d, ensure_ascii=False, indent=1))
    return {'file': fn, 'shots': n, 'static_before': static, 'llm': applied_llm,
            'fallback': applied_fb, 'H_pass': ok, 'warns': len(warns)}


def main():
    global API
    ap = argparse.ArgumentParser()
    ap.add_argument('--root', required=True, help='小说书根(或 all=全部书)')
    ap.add_argument('--chapter', type=int, default=0)
    ap.add_argument('--dry-run', action='store_true')
    ap.add_argument('--workers', type=int, default=4)
    args = ap.parse_args()
    API = load_api()
    roots = sorted(glob.glob('novel/*/')) if args.root == 'all' else [args.root]
    jobs = []
    for r in roots:
        for p in glob.glob(os.path.join(r, '素材', '分镜脚本', '*.json')):
            if args.chapter:
                m = re.match(r'第(\d+)章', os.path.basename(p))
                if not m or int(m.group(1)) != args.chapter:
                    continue
            jobs.append(p)
    print(f'待返工章数(未预筛): {len(jobs)}', flush=True)
    done = [0]

    def run(p):
        try:
            r = rework_chapter(p, dry=args.dry_run)
        except Exception as e:
            r = {'file': os.path.basename(p), 'error': str(e)}
        done[0] += 1
        print(f'[{done[0]}/{len(jobs)}] {r}', flush=True)
        return r

    with ThreadPoolExecutor(max_workers=args.workers) as ex:
        results = list(ex.map(run, jobs))
    okc = sum(1 for r in results if r and r.get('H_pass'))
    skip = sum(1 for r in results if r is None)
    err = sum(1 for r in results if r and 'error' in r)
    print(f'\n合计: {len(jobs)} 章 | 已合规跳过 {skip} | 返工 PASS {okc} | 异常 {err}')


if __name__ == '__main__':
    main()
