# -*- coding: utf-8 -*-
"""三部书角色卡面容特征排查(与渲染端 manjuFaceWeakness 同款词表)。"""
import json
import io
import glob
import os

EYE = ['almond eyes', 'slanting eyes', 'narrow eyes', 'round eyes', 'droopy eyes', 'deep-set eyes',
       'sharp eyes', 'keen eyes', 'warm eyes', 'dark eyes', 'bright eyes', 'sunken eyes',
       'beady eyes', 'piercing eyes', 'gentle eyes', 'sleepy eyes', 'hooded eyes', 'big round eyes']
BROW = ['thick brows', 'arched brows', 'straight brows', 'fierce brows', 'bushy brows',
        'heavy brows', 'slanting brows', 'thick eyebrows']
NOSE = ['straight nose', 'hooked nose', 'snub nose', 'broad nose', 'aquiline nose', 'flat nose', 'bulbous nose']
LIP = ['thin lips', 'full lips', 'firm lips', 'tight lips', 'full mouth']
FACE = ['square face', 'angular jaw', 'round face', 'oval face', 'lean face', 'gaunt face', 'long face',
        'broad face', 'chiseled jaw', 'strong jaw', 'soft jaw', 'sunken cheeks', 'hollow cheeks',
        'high cheekbones', 'haggard face']
SKIN = ['weather-beaten', 'weathered', 'wrinkled', 'leathery', 'sallow', 'ruddy', 'sun-darkened',
        'lined', 'calloused', 'greasy', 'pallid']
HAIR = ['crew cut', 'buzz cut', 'long hair', 'short hair', 'slicked-back', 'ponytail', 'bun', 'bald',
        'white hair', 'grey hair', 'gray hair', 'black hair', 'braid', 'curly hair', 'mohawk',
        'side parting', 'middle part', 'tousled', 'shaved head', 'thin hair', 'wispy hair',
        'salt-and-pepper hair', 'receding hairline']
MARK = ['scar', 'mole', 'birthmark', 'earring', 'tattoo', 'gold tooth', 'freckles', 'beauty mark',
        'missing tooth', 'broken nose', 'blind eye', 'glass eye', 'eyepatch', 'twin scars',
        'brand mark', 'beard', 'mustache', 'goatee', 'stubble', 'whiskers', 'sideburns']
GEN = ['handsome face', 'fair face', 'standard face', 'ordinary face', 'good-looking',
       'attractive face', 'clean-cut face', 'regular features']


def weakness(img):
    low = (img or '').lower()
    cats = sum(1 for ws in (EYE, BROW, NOSE, LIP, FACE, SKIN, HAIR) if any(w in low for w in ws))
    mark = any(w in low for w in MARK)
    gen = any(w in low for w in GEN)
    if gen:
        return '含泛化词', cats
    if cats < 4:
        return 'cats=%d' % cats, cats
    if not mark:
        return '无印记', cats
    return '', cats


def main():
    for sb in sorted(glob.glob('D:/Ai/NiliX/novel/*/素材/人物生成提示词.json')):
        book = os.path.basename(os.path.dirname(os.path.dirname(sb)))
        try:
            data = json.load(io.open(sb, encoding='utf-8'))
        except Exception as e:
            print(book, 'PARSE-FAIL', e)
            continue
        chars = data if isinstance(data, list) else data.get('characters', [])
        tot = bad = 0
        badlist = []
        for c in chars:
            cid = c.get('id', '') or ''
            img = c.get('image_prompt') or ''
            if not cid or not img:
                continue
            if c.get('minor') or '影灵' in cid or '影子' in cid:
                continue
            sp = c.get('species') or ''
            if sp and sp != '人':
                continue
            tot += 1
            why, cats = weakness(img)
            if why:
                bad += 1
                badlist.append('%s(%s)' % (cid, why))
        print('%s: 主要角色 %d, 不达标 %d' % (book, tot, bad))
        if badlist:
            print('   ' + '、'.join(badlist))
    # md/json 一致性抽查(每书第一张卡 image_prompt 是否一致)
    print('--- md/json 一致性 ---')
    for sb in sorted(glob.glob('D:/Ai/NiliX/novel/*/素材/人物生成提示词.json')):
        book = os.path.basename(os.path.dirname(os.path.dirname(sb)))
        md = sb[:-5] + '.md'
        if not os.path.exists(md):
            print(book, '无 .md')
            continue
        jtxt = io.open(sb, encoding='utf-8').read()
        mtxt = io.open(md, encoding='utf-8').read()
        # json 抽第一张卡 image_prompt,查其是否在 md 中出现
        data = json.load(io.open(sb, encoding='utf-8'))
        chars = data if isinstance(data, list) else data.get('characters', [])
        hit = miss = 0
        for c in chars[:6]:
            frag = (c.get('image_prompt') or '')[:60]
            if frag and frag in mtxt:
                hit += 1
            elif frag:
                miss += 1
        print('%s: 前6卡 md 含 json 片段 hit=%d miss=%d' % (book, hit, miss))


if __name__ == '__main__':
    main()
