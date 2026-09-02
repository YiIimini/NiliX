# 升级记录:远景人群/悬浮屏「外国人」根治(知识库标尺版,2026-09-02)

## 背景

《王牌三岁半》EP01 镜1/镜2 反复出现「三个外国人/外国人站主角两边」。前三轮修复(人群纪律/焦外构图/屏内容钉死)均未根治,用户点名:**先参考知识库 H3 知识再改,不要瞎改**。

## 知识库标尺(创作管理/H3群演与Q版角色质量控制实战.md)

1. **未定义 Subject 的人物 = 模型自由发挥 = 复制参考图最显眼形象 / 拟人先验西方脸** —— 人群/屏上人物无卡必自由发挥。
2. **「FRAME 纪律的判定条件必须是'提示词有 subject_definitions'而非'characters 非空'——群像无卡镜(群演无卡)最需要纪律却最容易被漏」** —— 直接推翻前轮 `hasChars=true 跳过` 的设计:镜15 chars=[棠棠] 但观众席无卡,H3 照样自由发挥个体。
3. **「写在正向里的否定句(no cleavage)对高重绘模型基本无效」** —— 前轮脚本堆 `no individual face / no recognizable person` 否定句无效,必须给正向指令(背影/后侧3/4/剪影/焦外)。
4. **官方远景避脸**:群演远景用背影/后侧 3/4/剪影,never show a distant frontal face。
5. **三级分级**:氛围群演(无名无台词)= 不出卡,文字纪律兜底。

## 真实根因(两层)

1. **屏内容未钉死(镜1 三面悬浮屏)**:源脚本只写 `glowing white-blue` 没写显示内容 → H3 把屏幕默认渲染成**人像特写屏**(人脸检测 3 张脸均在发光屏幕框内,脸区亮蓝占比 0.38-0.53)→ 西方脸=「三个外国人」;纪律管场景人群、管不到屏幕内画面。
2. **镜2 开头外国人 = 接缝续写**:01 结尾屏上人脸被 pin 头带进 02 开头 1 秒(02 自身 1.5s 后只剩屏里 1 张脸)。

## 修复

### 源脚本(novel/王牌三岁半/素材/分镜脚本/第1章_0.3%_分镜脚本.json)
- 镜1:三面屏内容钉死为纯数字/数据流无人物;人群句全部改**正向背影剪影**措辞(`rows of anonymous backs and silhouettes seen from behind`),删除全部否定句;
- 镜2:屏内回放钉死为 single-subject recording(只有洛光年单人+数字,无观众);summary/retention 同口径;
- 镜5/8/13/14:屏 Subject 全部钉死显示内容(数字/98.9%/0.3%,无人物影像);镜5 summary 补「single-figure replay」;
- 镜3/15:人群个体诱发句(a man in the crowd / 金链男 / 红裙女)改群体动作+正向剪影,镜15 保留「笑场是剧情主体」的群体语义。

### 渲染端(injectCrowdDiscipline 按知识库标尺重写)
- **判定条件改**:不再 `hasChars=true 跳过`——有登场角色但 detailed_description 含人群词(观众/看台/crowd/spectators)照常注入(群像无卡镜最需纪律);
- **措辞改正向**:`blurred silhouettes seen from behind or in rear three-quarter view... no frontal views`(官方远景避脸),不再用纯否定句;
- 纪律句位置:detailed_description 段标题前(OFF-SCREEN LINES TASK 同款高服从位),存量队尾旧句先删后插自愈;
- 脚本已内嵌正向锚(anonymous backs/silhouettes seen from behind)时不重复注入。

### 技能侧(NiliX-Novel 仓库,同步知识库标尺)
- `H3分镜脚本文档模板.md` 第 7 条远景人海纪律第三次重写:正向背影/剪影写法、禁否定句、判定不看 characters 非空、渲染端纪律位置契约;
- 第 7b 条新增「屏/回放内容必须钉死」硬契约(纯数字/单人回放/禁人脸特写屏三选一,summary 同口径);
- `分镜派发模板.md` 画面列新增屏内容钉死契约。

## 验证

- TestInjectCrowdDiscipline 更新:有登场角色+人群 → 注入;无人群 → 不注入;幂等;队尾旧句自愈;正向锚免重复注入。全量单测 PASS。
- 脚本 25 镜六段齐全;人群措辞全正向(否定句 0 残留)。
- NiliX.exe 已重编译(16:45)。源脚本比 plan 新 → 重启应用重跑自动重解析生效。
