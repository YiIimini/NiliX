# third_party/go-webview2 本地补丁清单

`third_party/` 是 github.com/jchv/go-webview2 的本地拷贝(go.mod `replace` 指向此处),
在 vendored 上游基础上做必要修复。**升级上游前须先核对本清单,补丁不随上游更新自动保留。**

## 已应用补丁

| # | 文件 | 内容 | 原因 |
|---|---|---|---|
| 1 | `common.go` / `pkg/edge/*` | `SetTransparent()` 真透明实现 | 上游 SetTransparent 在控制器就绪前静默失败/白底,桌面悬浮窗(灵动岛)需要 alpha=0 真透明,见知识库 [[WebView2桌面窗口技术]] |
| 2 | 创建即隐藏 | WebView2 创建时 SW_SHOW → 改为隐藏,导航完成后统一 透明+样式+显示 | 消灭启动白窗一闪(见 [[nilix-webview-white-flash]] 记忆) |
| 3 | 子进程窗口模式 | 支持 `--mainwin` 子进程独立 WebView2 环境 | WebView2 同进程只允许一个 environment(灵动岛已占),桌面主窗口必须独立进程 |

> 注:本目录 go.mod 仍为上游 go 1.16 / 旧 x/sys;升级上游时同步 bump 并重测 灵动岛透明/主窗口尺寸记忆 两条链路。

## 升级流程

1. 重新 clone 上游 → diff 本目录 → 重放上表补丁;
2. 重编译 `-ldflags "-H windowsgui -s -w"`(见 知识库 [[nilix-build-windowsgui-flag]] 记忆);
3. 实测:灵动岛透明置顶、主窗口创建尺寸无跳变、托盘退出链路(FindWindowW)。
