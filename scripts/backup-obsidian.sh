#!/bin/sh
# 把 seltool-go 源码备份到 Obsidian 库。
#
# 用法：
#   sh scripts/backup-obsidian.sh            # 备份一份
#   KEEP=20 sh scripts/backup-obsidian.sh    # 保留最近 20 份（默认 10）
#
# 设计说明：
# - 排除 .git / data / config：config 里有 CookieCloud 密钥和通知配置，不能进笔记库
# - 每次生成 sha256，方便确认备份完整
# - 只保留最近 N 份，避免无限增长
# - 在 iSH 里跑（Obsidian 挂载点在这边），不在 NAS 上

set -eu

SRC=${SRC_DIR:-/var/minis/workspace/seltool-f1}
DEST=${DEST_DIR:-/var/minis/mounts/obsidian/Minis/backups/seltool-go}
KEEP=${KEEP:-10}

[ -d "$SRC" ] || { echo "源目录不存在：$SRC" >&2; exit 1; }
[ -d "$(dirname "$DEST")" ] || { echo "Obsidian 未挂载：$(dirname "$DEST")" >&2; exit 1; }

mkdir -p "$DEST"
STAMP=$(date +%Y%m%d_%H%M)
NAME="seltool-go_${STAMP}.tar.gz"

cd "$SRC"
tar czf "$DEST/$NAME" \
  --exclude='.git' \
  --exclude='data' \
  --exclude='config' \
  --exclude='*.log' \
  --exclude='*.tar' \
  --exclude='*.tar.gz' \
  --exclude='seltool' \
  --exclude='vncproxy' \
  --exclude='__pycache__' \
  . 2>/dev/null

cd "$DEST"
sha256sum "$NAME" > "$NAME.sha256"

# 版本信息，方便日后对照
cat > LATEST.md <<EOF
# seltool-go 最近备份

- 文件：\`$NAME\`
- 时间：$(date '+%Y-%m-%d %H:%M:%S')
- 大小：$(awk "BEGIN{printf \"%.1f KB\", $(wc -c < "$DEST/$NAME")/1024}")
- 提交：$(cd "$SRC" && git log --oneline -1 2>/dev/null || echo '（无 git）')

## 恢复

\`\`\`sh
cd /var/minis/workspace
tar xzf "$DEST/$NAME"
\`\`\`

源码同时托管在 GitHub：\`a454735639-lab/seltool-go\`（master 分支）。
EOF

# 清理旧备份
ls -1t seltool-go_*.tar.gz 2>/dev/null | tail -n +$((KEEP + 1)) | while read -r f; do
  rm -f "$f" "$f.sha256"
  echo "  已清理旧备份：$f"
done

echo "备份完成：$DEST/$NAME（$(awk "BEGIN{printf \"%.1f KB\", $(wc -c < "$DEST/$NAME")/1024}")）"
