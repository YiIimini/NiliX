# -*- coding: utf-8 -*-
"""宽正则重扫:分清真缺特征 vs 检测器形态敏感漏检。

背景:渲染端 manjuFaceWeakness 用整词子串匹配("almond eyes"),"almond-shaped
dark brown eyes"(形容词插入/连字符形态)全部漏检——小满实为 6 类特征+印记达标
却被报 cats=2。本脚本用形态容忍正则重扫,输出真实不达标清单。
"""
import json
import io
import glob
import os
import re

RX = {
    'eye': re.compile(r'\b(?:almond|slanting|narrow|round|droopy|deep[- ]?set|sharp|keen|warm|sunken|beady|piercing|gentle|sleepy|hooded|bright|dark|big)[\w-]*(?:\s+[\w-]+){0,3}\s+eyes?\b', re.I),
    'brow': re.compile(r'\b(?:thick|arched|straight|fierce|bushy|heavy|slanting|gentle|curved|slim)[\w-]*(?:\s+[\w-]+){0,2}\s+(?:brows|eyebrows)\b', re.I),
    'nose': re.compile(r'\b(?:straight|hooked|snub|broad|aquiline|flat|bulbous|small|sharp|pointed|button)[\w-]*(?:\s+[\w-]+){0,2}\s+nose\b|\bbroken nose\b', re.I),
    'lip': re.compile(r'\b(?:thin|full|firm|tight|soft|small|wide)[\w-]*(?:\s+[\w-]+){0,2}\s+lips?\b|\bfull mouth\b', re.I),
    'face': re.compile(r'\b(?:square|round|oval|lean|gaunt|long|broad|narrow|haggard|baby|heart)[\w-]*(?:\s+[\w-]+){0,2}\s+face\b|\b(?:angular|chiseled|strong|soft|firm|delicate)[\w-]*(?:\s+[\w-]+){0,2}\s+jaw\b|\b(?:sunken|hollow|full|soft|high) cheek(?:bones|s)?\b', re.I),
    'skin': re.compile(r'\bweather[- ]?beaten\b|\bweathered\b|\bwrinkled\b|\bleathery\b|\bsallow\b|\bruddy\b|\bsun[- ]?darkened\b|\blined\b|\bcalloused\b|\bgreasy\b|\bpallid\b|\bfair rosy cheeks\b|\btanned\b', re.I),
    'hair': re.compile(r'\bcrew cut\b|\bbuzz cut\b|\blong hair\b|\bshort hair\b|\bslicked[- ]?back\b|\bponytail\b|\bbuns?\b|\bbald\b|\bwhite hair\b|\bgrey hair\b|\bgray hair\b|\bblack hair\b|\bbraid\w*\b|\bcurly hair\b|\bmohawk\b|\bside parting\b|\bmiddle part\b|\btousled\b|\bshaved head\b|\bthin hair\b|\bwispy bangs?\b|\bsalt[- ]and[- ]pepper\b|\breceding hairline\b|\bhair buns?\b|\bbangs\b', re.I),
}
MARK = re.compile(r'\bscar\w*\b|\bmole\b|\bbirthmark\b|\bearring\w*\b|\btattoo\w*\b|\bgold tooth\b|\bfreckles\b|\bbeauty mark\b|\bmissing tooth\b|\bbroken nose\b|\bblind eye\b|\bglass eye\b|\beyepatch\b|\bbrand mark\b|\bbeard\w*\b|\bmustache\b|\bmoustache\b|\bgoatee\b|\bstubble\b|\bwhiskers\b|\bsideburns\b', re.I)
GEN = re.compile(r'handsome face|fair face|standard face|ordinary face|good-looking|attractive face|clean-cut face|regular features', re.I)


def cats_of(img):
    return [k for k, rx in RX.items() if rx.search(img or '')]

def is_generic(img):
    # dead-regular features 豁免:"过分工整"伪善人设词(宋明堂实锤)
    return bool(GEN.search(re.sub(r'(?i)dead-regular features', '', img or '')))


def main():
    for sb in sorted(glob.glob('D:/Ai/NiliX/novel/*/素材/人物生成提示词.json')):
        book = os.path.basename(os.path.dirname(os.path.dirname(sb)))
        data = json.load(io.open(sb, encoding='utf-8'))
        chars = data if isinstance(data, list) else data.get('characters', [])
        tot = real_bad = false_pos = 0
        reallist = []
        for c in chars:
            cid = c.get('id', '') or ''
            img = c.get('image_prompt') or ''
            if not cid or not img or c.get('minor'):
                continue
            if '影灵' in cid or '影子' in cid:
                continue
            sp = c.get('species') or ''
            if sp and sp != '人':
                continue
            tot += 1
            cats = cats_of(img)
            mark = bool(MARK.search(img))
            gen = is_generic(img)
            # 宽匹配下真不达标:类<4 或无印记 或含泛化词
            if len(cats) < 4 or not mark or gen:
                real_bad += 1
                why = []
                if gen:
                    why.append('泛化词')
                if len(cats) < 4:
                    why.append('类=%d(%s)' % (len(cats), ','.join(cats)))
                if not mark:
                    why.append('无印记')
                reallist.append('%s(%s)' % (cid, '+'.join(why)))
        print('%s: 主要角色 %d | 真不达标 %d' % (book, tot, real_bad))
        if reallist:
            print('   ' + '、'.join(reallist))


if __name__ == '__main__':
    main()
