# 开发约定（仓库级规则）

## 🔁 更新后自动编译打包推送（铁律）

每次完成代码修改 / 修复后，**自动执行以下流程，无需用户再口头要求**：

1. **编译打包**：`build.bat`（`go build -ldflags "-H windowsgui -s -w" -o NiliX.exe .`）
2. **验证**：`go build ./...` + `go test ./internal/...` 全部通过后再提交
3. **提交**：`git add -A` → `git commit`（中文 commit message，按功能分提交：
   修复类单独一个提交、开发累积一个提交，历史清晰）
4. **推送**：`git push origin main`

> 若修改涉及第三方库（`third_party/`）或 WebView2 交互，编译后需实际验证
> 窗口/胶囊表现（黑底、闪色、透明失效等问题根源在 COM 线程，见下）。

## 🎬 渲染相关约定（防止"反复中断零进度"）

- **渲染进行中请勿退出应用**：H3 单镜编码 30~70 分钟（条件编码 ~10 分钟 +
  8 步采样 × 帧数 × ~1.42s/帧），中途退出会打断采样、条件缓存（`.pt`）不落盘，
  下次续跑该镜重编——此前 10 次"启动→续跑→退出"循环零进度的直接根因。
- **续跑保留历史日志**：`run.log` 追加 `新一次运行(续跑)` 分隔线，可从界面看到
  "上次跑到镜头 X、本次从哪续"，不再误判卡死；超 512KB 自动截尾。
- **自动续跑已禁用**（`main.go`，用户要求）：异常退出后不自动恢复，由用户
  在工作台手动点「续跑」按需恢复（幂等跳过已完成阶段、检查点收回未收产物）。
- **预编码 singleflight**（`manjuEncSingleflight`）：并发提交同一 `cacheName` 只
  执行一次，后到者等文件 / 失败接力。**禁止改回无缓冲 chan**（发送无接收者永不
  成功 → 全部死等 1900s 超时）。

## 🖥️ WebView2 透明 / 背景色铁律

- `SetTransparent` / `SetBackgroundColor` 内部必须经 `w.Dispatch` 投递到 UI 线程
  执行（WebView2 COM 接口只能在创建它的线程调用）。后台 goroutine / 事件线程
  直调 `PutDefaultBackgroundColor` 会静默失败 → 胶囊圆角外恒黑底、主窗口加载期
  闪色。`third_party/go-webview2/webview.go` 已按此实现，勿回退。
