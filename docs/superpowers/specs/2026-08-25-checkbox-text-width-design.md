# 复选框「允许仅大小写变更」文字截断修复设计

日期：2026-08-25
状态：已批准（方案 A：修正 Walk 库按钮宽度取值）

## 问题

参数配置区的复选框文字「允许仅大小写变更」显示不全，只显示前 6 个字「允许仅大小写」，「变更」被截断。

## 根因

Walk 的网格布局（`gridlayout.go` 的 `PerformLayout`）对**不可水平拉伸**的控件（复选框/按钮的 `LayoutFlags()==0`）取宽度时：

```go
if is, ok := item.(IdealSizer); ok {
    s = is.IdealSize()      // 复选框的 IdealSize 来自 Button.idealSize()
}
...
if lf&GrowableHorz == 0 {
    w = s.Width             // 直接用理想宽度，忽略 geometry.MinSize
}
```

`Button.idealSize()` 用的是 Win32 `BCM_GETIDEALSIZE`：

```go
var s win.SIZE
b.SendMessage(win.BCM_GETIDEALSIZE, 0, uintptr(unsafe.Pointer(&s)))
return maxSize(sizeFromSIZE(s), min)
```

实测该值对 CJK 长文本偏小（本机返回 ≈75px，仅够「允许仅大小写」）。因此：
- 复选框控件实际宽度被布局硬性设为 75px，与 `MinSize: 220` 无关；
- 文字「变更」被裁剪。

## 方案

修正 vendored `lxn/walk` 的 `Button.idealSize()`：使返回的理想宽度不小于布局显式指定的 `MinSize`，从而让 `MinSize` 真正生效、宽度可控。

### 改动 1：`vendor/github.com/lxn/walk/button.go`

`Button.idealSize()` 返回值前追加：

```go
// BCM_GETIDEALSIZE 对 CJK 长文本会返回偏小的宽度，导致复选框/按钮文字被截断。
// 保证理想宽度不小于布局显式指定的 MinSize，使 MinSize 真正生效。
if minWidth := b.MinSizePixels().Width; minWidth > size.Width {
    size.Width = minWidth
}
```

说明：
- 只影响显式设置了 `MinSize` 宽度的按钮；本项目仅「允许仅大小写变更」复选框设置了 `MinSize`，其余按钮不受影响。
- `MinSizePixels()` 来自 `WindowBase`（WindowBase→WidgetBase→Button 链上可访问）。

### 改动 2：`main.go`

保持复选框现有的 `MinSize: decl.Size{Width: 220}`（文本约需 110px，220 留足余量；可按需调整）。

## 验证

1. `go build` / `go vet` / `go test` 通过。
2. 运行后用 `EnumChildWindows` 实测复选框控件宽度 = 220px（不再是 75px）。
3. 截图像素分析 / OCR 确认「允许仅大小写变更」8 个字完整渲染，无截断。
