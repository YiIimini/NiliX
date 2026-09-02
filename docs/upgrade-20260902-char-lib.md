# 角色资产独立库 char_lib(2026-09-02)

> 用户需求:「生成的资产额外独立目录保存管理,后续技能侧角色输出可以直接复用
> 已有角色,就不用每次都重新渲染了」。

## 方案
跨项目角色资产库 `char_lib/<角色名>/`,与 voice_lib 同构(权威副本在自包含根,
项目目录只是工作副本,清理/重建不影响库;换电脑整体拷贝随迁)。

### 目录布局
```
char_lib/<角色名>/
  card.json        角色卡 + 形象指纹(fingerprint)+ 入库时间
  <角色名>.png           主定妆
  <角色名>_front.png     正脸特写
  <角色名>_full.png      全身视图
  <角色名>_side.png      侧面视图
  <角色名>_detail.png    细节特写
  <角色名>_q.png         Q版(正角)
  <角色名>_form2.png     真身形态(有 second_form 时)
```

### 形象指纹(复用判定)
`manjuCharFingerprint` = sha256(id + image_prompt + q_form + second_form + species +
gender + age) 前 12 字节。同名角色改过提示词 → 指纹不同 → 不误复用,自动重渲
后覆盖入库。指纹一致 → 直接复制全部资产,跳过 Krea-2 渲染(零成本+跨项目形象锁定)。

### 接入点(internal/api/manju_pipeline.go stageAssets)
1. **复用**:角色定妆前 `manjuCharLibReuse` —— 库命中则复制资产到项目
   assets/characters/ 并回写 asset_map,后续生成逻辑按"文件已存在"自动跳过;
2. **入库**:角色全部资产(主图+视图+Q版)生成后 `manjuCharLibStore` 复制进库
   (复用路径同样入库,幂等覆盖)。

### 管理 API
- `GET /api/manju/char-lib/list` → 库清单(名字/文件数/指纹前8/入库时间)
- `POST /api/manju/char-lib/delete {name}` → 删除库中角色

### 前端
渲染镜头管理弹窗新增「🎭 资产库」按钮 → 独立弹窗:列出库中角色(文件数/指纹/
入库时间),支持删除。

## 实现文件
- internal/api/paths.go:CharLibDir(自包含根/char_lib)
- internal/api/manju_char_lib.go:指纹/入库/复用/清单/删除 + 路由
- internal/api/manju_pipeline.go:stageAssets 接入(复用+入库)
- web/kb/js/manju.js + app.css:资产库弹窗

## 验证
- 单测:TestCharLibFingerprint(指纹同/异)、TestCharLibStoreReuse(入库→清项目→
  同形象复用/异形象不复用/清单/删除 全链路)。
- 全量 go test PASS;node --check PASS;enc_guard 体检正常;NiliX.exe 已重编译;
  ?v= 缓存戳 bump。

## 生效
替换 exe 后:渲染过的角色自动入库;新项目遇到同形象角色零渲染直接复用
(日志「♻️ 角色 X 复用资产库已有形象(N 张,零渲染)」)。

---

# 追加:资产库独立统一管理(2026-09-02 二轮)

> 用户:「资产库需要独立出来统一管理(弹窗显示,单角色一个数据展示卡片,点击预览
> 此角色详情包含提示词),针对的是所有项目不是单一项目」。

## 后端
- `GET /api/manju/char-lib/list` 每项补 `main`(主图缩略图,卡片封面);
- 新增 `GET /api/manju/char-lib/detail?name=` → 完整角色卡(card.json)+文件清单+主图;
- 新增 `GET /api/manju/char-lib/asset?name=&file=` → 库内图片预览(名称防穿越)。

## 前端
1. **全局入口**:右上统一操作区新增「🎭」角色资产库按钮(不依赖当前项目,
   与刷新/使用说明/设置同级)——所有项目共用一个库。
2. **统一管理弹窗**:角色卡片网格(主图缩略图/名字/资产数/入库时间/指纹),
   点击卡片或「👁 详情」→ 详情弹窗。
3. **详情弹窗**:左栏=角色卡字段(性别/年龄/物种/身份/服装/音色/角色位/记忆点)
   + 完整提示词(image_prompt/q_form/second_form,可滚动复制);
   右栏=全部视图缩略图(主图/正脸/全身/侧面/细节/Q版/真身)。
4. 渲染镜头管理弹窗的「🎭 资产库」按钮保留(同一入口,快速可达)。

## 验证
- TestCharLibDetailAsset(detail 完整卡/主图/文件清单/非法名防御);
- 全量 go test PASS;node --check PASS;enc_guard 正常;exe 已重编译(?v= bump)。

---

# 追加:角色管理弹窗「从资产库导入」(2026-09-02 三轮)

> 用户:「角色管理弹窗里可以手动选择资产库里的角色作为当前项目对应的角色」。

## 后端
- 新增 `POST /api/manju/char-lib/import {config, name}`:
  ①角色卡从库合并进当前项目 plan.characters(同名替换,新名追加);
  ②复制库内全部资产到项目 assets/characters + 回写 asset_map;
  ③落盘 plan + characters JSON——角色管理/渲染管线直接可用,零渲染。

## 前端
- 角色管理弹窗工具栏新增「🎭 从资产库导入」按钮;
- 导入选择弹窗:库中角色卡片(主图缩略图/名字/资产数/入库时间),点「📥 导入此角色」
  → 导入当前项目 → 弹窗关闭 + 角色管理原地刷新(新角色卡+资产就绪);
- 已存在同名角色会被资产库版本替换(以库为准)。

## 验证
- TestCharLibImport(库角色导入→plan 含新旧角色+资产复制+asset_map 回写);
- 全量 go test PASS;node --check PASS;exe 已重编译(?v= bump)。
