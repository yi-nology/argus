// html.go —— 自包含交互 HTML 视图（v3.183.0 P1）：快照 → 单文件 HTML。
// 零 CDN 依赖（215 内网部署无外网也完整可用）：Go 侧做分层布局产 SVG，
// 内嵌 JSON + 少量原生 JS 供给缩放/拖拽/搜索/点选详情。经 GET /tasks/:id/impact
// 下发（text/html），也可离线存档。
package impactview

import (
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
)

// 布局常量（分层 LR：深度列 × 列内栈）。
const (
	nodeW  = 240 // 节点框宽
	nodeH  = 30  // 节点框高
	colGap = 70  // 列间距
	rowGap = 12  // 行间距
	orgX   = 24  // 画布原点
	orgY   = 24
)

// RenderHTML 渲染自包含交互 HTML（taskID 仅作页头标识可空；快照空节点返回空串）。
func RenderHTML(s *Snapshot, taskID string) string {
	if s == nil || len(s.Nodes) == 0 {
		return ""
	}
	data, _ := json.Marshal(s)
	dataStr := strings.ReplaceAll(string(data), "</", `<\/`) // script 内闭合防线
	pos, w, h := layoutSVG(s)
	return htmlTemplate(html.EscapeString(riskBadge(s.Risk.Level)), s, taskID,
		string(dataStr), svgBody(s, pos), w, h)
}

// layoutSVG 分层布局：BFS 深度定列（改动=列 0），列内按 (Kind 权重, ID) 栈叠。
// 返回 节点坐标表 + 画布宽高。
func layoutSVG(s *Snapshot) (map[string][2]int, int, int) {
	depth := map[string]int{}
	// 改动节点为 0 列；其余由边 BFS 外推（环/多点入边取最小深度）
	for _, n := range s.Nodes {
		if n.Changed {
			depth[n.ID] = 0
		}
	}
	adj := map[string][]string{}
	for _, e := range s.Edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	queue := make([]string, 0, len(depth))
	for id := range depth {
		queue = append(queue, id)
	}
	sort.Strings(queue)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, nx := range adj[cur] {
			if _, seen := depth[nx]; !seen {
				depth[nx] = depth[cur] + 1
				queue = append(queue, nx)
			}
		}
	}
	// 未被边触达的节点：按种类给尾列
	maxDepth := 0
	for _, d := range depth {
		if d > maxDepth {
			maxDepth = d
		}
	}
	weight := map[string]int{KindSymbol: 0, KindPackage: 1, KindFile: 2, KindTest: 3}
	type colItem struct {
		id     string
		weight int
	}
	cols := map[int][]colItem{}
	for _, n := range s.Nodes {
		d, ok := depth[n.ID]
		if !ok {
			d = maxDepth + 1
			depth[n.ID] = d
		}
		w := weight[n.Kind]
		cols[d] = append(cols[d], colItem{n.ID, w})
	}
	colNums := make([]int, 0, len(cols))
	for d := range cols {
		colNums = append(colNums, d)
	}
	sort.Ints(colNums)
	pos := map[string][2]int{}
	width, height := 0, 0
	for _, d := range colNums {
		items := cols[d]
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].weight != items[j].weight {
				return items[i].weight < items[j].weight
			}
			return items[i].id < items[j].id
		})
		x := orgX + d*(nodeW+colGap)
		for y, it := range items {
			pos[it.id] = [2]int{x, orgY + y*(nodeH+rowGap)}
		}
		if h := orgY + len(items)*(nodeH+rowGap); h > height {
			height = h
		}
		if w := x + nodeW + orgX; w > width {
			width = w
		}
	}
	return pos, width, height
}

// nodeFill 节点配色（kind × changed）。
func nodeFill(n Node) (fill, stroke string) {
	if n.Changed {
		return "#fecaca", "#dc2626"
	}
	switch n.Kind {
	case KindPackage:
		return "#dbeafe", "#2563eb"
	case KindSymbol:
		return "#fef9c3", "#ca8a04"
	case KindTest:
		return "#dcfce7", "#16a34a"
	default:
		return "#e2e8f0", "#64748b"
	}
}

// svgBody 产出 SVG 主体（框 + 标签 + 贝塞尔边）。
func svgBody(s *Snapshot, pos map[string][2]int) string {
	var b strings.Builder
	// 边先画（垫底）；data-i 携带快照边序——JS 搜索联动按它回查 data.edges,
	// 不依赖「DOM 序=数据序」的隐式对齐
	for i, e := range s.Edges {
		p1, ok1 := pos[e.From]
		p2, ok2 := pos[e.To]
		if !ok1 || !ok2 {
			continue
		}
		x1 := p1[0] + nodeW
		y1 := p1[1] + nodeH/2
		x2 := p2[0]
		y2 := p2[1] + nodeH/2
		mid := (x1 + x2) / 2
		fmt.Fprintf(&b, `<path data-i="%d" d="M%d,%d C%d,%d %d,%d %d,%d" class="edge rel-%s"/><text class="elabel" x="%d" y="%d">%s</text>`+"\n",
			i, x1, y1, mid, y1, mid, y2, x2, y2, e.Rel, mid, y2-6, html.EscapeString(mermaidRel(e.Rel)))
	}
	for _, n := range s.Nodes {
		p, ok := pos[n.ID]
		if !ok {
			continue
		}
		fill, stroke := nodeFill(n)
		label := truncateRunes(n.Label, 30)
		tip := n.Kind + " · " + n.ID
		if n.Detail != "" {
			tip += "\n" + n.Detail
		}
		fmt.Fprintf(&b, `<g class="node kind-%s" data-id="%s" transform="translate(%d,%d)"><title>%s</title><rect width="%d" height="%d" rx="6" fill="%s" stroke="%s"/><text x="12" y="20">%s</text></g>`+"\n",
			n.Kind, html.EscapeString(n.ID), p[0], p[1],
			html.EscapeString(tip), nodeW, nodeH, fill, stroke, html.EscapeString(label))
	}
	return b.String()
}

// truncateRunes 显示截断（尾部省略号）。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// htmlTemplate 单文件 HTML（内联样式与脚本，零外链）。
func htmlTemplate(riskBadgeStr string, s *Snapshot, taskID, dataJSON, svg string, w, h int) string {
	var notes strings.Builder
	for _, n := range s.Notes {
		notes.WriteString("<li>" + html.EscapeString(n) + "</li>")
	}
	var riskList strings.Builder
	for _, r := range s.Risk.Reasons {
		riskList.WriteString("<li>" + html.EscapeString(r) + "</li>")
	}
	return `<!DOCTYPE html>
<html lang="zh"><head><meta charset="utf-8">
<title>Argus 影响面视图 · 风险 ` + riskBadgeStr + `</title>
<style>
body{font:14px/1.6 -apple-system,"PingFang SC","Microsoft YaHei",sans-serif;margin:0;color:#0f172a;background:#f8fafc}
header{padding:12px 20px;background:#fff;border-bottom:1px solid #e2e8f0;display:flex;gap:16px;align-items:center;flex-wrap:wrap}
header h1{font-size:16px;margin:0}
.badge{padding:2px 10px;border-radius:999px;font-weight:600}
.badge.high{background:#fecaca;color:#7f1d1d}.badge.medium{background:#fef3c7;color:#78350f}.badge.low{background:#dcfce7;color:#14532d}
.meta{color:#64748b;font-size:12px}
#toolbar{padding:8px 20px;background:#fff;border-bottom:1px solid #e2e8f0;display:flex;gap:8px;align-items:center}
#toolbar input{flex:0 1 280px;padding:4px 10px;border:1px solid #cbd5e1;border-radius:6px}
button{padding:4px 12px;border:1px solid #cbd5e1;background:#fff;border-radius:6px;cursor:pointer}
button:hover{background:#f1f5f9}
#viewport{overflow:hidden;height:calc(100vh - 118px);cursor:grab;position:relative;background:#f8fafc}
#viewport.dragging{cursor:grabbing}
svg{min-width:100%}
.node rect{stroke-width:1.5}
.node{cursor:pointer}
.node text{font-size:12px;fill:#0f172a;pointer-events:none}
.node.dim{opacity:.15}
.edge{fill:none;stroke:#94a3b8;stroke-width:1.2;marker-end:url(#arrow)}
.edge.dim{opacity:.12}
.elabel{font-size:10px;fill:#94a3b8;pointer-events:none}
aside{position:fixed;right:0;top:0;bottom:0;width:320px;background:#fff;border-left:1px solid #e2e8f0;padding:16px;display:none;overflow:auto}
aside h3{margin:0 0 8px;font-size:14px}
aside .kv{margin:2px 0;font-size:12px;color:#334155;word-break:break-all}
aside .kv b{color:#64748b;font-weight:500;margin-right:6px}
details{margin:8px 20px}
summary{cursor:pointer;color:#334155}
</style></head><body>
<header>
<h1>Argus 影响面视图</h1>
<span class="badge ` + s.Risk.Level + `">风险 ` + riskBadgeStr + ` · ` + fmt.Sprint(s.Risk.Score) + ` 分</span>
<span class="meta">` + html.EscapeString(fmt.Sprintf("%d 改动 · %d 节点 · %d 边 · 调用位 %d · 共变 %d", len(s.Changed), len(s.Nodes), len(s.Edges), s.Stats.CallSites, len(s.Coupling))) + `</span>
<span class="meta">` + taskMeta(taskID) + `</span>
</header>
<div id="toolbar">
<input id="q" placeholder="搜索文件 / 包 / 符号…">
<button id="zin">放大</button><button id="zout">缩小</button><button id="fit">复位</button>
<button id="closepanel" style="display:none">收起详情</button>
</div>
<details><summary>风险依据（` + fmt.Sprint(len(s.Risk.Reasons)) + ` 条）</summary><ul>` + riskList.String() + `</ul></details>
` + funcDiffDetails(s) + `<details><summary>降级注明（` + fmt.Sprint(len(s.Notes)) + ` 条）</summary><ul>` + notes.String() + `</ul></details>
<div id="viewport"><svg id="g" width="` + fmt.Sprint(w) + `" height="` + fmt.Sprint(h) + `" viewBox="0 0 ` + fmt.Sprint(w) + ` ` + fmt.Sprint(h) + `">
<defs><marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0,0L10,5L0,10z" fill="#94a3b8"/></marker></defs>
` + svg + `</svg></div>
<aside id="panel"></aside>
<script type="application/json" id="impact-data">` + dataJSON + `</script>
<script>
const data=JSON.parse(document.getElementById('impact-data').textContent);
const byId=Object.fromEntries(data.nodes.map(n=>[n.id,n]));
const svg=document.getElementById('g'),vp=document.getElementById('viewport'),q=document.getElementById('q'),panel=document.getElementById('panel');
let scale=1,tx=0,ty=0;
function apply(){svg.style.transformOrigin='0 0';svg.style.transform='translate('+tx+'px,'+ty+'px) scale('+scale+')';}
vp.addEventListener('wheel',e=>{e.preventDefault();const f=e.deltaY<0?1.15:1/1.15;const r=vp.getBoundingClientRect();const mx=e.clientX-r.left,my=e.clientY-r.top;tx=mx-(mx-tx)*f;ty=my-(my-ty)*f;scale=Math.min(4,Math.max(.2,scale*f));apply();},{passive:false});
let drag=null;
vp.addEventListener('mousedown',e=>{drag={x:e.clientX-tx,y:e.clientY-ty};vp.classList.add('dragging');});
window.addEventListener('mousemove',e=>{if(!drag)return;tx=e.clientX-drag.x;ty=e.clientY-drag.y;apply();});
window.addEventListener('mouseup',()=>{drag=null;vp.classList.remove('dragging');});
document.getElementById('zin').onclick=()=>{scale=Math.min(4,scale*1.25);apply();};
document.getElementById('zout').onclick=()=>{scale=Math.max(.2,scale/1.25);apply();};
document.getElementById('fit').onclick=()=>{scale=1;tx=0;ty=0;q.value='';filter('');panel.style.display='none';apply();};
q.addEventListener('input',()=>filter(q.value.trim().toLowerCase()));
function filter(kw){
 const hit={};
 document.querySelectorAll('#g .node').forEach(g=>{
  const id=g.dataset.id,n=byId[id];
  hit[id]=!kw||(id.toLowerCase().includes(kw)||(n&&(n.label||'').toLowerCase().includes(kw)));
  g.classList.toggle('dim',!hit[id]);
 });
 // 边联动：kw 为空全亮；有 kw 时仅两端都命中的边保持可见
 //（data-i 回查快照边序，不依赖 DOM 序=数据序）
 document.querySelectorAll('#g .edge').forEach(p=>{
  if(!kw){p.classList.remove('dim');return;}
  const e=data.edges[+p.dataset.i];
  p.classList.toggle('dim',!(e&&hit[e.from]&&hit[e.to]));
 });
}
svg.addEventListener('click',e=>{
 const g=e.target.closest('.node');if(!g)return;
 const n=byId[g.dataset.id];if(!n)return;
 const edgesIn=data.edges.filter(x=>x.to===n.id),edgesOut=data.edges.filter(x=>x.from===n.id);
 const li=a=>a.map(x=>'<div class="kv">'+(x.rel||'')+' → '+(byId[x.to]?byId[x.to].label:x.to)+'</div>').join('')||'<div class="kv">—</div>';
 panel.innerHTML='<h3>'+esc(n.label)+'</h3>'
  +'<div class="kv"><b>类型</b>'+esc(n.kind)+(n.changed?'（本轮改动）':'')+'</div>'
  +(n.detail?'<div class="kv"><b>详情</b>'+esc(n.detail)+'</div>':'')
  +'<div class="kv"><b>ID</b>'+esc(n.id)+'</div>'
  +'<h3 style="margin-top:12px">入边 '+edgesIn.length+'</h3>'+li(edgesIn)
  +'<h3 style="margin-top:12px">出边 '+edgesOut.length+'</h3>'+li(edgesOut);
 panel.style.display='block';
 document.getElementById('closepanel').style.display='inline-block';
});
document.getElementById('closepanel').onclick=function(){panel.style.display='none';this.style.display='none';};
function esc(s){return String(s).replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));}
apply();
</script></body></html>`
}

// taskMeta 页头任务标识（空省略）。
func taskMeta(taskID string) string {
	if taskID == "" {
		return ""
	}
	return "任务 " + html.EscapeString(taskID)
}

// funcDiffDetails 函数级图 diff 区块（before/after 双列卡片 + 调用边增减
// chips；无数据整块省略）。状态徽标：🟢 新增 / 🔴 删除 / 🟡 变更。
func funcDiffDetails(s *Snapshot) string {
	if s == nil || len(s.FuncDiffs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<details open><summary>函数级图 diff（` + fmt.Sprint(len(s.FuncDiffs)) + ` · base 对比，🟢 新增 / 🔴 删除 / 🟡 变更）</summary>`)
	b.WriteString(`<style>
.fdcard{border:1px solid #e2e8f0;border-radius:8px;margin:8px 0;padding:10px;background:#fff}
.fdhead{display:flex;gap:8px;align-items:center;font-size:13px}
.fdst{padding:1px 8px;border-radius:999px;font-size:12px;font-weight:600}
.st-add{background:#dcfce7;color:#14532d}.st-rem{background:#fecaca;color:#7f1d1d}.st-mod{background:#fef9c3;color:#713f12}
.fdcols{display:flex;gap:8px;margin-top:8px}
.fdcol{flex:1;min-width:0}
.fdcol h4{margin:0 0 4px;font-size:11px;color:#64748b;font-weight:600}
.fdcol pre{background:#f1f5f9;padding:6px 8px;border-radius:6px;overflow:auto;font-size:12px;margin:0;white-space:pre-wrap;word-break:break-all}
.fdcalls{margin-top:6px;font-size:12px}
.chip{display:inline-block;padding:1px 8px;border-radius:999px;margin:2px 4px 0 0;font-size:12px}
.chip-add{background:#dcfce7;color:#14532d}.chip-rem{background:#fecaca;color:#7f1d1d}
</style><div>`)
	shown := s.FuncDiffs
	if len(shown) > 12 {
		shown = shown[:12]
	}
	for _, fd := range shown {
		var st, stCls string
		switch fd.Status {
		case FuncAdded:
			st, stCls = "新增", "st-add"
		case FuncRemoved:
			st, stCls = "删除", "st-rem"
		default:
			st, stCls = "变更", "st-mod"
		}
		b.WriteString(`<div class="fdcard"><div class="fdhead"><span class="fdst ` + stCls + `">` + st + `</span><b>` + html.EscapeString(fd.Name) + `</b><span style="color:#64748b;font-size:12px">` + html.EscapeString(fd.File) + `</span></div>`)
		before, after := "", ""
		if fd.Before != nil {
			before = fd.Before.Signature
		}
		if fd.After != nil {
			after = fd.After.Signature
		}
		if fd.Status != FuncAdded && before != "" {
			b.WriteString(`<div class="fdcols"><div class="fdcol"><h4>BEFORE</h4><pre>` + html.EscapeString(before) + `</pre></div>`)
		} else {
			b.WriteString(`<div class="fdcols"><div class="fdcol"><h4>BEFORE</h4><pre>（无）</pre></div>`)
		}
		if fd.Status != FuncRemoved && after != "" {
			b.WriteString(`<div class="fdcol"><h4>AFTER</h4><pre>` + html.EscapeString(after) + `</pre></div></div>`)
		} else {
			b.WriteString(`<div class="fdcol"><h4>AFTER</h4><pre>（已删除）</pre></div></div>`)
		}
		if len(fd.CallsAdded) > 0 || len(fd.CallsRemoved) > 0 {
			b.WriteString(`<div class="fdcalls">`)
			for _, c := range capShown(fd.CallsAdded, 6) {
				b.WriteString(`<span class="chip chip-add">+ ` + html.EscapeString(c) + `</span>`)
			}
			for _, c := range capShown(fd.CallsRemoved, 6) {
				b.WriteString(`<span class="chip chip-rem">− ` + html.EscapeString(c) + `</span>`)
			}
			b.WriteString(`</div>`)
		}
		b.WriteString(`</div>`)
	}
	if rest := len(s.FuncDiffs) - len(shown); rest > 0 {
		b.WriteString(`<div style="color:#64748b;font-size:12px">…其余 ` + fmt.Sprint(rest) + ` 条略（完整见 MCP get_impact / JSON 快照）</div>`)
	}
	b.WriteString(`</div></details>`)
	return b.String()
}
