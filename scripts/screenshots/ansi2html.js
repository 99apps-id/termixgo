// Converts a tmux capture (ANSI SGR text) into a self-contained terminal-window HTML page.
// usage: node ansi2html.js <in.ansi> <out.html> <title> <cols>
const fs = require("fs");
const [, , input, output, title = "termixgo", colsArg = "118"] = process.argv;
const COLS = parseInt(colsArg, 10);

const DEFAULT_FG = [215, 221, 232];
const DEFAULT_BG = [11, 16, 24];
const BASE16 = [
  [30, 34, 46], [237, 106, 94], [95, 207, 128], [229, 192, 90],
  [96, 153, 247], [199, 124, 235], [74, 207, 207], [200, 207, 220],
  [90, 98, 116], [255, 138, 128], [125, 230, 155], [250, 217, 120],
  [135, 180, 255], [221, 153, 250], [110, 228, 228], [245, 248, 252],
];

function xterm256(n) {
  if (n < 16) return BASE16[n];
  if (n >= 232) { const v = 8 + (n - 232) * 10; return [v, v, v]; }
  n -= 16;
  const steps = [0, 95, 135, 175, 215, 255];
  return [steps[Math.floor(n / 36)], steps[Math.floor(n / 6) % 6], steps[n % 6]];
}

const hex = (c) => "#" + c.map((v) => v.toString(16).padStart(2, "0")).join("");
const blend = (a, b, t) => a.map((v, i) => Math.round(v * t + b[i] * (1 - t)));

function parse(text) {
  const rows = [];
  let row = [];
  let st = { fg: null, bg: null, bold: false, dim: false, italic: false, underline: false, reverse: false };
  const apply = (params) => {
    if (params.length === 0) params = [0];
    for (let i = 0; i < params.length; i++) {
      const p = params[i];
      if (p === 0) st = { fg: null, bg: null, bold: false, dim: false, italic: false, underline: false, reverse: false };
      else if (p === 1) st.bold = true;
      else if (p === 2) st.dim = true;
      else if (p === 3) st.italic = true;
      else if (p === 4) st.underline = true;
      else if (p === 7) st.reverse = true;
      else if (p === 22) { st.bold = false; st.dim = false; }
      else if (p === 23) st.italic = false;
      else if (p === 24) st.underline = false;
      else if (p === 27) st.reverse = false;
      else if (p >= 30 && p <= 37) st.fg = BASE16[p - 30];
      else if (p === 39) st.fg = null;
      else if (p >= 40 && p <= 47) st.bg = BASE16[p - 40];
      else if (p === 49) st.bg = null;
      else if (p >= 90 && p <= 97) st.fg = BASE16[p - 90 + 8];
      else if (p >= 100 && p <= 107) st.bg = BASE16[p - 100 + 8];
      else if (p === 38 || p === 48) {
        const target = p === 38 ? "fg" : "bg";
        if (params[i + 1] === 5) { st[target] = xterm256(params[i + 2] | 0); i += 2; }
        else if (params[i + 1] === 2) { st[target] = [params[i + 2] | 0, params[i + 3] | 0, params[i + 4] | 0]; i += 4; }
      }
    }
  };
  const re = /\x1b\[([0-9;:]*)m|\x1b\[[0-9;?]*[A-Za-z]|\x1b[()][A-Z0-9]|([\s\S])/g;
  let m;
  while ((m = re.exec(text)) !== null) {
    if (m[1] !== undefined) { apply(m[1] === "" ? [] : m[1].split(/[;:]/).map(Number)); continue; }
    if (m[2] === undefined) continue; // other escape sequences
    const ch = m[2];
    if (ch === "\r") continue;
    if (ch === "\n") { rows.push(row); row = []; continue; }
    row.push({ ch, ...st });
  }
  if (row.length) rows.push(row);
  return rows;
}

function esc(s) {
  return s === "&" ? "&amp;" : s === "<" ? "&lt;" : s === ">" ? "&gt;" : s === '"' ? "&quot;" : s;
}

const rows = parse(fs.readFileSync(input, "utf8"));
while (rows.length && rows[rows.length - 1].every((c) => c.ch === " " && !c.bg)) rows.pop();

const FS = 14;
const CELL = FS * 0.6;
const LH = 20;
const PAD_X = 20;
const PAD_Y = 16;
const BAR = 38;
const OUTER = 28;
const winW = Math.round(COLS * CELL + PAD_X * 2);
const winH = BAR + rows.length * LH + PAD_Y * 2;

let body = "";
for (const row of rows) {
  let line = "";
  for (const c of row) {
    let fg = c.fg || DEFAULT_FG;
    let bg = c.bg;
    if (c.reverse) { const t = fg; fg = bg || DEFAULT_BG; bg = t; }
    if (c.dim) fg = blend(fg, bg || DEFAULT_BG, 0.55);
    const css = [`color:${hex(fg)}`];
    if (bg) css.push(`background:${hex(bg)}`);
    if (c.bold) css.push("font-weight:700");
    if (c.italic) css.push("font-style:italic");
    if (c.underline) css.push("text-decoration:underline");
    line += `<i style="${css.join(";")}">${esc(c.ch)}</i>`;
  }
  body += `<div class="r">${line || "&nbsp;"}</div>`;
}

const page = `<!doctype html><html><head><meta charset="utf-8"><style>
@font-face{font-family:"JBM";src:url("fonts/jetbrains-mono-latin-400-normal.woff2");font-weight:400}
@font-face{font-family:"JBM";src:url("fonts/jetbrains-mono-latin-700-normal.woff2");font-weight:700}
html,body{margin:0;background:radial-gradient(1200px 700px at 30% 0%,#12202b 0%,#0a0f16 60%,#070a10 100%)}
body{padding:${OUTER}px;width:${winW}px;height:${winH}px;display:block}
.win{width:${winW}px;height:${winH}px;background:rgb(${DEFAULT_BG.join(",")});border:1px solid #223041;border-radius:12px;overflow:hidden;box-shadow:0 24px 60px rgba(0,0,0,.55),0 0 0 1px rgba(45,212,191,.06)}
.bar{height:${BAR}px;display:flex;align-items:center;gap:8px;padding:0 14px;background:#0f1622;border-bottom:1px solid #1b2636;position:relative}
.dot{width:12px;height:12px;border-radius:50%}
.t{position:absolute;left:0;right:0;text-align:center;font:500 12px "JBM",monospace;color:#7d8aa3;letter-spacing:.02em}
.scr{padding:${PAD_Y}px ${PAD_X}px;font:400 ${FS}px/${LH}px "JBM","DejaVu Sans Mono","Segoe UI Symbol",monospace;color:#d7dde8;white-space:pre}
.r{height:${LH}px;white-space:pre}
.r i{display:inline-block;width:${CELL}px;height:${LH}px;font-style:normal;text-align:center;overflow:visible;vertical-align:top}
.r i[style*="italic"]{font-style:italic}
</style></head><body><div class="win"><div class="bar"><span class="dot" style="background:#ff5f57"></span><span class="dot" style="background:#febc2e"></span><span class="dot" style="background:#28c840"></span><span class="t">${title}</span></div><div class="scr">${body}</div></div></body></html>`;

fs.writeFileSync(output, page);
console.log(JSON.stringify({ width: winW + OUTER * 2, height: winH + OUTER * 2, rows: rows.length }));
