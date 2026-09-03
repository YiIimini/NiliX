# NiliX 模块架构(2026-09-03 拆分后)

## 包职责地图

```
main.go                    入口:cfg 装载 → paths.InitPaths → 注入各域包状态 → HTTP 服务装配
internal/
├── util/      跨包纯函数唯一定义(无任何内部依赖,被所有域包引用):
│              Str / MustAtoi / Itoa / DirExists / FileExists / NowUnix / Truncate /
│              CopyFile / MaskKey / WriteJSON / WriteErr / NovelTitleSan / ReChapter /
│              ManjuPythonPath / IsPidAlive / CopyTree
├── paths/     路径全局唯一定义(纯路径解析,零业务依赖):
│              ManjuRootDir / NovelRootDir / ComfyRootDir / ComfySharedDir /
│              NovelSkillDir / VoiceLibDir / CharLibDir / AssetLibDir
│              InitPaths / MigrateAssetLibs / LegacyNovelSkill / OnPathsMigrated
├── manju/     漫剧制作 + 小说库域(业务大头,119+ 文件):
│              manju.go(RegisterRoutes 主路由) novel 路由 / fs 路由(RegisterFsRoutes) /
│              分镜脚本解析 / 提示词对齐 / 资产库 / 画布 / 质检 / 代理
├── comfy/     ComfyUI 服务域(客户端 + 安装 + 路由):
│              client.go(导出 Client) RegisterRoutes(comfy 路由)
├── api/       HTTP 壳 + 跨域杂项(最薄):
│              api.go(NewServer 装配,接线 manju/comfy 路由)
│              harness / island / kb / render / script / sysmon / zcode
├── agent/  config/  island/  sysmon/  watchdog/  cleanup/  autostart/
│              独立支撑域(单体时代已是独立包,未动)
└── render/ storyboard/ assemble/ verify/ backend/ kb_work/
               渲染管线与配套(单体时代已是独立包,未动)
```

## 依赖规则(拆分铁律)

1. **单向依赖**:util ← paths ← manju/comfy ← api ← main。各域包**只允许**依赖
   util/paths 与自己的子文件,禁止互相 import(防 import cycle)。
2. **包内别名模式**:拆包时原 internal/api 内大量小函数(str/dirExists/copyTree 等)
   唯一定义迁到 util,各业务包保留**同名非导出包装**(如 `func dirExists(p string) bool { return util.DirExists(p) }`),
   使包内调用点零改动、跨包只用导出面。
3. **路由注册函数化**:manju 域 `RegisterRoutes(mux)`、`RegisterFsRoutes(mux)`、
   comfy 域 `RegisterRoutes(mux)`;api 的 NewServer 只做接线,不再罗列各域路由行。
4. **导出面收敛**:跨包访问一律大写导出 + 注释说明归属域(如 comfy.SetComfyParams、
   manju.SetGlobalAgentCfg、paths.OnPathsMigrated)。原 comfyClient 私有类型导出为
   comfy.Client(字段 Base/HTTP、方法全大写),manju 侧经 http_helpers.go 别名调用。
5. **测试分布**:域内逻辑测试留在 `package manju`(内部测试,可碰私有符号,经
   internal/manju/manju_test_helpers_test.go 的 TestMain 统一初始化路径);
   跨域安全/集成测试在 `internal/api`(security_token_test.go)。全库扫描测试动态
   列书库(TestMain 注入 NovelRootDir),不写死书名——删书/加书自适应。

## 路径初始化时序

main → paths.InitPaths(exeDir, ...) → 解析 8 个全局 + MigrateAssetLibs(旧 char_lib/
voice_lib 迁 asset_lib)→ OnPathsMigrated 回调(manju init 注入索引重建)。
测试环境:manju TestMain 直接赋值临时根 + 真实技能库,不跑 InitPaths。

## 常见问题排查

- **改提示词不重渲** → 见运维手册「指纹链」;对齐改词=存量全量重渲预期。
- **全库扫描测试挂** → 先确认 novel/ 书库在位(TestMain 动态探测,不在位自动 skip)。
- **JSON 分镜解析拒稿** → 解析器已容错:episode 任意形态 / light·sound 数组 /
  字符串内裸控制字符转空格。若新形态再拒,去 internal/manju/manju_script_parse.go
  的 flexText/salvageJSONControlChars 扩展,别收紧为严格类型。
