#!/bin/sh
# 生成后端二进制的第三方许可证清单（D-028）。
#
# 收录实际编译进 server 二进制的每个 Go 模块（go list -deps，不含测试依赖），附上它的 LICENSE / NOTICE /
# PATENTS 等文件原文，再加上 Go 标准库。MIT、BSD、Apache-2.0 等许可证要求分发二进制时附带版权和许可声明。
# 某个模块找不到许可证文件时报错退出：新引入的依赖必须先弄清许可证。
#
# 用法：go-licenses.sh <Go 模块目录> <输出文件> [入口包 ...]；默认入口为 .
# 依赖集合随构建环境变化，调用方应使用与正式构建相同的 CGO_ENABLED / GOOS / GOARCH。
# 用 POSIX sh 写，Alpine 构建镜像里也能跑。
set -eu

[ $# -ge 2 ] || { echo "usage: $0 <go-module-dir> <output-file> [packages ...]" >&2; exit 2; }
out_dir=$(cd "$(dirname "$2")" && pwd)
out="$out_dir/$(basename "$2")"
cd "$1"
shift 2
[ $# -gt 0 ] || set -- .

tmp=$(mktemp)
trap 'rm -f "$tmp" "$tmp.raw" "$tmp.list"' EXIT

# 路径|版本|目录；有 replace 时用替换后的目录。先写进文件再排序：管道里 go list 失败时退出码会被 sort 盖掉，
# 只剩一份标准库清单却报成功（D-055）
go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}|{{.Version}}|{{with .Replace}}{{.Dir}}{{else}}{{.Dir}}{{end}}{{end}}{{end}}' "$@" >"$tmp.raw"
sort -u "$tmp.raw" >"$tmp.list"

# 模块根目录里的许可证类文件（许可证正文，加上 NOTICE、PATENTS 这类附带说明，一起收录）
notice_files() {
  ls -1 "$1" 2>/dev/null | grep -iE '^(licen[cs]e|copying|unlicense|notice|patents|third[-_]party[-_]notices?)([._-].*)?$' || true
}

# 许可证正文：只有 NOTICE、PATENTS 不算有许可证（D-055）
license_files() {
  ls -1 "$1" 2>/dev/null | grep -iE '^(licen[cs]e|copying|unlicense)([._-].*)?$' || true
}

missing=0
{
  echo 'Third-party software included in the server binary'
  echo '===================================================='
  echo
  echo "- Go standard library $(go env GOVERSION)"
  while IFS='|' read -r path version dir; do
    [ -n "$path" ] && echo "- $path $version"
  done <"$tmp.list"
  echo

  goroot=$(go env GOROOT)
  printf '%072d\n' 0 | tr 0 -
  echo "Go standard library $(go env GOVERSION)"
  printf '%072d\n' 0 | tr 0 -
  for f in LICENSE PATENTS; do
    if [ -f "$goroot/$f" ]; then
      echo "[$f]"
      cat "$goroot/$f"
      echo
    fi
  done
  echo

  while IFS='|' read -r path version dir; do
    [ -n "$path" ] || continue
    printf '%072d\n' 0 | tr 0 -
    echo "$path $version"
    printf '%072d\n' 0 | tr 0 -
    files=$(notice_files "$dir")
    if [ -z "$(license_files "$dir")" ]; then
      echo "go-licenses: no license file in $path $version ($dir)" >&2
      missing=1
      continue
    fi
    for f in $files; do
      echo "[$f]"
      cat "$dir/$f"
      echo
    done
    echo
  done <"$tmp.list"
} >"$tmp"

# 上面的 { } 和 while 都是重定向而不是管道，不在子 shell 里，missing 的值能带出来
if [ "$missing" -ne 0 ]; then
  echo "go-licenses: some modules have no license file; check their licence before depending on them" >&2
  exit 1
fi
chmod 644 "$tmp"
mv "$tmp" "$out"
echo "go-licenses: wrote $out ($(grep -c '^- ' "$out") components)"
