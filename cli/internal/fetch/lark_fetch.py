#!/usr/bin/env python3
"""prd_fetch — 飞书 Wiki PRD 全量拉取（含画板 whiteboard 提取）。

核心用途：把飞书 PRD 的完整内容（包括画板里的流程图文本节点）下载到本地，
清洗成标准 Markdown，供 agent/LLM 阅读。避免因看不到画板内容而产出错误 plan。

用法：
  python3 prd_fetch.py [URL]

产物（默认 $TMPDIR/prd-fetch/，可用 --output-dir 覆盖）：
  <slug>-original.json     lark-cli 原始响应
  <slug>-clean.md          清洗后的标准 Markdown（含正文图片和画板预览/拓扑）
  <slug>-media/            正文图片及媒体索引
  <slug>-whiteboards/      画板产物目录
    index.json             画板元数据索引
    <token>-preview.*      画板原始缩略图
    <token>-topology.md    可读的节点和连线关系
"""

from __future__ import annotations

import csv
import html
import io
import json
import math
import os
import re
import subprocess
import sys
import urllib.parse
from html.parser import HTMLParser
from pathlib import Path

# ---------------------------------------------------------------------------
# 配置区
# ---------------------------------------------------------------------------

# 分步执行：step1=拉取原始 JSON，step2=清洗 Markdown + 提取画板。
# 知识提炼由当前任务 Agent 在完整上下文内完成，本脚本不调用模型、不写正式 owner。

# 输出根目录
OUTPUT_ROOT = Path(os.environ.get('TMPDIR', '/tmp')) / 'prd-fetch'

# lark-cli 路径
LARK_CLI = 'lark-cli'

# 默认域名（运行时自动从输入 URL 派生）
BASE_ORIGIN = 'https://feishu.cn'

# ---------------------------------------------------------------------------
# Emoji 映射（callout emoji 名字 → 字符）
# ---------------------------------------------------------------------------

_EMOJI_MAP = {
    'bulb': '💡', 'light_bulb': '💡', 'idea': '💡', 'tip': '💡',
    'warning': '⚠️', 'warn': '⚠️', 'alert': '⚠️',
    'info': 'ℹ️', 'information_source': 'ℹ️',
    'check': '✅', 'white_check_mark': '✅', 'ok': '✅',
    'cross': '❌', 'x': '❌', 'fail': '❌',
    'question': '❓', 'help': '❓',
    'star': '⭐', 'star2': '🌟',
    'fire': '🔥', 'hot': '🔥',
    'rocket': '🚀',
    'beer': '🍺', 'beers': '🍻',
    'coffee': '☕',
    'book': '📖', 'bookmark': '🔖',
    'memo': '📝', 'pencil': '✏️',
    'wrench': '🔧', 'hammer': '🔨',
    'bug': '🐛',
    'construction': '🚧',
    'lock': '🔒', 'unlock': '🔓',
    'exclamation': '❗', 'bangbang': '‼️',
    'speech_balloon': '💬', 'thought_balloon': '💭',
    'tada': '🎉', 'sparkles': '✨',
}

# ---------------------------------------------------------------------------
# 工具函数
# ---------------------------------------------------------------------------


def _status(message: str = '') -> None:
    print(message, file=sys.stderr)


def _slug_from_url(url: str) -> str:
    """从 URL 提取文档 token 作为 slug。"""
    parsed = urllib.parse.urlparse(url)
    path = parsed.path.rstrip('/')
    # /wiki/<token> 或 /docx/<token>
    parts = path.split('/')
    for i, p in enumerate(parts):
        if p in ('wiki', 'docx', 'docs') and i + 1 < len(parts):
            token = parts[i + 1]
            # 去掉 query 参数可能混入的部分
            token = token.split('?')[0]
            return token
    return parts[-1] if parts else 'prd'


def _run_lark_cli(args: list[str], timeout: int = 60) -> dict:
    """运行 lark-cli 并返回解析后的 JSON。"""
    try:
        result = subprocess.run(
            [LARK_CLI] + args,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except FileNotFoundError:
        sys.exit(f"Error: {LARK_CLI!r} not found. Please install lark-cli.")
    except subprocess.TimeoutExpired:
        sys.exit(f"Error: lark-cli timed out after {timeout}s.")

    response = _parse_cli_json(result.stdout) or _parse_cli_json(result.stderr)
    if response:
        return response
    sys.exit(
        "Error: lark-cli produced no JSON output.\n"
        f"stderr: {result.stderr.strip() or '<empty>'}"
    )


def _parse_cli_json(stdout: str) -> dict:
    """解析 lark-cli 输出，兼容 JSON 前的状态行。"""
    lines = stdout.strip().splitlines()
    for index, line in enumerate(lines):
        if line.lstrip().startswith('{'):
            try:
                return json.loads('\n'.join(lines[index:]))
            except json.JSONDecodeError:
                break
    return {}


def _run_lark_command(
    args: list[str],
    cwd: Path | None = None,
    timeout: int = 60,
) -> dict:
    """运行只读 lark-cli 命令；失败时返回错误信封，不终止整个抓取。"""
    try:
        result = subprocess.run(
            [LARK_CLI] + args,
            cwd=cwd,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired) as error:
        return {'ok': False, 'error': {'message': str(error)}}
    response = _parse_cli_json(result.stdout)
    if response:
        return response
    return {
        'ok': False,
        'error': {'message': result.stderr.strip() or 'lark-cli returned no JSON'},
    }


def _download_media(
    token: str,
    output_dir: Path,
    filename: str,
    media_type: str = 'media',
) -> Path | None:
    """下载文档媒体或画板缩略图，返回实际文件路径。"""
    output_dir.mkdir(parents=True, exist_ok=True)
    command = [
        LARK_CLI, 'docs', '+media-download',
        '--token', token,
        '--type', media_type,
        '--output', f'./{filename}',
        '--overwrite',
        '--format', 'json',
    ]

    def run(args: list[str]) -> tuple[subprocess.CompletedProcess, dict]:
        result = subprocess.run(
            args,
            cwd=output_dir,
            capture_output=True,
            text=True,
            timeout=60,
        )
        return result, _parse_cli_json(result.stdout)

    try:
        result, response = run(command)
        if not response.get('ok') and media_type == 'media':
            preview_command = command.copy()
            preview_command[2] = '+media-preview'
            type_index = preview_command.index('--type')
            del preview_command[type_index:type_index + 2]
            result, response = run(preview_command)
    except (FileNotFoundError, subprocess.TimeoutExpired) as error:
        _status(f"  [media] Download failed for {token}: {error}")
        return None

    if not response.get('ok'):
        message = (
            response.get('error', {}).get('message')
            or result.stderr.strip()
            or 'unknown error'
        )
        _status(f"  [media] Download failed for {token}: {message}")
        return None

    saved_path = response.get('data', {}).get('saved_path', '')
    if saved_path:
        path = Path(saved_path)
        if not path.is_absolute():
            path = output_dir / path
        if path.is_file():
            return path.resolve()

    candidates = sorted(output_dir.glob(f'{filename}*'))
    return candidates[0].resolve() if candidates else None


def _column_name(column_count: int) -> str:
    """把 1-based 列号转换为 A1 列名。"""
    if column_count < 1:
        return 'A'
    letters = []
    value = column_count
    while value:
        value, remainder = divmod(value - 1, 26)
        letters.append(chr(ord('A') + remainder))
    return ''.join(reversed(letters))


def _csv_to_markdown(csv_content: str) -> str:
    rows = list(csv.reader(io.StringIO(csv_content)))
    if not rows:
        return ''
    width = max(len(row) for row in rows)

    def escape(value: str) -> str:
        return value.strip().replace('\n', '<br>').replace('|', '\\|')

    normalized = [
        [escape(value) for value in row] + [''] * (width - len(row))
        for row in rows
    ]
    lines = [
        '| ' + ' | '.join(normalized[0]) + ' |',
        '| ' + ' | '.join(['---'] * width) + ' |',
    ]
    lines.extend('| ' + ' | '.join(row) + ' |' for row in normalized[1:])
    return '\n'.join(lines) + '\n'


def _extract_text_from_node(node: dict) -> str:
    """从画板节点提取文本内容。"""
    text_field = node.get('text')
    if text_field:
        if isinstance(text_field, str):
            return text_field.strip()
        if isinstance(text_field, dict):
            return text_field.get('text', '').strip()
    sticky_note = node.get('sticky_note', {})
    if isinstance(sticky_note, dict):
        sticky_text = sticky_note.get('text', {})
        if isinstance(sticky_text, dict):
            return sticky_text.get('text', '').strip()
    return ''


def _extract_connector_caption(node: dict) -> str:
    captions = node.get('connector', {}).get('captions', {}).get('data', [])
    return '\n'.join(
        caption.get('text', '').strip()
        for caption in captions
        if caption.get('text', '').strip()
    )


def _node_center(node: dict) -> tuple[float, float]:
    return (
        float(node.get('x', 0)) + float(node.get('width', 0)) / 2,
        float(node.get('y', 0)) + float(node.get('height', 0)) / 2,
    )


def _contains(container: dict, child: dict) -> bool:
    child_x, child_y = _node_center(child)
    return (
        float(container.get('x', 0)) <= child_x <=
        float(container.get('x', 0)) + float(container.get('width', 0))
        and float(container.get('y', 0)) <= child_y <=
        float(container.get('y', 0)) + float(container.get('height', 0))
    )


def _nearest_node_id(
    position: dict | None,
    nodes: list[dict],
    excluded_id: str = '',
) -> str:
    if not position:
        return ''
    point_x = float(position.get('x', 0))
    point_y = float(position.get('y', 0))
    best_id = ''
    best_distance = math.inf
    for node in nodes:
        if node.get('id') == excluded_id:
            continue
        center_x, center_y = _node_center(node)
        distance = math.hypot(center_x - point_x, center_y - point_y)
        if distance < best_distance:
            best_id = node.get('id', '')
            best_distance = distance
    return best_id


def _normalize_whiteboard(nodes: list[dict]) -> dict:
    """把画板节点归一化为可读节点和带方向的连线关系。"""
    connectors = [node for node in nodes if node.get('type') == 'connector']
    shapes = [node for node in nodes if node.get('type') != 'connector']
    text_shapes = [
        node for node in shapes
        if _extract_text_from_node(node)
        and node.get('type') in ('text_shape', 'sticky_note')
    ]

    normalized_nodes = []
    for node in shapes:
        text = _extract_text_from_node(node)
        if not text:
            contained = [
                candidate for candidate in text_shapes
                if candidate.get('id') != node.get('id') and _contains(node, candidate)
            ]
            contained.sort(key=lambda item: (item.get('y', 0), item.get('x', 0)))
            text = '\n'.join(_extract_text_from_node(item) for item in contained)
        normalized_nodes.append({
            'id': node.get('id', ''),
            'type': node.get('type', ''),
            'text': text,
            'x': node.get('x', 0),
            'y': node.get('y', 0),
            'width': node.get('width', 0),
            'height': node.get('height', 0),
            'parent_id': node.get('parent_id', ''),
        })

    by_id = {node['id']: node for node in normalized_nodes if node['id']}
    labeled_nodes = [node for node in normalized_nodes if node['text']]
    relations = []
    for connector_node in connectors:
        connector = connector_node.get('connector', {})
        start = connector.get('start_object', {}).get('id', '')
        if not start:
            start = connector.get('start', {}).get('attached_object', {}).get('id', '')
        end = connector.get('end_object', {}).get('id', '')
        if not end:
            end = connector.get('end', {}).get('attached_object', {}).get('id', '')

        start_position = connector.get('start', {}).get('position')
        end_position = connector.get('end', {}).get('position')
        if not start:
            start = _nearest_node_id(start_position, labeled_nodes)
        if not end:
            end = _nearest_node_id(end_position, labeled_nodes, excluded_id=start)

        end_arrow = connector.get('end', {}).get('arrow_style', 'none')
        start_arrow = connector.get('start', {}).get('arrow_style', 'none')
        relations.append({
            'id': connector_node.get('id', ''),
            'source': start,
            'source_text': by_id.get(start, {}).get('text', ''),
            'target': end,
            'target_text': by_id.get(end, {}).get('text', ''),
            'label': _extract_connector_caption(connector_node),
            'directed': end_arrow != 'none' or start_arrow != 'none',
            'start_arrow': start_arrow,
            'end_arrow': end_arrow,
        })

    return {
        'node_count': len(nodes),
        'shape_count': len(shapes),
        'connector_count': len(connectors),
        'nodes': normalized_nodes,
        'relations': relations,
    }


def _single_line(text: str) -> str:
    return re.sub(r'\s+', ' ', text).strip()


def _whiteboard_markdown(topology: dict) -> str:
    """生成同时保留节点内容和连线语义的可读 Markdown。"""
    lines = ['## 连线关系']
    for relation in topology['relations']:
        source = _single_line(relation['source_text']) or relation['source'] or '未识别起点'
        target = _single_line(relation['target_text']) or relation['target'] or '未识别终点'
        arrow = '→' if relation['directed'] else '—'
        label = _single_line(relation['label'])
        suffix = f'：{label}' if label else ''
        lines.append(f"- {source} {arrow} {target}{suffix}")
    if not topology['relations']:
        lines.append('- 无连线节点')

    lines.extend(['', '## 节点'])
    labeled_nodes = [node for node in topology['nodes'] if node['text']]
    labeled_nodes.sort(key=lambda node: (node['y'], node['x']))
    for node in labeled_nodes:
        lines.append(f"- `{node['id']}` {_single_line(node['text'])}")
    return '\n'.join(lines).strip() + '\n'


def _merge_markdown_page(existing: str, page: str) -> tuple[str, int]:
    """拼接分页内容，并去掉 API 在分页边界重复返回的前缀。"""
    max_overlap = min(len(existing), len(page))
    overlap = 0
    for size in range(max_overlap, 0, -1):
        if existing.endswith(page[:size]):
            overlap = size
            break
    suffix = page[overlap:]
    return existing + suffix, len(suffix)


_MARKDOWN_LINK_RE = re.compile(r'(\]\()([^)]+)(\))')


def _decode_markdown_urls(markdown: str) -> str:
    """解码飞书导出中整段 percent-encoded 的 Markdown 链接。"""
    def replace(match: re.Match) -> str:
        target = match.group(2)
        decoded = urllib.parse.unquote(target)
        return f"{match.group(1)}{decoded}{match.group(3)}"

    return _MARKDOWN_LINK_RE.sub(replace, markdown)


# ---------------------------------------------------------------------------
# Step 1: 拉取 PRD 原始内容
# ---------------------------------------------------------------------------


def step1_fetch(url: str) -> dict:
    """调用 lark-cli docs +fetch 拉取 PRD 原始 JSON。"""
    _status(f"[step1] Fetching: {url}")

    data = _run_lark_cli(['docs', '+fetch', '--doc', url, '--format', 'json'])

    if not data.get('ok'):
        sys.exit(f"Error: lark-cli fetch failed: {data.get('msg', data)}")

    payload = data.get('data', {})
    title = payload.get('title', 'untitled')
    doc_id = payload.get('doc_id', '')
    markdown = payload.get('markdown', '')
    total_len = payload.get('total_length') or 0

    _status(f"[step1] Title: {title}")
    _status(f"[step1] Doc ID: {doc_id}")
    _status(f"[step1] Markdown: {len(markdown)} chars (total_length={total_len})")

    # 回退：markdown 为空但 document.content (HTML) 非空时，使用 HTML 内容作为清洗输入。
    # 部分文档 lark-cli 返回内容在 document.content 而非 markdown 字段，只读 markdown 会得到 0 字符。
    markdown_source = 'markdown'
    if not markdown.strip():
        document_content = payload.get('document', {}).get('content', '')
        if document_content:
            markdown_source = 'document.content'
            markdown = document_content
            _status(f"[step1] markdown empty, falling back to document.content: "
                    f"{len(markdown)} chars HTML")

    # docs +fetch owns full-document retrieval.  Keep total_length only as
    # evidence of an upstream mismatch; do not invent unsupported pagination
    # flags in this conversion layer.
    if markdown_source == 'markdown' and total_len and len(markdown) < total_len:
        raise RuntimeError(
            "lark-cli docs +fetch returned incomplete markdown: "
            f"received={len(markdown)} total_length={total_len}"
        )

    # 更新 payload
    payload['markdown'] = markdown
    payload['_markdown_source'] = markdown_source
    document = payload.get('document', {})
    if not payload.get('title'):
        title_match = re.search(r'<title[^>]*>(.*?)</title>', markdown, flags=re.DOTALL)
        payload['title'] = (
            html.unescape(title_match.group(1)).strip()
            if title_match
            else document.get('title', 'Untitled')
        )
    if not payload.get('doc_id'):
        payload['doc_id'] = document.get('document_id', '')
    if not payload.get('total_length'):
        payload['total_length'] = len(markdown)

    slug = _slug_from_url(url)
    out_dir = OUTPUT_ROOT
    out_dir.mkdir(parents=True, exist_ok=True)
    out_path = out_dir / f"{slug}-original.json"
    out_path.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding='utf-8')
    _status(f"[step1] Saved: {out_path}")

    return payload


# ---------------------------------------------------------------------------
# Step 2: 清洗 Markdown + 提取画板
# ---------------------------------------------------------------------------


class LarkMarkdownParser(HTMLParser):
    """飞书混合标签解析器：把 Markdown + 飞书自定义标签转为纯 Markdown。

    支持的标签：
    - <lark-table>/<lark-tr>/<lark-td> → Markdown 表格（含 rowspan/colspan）
    - <callout> → 引用块 + emoji
    - <whiteboard token="..."/> → 画板文本提取（通过回调）
    - <mention-doc token="..." type="...">text</mention-doc> → Markdown 链接
    - <mention-user id="..."/> → 移除
    - <text color="..." bgcolor="...">text</text> → 保留文字，去掉样式
    - <image token="..." .../> → 下载为本地图片链接，失败时保留 token/alt
    """

    def __init__(
        self,
        whiteboard_extractor=None,
        media_extractor=None,
        sheet_extractor=None,
        synced_reference_extractor=None,
    ):
        super().__init__(convert_charrefs=True)
        self._parts: list[str] = []
        self._stack: list[dict] = [{'type': 'root', 'parts': self._parts}]
        self._whiteboard_extractor = whiteboard_extractor
        self._media_extractor = media_extractor
        self._sheet_extractor = sheet_extractor
        self._synced_reference_extractor = synced_reference_extractor
        # HTML 回退支持：列表上下文（document.content 为 HTML 时使用）
        self._list_stack: list[dict] = []  # {'ordered': bool, 'counter': int}

    def _top(self) -> dict:
        return self._stack[-1]

    _STRUCTURAL_TYPES = {'table', 'row'}  # 这些上下文不直接收集文本

    def _append_text(self, text: str):
        top = self._top()
        top_type = top.get('type')
        if top_type in self._STRUCTURAL_TYPES:
            # 在 table/row 结构层：丢弃纯空白；非空白说明标签嵌套异常，也丢弃（避免污染输出）
            return
        if 'parts' in top:
            top['parts'].append(text)
        else:
            self._parts.append(text)

    # ---- 表格 ----

    def _render_table(self, rows: list) -> str:
        """rows: list of list of {text, rowspan, colspan}"""
        if not rows:
            return '\n'

        # 计算最大列数
        max_cols = 0
        for row in rows:
            col_count = sum(cell.get('colspan', 1) for cell in row)
            max_cols = max(max_cols, col_count)

        if max_cols == 0:
            return '\n'

        # 展开 rowspan/colspan 到网格
        grid: list[list[str]] = []
        for r_idx, row in enumerate(rows):
            while len(grid) <= r_idx:
                grid.append([''] * max_cols)
            c_idx = 0
            for cell in row:
                # 跳过已被 rowspan 占据的位置
                while c_idx < max_cols and grid[r_idx][c_idx] != '':
                    c_idx += 1
                if c_idx >= max_cols:
                    break
                text = cell.get('text', '').strip()
                rs = cell.get('rowspan', 1)
                cs = cell.get('colspan', 1)
                # 填充
                for dr in range(rs):
                    for dc in range(cs):
                        nr, nc = r_idx + dr, c_idx + dc
                        while len(grid) <= nr:
                            grid.append([''] * max_cols)
                        if nc < max_cols and grid[nr][nc] == '':
                            grid[nr][nc] = text if dr == 0 and dc == 0 else ''
                c_idx += cs

        # 确保所有行等长
        for row in grid:
            while len(row) < max_cols:
                row.append('')

        def _esc(s: str) -> str:
            # 单元格内多行用 <br>；去掉行首缩进空白
            lines = [ln.strip() for ln in s.split('\n')]
            lines = [ln for ln in lines if ln]
            text = '<br>'.join(lines)
            return text.replace('|', '\\|')

        out_lines = []
        # 表头（第一行）
        header = [_esc(c) for c in grid[0]]
        out_lines.append('| ' + ' | '.join(header) + ' |')
        out_lines.append('| ' + ' | '.join(['---'] * max_cols) + ' |')
        # 数据行
        for row in grid[1:]:
            cells = [_esc(c) for c in row]
            out_lines.append('| ' + ' | '.join(cells) + ' |')

        return '\n' + '\n'.join(out_lines) + '\n\n'

    # ---- callout ----

    def _render_callout(self, attrs: dict, content: str) -> str:
        emoji_val = attrs.get('emoji') or attrs.get('emoji-icon') or ''
        emoji = _EMOJI_MAP.get(emoji_val, emoji_val) or '💡'
        text = content.strip()
        if not text:
            return f'\n> {emoji} **提示**\n\n'

        lines = text.split('\n')
        prefixed = []
        for line in lines:
            stripped = line.rstrip()
            if stripped == '':
                prefixed.append('>')
            else:
                prefixed.append('> ' + stripped)
        return f'\n> {emoji} **提示**\n> \n' + '\n'.join(prefixed) + '\n\n'

    # ---- mention-doc ----

    def _render_mention_doc(self, attrs: dict, content: str) -> str:
        token = attrs.get('token', '')
        doc_type = attrs.get('type', 'wiki')
        text = content.strip() or token
        if doc_type == 'docx':
            url = f'{BASE_ORIGIN}/docx/{token}'
        else:
            url = f'{BASE_ORIGIN}/wiki/{token}'
        url = urllib.parse.unquote(url)
        return f'[{text}]({url})'

    def _render_cite(self, attrs: dict) -> str:
        if attrs.get('type') == 'user':
            return ''
        if attrs.get('type') != 'doc':
            return attrs.get('title', '')
        token = attrs.get('doc-id') or attrs.get('token', '')
        doc_type = attrs.get('file-type', 'wiki')
        title = attrs.get('title') or token or '关联文档'
        if doc_type in ('wiki', 'docx') and token:
            return f'[{title}]({BASE_ORIGIN}/{doc_type}/{token})'
        return f'{title}（{doc_type}: {token}）'

    def _render_image(self, attrs: dict) -> str:
        token = attrs.get('src') or attrs.get('token', '')
        alt = attrs.get('alt') or attrs.get('name') or '文档图片'
        if self._media_extractor and token:
            relative_path = self._media_extractor(token, attrs)
            if relative_path:
                return f'\n\n![{alt}]({relative_path})\n\n'
        suffix = f'：{alt}' if alt else ''
        return f'\n\n> 🖼️ **文档图片**（token: `{token}`）{suffix}\n\n'

    def _render_resource(self, label: str, attrs: dict) -> str:
        properties = ', '.join(
            f'{key}=`{value}`'
            for key, value in attrs.items()
            if value
        )
        return f'\n\n> 📎 **{label}**：{properties or "未提供资源标识"}\n\n'

    # ---- whiteboard（自闭合，通过外部提取器获取内容）----

    def _render_whiteboard(self, token: str) -> str:
        if not token:
            return '\n> [画板：空 token]\n\n'
        if self._whiteboard_extractor:
            text_content = self._whiteboard_extractor(token)
        else:
            text_content = ''

        if text_content:
            return f'\n\n{text_content.strip()}\n\n'
        else:
            return f'\n> 📋 **画板** (token: {token}) — 提取失败或无文本内容\n\n'

    # ---- 解析器事件 ----

    def handle_data(self, data: str):
        self._append_text(data)

    def handle_starttag(self, tag: str, attrs: list):
        attrs_d = dict(attrs)
        tag = tag.lower()

        if tag in ('lark-table', 'table'):
            self._stack.append({'type': 'table', 'rows': []})
        elif tag == 'thead' or tag == 'tbody' or tag == 'colgroup':
            pass
        elif tag == 'col':
            pass
        elif tag in ('lark-tr', 'tr'):
            if self._top()['type'] == 'table':
                self._stack.append({'type': 'row', 'cells': []})
        elif tag in ('lark-td', 'td', 'th'):
            if self._top()['type'] == 'row':
                rowspan = int(attrs_d.get('rowspan', 1))
                colspan = int(attrs_d.get('colspan', 1))
                self._stack.append({
                    'type': 'cell', 'parts': [],
                    'rowspan': rowspan, 'colspan': colspan,
                })
        elif tag == 'callout':
            self._stack.append({'type': 'callout', 'attrs': attrs_d, 'parts': []})
        elif tag == 'mention-doc':
            self._stack.append({'type': 'mention-doc', 'attrs': attrs_d, 'parts': []})
        elif tag == 'text':
            self._stack.append({'type': 'text', 'parts': []})
        elif tag == 'whiteboard':
            token = attrs_d.get('token', '')
            rendered = self._render_whiteboard(token)
            self._append_text(rendered)
        elif tag == 'cite':
            self._append_text(self._render_cite(attrs_d))
        elif tag in ('image', 'img'):
            self._append_text(self._render_image(attrs_d))
        elif tag == 'sheet':
            if self._sheet_extractor:
                self._append_text(self._sheet_extractor(attrs_d))
            else:
                self._append_text(self._render_resource('嵌入电子表格', attrs_d))
        elif tag == 'bitable':
            self._append_text(self._render_resource('嵌入多维表格', attrs_d))
        elif tag == 'synced_reference':
            if self._synced_reference_extractor:
                self._append_text(self._synced_reference_extractor(attrs_d))
            else:
                self._append_text(self._render_resource('同步引用块', attrs_d))
        elif tag == 'mention-user':
            pass
        elif tag == 'title':
            self._stack.append({'type': 'discard', 'parts': []})
        # ---- 基础 HTML 标签（document.content 回退时使用）----
        elif tag in ('strong', 'b'):
            self._append_text('**')
        elif tag in ('em', 'i'):
            self._append_text('*')
        elif tag == 'code':
            self._append_text('`')
        elif tag == 'br':
            self._append_text('\n')
        elif tag == 'p':
            self._append_text('\n\n')
        elif tag == 'blockquote':
            self._stack.append({'type': 'blockquote', 'parts': []})
        elif tag == 'pre':
            self._append_text('\n\n```\n')
        elif tag in ('h1', 'h2', 'h3', 'h4', 'h5', 'h6'):
            level = int(tag[1])
            self._append_text('\n\n' + '#' * level + ' ')
        elif tag == 'a':
            self._stack.append({'type': 'link', 'attrs': attrs_d, 'parts': []})
        elif tag == 'ul':
            self._list_stack.append({'ordered': False, 'counter': 0})
            self._append_text('\n')
        elif tag == 'ol':
            self._list_stack.append({'ordered': True, 'counter': 0})
            self._append_text('\n')
        elif tag == 'li':
            if self._list_stack:
                ctx = self._list_stack[-1]
                ctx['counter'] += 1
                indent = '  ' * (len(self._list_stack) - 1)
                if ctx['ordered']:
                    self._append_text(f"\n{indent}{ctx['counter']}. ")
                else:
                    self._append_text(f'\n{indent}- ')
            else:
                self._append_text('\n- ')

    def handle_endtag(self, tag: str):
        tag = tag.lower()

        if tag in ('lark-table', 'table'):
            if self._top()['type'] == 'table':
                ctx = self._stack.pop()
                self._append_text(self._render_table(ctx['rows']))
        elif tag in ('lark-tr', 'tr'):
            if self._top()['type'] == 'row':
                row_ctx = self._stack.pop()
                if self._top()['type'] == 'table':
                    self._top()['rows'].append(row_ctx['cells'])
        elif tag in ('lark-td', 'td', 'th'):
            if self._top()['type'] == 'cell':
                cell_ctx = self._stack.pop()
                cell_text = ''.join(cell_ctx['parts']).strip()
                if self._top()['type'] == 'row':
                    self._top()['cells'].append({
                        'text': cell_text,
                        'rowspan': cell_ctx.get('rowspan', 1),
                        'colspan': cell_ctx.get('colspan', 1),
                    })
        elif tag == 'callout':
            if self._top()['type'] == 'callout':
                ctx = self._stack.pop()
                content = ''.join(ctx['parts'])
                self._append_text(self._render_callout(ctx['attrs'], content))
        elif tag == 'mention-doc':
            if self._top()['type'] == 'mention-doc':
                ctx = self._stack.pop()
                content = ''.join(ctx['parts'])
                self._append_text(self._render_mention_doc(ctx['attrs'], content))
        elif tag == 'text':
            if self._top()['type'] == 'text':
                ctx = self._stack.pop()
                self._append_text(''.join(ctx['parts']))
        elif tag == 'title':
            if self._top()['type'] == 'discard':
                self._stack.pop()
        # ---- 基础 HTML 标签闭合（document.content 回退时使用）----
        elif tag in ('strong', 'b'):
            self._append_text('**')
        elif tag in ('em', 'i'):
            self._append_text('*')
        elif tag == 'code':
            self._append_text('`')
        elif tag == 'pre':
            self._append_text('\n```\n\n')
        elif tag in ('h1', 'h2', 'h3', 'h4', 'h5', 'h6'):
            self._append_text('\n\n')
        elif tag == 'p':
            self._append_text('\n\n')
        elif tag == 'blockquote':
            if self._top()['type'] == 'blockquote':
                ctx = self._stack.pop()
                content = ''.join(ctx['parts']).strip()
                quoted = '\n'.join(
                    f'> {line}' if line else '>'
                    for line in content.splitlines()
                )
                self._append_text(f'\n\n{quoted}\n\n')
        elif tag == 'a':
            if self._top()['type'] == 'link':
                ctx = self._stack.pop()
                link_text = ''.join(ctx['parts']).strip() or ctx['attrs'].get('href', '')
                href = ctx['attrs'].get('href', '')
                self._append_text(f'[{link_text}]({href})')
        elif tag in ('ul', 'ol'):
            if self._list_stack:
                self._list_stack.pop()
            self._append_text('\n')
        elif tag == 'li':
            self._append_text('\n')

    def get_clean_markdown(self) -> str:
        if len(self._stack) != 1 or self._stack[0]['type'] != 'root':
            _status(f'  [WARN] 解析结束时栈不平衡: depth={len(self._stack)}, '
                    f'top type={self._stack[-1].get("type")}')
        raw = ''.join(self._parts)
        # 清理多余空行（最多连续 2 个）
        cleaned = re.sub(r'\n{3,}', '\n\n', raw)
        return cleaned.strip() + '\n'


# ---------------------------------------------------------------------------
# 画板提取
# ---------------------------------------------------------------------------


class WhiteboardExtractor:
    """提取画板缩略图和代码生成的可读拓扑文本并缓存。"""

    def __init__(self, slug: str, out_dir: Path):
        self._slug = slug
        self._out_dir = out_dir
        self._cache: dict[str, str] = {}  # token -> extracted text
        self._index: list[dict] = []

    def extract(self, token: str) -> str:
        if token in self._cache:
            return self._cache[token]

        token_hint = f"{token[:6]}...{token[-4:]}" if len(token) > 12 else "<redacted>"
        _status(f"  [whiteboard] Querying token={token_hint}")
        try:
            data = _run_lark_cli([
                'whiteboard', '+query',
                '--whiteboard-token', token,
                '--output_as', 'raw',
                '--format', 'json',
            ], timeout=30)
        except SystemExit as e:
            _status(f"  [whiteboard] Query failed: {e}")
            self._cache[token] = ''
            self._index.append({'token': token, 'status': 'error', 'error': str(e)})
            return ''

        if not data.get('ok'):
            message = data.get('error', {}).get('message', '画板查询失败')
            _status(f"  [whiteboard] Query failed: {message}")
            self._cache[token] = ''
            self._index.append({
                'token': token,
                'status': 'error',
                'error': message,
            })
            return ''

        nodes = data.get('data', {}).get('nodes', [])
        _status(f"  [whiteboard] Got {len(nodes)} nodes")

        wb_dir = self._out_dir / f"{self._slug}-whiteboards"
        wb_dir.mkdir(parents=True, exist_ok=True)

        topology = _normalize_whiteboard(nodes)
        markdown_content = _whiteboard_markdown(topology)
        markdown_path = wb_dir / f"{token}-topology.md"
        markdown_path.write_text(markdown_content, encoding='utf-8')

        image_path = _download_media(
            token,
            wb_dir,
            f'{token}-preview',
            media_type='whiteboard',
        )
        preview = ''
        if image_path:
            preview = f'![画板预览]({self._relative_path(image_path)})\n\n'
        text_fallback = (
            f'[文字版节点与连线（仅无法读取图片时使用）]'
            f'({self._relative_path(markdown_path)})'
        )

        inline_content = (
            f'> 📋 **画板**（token: `{token}`；'
            f'{topology["shape_count"]} 个图形，'
            f'{topology["connector_count"]} 条连线）\n\n'
            f'{preview}'
            f'> {text_fallback}'
        )
        _status(
            f"  [whiteboard] Extracted {topology['shape_count']} shapes, "
            f"{topology['connector_count']} connectors → {markdown_path}"
        )

        self._cache[token] = inline_content
        self._index.append({
            'token': token,
            'status': 'ok',
            'node_count': len(nodes),
            'shape_count': topology['shape_count'],
            'connector_count': topology['connector_count'],
            'markdown_path': str(markdown_path),
            'preview_path': str(image_path) if image_path else '',
        })

        return inline_content

    def _relative_path(self, path: Path) -> str:
        clean_path = self._out_dir / f'{self._slug}-clean.md'
        return os.path.relpath(path, clean_path.parent)

    def save_index(self):
        if not self._index:
            return
        wb_dir = self._out_dir / f"{self._slug}-whiteboards"
        wb_dir.mkdir(parents=True, exist_ok=True)
        index_path = wb_dir / "index.json"
        index_path.write_text(
            json.dumps(self._index, ensure_ascii=False, indent=2),
            encoding='utf-8',
        )
        _status(f"  [whiteboard] Index saved: {index_path}")


class DocumentMediaExtractor:
    """下载正文图片并生成相对 clean Markdown 的路径。"""

    def __init__(self, slug: str, out_dir: Path):
        self._slug = slug
        self._out_dir = out_dir
        self._cache: dict[str, str] = {}
        self._counter = 0
        self._index: list[dict] = []

    def extract(self, token: str, attrs: dict) -> str:
        if token in self._cache:
            return self._cache[token]

        self._counter += 1
        media_dir = self._out_dir / f'{self._slug}-media'
        base_name = f'image-{self._counter:03d}-{token[:8]}'
        path = _download_media(token, media_dir, base_name)
        relative_path = ''
        status = 'error'
        if path:
            clean_path = self._out_dir / f'{self._slug}-clean.md'
            relative_path = os.path.relpath(path, clean_path.parent)
            status = 'ok'
        self._cache[token] = relative_path
        self._index.append({
            'token': token,
            'status': status,
            'path': str(path) if path else '',
            'relative_path': relative_path,
            'name': attrs.get('name', ''),
            'alt': attrs.get('alt', ''),
            'mime': attrs.get('mime', ''),
        })
        return relative_path

    def save_index(self):
        if not self._index:
            return
        media_dir = self._out_dir / f'{self._slug}-media'
        media_dir.mkdir(parents=True, exist_ok=True)
        index_path = media_dir / 'index.json'
        index_path.write_text(
            json.dumps(self._index, ensure_ascii=False, indent=2),
            encoding='utf-8',
        )
        _status(f"  [media] Index saved: {index_path}")


class EmbeddedSheetExtractor:
    """读取嵌入 Sheet 的完整当前子表，并转为 Markdown 表格。"""

    def __init__(self):
        self._workbooks: dict[str, dict[str, dict]] = {}
        self._cache: dict[tuple[str, str], str] = {}

    def extract(self, attrs: dict) -> str:
        token = attrs.get('token', '')
        sheet_id = attrs.get('sheet-id') or attrs.get('sheet_id', '')
        if not token or not sheet_id:
            return self._fallback(attrs, '缺少 token 或 sheet-id')
        key = (token, sheet_id)
        if key in self._cache:
            return self._cache[key]

        sheet_info = self._sheet_info(token, sheet_id)
        if not sheet_info:
            result = self._fallback(attrs, '无法读取工作表元数据')
            self._cache[key] = result
            return result

        row_count = int(sheet_info.get('row_count') or 1)
        column_count = int(sheet_info.get('column_count') or 1)
        cell_range = f'A1:{_column_name(column_count)}{row_count}'
        response = _run_lark_command([
            'sheets', '+csv-get',
            '--spreadsheet-token', token,
            '--sheet-id', sheet_id,
            '--range', cell_range,
            '--include-row-prefix=false',
            '--format', 'json',
        ])
        if not response.get('ok'):
            message = response.get('error', {}).get('message', '读取失败')
            result = self._fallback(attrs, message)
            self._cache[key] = result
            return result

        csv_content = response.get('data', {}).get('annotated_csv', '')
        table = _csv_to_markdown(csv_content)
        if not table:
            result = self._fallback(attrs, '工作表为空')
            self._cache[key] = result
            return result

        title = sheet_info.get('sheet_name') or sheet_id
        result = (
            f'\n\n> 📊 **嵌入电子表格：{title}**'
            f'（token: `{token}`；sheet-id: `{sheet_id}`；范围: `{cell_range}`）\n\n'
            f'{table}\n'
        )
        self._cache[key] = result
        return result

    def _sheet_info(self, token: str, sheet_id: str) -> dict:
        if token not in self._workbooks:
            response = _run_lark_command([
                'sheets', '+workbook-info',
                '--spreadsheet-token', token,
                '--format', 'json',
            ])
            sheets = response.get('data', {}).get('sheets', []) if response.get('ok') else []
            self._workbooks[token] = {
                sheet.get('sheet_id', ''): sheet for sheet in sheets
            }
        return self._workbooks[token].get(sheet_id, {})

    @staticmethod
    def _fallback(attrs: dict, reason: str) -> str:
        properties = ', '.join(
            f'{key}=`{value}`' for key, value in attrs.items() if value
        )
        return (
            f'\n\n> 📎 **嵌入电子表格**：{properties or "未提供资源标识"}'
            f'（{reason}）\n\n'
        )


class SyncedReferenceExtractor:
    """精确读取同步引用源 block，并递归走同一 PRD 渲染链路。"""

    def __init__(self, parser_factory):
        self._parser_factory = parser_factory
        self._cache: dict[tuple[str, str], str] = {}

    def extract(self, attrs: dict) -> str:
        token = attrs.get('src-token', '')
        block_id = attrs.get('src-block-id', '')
        if not token or not block_id:
            return self._fallback(attrs, '缺少 src-token 或 src-block-id')
        key = (token, block_id)
        if key in self._cache:
            return self._cache[key]

        response = _run_lark_command([
            'docs', '+fetch',
            '--doc', token,
            '--scope', 'range',
            '--start-block-id', block_id,
            '--end-block-id', block_id,
            '--doc-format', 'markdown',
            '--format', 'json',
        ])
        if not response.get('ok'):
            message = response.get('error', {}).get('message', '读取失败')
            result = self._fallback(attrs, message)
            self._cache[key] = result
            return result

        content = response.get('data', {}).get('document', {}).get('content', '')
        if not content:
            result = self._fallback(attrs, '源 block 为空')
            self._cache[key] = result
            return result

        parser = self._parser_factory()
        parser.feed(content)
        rendered = parser.get_clean_markdown().strip()
        result = (
            f'\n\n> 🔄 **同步引用块**'
            f'（src-token: `{token}`；src-block-id: `{block_id}`）\n\n'
            f'{rendered}\n\n'
        )
        self._cache[key] = result
        return result

    @staticmethod
    def _fallback(attrs: dict, reason: str) -> str:
        properties = ', '.join(
            f'{key}=`{value}`' for key, value in attrs.items() if value
        )
        return (
            f'\n\n> 📎 **同步引用块**：{properties or "未提供资源标识"}'
            f'（{reason}）\n\n'
        )


# ---------------------------------------------------------------------------
# Step 2 主函数
# ---------------------------------------------------------------------------


def step2_clean(payload: dict, url: str) -> str:
    """清洗 markdown，提取画板，输出 clean Markdown。"""
    global BASE_ORIGIN
    parsed_url = urllib.parse.urlparse(url)
    if parsed_url.scheme in ('http', 'https') and parsed_url.netloc:
        BASE_ORIGIN = f'{parsed_url.scheme}://{parsed_url.netloc}'
    slug = _slug_from_url(url)
    markdown = payload.get('markdown', '')
    document = payload.get('document', {})
    title_match = re.search(r'<title[^>]*>(.*?)</title>', markdown, flags=re.DOTALL)
    title = (
        payload.get('title')
        or document.get('title')
        or (html.unescape(title_match.group(1)).strip() if title_match else '')
        or 'Untitled'
    )
    doc_id = payload.get('doc_id') or document.get('document_id', '')
    total_length = payload.get('total_length') or len(markdown)

    _status(f"[step2] Cleaning: {title}")
    markdown_source = payload.get('_markdown_source', 'markdown')
    _status(f"[step2] Input: {len(markdown)} chars of mixed markdown "
            f"(source: {markdown_source})")

    # 查找所有画板 token
    wb_tokens = re.findall(r'<whiteboard[^>]*token="([^"]+)"', markdown)
    _status(f"[step2] Found {len(wb_tokens)} whiteboard(s): {wb_tokens}")

    # 创建画板提取器
    extractor = WhiteboardExtractor(slug, OUTPUT_ROOT)
    media_extractor = DocumentMediaExtractor(slug, OUTPUT_ROOT)
    sheet_extractor = EmbeddedSheetExtractor()
    synced_extractor = None

    def parser_factory():
        return LarkMarkdownParser(
            whiteboard_extractor=extractor.extract,
            media_extractor=media_extractor.extract,
            sheet_extractor=sheet_extractor.extract,
            synced_reference_extractor=synced_extractor.extract,
        )

    synced_extractor = SyncedReferenceExtractor(parser_factory)

    # 解析
    parser = parser_factory()
    parser.feed(markdown)

    # 保存资产索引
    extractor.save_index()
    media_extractor.save_index()

    # 生成 clean Markdown
    clean_md = parser.get_clean_markdown()
    clean_md = _decode_markdown_urls(clean_md)

    # 在顶部加元数据头
    header = (
        f"# {title}\n\n"
        f"> **来源**: {url}\n"
        f"> **Doc ID**: {doc_id}\n"
        f"> **原始长度**: {total_length} chars\n\n"
        f"---\n\n"
    )

    final_md = header + clean_md

    out_path = OUTPUT_ROOT / f"{slug}-clean.md"
    out_path.write_text(final_md, encoding='utf-8')
    _status(f"[step2] Saved: {out_path} ({len(final_md)} chars)")

    return final_md


# ---------------------------------------------------------------------------
# 主入口
# ---------------------------------------------------------------------------


def main():
    import argparse
    global OUTPUT_ROOT

    parser = argparse.ArgumentParser(
        description="Fetch Feishu Wiki PRD with whiteboard extraction",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python3 prd_fetch.py <wiki-url>                    # fetch + clean (default)
  python3 prd_fetch.py <wiki-url> --steps all        # same as step1,step2
  python3 prd_fetch.py <wiki-url> --steps step2      # re-clean from saved -original.json
        """,
    )
    parser.add_argument("url", nargs="?", default=None,
                        help="Feishu Wiki or Docx URL")
    parser.add_argument("--steps", default="step1,step2",
                        help="Comma-separated steps to run: step1,step2 or 'all' "
                             "(default: step1,step2)")
    parser.add_argument(
        "--output-dir",
        default=str(OUTPUT_ROOT),
        help="Artifact directory (default: $TMPDIR/prd-fetch)",
    )
    args = parser.parse_args()
    OUTPUT_ROOT = Path(args.output_dir).expanduser().resolve()

    # Parse steps
    if args.steps == "all":
        steps = {"step1", "step2"}
    else:
        steps = {s.strip() for s in args.steps.split(",")}
    unknown_steps = steps - {"step1", "step2"}
    if unknown_steps:
        parser.error(
            "unsupported steps: "
            + ",".join(sorted(unknown_steps))
            + "; knowledge extraction and owner updates belong to the current task Agent"
        )

    if not args.url:
        parser.error("URL is required")

    url = args.url

    _status("=== prd_fetch ===")
    _status(f"URL: {url}")
    _status(f"Steps: {sorted(steps)}")
    _status(f"Output: {OUTPUT_ROOT}")
    _status()

    slug = _slug_from_url(url)
    OUTPUT_ROOT.mkdir(parents=True, exist_ok=True)

    payload = None
    # Step 1: 拉取
    if 'step1' in steps:
        payload = step1_fetch(url)
    else:
        # 从已保存的文件加载
        in_path = OUTPUT_ROOT / f"{slug}-original.json"
        if in_path.is_file():
            payload = json.loads(in_path.read_text(encoding='utf-8'))
            _status(f"[step1] Skipped, loaded from: {in_path}")
        else:
            sys.exit(f"Error: no saved data at {in_path}, run with step1 first.")

    # Step 2: 清洗 + 画板提取
    if 'step2' in steps:
        step2_clean(payload, url)

    artifacts = []
    for path in sorted(OUTPUT_ROOT.glob(f"{slug}*")):
        artifacts.append({
            "path": str(path),
            "type": "directory" if path.is_dir() else "file",
            "size": None if path.is_dir() else path.stat().st_size,
        })
    print(json.dumps({
        "ok": True,
        "url": url,
        "output_dir": str(OUTPUT_ROOT),
        "artifacts": artifacts,
    }, ensure_ascii=False))


if __name__ == '__main__':
    main()
