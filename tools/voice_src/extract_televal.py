# -*- coding: utf-8 -*-
"""TELEVAL 方言/年龄数据提取(2026-09-04,Tele-AI Apache-2.0):
按 speaker 分组拼接干声,F0 自相关判性别,导出档位候选。

方言档位映射(与 NiliX manjuVoiceLib 档位性别一致):
  cantonese→hk_female/hk_male;northeastern→cn_dongbei(女);
  henan→cn_henan(男);sichuanese→cn_sichuan(男)
age-zh:老年组按 F0 分男女 → female_elder 系候选。
"""
import io
import json
import os
import struct
import sys
import wave

import numpy as np
import pyarrow.parquet as pq

HERE = os.path.dirname(os.path.abspath(__file__))


def wav_to_f0_hz(b):
    try:
        with wave.open(io.BytesIO(b)) as w:
            sr = w.getframerate()
            n = w.getnframes()
            ch = w.getnchannels()
            sw = w.getsampwidth()
            raw = w.readframes(n)
        if sw == 2:
            x = np.frombuffer(raw, dtype=np.int16).astype(np.float64)
        elif sw == 4:
            x = np.frombuffer(raw, dtype=np.int32).astype(np.float64) / 65536
        else:
            return []
        if ch > 1:
            x = x.reshape(-1, ch).mean(axis=1)
        x /= (np.max(np.abs(x)) + 1e-9)
        frame, hop = int(sr * 0.04), int(sr * 0.02)
        f0s = []
        for i in range(0, len(x) - frame, hop):
            seg = x[i:i + frame] * np.hanning(frame)
            if np.sqrt(np.mean(seg ** 2)) < 0.02:
                continue
            ac = np.correlate(seg, seg, 'full')[frame - 1:]
            lo, hi = int(sr / 400), int(sr / 70)  # 70-400Hz
            if hi >= len(ac):
                continue
            lag = lo + int(np.argmax(ac[lo:hi]))
            if ac[lag] > 0.3 * ac[0]:
                f0s.append(sr / lag)
        return f0s
    except Exception:
        return []


def gender_of(wavs):
    f0s = []
    for b in wavs[:4]:
        f0s.extend(wav_to_f0_hz(b))
    if len(f0s) < 10:
        return 'unknown', 0.0
    med = float(np.median(f0s))
    return ('female' if med >= 165 else 'male'), med


def save_combo(wavs, texts, out, max_sec=17.0):
    os.makedirs(os.path.dirname(out), exist_ok=True)
    total = 0.0
    picked = []
    man = []
    for b, txt in zip(wavs, texts):
        with wave.open(io.BytesIO(b)) as w:
            sec = w.getnframes() / w.getframerate()
        if sec < 1.5 or len(txt) < 8:
            continue
        if total + sec > max_sec:
            continue
        picked.append(b)
        man.append({'sec': round(sec, 1), 'text': txt[:50]})
        total += sec
        if len(picked) >= 4:
            break
    if total < 6:
        return None
    data = b''.join(picked)
    with open(out + '.wav', 'wb') as f:
        f.write(data)
    io.open(out + '.manifest.json', 'w', encoding='utf-8').write(
        json.dumps(man, ensure_ascii=False, indent=1))
    return total


def extract_dialect(name, key_filter, want, out_prefix):
    t = pq.read_table(os.path.join(HERE, 'tv_%s.parquet' % name),
                      columns=['key', 'query', 'audio'])
    d = t.to_pydict()
    groups = {}
    for k, q, a in zip(d['key'], d['query'], d['audio']):
        spk = k.split('_')[1] if key_filter else 'ALL'
        b = a['bytes']
        if isinstance(b, str):
            continue
        groups.setdefault(spk, {'wavs': [], 'texts': []})
        if len(groups[spk]['wavs']) < 8:
            groups[spk]['wavs'].append(b)
            groups[spk]['texts'].append(q or '')
    results = []
    for spk, g in sorted(groups.items(), key=lambda kv: -len(kv[1]['wavs'])):
        gen, f0 = gender_of(g['wavs'])
        results.append((spk, gen, f0, g))
        print('  %s speaker=%s %s F0=%.0fHz (%d 条)' % (name, spk, gen, f0, len(g['wavs'])))
    for want_gender, tag in want:
        hit = next((r for r in results if r[1] == want_gender), None)
        if not hit:
            print('  %s 无 %s speaker' % (name, want_gender))
            continue
        spk, gen, f0, g = hit
        out = os.path.join(HERE, out_prefix + '_' + tag)
        total = save_combo(g['wavs'], g['texts'], out)
        print('  → %s (%.1fs, speaker=%s)' % (out + '.wav', total or 0, spk) if total else '  → %s 拼接不足' % tag)


def extract_aged():
    t = pq.read_table(os.path.join(HERE, 'tv_age-zh.parquet'))
    d = t.to_pydict()
    groups = {}
    for age, a, q in zip(d['age'], d['audio'], d['query']):
        b = a['bytes']
        if isinstance(b, str) or age != '老年':
            continue
        groups.setdefault(age, {'wavs': [], 'texts': []})
        groups[age]['wavs'].append(b)
        groups[age]['texts'].append(q or '')
    g = groups.get('老年')
    if not g:
        print('  无老年组')
        return
    gen, f0 = gender_of(g['wavs'])
    print('  老年组 %d 条,整体 %s F0=%.0fHz' % (len(g['wavs']), gen, f0))
    # 逐条 F0 分男女,各拼一组
    males, females = [], []
    for b, txt in zip(g['wavs'], g['texts']):
        gg, ff = gender_of([b])
        if gg == 'male':
            males.append((b, txt))
        elif gg == 'female':
            females.append((b, txt))
    print('  老年单条: 男 %d / 女 %d' % (len(males), len(females)))
    for tag, arr in (('elder_female', females), ('elder_male', males)):
        if len(arr) >= 3:
            out = os.path.join(HERE, 'aged_' + tag)
            total = save_combo([b for b, _ in arr], [t2 for _, t2 in arr], out)
            print('  → aged_%s.wav %.1fs' % (tag, total or 0))


if __name__ == '__main__':
    print('== cantonese → hk_female/hk_male')
    extract_dialect('chitchat-cantonese', True, [('female', 'hk_female'), ('male', 'hk_male')], 'tvout')
    print('== northeastern → cn_dongbei(女)')
    extract_dialect('chitchat-northeastern_mandarin', True, [('female', 'cn_dongbei')], 'tvout')
    print('== henan → cn_henan(男)')
    extract_dialect('chitchat-henan_dialect', True, [('male', 'cn_henan')], 'tvout')
    print('== sichuanese → cn_sichuan(男)')
    extract_dialect('chitchat-sichuanese', True, [('male', 'cn_sichuan')], 'tvout')
    print('== age-zh 老年 → female_elder 系')
    extract_aged()
