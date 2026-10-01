#!/usr/bin/env bash
# 命名检查：本项目有自己的命名体系，这里拦截一批不允许出现的标识符。
#
# 词表在 .local/banned-words.txt（私有文件，不进 git）：每行一个扩展正则，"#" 开头是注释。
# 词表不存在时跳过检查（开源仓库的克隆里没有这个文件）。
#
# 检查范围是整个公开树：所有文本文件的内容、所有文件名，以及 git 历史里的每一条提交信息
# （提交信息推上去也是公开材料）。只跳过 .git、.local、依赖缓存、构建产物（含测试生成的 coverage.out）和锁文件
# （锁文件里的哈希是随机串，只会误报）。
set -euo pipefail
cd "$(dirname "$0")/.."

LIST=${BANNED_WORDS_FILE:-.local/banned-words.txt} # 自测时可以换一份词表（scripts/test-gates.sh）
if [ ! -f "$LIST" ]; then
  echo "banned-words: 跳过（没有 $LIST）"
  exit 0
fi

PATTERN=$(grep -vE '^\s*(#|$)' "$LIST" | paste -sd '|' -)
if [ -z "$PATTERN" ]; then
  echo "banned-words: 跳过（词表为空）"
  exit 0
fi

fail=0

# grep 的退出码：0 命中、1 没命中、其他是出错（词表里的正则写错了、文件读不了）。出错不能当成"没命中"（D-055）
hits() {
  local rc=0
  "$@" || rc=$?
  case $rc in
    0) return 0 ;;
    1) return 1 ;;
    *) echo "banned-words: 检查命令出错（退出码 $rc）：$*" >&2; exit 2 ;;
  esac
}

# 1. 文件内容：整个公开树，所有文本文件
if hits grep -rnE --binary-files=without-match \
    --exclude-dir=.git --exclude-dir=.local --exclude-dir=node_modules --exclude-dir=dist --exclude-dir=bin \
    --exclude-dir=_to_delete --exclude-dir=test-results --exclude-dir=playwright-report --exclude-dir=coverage \
    --exclude=pnpm-lock.yaml --exclude=package-lock.json --exclude=yarn.lock --exclude=go.sum --exclude=coverage.out \
    "$PATTERN" . ; then
  echo "存在命中词表的文件内容"; fail=1
fi

# 2. 文件名：先完整列出来（find 出错时这里就失败退出），再匹配
files=$(find . \( -path ./.git -o -path ./.local -o -path '*/node_modules' -o -path '*/dist' -o -path ./_to_delete \) -prune -o -type f -print)
if hits grep -E "$PATTERN" <<<"$files" ; then
  echo "存在命中词表的文件名"; fail=1
fi

# 3. 提交信息（有 git 历史时）：同样先完整取出来
if git rev-parse --git-dir >/dev/null 2>&1; then
  msgs=$(git log --all --format='%H %s%n%b')
  if hits grep -nE "$PATTERN" <<<"$msgs" ; then
    echo "存在命中词表的提交信息（用 git rebase / git commit --amend 改写后再推）"; fail=1
  fi
fi

if [ "$fail" -ne 0 ]; then
  echo "banned-words: FAILED（命中了词表）"; exit 1
fi
echo "banned-words: OK"
