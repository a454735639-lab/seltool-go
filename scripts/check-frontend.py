#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""web/index.html 静态校验。

历史教训（三次真实事故，都是「语法看着没问题但结构坏了」）：
  ① 用正则删 .style-picker 时吞掉同行的 .theme-picker 规则
     → 「外观」区块退化成原生 <details>
  ② 删掉 setVisualStyle 函数但调用点还在
     → ReferenceError 阻断 load()，整页 0 个目标
  ③ <details> 被设成 position:fixed
     → 父容器塌陷成 4px，手机上点不到主题选择器

这三类都能在编译前静态发现，所以放在构建流程最前面。

用法：python3 scripts/check-frontend.py [web/index.html]
退出码：0 通过，1 有问题
"""
import re
import sys

path = sys.argv[1] if len(sys.argv) > 1 else 'web/index.html'
try:
    s = open(path, encoding='utf-8').read()
except FileNotFoundError:
    print('  ✗ 找不到 %s' % path)
    sys.exit(1)

errs = []

# ── ① CSS 结构 ──
m = re.search(r'<style>(.*?)</style>', s, re.S)
if not m:
    errs.append('未找到 <style> 块')
else:
    css = m.group(1)
    diff = css.count('{') - css.count('}')
    if diff:
        errs.append('CSS 花括号不平衡：{ 比 } 多 %+d 个' % diff)

    lines = css.split('\n')
    for i in range(len(lines) - 1):
        cur, nxt = lines[i].rstrip(), lines[i + 1].strip()
        # 以逗号结尾、下一行却是属性 = 选择器组被腰斩
        if cur.endswith(',') and re.match(r'^[-a-z]+\s*:', nxt):
            errs.append('悬空选择器（第 %d 行）：%s' % (i + 1, cur.strip()[:60]))

# ── ② getElementById 引用的 id 必须存在 ──
ids = set(re.findall(r'\bid="([^"]+)"', s))
for idx, js in enumerate(re.findall(r'<script[^>]*>(.*?)</script>', s, re.S)):
    refs = set(re.findall(r"getElementById\(['\"]([^'\"]+)['\"]\)", js))
    miss = sorted(refs - ids)
    if miss:
        errs.append('script#%d 引用了不存在的 id：%s' % (idx, ', '.join(miss)))

# ── ③ 调用了但没定义的函数（只查踩过坑的，避免误报） ──
defined = set(re.findall(r'function\s+([A-Za-z_$][\w$]*)\s*\(', s))
defined |= set(re.findall(r'(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:function|\()', s))
for fn in ('setVisualStyle', 'applyVisualStyle', 'setTheme', 'setScheme'):
    if fn not in defined and re.search(r'(?<!function )\b%s\s*\(' % fn, s):
        errs.append('调用了未定义的函数 %s()' % fn)

# ── ④ <details> 不允许 position:fixed（会让父容器塌陷） ──
for cls in ('theme-picker', 'scheme-picker'):
    if re.search(r'\.%s\s*[,{][^}]*position\s*:\s*fixed' % cls, s):
        errs.append('.%s 是 <details>，不能设 position:fixed（父容器会塌陷）' % cls)

# ── 输出 ──
if errs:
    print('\n'.join('  ✗ ' + e for e in errs))
    sys.exit(1)

print('  ✓ CSS 平衡 / 无悬空选择器 / id 引用完整 / 无失效函数调用 / details 定位正常')
