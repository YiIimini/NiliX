#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""rework_lead_costume_face.py — 全库主角服装演化 + 正角面容优化返工工具

规矩(2026-09-02):主角服装随剧情实时变化,禁一套穿到底;正角面容必须好看(男俊帅/女美萌可爱/老者慈祥硬朗)。

落点:
  1) 各书 素材/分镜脚本/*.json 每镜 h3_prompt:
     - subject_definitions 段 <Subject N> is ... 定义行(分号前):主角服装→该章档位;面容 epithet 注入(幂等)
       归属判定:行内名字(中/英/变体) → 泛指兜底(镜 characters 含该主角 + generic_re 命中 + 不含 exclude 名)
     - retention_analysis 行(<Subject N> (appears in Shot X): ... retained):主角编号行内旧服装短语→档位服装
     - detailed_description 等其余段不替换(防误换他人服装),仅统计残留
  2) 各书 素材/人物生成提示词.json:主角 costume 改分档说明;正角 appearance/image_prompt/q_form 面容增强(幂等)
  3) 各书 素材/人物生成提示词.md:头部追加服装分档说明(JSON 为准,md 兼容速查)

用法:
  python rework_lead_costume_face.py --dry            # 全库演练,只统计
  python rework_lead_costume_face.py --book 王牌三岁半  # 单书执行
  python rework_lead_costume_face.py                  # 全库执行

数据表:rework_lead_costume_face_data.json(服装档位/面容规则/泛指与排除均在此维护)
幂等:重复运行无二次变化。
"""
import argparse
import glob
import io
import json
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
DATA_PATH = os.path.join(HERE, 'rework_lead_costume_face_data.json')
ROOT = r'D:\Ai\NiliX\novel'

# 服装名词后的后置修饰尾巴(buttoned to the top / draped over his shoulders 等),
# 替换时一并吞掉,防止"新值尾部 + 残留旧尾巴"重复
EXT_TAIL = re.compile(r'\s+(?:buttoned|tied|draped|wrapped|hanging|with|and|tucked)\b[^,;.]{0,60}')


def _fix_regex(s):
    """heredoc 写入数据表时 \\b 可能被压成退格符 \x08,统一还原为正则 \b 转义。"""
    return s.replace('\x08', '\\b') if isinstance(s, str) else s


def load_data():
    with io.open(DATA_PATH, encoding='utf-8') as f:
        data = json.load(f)
    for cfg in data['books'].values():
        for lead in cfg['leads'].values():
            if lead.get('generic_re'):
                lead['generic_re'] = _fix_regex(lead['generic_re'])
            for r in lead.get('epithet_rules', []):
                r[0] = _fix_regex(r[0])
                r[1] = _fix_regex(r[1])
    return data


def stage_for(stages, ep):
    for st in stages:
        if st['from'] <= ep <= st['to']:
            return st
    return None


def shoes_for(shoes_stages, ep):
    for st in shoes_stages:
        if st['from'] <= ep <= st['to']:
            return st['shoes']
    return None


def fix_article(art, new_wear):
    if art == 'a ' and re.match(r'[aeiouAEIOU]', new_wear):
        return 'an '
    if art == 'an ' and not re.match(r'[aeiouAEIOU]', new_wear):
        return 'a '
    return art


def compile_owner(lead_cfg, exclude_names):
    """返回 (names, generic_re, excl):行归属判定用。"""
    names = lead_cfg['match']
    gre = re.compile(lead_cfg['generic_re']) if lead_cfg.get('generic_re') else None
    excl = [x for x in exclude_names if not any(x in n or n in x for n in names)]
    return names, gre, excl


def line_owned(seg, names, gre, excl, characters):
    if any(n in seg for n in names):
        return True
    if gre and characters and names[0] in characters:
        if gre.search(seg) and not any(x in seg for x in excl):
            return True
    return False


def replace_first_wear(seg, wear_re, new_wear, inject=True):
    """把 seg 中第一处旧服装短语替换为 new_wear(吞后置尾巴);无匹配且 inject 时行尾注入。返回 (新seg, 是否变更)。"""
    m = wear_re.search(seg)
    if not m:
        if not inject:
            return seg, False
        if seg.rstrip().endswith('.'):
            return seg.rstrip()[:-1].rstrip() + ', wearing ' + new_wear + '.', True
        return seg.rstrip() + ', wearing ' + new_wear + '.', True
    end = m.end()
    ext = EXT_TAIL.match(seg, end)
    if ext:
        end = ext.end()
    tok = seg[m.start():end]
    art = ''
    m2 = re.match(r'(an?|the) (.+)', tok)
    if m2:
        art, tok = m2.group(1) + ' ', m2.group(2)
    if tok == new_wear:
        return seg, False
    art = fix_article(art, new_wear)
    return seg[:m.start()] + art + new_wear + seg[end:], True


def is_dog_line(seg, guard, hints):
    if not guard:
        return False
    low = seg.lower()
    return any(g in low for g in guard) and not any(h in low for h in hints)


def rework_shot_prompt(h3, ep, book_leads, wear_re, counters, exclude_names, characters):
    """对单镜 h3_prompt 做服装+面容返工,返回新文本。"""
    changed_any = False
    lines = h3.split('\n')

    # pass1: 收集各主角的 Subject 编号(用于 retention 行归属)
    compiled = {}
    for lead_name, lead_cfg in book_leads.items():
        compiled[lead_name] = compile_owner(lead_cfg, exclude_names) + (lead_cfg,)
    subj_lead_ids = {name: set() for name in book_leads}
    for i, line in enumerate(lines):
        st = line.strip()
        if not st.startswith('<Subject') or '(appears' in st[:40]:
            continue
        seg = st.split(';')[0]
        for lead_name, (names, gre, excl, cfg) in compiled.items():
            if line_owned(seg, names, gre, excl, characters):
                m = re.match(r'<Subject (\d+)>', st)
                if m:
                    subj_lead_ids[lead_name].add(m.group(1))

    in_subject_defs = False
    out_lines = []
    for line in lines:
        stripped = line.strip()
        if stripped.startswith('subject_definitions:'):
            in_subject_defs = True
            out_lines.append(line)
            continue
        if stripped.startswith('<Subject') and (in_subject_defs or '(appears' in stripped[:40]):
            is_retention = '(appears' in stripped[:40]
            if is_retention:
                m = re.match(r'<Subject (\d+)>', stripped)
                if m:
                    for lead_name, (names, gre, excl, cfg) in compiled.items():
                        if m.group(1) in subj_lead_ids[lead_name]:
                            st_ = stage_for(cfg.get('stages', []), ep)
                            if st_ and not is_dog_line(stripped, cfg.get('dog_guard'), cfg.get('human_hint')):
                                new_line, did = replace_first_wear(stripped, wear_re, st_['wear'], inject=False)
                                if did:
                                    counters['retention'] += 1
                                    line = line.replace(stripped, new_line)
                                    changed_any = True
                            break
                out_lines.append(line)
                continue
            # is 定义行
            seg_tail = ''
            seg = stripped
            if ';' in stripped:
                idx = stripped.index(';')
                seg, seg_tail = stripped[:idx], stripped[idx:]
            line_did = False
            for lead_name, (names, gre, excl, cfg) in compiled.items():
                if not line_owned(seg, names, gre, excl, characters):
                    continue
                dog = is_dog_line(seg, cfg.get('dog_guard'), cfg.get('human_hint'))
                st = None if dog else stage_for(cfg.get('stages', []), ep)
                if st:
                    new_seg, did = replace_first_wear(seg, wear_re, st['wear'])
                    if did:
                        counters['wear'] += 1
                        changed_any = True
                        line_did = True
                        seg = new_seg
                shoes_re = cfg.get('shoes_re')
                if shoes_re and cfg.get('shoes_stages') and not dog:
                    sh = shoes_for(cfg['shoes_stages'], ep)
                    if sh and re.search(shoes_re, seg) and sh != shoes_re:
                        seg = re.sub(shoes_re, sh, seg)
                        counters['shoes'] += 1
                        changed_any = True
                        line_did = True
                fw = cfg.get('face_word', '')
                if fw and not dog and fw.lower() not in seg.lower():
                    for pat, rep in [tuple(r) for r in cfg.get('epithet_rules', [])]:
                        new_seg = re.sub(pat, rep, seg, count=1)
                        if new_seg != seg:
                            counters['face'] += 1
                            changed_any = True
                            line_did = True
                            seg = new_seg
                            break
            if line_did:
                line = line.replace(stripped, seg + seg_tail)
            out_lines.append(line)
            continue
        if in_subject_defs and stripped and not stripped.startswith('<Subject'):
            in_subject_defs = False
        out_lines.append(line)

    new_h3 = '\n'.join(out_lines)
    if changed_any:
        counters['shots_changed'] += 1
    # dd 残留统计:非 Subject 行中含主角名+服装词的镜(供人工复核,不替换)
    stale = False
    for line in new_h3.split('\n'):
        st2 = line.strip()
        if st2.startswith('<Subject') or stale:
            if not st2.startswith('<Subject'):
                continue
            continue
        for lead_name, (names, gre, excl, cfg) in compiled.items():
            if any(n in st2 for n in names) and wear_re.search(st2):
                stale = True
                break
    if stale:
        counters['dd_stale'] += 1
    return new_h3


def rework_book(book, cfg, wear_re, dry):
    bp = os.path.join(ROOT, book)
    script_dir = os.path.join(bp, '素材', '分镜脚本')
    files = sorted(glob.glob(os.path.join(script_dir, '*.json')),
                   key=lambda f: int(re.search(r'第(\d+)章', os.path.basename(f)).group(1)))
    counters = {'wear': 0, 'face': 0, 'shoes': 0, 'retention': 0, 'dd_stale': 0,
                'shots': 0, 'shots_changed': 0, 'chapters': 0}
    stage_stat = {}
    exclude_names = set(cfg.get('exclude_names', []))
    for lc in cfg['leads'].values():
        exclude_names.update(lc['match'])  # 其他主角名互斥(归属判定时剔除自身)
    for f in files:
        ep = int(re.search(r'第(\d+)章', os.path.basename(f)).group(1))
        with io.open(f, encoding='utf-8') as fh:
            d = json.load(fh)
        ch_changed = False
        for s in d.get('shots', []):
            h3 = s.get('h3_prompt', '')
            if not h3:
                continue
            counters['shots'] += 1
            # 按镜 characters 做泛指归属 → 把 characters 传入(重构:在 rework_shot_prompt 内部
            # 通过闭包读取不便,这里直接在 h3 前缀临时注入判定信息不可行;改为参数传递)
            new_h3 = rework_shot_prompt(h3, ep, cfg['leads'], wear_re, counters,
                                        frozenset(exclude_names), s.get('characters') or [])
            if new_h3 != h3:
                s['h3_prompt'] = new_h3
                ch_changed = True
        if ch_changed:
            counters['chapters'] += 1
            if not dry:
                with io.open(f, 'w', encoding='utf-8', newline='') as fh:
                    json.dump(d, fh, ensure_ascii=False, indent=1)
    # 档位统计(dry 校验用):重扫一遍最终文本里的 wear 短语归属
    for lead_name, lead_cfg in cfg['leads'].items():
        stage_stat[lead_name] = {}
        for st in lead_cfg.get('stages', []):
            stage_stat[lead_name][st['zh']] = 0
    for f in files:
        ep = int(re.search(r'第(\d+)章', os.path.basename(f)).group(1))
        with io.open(f, encoding='utf-8') as fh:
            d = json.load(fh)
        for s in d.get('shots', []):
            h3 = s.get('h3_prompt', '')
            for lead_name, lead_cfg in cfg['leads'].items():
                st = stage_for(lead_cfg.get('stages', []), ep)
                if not st:
                    continue
                if st['wear'] in h3:
                    stage_stat[lead_name][st['zh']] += 1
    # 人物卡
    cards_path = os.path.join(bp, '素材', '人物生成提示词.json')
    if os.path.exists(cards_path):
        with io.open(cards_path, encoding='utf-8') as fh:
            cards = json.load(fh)
        for c in cards:
            cid = c.get('id', '')
            lead_cfg = cfg['leads'].get(cid)
            if lead_cfg and lead_cfg.get('stages'):
                parts = ['EP%d-%d %s' % (st['from'], st['to'], st['zh']) for st in lead_cfg['stages']]
                new_costume = '服装随剧情分档(禁一套穿到底):' + ';'.join(parts) + '。面部特征锚点不变。'
                if c.get('costume') != new_costume:
                    c['costume'] = new_costume
                    counters['cards_costume'] = counters.get('cards_costume', 0) + 1
            face = cfg.get('card_faces', {}).get(cid)
            if face:
                ap = c.get('appearance', '')
                if face['appearance_add'] and face['appearance_add'] not in ap:
                    c['appearance'] = face['appearance_add'] + ';' + ap
                    counters['cards_face'] = counters.get('cards_face', 0) + 1
                ip = c.get('image_prompt', '')
                add = face.get('image_prompt_add', '')
                if add and add not in ip:
                    anchor = 'pure white background'
                    if anchor in ip:
                        ip = ip.replace(anchor, add + ', ' + anchor, 1)
                    else:
                        ip = ip.rstrip('.') + ', ' + add
                    c['image_prompt'] = ip
                qf = c.get('q_form', '')
                qadd = face.get('q_form_add', '')
                if qf and qadd and qadd not in qf:
                    c['q_form'] = qf.rstrip('.') + ', ' + qadd
        if not dry:
            with io.open(cards_path, 'w', encoding='utf-8', newline='') as fh:
                json.dump(cards, fh, ensure_ascii=False, indent=1)
            md_path = os.path.join(bp, '素材', '人物生成提示词.md')
            if os.path.exists(md_path):
                with io.open(md_path, encoding='utf-8') as fh:
                    md = fh.read()
                note = '\n> **服装分档说明(2026-09-02)**:主角服装随剧情实时变化(禁一套穿到底),各章具体着装以 `分镜脚本` 内 subject 定义为准;正角面容已按「男俊帅/女美萌可爱/老者慈祥硬朗」增强。 costume 字段记录全档位表。\n'
                if '服装分档说明(2026-09-02)' not in md:
                    md = md.replace('\n# 统一风格前缀', note + '\n# 统一风格前缀', 1)
                    with io.open(md_path, 'w', encoding='utf-8', newline='') as fh:
                        fh.write(md)
    return counters, stage_stat


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--book', default=None)
    ap.add_argument('--dry', action='store_true')
    args = ap.parse_args()
    data = load_data()
    wear_re = re.compile(data['WEAR_RE'])
    cfg_all = data['books']
    books = [args.book] if args.book else sorted(cfg_all.keys())
    for book in books:
        if book not in cfg_all:
            print('!! 未知书:', book)
            sys.exit(1)
        counters, stage_stat = rework_book(book, cfg_all[book], wear_re, args.dry)
        print('### %s %s' % (book, '[dry]' if args.dry else ''))
        print('   分镜: %d章改/%d镜改 | 服装%d行 retention%d行 鞋%d 面容%d | dd残留%d镜 | 卡:面容%d 服装%d'
              % (counters['chapters'], counters['shots_changed'], counters['wear'], counters['retention'],
                 counters['shoes'], counters['face'], counters['dd_stale'],
                 counters.get('cards_face', 0), counters.get('cards_costume', 0)))
        for lead, sts in stage_stat.items():
            print('   [%s]' % lead)
            for zh, n in sts.items():
                print('     %-52s %d镜' % (zh, n))


if __name__ == '__main__':
    main()
