# -*- coding: utf-8 -*-
"""分镜契约 P/Q/R 存量返工(2026-09-03 两本书同步升级):
P 发色与角色卡一致(argmax 特征词重叠归属,镜像渲染端 fixSubjectHairColor)
Q 内心镜 chibi Subject 注入剧情情绪表情(六类中文情绪映射,镜像 fixChibiEmotion)
R 高危虚影动词替换(ghost 作动词→直白视觉语言,镜像渲染端词表)

用法: python storyboard_contract_rework.py [--dry]
备份: tools/logs/backup/sb_rework_20260903/
"""
import argparse, glob, json, os, re, shutil, sys

HAIR_COLOR_RE = re.compile(
    r'\b(platinum[- ]white|silver[- ]white|snow[- ]white|platinum|silver|golden|blonde|blond|white|black|brown|auburn|chestnut|raven|grey|gray|red)\b[^,.;]{0,24}?\bhair\b(?![- ](?:ornaments?|pins?|clips?|ribbons?|bands?|ties|strings?|sticks?|combs?|brushes?))', re.I)
CHIBI_FACE_RE = re.compile(
    r'(?i)\b(grin|smil|frown|scowl|glare|grit|wide-eyed|tear|pout|smirk|wince|panic|angry|furious|sad|sorrow|happy|delight|shock|startl|worried|trembl|grief|indignan|mood:)')
PHANTOM_DANGER = [
    (re.compile(r'\bghosts beside\b', re.I), 'lingers beside'),
    (re.compile(r'\bghost through\b', re.I), 'show through'),
    (re.compile(r'\bghosts across\b', re.I), 'drifts across'),
    (re.compile(r'\bghosts through\b', re.I), 'drift through'),
]

# 六类情绪(与渲染端 manjuMoodRules 同源:怒>悲>惊>惧>喜>愁)
MOOD_RULES = [
    ('怒', '怒 愤 气 恼 狠 咬牙 喝道 恨', 'an angry frown, brows knotted'),
    ('悲', '悲 泪 哀 凄 呜 亡 孤 苦 悔 痛', 'a sorrowful drooping look, eyes downcast'),
    ('惊', '惊 愕 愣 骇 呆 怔', 'wide startled eyes, mouth agape'),
    ('惧', '怕 惧 颤 惶 慌 急', 'a trembling worried expression'),
    ('喜', '喜 甜 暖 开心 得意', 'a bright delighted grin'),
    ('愁', '愁 叹 无奈 沉重 压抑 犹豫 烦 闷 羞 愧 酸 涩', 'a bitter heavy-hearted expression'),
]


def mood_face(text):
    if not text.strip():
        return None
    for _, words, face in MOOD_RULES:
        for w in words.split():
            if w in text:
                return face
    return None


def load_cards(book_dir):
    p = os.path.join(book_dir, '素材', '人物生成提示词.json')
    if not os.path.exists(p):
        return {}
    with open(p, encoding='utf-8') as f:
        cards = json.load(f)
    return {c['id']: c for c in cards}


def en_words(text):
    return {w.lower() for w in re.findall(r'[A-Za-z]{4,}', text or '')}


def card_hair(card):
    src = (card.get('image_prompt') or '') + ' ' + (card.get('appearance') or '') + ' ' + (card.get('costume') or '')
    m = HAIR_COLOR_RE.search(src)
    return m.group(1).lower() if m else ''


def norm(s):
    return re.sub(r'[^a-z]', '', s.lower())


def rework_hair(hp, chars, cards):
    """契约P:subject 行发色按角色卡校正。
    归属策略(2026-09-03 实测 argmax 纯特征词重叠对人名拼音盲——拼音不在卡词集,
    多镜重叠为零):①行内色在登场卡色集合内→跳过(可能正确);②不在集合且集合
    只有一种颜色→直接替换为该色(裴照 20+ 镜 white→black 主力场景);③多色集合
    →argmax 特征词重叠定角色,零重叠跳过(不臆造)。"""
    fixed = 0
    if 'hair' not in hp or not chars:
        return hp, fixed
    di = hp.find('subject_definitions:')
    if di < 0:
        return hp, fixed
    de = hp.find('detailed_description:')
    seg = hp[di:de if de > 0 else len(hp)]
    entries = []
    for cid in chars:
        card = cards.get(cid)
        if not card:
            continue
        hair = card_hair(card)
        if not hair:
            continue
        entries.append((cid, hair, en_words(card.get('image_prompt'))))
    if not entries:
        return hp, fixed
    color_set = {norm(h) for _, h, _ in entries}
    for ln in seg.split('\n'):
        if 'hair' not in ln or '<Subject' not in ln or ' is ' not in ln:
            continue
        if 'reference attachment' in ln.lower() or 'multiple views of' in ln.lower():
            continue
        m = HAIR_COLOR_RE.search(ln)
        if not m:
            continue
        got = norm(m.group(1))
        if got in color_set:
            continue
        target = ''
        if len(color_set) == 1:
            target = list(color_set)[0]
        else:
            lw = en_words(ln)
            best, bestn = -1, 0
            for i, (_, _, words) in enumerate(entries):
                n = len(lw & words)
                if n > bestn:
                    best, bestn = i, n
            if best < 0 or bestn == 0:
                continue
            target = norm(entries[best][1])
        if not target or target == got:
            continue
        orig_hair = [h for _, h, _ in entries if norm(h) == target][0]
        new_ln = ln.replace(m.group(0), orig_hair + ' hair', 1)
        hp = hp.replace(ln, new_ln, 1)
        fixed += 1
    return hp, fixed


def rework_chibi(hp, narr, dlg):
    """契约Q:chibi 行无表情词→按 narration/dialogue 情绪注入(镜像 fixChibiEmotion)"""
    if 'chibi version of' not in hp:
        return hp, 0
    # 清理首轮注入的句号衔接瑕疵(chin., its face → chin, its face)
    hp = re.sub(r'[.;], its face showing', ', its face showing', hp)
    face = mood_face((narr or '') + ' ' + (dlg or ''))
    if not face:
        return hp, 0
    for ln in hp.split('\n'):
        if 'is the chibi version of' not in ln or CHIBI_FACE_RE.search(ln):
            continue
        new_ln = ln.rstrip(' \r').rstrip('.;') + ', its face showing the current mood: ' + face
        return hp.replace(ln, new_ln, 1), 1
    return hp, 0


def rework_phantom(hp):
    """契约R:高危虚影动词替换(镜像渲染端 manjuRenderWordReplace)"""
    fixed = 0
    for pat, rep in PHANTOM_DANGER:
        n = len(pat.findall(hp))
        if n:
            hp = pat.sub(rep, hp)
            fixed += n
    return hp, fixed


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--dry', action='store_true', help='只统计不写盘')
    ap.add_argument('--root', default='D:/Ai/NiliX/novel')
    args = ap.parse_args()
    backup = 'tools/logs/backup/sb_rework_20260903'
    stats = {'P': 0, 'Q': 0, 'R': 0, 'files': 0}
    for book_dir in sorted(glob.glob(os.path.join(args.root, '*'))):
        cards = load_cards(book_dir)
        for fp in sorted(glob.glob(os.path.join(book_dir, '素材', '分镜脚本', '*.json'))):
            with open(fp, encoding='utf-8') as f:
                doc = json.load(f)
            shots = doc if isinstance(doc, list) else doc.get('shots')
            if not shots:
                continue
            changed = False
            for s in shots:
                hp = s.get('h3_prompt') or ''
                if not hp:
                    continue
                hp2, np_ = rework_hair(hp, s.get('characters') or [], cards)
                hp2, nq = rework_chibi(hp2, s.get('narration'), s.get('dialogue'))
                hp2, nr = rework_phantom(hp2)
                if hp2 != hp:
                    s['h3_prompt'] = hp2
                    changed = True
                    stats['P'] += np_
                    stats['Q'] += nq
                    stats['R'] += nr
            if changed and not args.dry:
                os.makedirs(backup, exist_ok=True)
                dst = os.path.join(backup, os.path.basename(fp))
                if not os.path.exists(dst):
                    shutil.copy2(fp, dst)
                with open(fp, 'w', encoding='utf-8', newline='\n') as f:
                    json.dump(doc, f, ensure_ascii=False, indent=2)
                stats['files'] += 1
    print(('[dry] ' if args.dry else '') + f"返工完成: P发色 {stats['P']} 处 / Q表情 {stats['Q']} 处 / R虚影 {stats['R']} 处 / 改写 {stats['files']} 章")


if __name__ == '__main__':
    main()
