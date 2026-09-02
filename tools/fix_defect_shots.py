# -*- coding: utf-8 -*-
"""修复全库预存六段缺陷镜(占位 h3 重生成):deepseek-chat 逐镜生成+程序化校验。
2026-09-02 配套「全库统一排查处理」:38 镜 "The scene continues..." 占位残骸。"""
import json, re, glob, os, sys, time, base64, urllib.request
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

NEED = ['subject_definitions:','summary:','retention_analysis:','detailed_description:','overall_soundscape:','non_diegetic_music:']

def load_api():
    key = open('.secret.key', 'rb').read()
    d = json.load(open('settings.json', encoding='utf-8'))
    enc = d['llm']['api_key']
    if enc.startswith('enc:'):
        raw = base64.b64decode(enc[4:])
        api = AESGCM(key).decrypt(raw[:12], raw[12:], None).decode()
    else:
        api = enc
    return api

API = load_api()

def extract_d_lines(dialogue, narration):
    out = []
    for src in (dialogue or '', narration or ''):
        for ln in src.split('\n'):
            ln = ln.strip()
            if not ln: continue
            if ln.startswith('内心'):
                rest = re.sub(r'^内心·?[^:：]*[:：]', '', ln).strip().strip('"“” ')
                if rest: out.append({'s': None, 'spk': None, 'txt': rest, 'inner': True})
                continue
            m = re.match(r'\((S\d+)\)([^:：]+)[:：](.*)$', ln)
            if not m: continue
            sn, spk, rest = m.groups()
            q = re.search(r'["“]([^"”]*)["”]', rest)
            content = (q.group(1) if q else rest).strip()
            if not content: continue
            out.append({'s': sn, 'spk': spk.split('画外·')[-1], 'txt': content, 'off': '画外' in spk})
    return out

def build_prompt(book, ch_file, shot, prev_shot, d_lines, start_ms):
    dur = shot.get('duration') or 5
    at = f"{start_ms//60000:02d}:{(start_ms%60000)//1000:02d}.{start_ms%1000:03d}"
    dl = '\n'.join((f"- ({l['s']}) {'画外·' if l.get('off') else ''}{l['spk']}: <d>[Chinese] {l['txt']}</d>"
                    if not l.get('inner') else f"- 内心: <d>[Chinese] {l['txt']}</d>") for l in d_lines)
    prev_desc = (prev_shot or {}).get('action', '(本章首镜)')[:100]
    sid = shot.get('shot_id')
    return f"""你是 NiliX 漫剧 H3 分镜提示词工程师。为下列镜头写完整六段式 h3_prompt(全英文,仅 <d> 内保留中文)。

书:{book} | 章:{ch_file} | 镜:[Shot {sid}] At {at}
景别:{shot.get('shot_size','')} | 运镜:{shot.get('camera','')} | 时长:{dur}s | 光:{shot.get('light','')} | 声:{shot.get('sound','')}
登场角色:{', '.join(shot.get('characters') or []) or '无'}
上一镜收尾(承接参考):{prev_desc}
本镜画面(action 中文):{shot.get('action','')}

台词(必须逐字嵌入,<d> 内容一字不改,可拆多句;无台词则六段式不写任何 <d>):
{dl or '(无台词)'}

要求:
1. 六段结构,每段一段、段标题带冒号、纯文本(禁 markdown/列表符号/多余空行):
subject_definitions:
summary:
retention_analysis:
detailed_description:
overall_soundscape:
non_diegetic_music:
2. subject_definitions 每主体一行,< > 尖括号符号必须原样输出,示例:
<Subject 1> is Jin Zhu, a composed female accountant in a dark robe, in <Picture 1>, with a steady gaze and a faintly cold expression.
Subject 顺序=characters 登场序。
3. retention_analysis 每主体一行官方格式,示例:
<Subject 1> (appears in [Shot {sid}]): fully_preserved - her steady gaze and the glowing spirit stone are retained.
4. detailed_description:首行风格句 `The target video uses a realistic live-action film style with photorealistic human characters, natural skin texture, cinematic lighting`。随后一行 `[Shot {sid}] At {at}`。非首镜第一句承接:`The clip opens holding the previous shot's closing framing for about one second - <上一镜收尾画面>, then ...`;固定镜写 `The camera holds still`;运动镜运镜句紧跟承接句之后。
5. 台词句式:画面内 `<Subject N> (Sx) says: <d>[Chinese] 原文</d>`;画外 `A <声线身份> voice off-screen (Sx) says in an off-screen voiceover: <d>[Chinese] 原文</d> while no on-screen character's lips move`;内心 `The narrator says in an off-screen voiceover, in a soft inward voice: <d>[Chinese] 原文</d> while the on-screen character's lips remain completely closed`。
6. 站位:每个登场角色写屏幕位置(左/中/右三分+前景/中景/背景+朝向)。
7. 动作:说话者说话时有可见反应;静止段写微小动作(呼吸/重心/视线)。
8. 只输出六段式文本。detailed_description 200-400 词。

完整输出示例(格式照抄;所有 < > 尖括号标签是真实文本的一部分,必须原样输出,不是占位符):
subject_definitions:
<Subject 1> is Lin Jiu, a lean young clerk in a dark grey robe, in <Picture 1>, with sharp calm eyes and an ink-stained right cuff.
summary:
[reference generation] Lin Jiu states the date plainly, and the registrar's smile freezes.
retention_analysis:
<Subject 1> (appears in [Shot 13]): fully_preserved - his sharp calm eyes, dark grey robe and ink-stained cuff are retained.
detailed_description:
The target video uses a realistic live-action film style with photorealistic human characters, natural skin texture, cinematic lighting.
[Shot 13] At 01:02.000
The clip opens holding the previous shot's closing framing for about one second - Lin Jiu at the ledger table in the center of the frame - then settles. The camera holds still. Lin Jiu stands at the left third of the frame in the midground, facing right toward the registrar; his fingertip rests on the ink mark, and as he speaks his brow steadies and the registrar's smile dies line by line. <Subject 1> (S1) says: <d>[Chinese] 是三个月前。</d> The registrar's brush stops mid-air; a bead of ink gathers at its tip and does not fall.
overall_soundscape:
The dry scratch of the halted brush, the low hum of the hall, cloth shifting on the bench.
non_diegetic_music:
A single low guqin note, sparse and cold, fading under the silence."""

def call_llm(prompt):
    body = json.dumps({'model': 'deepseek-chat', 'messages': [{'role': 'user', 'content': prompt}],
                       'max_tokens': 3000, 'temperature': 0.3}).encode()
    req = urllib.request.Request('https://api.deepseek.com/chat/completions', data=body,
                                 headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + API})
    r = json.load(urllib.request.urlopen(req, timeout=240))
    return r['choices'][0]['message']['content'].strip()

def norm(s):
    return re.sub(r'[\s,，。.．!！?？\-—·、:；;"\'‘’“”()()\[\]{}<>《》【】~～…]+', '', s)

def validate(out, sid, d_lines):
    if not all(k in out for k in NEED): return '缺段'
    if f'[Shot {sid}]' not in out: return '缺 [Shot N]'
    if 'At ' not in out: return '缺 At 时码'
    if not re.search(r'<Picture \d+>', out): return '缺 <Picture N>(尖括号)'
    dws = re.findall(r'<d>\[Chinese\]\s*([^<]+)</d>', out)
    if len(dws) != len(d_lines): return f'<d> 数不符({len(dws)}/{len(d_lines)})'
    pool = [norm(x) for x in dws]
    for l in d_lines:
        t = norm(l['txt'])
        if not any(t in p or p in t for p in pool): return f'台词缺失:{l["txt"][:20]}'
    if len(out) < 1000 or len(out) > 5500: return f'长度异常({len(out)})'
    return ''

def clean(out):
    out = out.strip().strip('`').strip()
    out = re.sub(r'[ \t]+\n', '\n', out)
    out = re.sub(r'[ \t]{2,}', ' ', out)
    out = re.sub(r'\n{3,}', '\n\n', out)
    # LLM 稳定把 <Picture N> 写成裸 Picture N(deepseek 视尖括号为占位装饰剥掉)——
    # 程序化后修:in Picture N -> in <Picture N>;Subject 同款兜底
    out = re.sub(r'in Picture (\d+)', r'in <Picture \1>', out)
    out = re.sub(r'(?<!<)Subject (\d+) is', r'<Subject \1> is', out)
    return out.strip()

def main():
    only = sys.argv[1] if len(sys.argv) > 1 else None
    fixed = failed = 0
    for b in sorted(glob.glob('novel/*/')):
        book = os.path.basename(os.path.normpath(b))
        for s in glob.glob(os.path.join(b, '素材', '分镜脚本', '*.json')):
            if only and only not in s: continue
            try: d = json.load(open(s, encoding='utf-8'))
            except Exception: continue
            shots = d.get('shots') if isinstance(d, dict) else d
            if not shots: continue
            dirty = False
            for i, sh in enumerate(shots):
                hp = sh.get('h3_prompt') or ''
                if all(k in hp for k in NEED): continue
                dls = extract_d_lines(sh.get('dialogue'), sh.get('narration', ''))
                start_ms = sum((x.get('duration') or 5) * 1000 for x in shots[:i])
                sid = sh.get('shot_id')
                ok = False
                for attempt in range(3):
                    try:
                        out = clean(call_llm(build_prompt(book, os.path.basename(s), sh,
                                                          shots[i-1] if i > 0 else None, dls, start_ms)))
                        err = validate(out, sid, dls)
                        if not err:
                            sh['h3_prompt'] = out; dirty = True; ok = True; fixed += 1
                            print(f'[OK] {book} {os.path.basename(s)} 镜{sid} (attempt{attempt+1})', flush=True)
                            break
                        print(f'[RETRY] 镜{sid} 校验失败: {err}', flush=True)
                    except Exception as e:
                        print(f'[ERR] 镜{sid}: {e}', flush=True)
                        time.sleep(3)
                if not ok: failed += 1
            if dirty:
                open(s, 'w', encoding='utf-8').write(json.dumps(d, ensure_ascii=False, indent=1))
    print(f'\n完成: 修复 {fixed} 镜, 失败 {failed} 镜')

if __name__ == '__main__':
    main()
