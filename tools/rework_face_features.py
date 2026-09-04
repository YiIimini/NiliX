# -*- coding: utf-8 -*-
"""角色卡面容特征存量补写(2026-09-04,面容独特性硬规范双侧同步的存量层)。

排查结论(宽正则修复误报后):三部书主要角色 40 张,真不达标 24 张——
缺五官类别/无独有印记/含泛化词。LLM 补写 image_prompt(只增不删:保留既有
发色/发型/服装/年龄/气质/风格句,插入缺失类别的具体五官词+1 个贴人设的
独有印记;泛化词删除;发型措辞与全剧已用短语互异)。

安全设计:
  ① 校验闭环:补写后宽正则达标(≥4类+印记+无泛化词);原文 hair 类命中词
     全部保留;长度增幅 ≤70 词;失败保留原文
  ② q_form 继承(规范④):Q 版提示词缺新印记核心词时机械追加 "with <印记短语>"
  ③ md/json 双侧同步落盘(.md 旧 image_prompt 全文替换);.bak 保护;断点续跑
用法: python tools/rework_face_features.py [--dry-run] [--book 书名] [--limit N] [--workers 6]
"""
import argparse
import glob
import io
import json
import os
import re
import sys
import threading
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import storyboard_regen as sr

NOVEL_ROOT = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "novel")
LOG_DIR = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "tools", "logs")
PROGRESS = os.path.join(LOG_DIR, "face_feature_progress.json")

from face_feature_rescan import RX, MARK, GEN, cats_of  # noqa: E402

SYSTEM = """你是角色形象设计师。给定一张面容特征不足的角色定妆提示词(image_prompt),补写具体五官特征与 1 个独有印记,使定妆图不与他人撞脸。输出严格 JSON:{"image_prompt": "补写后的完整提示词全文"}

铁律(输出前逐条自查):
1. 【只增不删】原文全部内容逐字保留(发色/发型/服装/年龄/气质/风格句/构图/质量词/拟漫化锚句一个不动),只在既有外貌描述流里的恰当位置插入缺失特征;
2. 【缺什么补什么】按诊断补:缺哪类五官就补哪类(用具体词,与角色年龄/性别/身份/气质匹配——村妇老妇配 rough/worn 特征,少女配 soft/round 特征,武者配 sharp 特征);
3. 【独有印记必加】加且仅加 1 个与人设贴合的印记(scar/mole/birthmark/freckles/beauty mark/broken nose/missing tooth/gold tooth/eyepatch 等),位置具体(左眉骨上方/右耳垂/下巴左侧);
4. 【泛化词清除】handsome face/good-looking/attractive face 类泛化词删除,用具体特征替代;
5. 【发型互异】发型措辞不得与"全剧已用发型短语清单"里其它角色的短语雷同(近义可,逐字复述不可);
6. 英文自然流畅,插入后通顺;总长度增幅 ≤60 个英文词;禁止新增服装/道具/场景内容。"""


def hair_phrases(img):
    return RX['hair'].findall(img or '')


def build_user(card, diag, hairlist):
    lines = ["【角色】%s(%s,%s)" % (card.get('id'), card.get('gender', ''), card.get('age', ''))]
    if card.get('appearance'):
        lines.append("【人设】" + str(card['appearance'])[:160])
    lines.append("【诊断】" + diag)
    lines.append("【image_prompt 原文】\n" + (card.get('image_prompt') or ''))
    others = '; '.join(hairlist[:18])
    lines.append("【全剧已用发型短语清单(勿雷同)】" + (others or '无'))
    return '\n'.join(lines)


def diagnose(img):
    cats = cats_of(img)
    whys = []
    if GEN.search(img):
        whys.append('含泛化词(handsome face 类),须删除并用具体特征替代')
    missing = [k for k in ('eye', 'brow', 'nose', 'lip', 'face', 'skin') if k not in cats and k != 'skin']
    if len(cats) < 4:
        need = [k for k in ('eye', 'brow', 'nose', 'lip', 'face', 'skin', 'hair') if k not in cats]
        whys.append('五官特征类别不足(现有 %s),需补: %s' % (','.join(cats) or '无', ','.join(need)))
    if not MARK.search(img):
        whys.append('无独有印记,需加 1 个贴人设印记')
    return ';'.join(whys)


def validate(old, new):
    if not new or len(new) < 80:
        return '过短'
    if len(cats_of(new)) < 4:
        return '类别仍不足(%d)' % len(cats_of(new))
    if not MARK.search(new):
        return '仍无印记'
    if GEN.search(new):
        return '仍含泛化词'
    old_words = len(old.split())
    if len(new.split()) - old_words > 70:
        return '增幅超限'
    # 原文 hair 类命中词必须保留(发型/发色基调不动)
    for ph in set(hair_phrases(old)):
        if ph.lower() not in new.lower():
            return '原发型词丢失(%s)' % ph
    return ''


def mark_phrase(new_img):
    m = re.search(r'(?i)(?:a|an|one)?\s*(?:small|thin|faint|deep|pale|dark)?\s*[\w-]*\s*(?:scar|birthmark|mole|beauty mark|freckles|broken nose|missing tooth|gold tooth)\b[^,;.]{0,30}', new_img)
    return (m.group(0).strip() if m else '').rstrip(' ,;.')


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--dry-run', action='store_true')
    ap.add_argument('--book', default='')
    ap.add_argument('--limit', type=int, default=0)
    ap.add_argument('--workers', type=int, default=6)
    args = ap.parse_args()

    cfg = sr.load_api(None)
    done = set()
    if os.path.exists(PROGRESS):
        try:
            done = set(json.load(io.open(PROGRESS, encoding='utf-8')))
        except Exception:
            done = set()

    books = sorted(os.listdir(NOVEL_ROOT)) if not args.book else [args.book]
    jobs = []  # (json_path, md_path, book, card_idx, card)
    for book in books:
        for jp in sorted(glob.glob(os.path.join(NOVEL_ROOT, book, '素材', '人物生成提示词.json'))):
            data = json.load(io.open(jp, encoding='utf-8'))
            chars = data if isinstance(data, list) else data.get('characters', [])
            # 全剧发型清单(供互异参考)
            hairlist = []
            for c in chars:
                img = c.get('image_prompt') or ''
                if img and not (c.get('minor') or '影灵' in (c.get('id') or '') or '影子' in (c.get('id') or '')):
                    hairlist.extend(hair_phrases(img))
            for i, c in enumerate(chars):
                cid = c.get('id', '') or ''
                img = c.get('image_prompt') or ''
                if not cid or not img or c.get('minor') or '影灵' in cid or '影子' in cid:
                    continue
                sp = c.get('species') or ''
                if sp and sp != '人':
                    continue
                if len(cats_of(img)) >= 4 and MARK.search(img) and not GEN.search(img):
                    continue  # 达标
                if '%s|%s' % (book, cid) in done:
                    continue
                jobs.append((jp, jp[:-5] + '.md', book, i, c, hairlist))
    if args.limit > 0:
        jobs = jobs[: args.limit]
    print('待补写角色卡: %d(断点已完成 %d)' % (len(jobs), len(done)))
    if args.dry_run or not jobs:
        for jp, mp, book, i, c, hl in jobs[:30]:
            print('  %s / %s: %s' % (book, c.get('id'), diagnose(c.get('image_prompt') or '')))
        return 0

    os.makedirs(LOG_DIR, exist_ok=True)
    sem = threading.Semaphore(args.workers)
    lock = threading.Lock()
    stats = {'ok': 0, 'fail': 0}
    fails = []
    results = {}  # (jp) -> {idx: (new_img, mark)} ;落盘按文件聚合

    def expand(job):
        jp, mp, book, i, c, hairlist = job
        diag = diagnose(c.get('image_prompt') or '')
        for attempt in range(2):
            try:
                with sem:
                    data = sr.llm_json(cfg, SYSTEM, build_user(c, diag, hairlist), temp=0.4 if attempt == 0 else 0.6)
            except Exception:
                time.sleep(2)
                continue
            new = (data.get('image_prompt') or '').strip()
            if why := validate(c.get('image_prompt') or '', new):
                continue
            return (jp, i, new)
        return (jp, i, None)

    from concurrent.futures import ThreadPoolExecutor, as_completed
    with ThreadPoolExecutor(max_workers=args.workers) as ex:
        futs = {ex.submit(expand, j): j for j in jobs}
        for fut in as_completed(futs):
            jp, i, new = fut.result()
            job = futs[fut]
            cid = job[4].get('id')
            if new:
                with lock:
                    results.setdefault(jp, {})[i] = new
                    done.add('%s|%s' % (job[2], cid))
                    stats['ok'] += 1
            else:
                with lock:
                    stats['fail'] += 1
                    fails.append('%s|%s: %s' % (job[2], cid, diagnose(job[4].get('image_prompt') or '')))
            n = stats['ok'] + stats['fail']
            if n % 10 == 0:
                print('进度 %d/%d ok=%d fail=%d' % (n, len(jobs), stats['ok'], stats['fail']), flush=True)

    # 落盘(json+md 双侧)
    for jp, idxmap in results.items():
        mp = jp[:-5] + '.md'
        data = json.load(io.open(jp, encoding='utf-8'))
        chars = data if isinstance(data, list) else data.get('characters', [])
        md = io.open(mp, encoding='utf-8').read() if os.path.exists(mp) else ''
        for i, new in idxmap.items():
            old = chars[i].get('image_prompt') or ''
            chars[i]['image_prompt'] = new
            # q_form 印记继承(规范④):Q 版缺新印记词时机械追加
            qf = chars[i].get('q_form') or ''
            mp_ph = mark_phrase(new)
            if qf and mp_ph:
                core = re.sub(r'[^a-z]', '', mp_ph.lower())[:12]
                if core and core not in re.sub(r'[^a-z]', '', qf.lower()):
                    chars[i]['q_form'] = qf.rstrip(' .') + ', ' + mp_ph
            if md and old and old in md:
                md = md.replace(old, new)
        for jp2, mk in ((jp, '.json'), (mp, '.md')):
            if not os.path.exists(jp2):
                continue
            if not os.path.exists(jp2 + '.bak'):
                io.open(jp2 + '.bak', 'w', encoding='utf-8', newline='').write(
                    io.open(jp2, encoding='utf-8').read())
        io.open(jp, 'w', encoding='utf-8', newline='').write(
            json.dumps(data, ensure_ascii=False, indent=2))
        if md:
            io.open(mp, 'w', encoding='utf-8', newline='').write(md)
    io.open(PROGRESS, 'w', encoding='utf-8').write(json.dumps(sorted(done), ensure_ascii=False))
    if fails:
        io.open(os.path.join(LOG_DIR, 'face_feature_failures.txt'), 'w', encoding='utf-8').write('\n'.join(fails))
    print('完成: ok=%d fail=%d(失败保留原文)' % (stats['ok'], stats['fail']))
    return 0


if __name__ == '__main__':
    sys.exit(main())
