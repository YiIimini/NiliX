# -*- coding: utf-8 -*-
"""口音词与声源对齐返工(2026-09-04,配音去电子味):
无源口音描述(Shaanxi/湖南/广西等)从分镜 h3 删除——描述与真实声源打架时 H3
折中"捏声"=电子味;有源口音(Henan=cn_henan★/Northeastern=cn_dongbei★/
Sichuan=cn_sichuan★/Cantonese=hk★)保留(描述与参考一致,增真实感)。
同步:石头卡音色行去"陕西腔"+voice_lib 换 boy_teen_2(雷泽野性贴憨直)。
"""
import glob
import io
import json
import os
import re

# 无源口音(删除):陕西(无男声源)/湖南/广西
ACC_DEL = re.compile(
    r'(?:,\s*)?(?:an?\s+)?(?:energetic\s+teenage\s+boy\s+with\s+a\s+|with\s+a\s+|in\s+a\s+)?'
    r'(?:heavy\s+|thick\s+|strong\s+|broad\s+)?(?:Shaanxi|Hunan|Guangxi)\s+accent', re.I)
# 保留(有真实声源):Henan/Northeastern|Dongbei/Sichuan/Cantonese


def clean(hp):
    out = ACC_DEL.sub('', hp)
    out = re.sub(r',\s*,', ',', out)
    out = re.sub(r'\(\s*,', '(', out)
    out = re.sub(r'\s{2,}', ' ', out)
    return out


def main():
    n_files = n_hit = 0
    for f in sorted(glob.glob('D:/Ai/NiliX/novel/*/素材/分镜脚本/*.json')):
        try:
            data = json.load(io.open(f, encoding='utf-8'))
        except Exception:
            continue
        dirty = False
        for s in data.get('shots', []):
            hp = s.get('h3_prompt') or ''
            nh = clean(hp)
            if nh != hp:
                s['h3_prompt'] = nh
                n_hit += 1
                dirty = True
        if dirty:
            if not os.path.exists(f + '.bak'):
                io.open(f + '.bak', 'w', encoding='utf-8', newline='').write(io.open(f, encoding='utf-8').read())
            io.open(f, 'w', encoding='utf-8', newline='').write(json.dumps(data, ensure_ascii=False, indent=2))
            n_files += 1
    print('口音清洗: %d 文件 %d 镜(Shaanxi/Hunan/Guangxi 删除;Henan/东北/四川/粤保留)')

    # 石头卡:音色行去陕西腔 + voice_lib 换 boy_teen_2
    jp = 'D:/Ai/NiliX/novel/递了三千年葫芦，她给自己发了飞升任务/素材/人物生成提示词.json'
    data = json.load(io.open(jp, encoding='utf-8'))
    chars = data if isinstance(data, list) else data.get('characters', [])
    for c in chars:
        if c.get('id') == '石头':
            c['voice_lib'] = 'boy_teen_2'
            if c.get('voice'):
                c['voice'] = c['voice'].replace('陕西腔', '乡野大嗓门').replace('陕西口音', '乡音')
            print('石头: voice_lib=boy_teen_2(雷泽), 音色行=%r' % (c.get('voice') or '')[:40])
    io.open(jp, 'w', encoding='utf-8', newline='').write(json.dumps(data, ensure_ascii=False, indent=2))
    mp = jp[:-5] + '.md'
    md = io.open(mp, encoding='utf-8').read()
    md = md.replace('少年陕西腔,嗓门大', '少年乡野大嗓门').replace('配音档位:boy_teen\n', '配音档位:boy_teen_2\n')
    io.open(mp, 'w', encoding='utf-8', newline='').write(md)
    print('石头 md 同步')


if __name__ == '__main__':
    main()
