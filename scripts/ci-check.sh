#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT_DIR"

# 迁移版本必须唯一，避免不同文件争用同一执行顺序。
versions=$(find migrations -name '*.up.sql' -exec basename {} .up.sql \; | cut -d_ -f1 | sort)
duplicates=$(printf '%s\n' "$versions" | uniq -d)
if [ -n "$duplicates" ]; then
  echo "duplicate migration versions: $duplicates" >&2
  exit 1
fi

# 在进入测试前只扫描 Git 跟踪文件，既拦截误提交，也避免读取并回显本地忽略的 .env。
if git grep -q -E 'sk-[A-Za-z0-9_-]{20,}' -- .; then
  echo 'possible API key committed to repository' >&2
  exit 1
fi

# Go 格式差异视为 CI 失败，并输出需要修复的文件。
test "$(gofmt -l cmd internal test | wc -l | tr -d ' ')" = "0" || {
  echo 'Go files are not formatted' >&2
  gofmt -l cmd internal test >&2
  exit 1
}
