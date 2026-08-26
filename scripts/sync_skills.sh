#!/usr/bin/env bash
# sync_skills.sh  (定稿 v5.1)
# 把各 agent 私有 skills 汇聚到 ~/.agents/skills（开放标准共享目录）
#   - 边界识别：目录直接含 SKILL.md ⇒ 是一个 skill，不再深入资源子目录
#   - 补丁①：跳过 /plugins/、/marketplaces/ 插件市场
#   - 补丁②：内置守卫 builtin_*/default_*/template*/dl_*/__pycache__ 一律跳过
#   - 补丁③：ROOTS 不含 .codebuddy 这类大市场目录
#   - 补丁④：boundary 下钻跳过 builtin_skills / builtin 目录本身（治本）
#   - 补丁⑤：跳过 node_modules/.git/.cache 依赖目录（匹配目录本身与内层）
#   - 自引用防御：ROOTS 只放 agent 私有目录，绝不包含 .agents(DEST)
#   - 跨 Windows(WSL /mnt/c) 镜像：同步一份到 Windows 侧 .agents/skills
#   - 注意：文件必须用 LF 行尾（CRLF 会让 bash 把 \r 当 token，导致语法错）
set -uo pipefail

DEST="$HOME/.agents/skills"
mkdir -p "$DEST"

MIRROR_TO_WINDOWS=1

# ===== 自动探测 Windows 用户目录（WSL 下 /mnt/c 挂载）=====
WIN_TOP=""
if [ -d "/mnt/c/Users" ]; then
  seed="$(cmd.exe /c 'echo %USERNAME%' 2>/dev/null | tr -d '\r')"
  if [ -n "$seed" ] && [ -d "/mnt/c/Users/$seed" ]; then
    WIN_TOP="/mnt/c/Users/$seed"
  else
    for d in /mnt/c/Users/*/; do
      b="$(basename "$d")"
      case "$b" in Public|Default|Default\ User|All\ Users) continue;; esac
      if [ -d "$d/.claude/skills" ] || [ -d "$d/.agents/skills" ] || [ -d "$d/.codex/skills" ]; then
        WIN_TOP="${d%/}"; break
      fi
    done
  fi
fi
WDEST="${WIN_TOP:+$WIN_TOP/.agents/skills}"

subdirs() { for d in "$1"/*/; do [ -d "${d%/}" ] && echo "${d%/}"; done; }

# 补丁⑤：依赖/元数据目录，永不作为 skill，也不进入
is_dep_dir() {
  case "$1" in
    */node_modules|*/node_modules/*|*/.git|*/.git/*|*/.cache|*/.cache/*) return 0;;
  esac
  return 1
}

# 补丁①：插件/市场目录不是用户技能
is_market_dir() { case "$1" in */plugins/*|*/marketplaces/*) return 0;; esac; return 1; }

# 补丁④：内置技能目录本身就是非用户技能
is_builtin_dir() { case "$(basename "$1")" in builtin_skills|builtin) return 0;; esac; return 1; }

boundary() {
  local d="$1" f
  is_dep_dir "$d" && return 0
  is_market_dir "$d" && return 0
  is_builtin_dir "$d" && return 0
  # 边界：本目录直接含 SKILL.md ⇒ 是一个 skill，处理并停止下钻
  for f in "$d"/*/SKILL.md; do
    [ -f "$f" ] || continue
    process_skill "$f"
    return 0
  done
  # 未命中：下钻，但跳过依赖/市场/内置/标准资源目录
  local child
  for child in $(subdirs "$d"); do
    is_dep_dir "$child" && continue
    is_market_dir "$child" && continue
    is_builtin_dir "$child" && continue
    case "$(basename "$child")" in
      skills|references|assets|components|ui_kits|examples|preview|scripts|templates|__pycache__) continue;;
    esac
    boundary "$child"
  done
}

copied=0; skipped=0
declare -A seen

process_skill() {
  local skillfile="$1" skill name
  skill="$(dirname "$skillfile")"
  name="$(basename "$skill")"
  # 补丁②：内置/模板/下载型目录一律跳过
  case "$name" in
    builtin_*|default_*|template*|dl_*|__pycache__)
      echo "skip(内置/模板): $name"; skipped=$((skipped+1)); return;;
  esac
  if [ -n "${seen[$name]:-}" ]; then
    echo "skip(重复): $name"; skipped=$((skipped+1)); return
  fi
  # WSL 本地用软链；/mnt(Windows盘) 用拷贝（软链跨盘不可靠）
  if [ "$(is_mnt "$skill")" = 0 ]; then
    if ln -s "$skill" "$DEST/$name" 2>/dev/null; then
      echo "link : $name"
    else
      cp -r "$skill" "$DEST/$name"; echo "copy: $name"
    fi
  else
    cp -r "$skill" "$DEST/$name"; echo "copy(/mnt): $name"
  fi
  seen[$name]=1; copied=$((copied+1))
}
is_mnt() { case "$1" in /mnt/*) echo 1;; *) echo 0;; esac; }

# ===== 来源列表（补丁③：只放真实用户技能目录，不含插件市场）=====
ROOTS=(
  "$HOME/.claude"
  "$HOME/.codex"
  "$HOME/.trae-cn"
  "$HOME/.dsh"
  "$HOME/.zcode"
)
if [ -n "$WIN_TOP" ]; then
  ROOTS+=(
    "$WIN_TOP/.claude"
    "$WIN_TOP/.codex"
    "$WIN_TOP/.trae-cn"
  )
fi

for root in "${ROOTS[@]}"; do
  [ -d "$root" ] || continue
  boundary "$root"
done

echo "---"
echo "已汇聚: $copied   因重名/内置/依赖跳过: $skipped"
echo "WSL 汇聚目录: $DEST"

if [ "$MIRROR_TO_WINDOWS" = 1 ] && [ -n "$WDEST" ]; then
  [ -d "$WDEST" ] || mkdir -p "$WDEST"
  # DrvFs 上的符号链接无法被 Windows 原生工具解析(指向 WSL 路径)，
  # 因此先清掉上次留下的坏链，再用 -L 解引用复制真实内容。
  for e in "$WDEST"/*; do
    [ -L "$e" ] && rm -f "$e"
  done
  cp -rL "$DEST"/. "$WDEST"/
  echo "已同步一份到 Windows(解引用): $WDEST"
fi