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

四大反派档全配"不好听"系声线,主角/正面档全好听——正反声线对位完成(17 档位真实干声)。
待补(仍是 edge-tts):beast_cute×2/child_boy×2/elder 系/female_mature×2/male_mag_3/
female_narrator/方言×6/港台×4 等 22 档——用 `--list` 查看,渠道见上表,
`voice_import.py --src 干声 --key <档位>` 一条命令替换。多句拼接成 8-17s 参考
(ffmpeg concat)更贴官方 10s+ 建议。
