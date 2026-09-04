# -*- coding: utf-8 -*-
"""重建 char_lib 索引(删目录后清死条目)+ 验证小蒋归一。"""
import glob
import io
import json
import os
import re
import time

lib = 'D:/Ai/NiliX/asset_lib/characters'
entries = []
for d in sorted(glob.glob(os.path.join(lib, '*', 'card.json'))):
    name = os.path.basename(os.path.dirname(d))
    c = json.load(io.open(d, encoding='utf-8'))
    card = c.get('Card', c)
    fp = c.get('fingerprint') or c.get('Fingerprint', '')
    entries.append({
        'name': name, 'fingerprint': fp,
        'source_project': c.get('source_project') or c.get('SourceProject', ''),
        'created_at': c.get('created_at') or c.get('CreatedAt', 0),
        'image_prompt': (card.get('image_prompt') or '')[:80],
        'gender': card.get('gender'), 'age': card.get('age'), 'species': card.get('species'),
        'q_form': (card.get('q_form') or '')[:40], 'second_form': (card.get('second_form') or '')[:40],
        'assets': [os.path.basename(f) for f in glob.glob(os.path.join(lib, name, '*.png'))],
    })
io.open(os.path.join(lib, 'index.json'), 'w', encoding='utf-8', newline='').write(
    json.dumps({'count': len(entries), 'updated_at': int(time.time()), 'characters': entries},
               ensure_ascii=False, indent=1))
print('库索引重建:', len(entries), '条')

sb = glob.glob('D:/Ai/NiliX/novel/被论斤卖掉后我成了全网AI之母/素材/分镜脚本/第001章*.json')[0]
d = json.load(io.open(sb, encoding='utf-8'))
for s in d.get('shots', []):
    if '小蒋' in (s.get('characters') or []):
        hp = s.get('h3_prompt') or ''
        subs = re.findall(r'<Subject \d+> is [^\n]{0,80}', hp)
        print('镜%s chars=%s | %s' % (s.get('shot_id'), s.get('characters'), [x[:66] for x in subs][:3]))
