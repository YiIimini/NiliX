# -*- coding: utf-8 -*-
import io
import os
import re

SK = r'C:\Users\Administrator\.agents\skills\NiliX-Novel'

# 1) 示例第2处黑话句
p = os.path.join(SK, 'agents', '分镜示例.json')
raw = io.open(p, encoding='utf-8').read()
old = 'performs a slow cinematic dolly push-in with small amplitude toward the rubbing wall'
new = 'pushes in with small amplitude at slow speed toward the rubbing wall'
assert raw.count(old) == 1, raw.count(old)
io.open(p, 'w', encoding='utf-8', newline='').write(raw.replace(old, new))
print('example-2 OK')

# 2) 核查全仓剩余 dolly/orbital/crane 命中语境(应只剩"返正说明"文字,非指令句)
for root, dirs, files in os.walk(SK):
    dirs[:] = [d for d in dirs if d not in ('.git',)]
    for fn in files:
        if not fn.endswith(('.md', '.json', '.py')):
            continue
        fp = os.path.join(root, fn)
        s = io.open(fp, encoding='utf-8').read()
        for m in re.finditer(r'.{0,30}(?:dolly|orbital arc|crane rise|crane drop).{0,30}', s):
            print(os.path.relpath(fp, SK), '::', m.group(0).replace('\n', ' '))
print('SCAN DONE')
