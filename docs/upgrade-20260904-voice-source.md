# 配音音源升级:外部真实音源接入(2026-09-04)

用户主诉「配音 AI 味十足」。排查实锤:**音色库 39 个档位全部由 edge-tts 合成**(每个约
2 秒机械念白),H3 拿它当音色参考(`<Audio N>` ref_audios),平板韵律被继承放大——AI 味根源。

## 一、官方规范(MiniMax 音色克隆最佳实践)

- **干净干声**:无混响、无背景噪音、单说话人(混响/多说话人/背景噪声直接影响克隆质量)
- 时长 **10 秒起步**(H3 ref2vid 实测 2 秒也能克隆但效果打折;官方区间 10s~5min)
- 像干净图像参考一样:裁到相关片段、降噪

## 二、升级架构:外部音源覆盖机制

- **Go 侧**(`genVoiceLibAudio`):权威目录 `asset_lib/voices/audio/lib_xxx.mp3` 旁挂
  `lib_xxx.src` 标记文件 → 该档位视为**外部音源**,跳过 edge-tts 合成与 meta 一致性
  自愈(手工导入的真实音源永不被合成版覆盖),只同步到 Comfy input。
- **导入工具** `tools/voice_import.py`:
  ```
  python tools/voice_import.py --list                                # 查看档位与来源
  python tools/voice_import.py --src 台词干声.mp3 --key lib_male_deep \
      [--ss 12.5] [--t 12] [--note 来源说明]                         # 截取导入
  ```
  自动:ffmpeg 截取(默认前 15s)+ 单声道 44.1k mp3 + EBU R128 响度归一(-16 LUFS 口播
  标准)+ `.bak` 备份旧音色 + `.src` 标记 + 同步 Comfy input + 更新 index.json。
- **渲染联动零配置**:镜头条件指纹含参考音频 mtime+size——音源替换后已渲镜头自动
  stale 重编重渲,新音色即刻生效。

## 三、音源获取渠道(热门动漫/二游角色)

| 渠道 | 内容 | 获取方式 | 适用 |
|------|------|---------|------|
| [hanamizuki-ai/genshin-voice-v3.3-mandarin](https://huggingface.co/datasets/hanamizuki-ai/genshin-voice-v3.3-mandarin)(hf-mirror 可达) | 原神 3.3 全角色中文干声台词(75033 条,npcName 标注) | parquet 分片下载解析(本轮示范脚本) | 直接当 H3 音色参考 |
| [ModelScope aihobbyist/Genshin_Dataset](https://modelscope.cn/datasets/aihobbyist/Genshin_Dataset) | 原神四语种语音整包(7z,258GB) | 整包下载较重 | 全量本地库 |
| [ModelScope aihobbyist/GPT-SoVITS_Model_Collection](https://modelscope.cn/models/aihobbyist/GPT-SoVITS_Model_Collection) | **3918 个动漫/二游角色 GPT-SoVITS 模型**(771GB,含原神 876 个) | 按角色下载(需 GPT-SoVITS 整合包推理出音频) | 高度还原克隆声线 |
| GPT-SoVITS V4 一键包(B 站/ai-hobbyist) | 整合包+600+二游音色 | 本地部署后任意文本合成干声 | 可控念白文本 |
| Fish Audio / CosyVoice 官方声线 | 可商用声线 | API/本地 | **商用合规首选** |

本轮示范:从 genshin-voice 分片解析热门角色干声 → 按档位导入(见 tools/voice_src/)。

## 四、⚠️ 版权红线(必读)

动漫角色台词的**声线权益归声优与版权方**(米哈游/任天堂等)所有:

- **个人研究/自用**:通常无碍(同人创作普遍存在);
- **公开发布/商用**:使用克隆声线有**声音权**(民法典声音权益参照肖像权保护)与
  **著作权**风险,且《深度合成管理规定》要求 AI 生成内容显著标识;
- **商用合规路径**:Fish/CosyVoice 官方可商用声线、开源多说话人声库(AISHELL-3 等)、
  自录真人声、或取得授权。

## 五、音色档位 → 推荐角色类型(参考)

选声线审美主观,以下为档位与角色气质对照的起点建议(具体角色由你在渠道清单里挑,
`voice_import.py` 一条命令导入):

## 五、已导入档位图(2026-09-04 二轮,正反派声线对位)

用户规则:**主角/正面角色=好听声线,反派=不好听声线**。当前 11 档位已换真实干声:

| 档位 | 音源角色 | 声线气质 | 阵营 |
|------|---------|---------|------|
| lib_male_sun(青年·阳光男主) | 空(游戏男主) | 温暖从容 | 正 |
| lib_boy_teen(少年·元气) | 温迪 | 清亮俏皮 | 正 |
| lib_female_warm / _2(温柔/知性女) | 神里绫华 / 荧 | 文雅 / 沉稳少女 | 正 |
| lib_girl_lively / _2(活泼/甜美少女) | 芭芭拉 / 安柏 | 明亮关切 / 活泼 | 正 |
| lib_male_mag / _2(磁性/沉稳男) | 白术 / 艾尔海森 | 温润 / 清冷智性 | 正 |
| lib_child_girl(童声) | 派蒙 | 咕咕高亢 | 正 |
| lib_male_narrator(旁白) | 戴因斯雷布 | 沧桑叙事 | 叙述 |
| **lib_male_deep(反派·威压)** | **深渊法师** | **阴森谄媚(「殿下…您的仆人又为您带回了一场胜利」)** | **反** |
| **lib_male_deep_2(反派·沙哑)** | **散兵** | **尖刻嘲讽** | **反** |
| **lib_female_deep(反派·冷冽)** | **罗莎莉亚** | **冷淡暗黑(「…哼,会是什么呢?」)** | **反** |
| **lib_female_deep_2(反派·肃杀)** | **雷电将军** | **威压庄重** | **反** |
| lib_boy_teen_2(少年·清亮) | 雷泽 | 野性直觉 | 正 |
| lib_male_sun_2(青年·清爽) | 托马 | 爽朗可靠 | 正 |

**终态 28/39 档真实干声**(多轮补齐):第四轮再补 child_girl_2←七七 / female_mature←申鹤
·_2←坎蒂丝 / female_narrator←琴 / male_mag_3←赛诺 / beast_cute←早柚·_2←砂糖 /
male_elder←钟离(帝王厚重)·_2←荒泷一斗(浑厚大嗓) / child_boy←五郎·_2←鹿野院平藏。

**GitHub 开源挖掘终态 35/39(第五轮,Tele-AI/TELEVAL,Apache-2.0)**:ModelScope
`TeleAI/TELEVAL` 评测集(parquet 内嵌 wav)补 7 档——cn_dongbei←东北话女声 / cn_henan←
河南话男声 / cn_sichuan←四川话男声 / hk_female←粤语女声 / hk_male←粤语男声(单条 5.6s
略短) / female_elder·_2←老年女声两组(age-zh 老年 70 条按 F0 男女分流)。工具
`tools/voice_src/extract_televal.py`:按 speaker 分组 + numpy 自相关基频自动判性别
(F0≥165Hz 女),多句拼接。

**剩余 5 档(陕西/广西/湖南方言 + 台普×2)为开源数据边界**:KeSpeech/WenetSpeech 系均
注册申请制,Common Voice 无方言/台腔音频,ModelScope 个人免费集无此三省——暂留
edge-tts(陕/湘/桂本就无方言声源走普通话+口音描述;台普为 zh-TW 原生声源,edge-tts
里效果最好的用法)。补齐路径:①KeSpeech 申请(学术) ②GPT-SoVITS 整合包克隆
(含台配角色模型) ③自录,`voice_import.py` 一条命令导入。——用 `--list` 查看,渠道见上表,
`voice_import.py --src 干声 --key <档位>` 一条命令替换。多句拼接成 8-17s 参考
(ffmpeg concat)更贴官方 10s+ 建议。
