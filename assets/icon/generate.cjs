// Regenerates every icon artifact from icon.svg:
//   png/icon-<size>.png, worldhop.ico, preview.png, ui/static/favicon.{svg,png}
// Usage (from the repo root):  node assets/icon/generate.cjs
// Needs Node and Playwright with Chromium (npm i -g playwright, or any
// install that `require('playwright')` can resolve). Chromium rasterises the
// SVG, so no other image tooling is needed. Output is deterministic.
const fs = require('fs');
const path = require('path');
const { chromium } = require('playwright');

const dir = __dirname;
const root = path.resolve(dir, '..', '..');
const SIZES = [16, 32, 48, 64, 128, 256];
const svg = fs.readFileSync(path.join(dir, 'icon.svg'), 'utf8');
const svgURL = 'data:image/svg+xml;base64,' + Buffer.from(svg).toString('base64');

// Rasterise the SVG at each size in the page; return raw RGBA and PNG data.
async function render(page) {
  return page.evaluate(async ({ url, sizes }) => {
    const img = new Image();
    img.src = url;
    await img.decode();
    const out = {};
    for (const s of sizes) {
      const c = document.createElement('canvas');
      c.width = c.height = s;
      const g = c.getContext('2d');
      g.drawImage(img, 0, 0, s, s);
      out[s] = {
        rgba: Array.from(g.getImageData(0, 0, s, s).data),
        png: c.toDataURL('image/png').split(',')[1],
      };
    }
    return out;
  }, { url: svgURL, sizes: SIZES });
}

// 32-bit BMP (DIB) payload for an ICO entry: bottom-up BGRA plus empty AND mask.
function dib(size, rgba) {
  const maskRow = Math.ceil(size / 32) * 4;
  const buf = Buffer.alloc(40 + size * size * 4 + maskRow * size);
  buf.writeUInt32LE(40, 0);
  buf.writeInt32LE(size, 4);
  buf.writeInt32LE(size * 2, 8);
  buf.writeUInt16LE(1, 12);
  buf.writeUInt16LE(32, 14);
  buf.writeUInt32LE(size * size * 4 + maskRow * size, 20);
  let o = 40;
  for (let y = size - 1; y >= 0; y--) {
    for (let x = 0; x < size; x++) {
      const i = (y * size + x) * 4;
      buf[o++] = rgba[i + 2];
      buf[o++] = rgba[i + 1];
      buf[o++] = rgba[i];
      buf[o++] = rgba[i + 3];
    }
  }
  return buf;
}

// Small sizes use BMP frames (the most compatible form for the Windows tray
// and LoadImage); 256 uses PNG as Windows expects.
function ico(frames) {
  const head = Buffer.alloc(6 + 16 * frames.length);
  head.writeUInt16LE(1, 2);
  head.writeUInt16LE(frames.length, 4);
  let offset = head.length;
  frames.forEach((f, n) => {
    const e = 6 + 16 * n;
    head[e] = f.size >= 256 ? 0 : f.size;
    head[e + 1] = f.size >= 256 ? 0 : f.size;
    head.writeUInt16LE(1, e + 4);
    head.writeUInt16LE(32, e + 6);
    head.writeUInt32LE(f.data.length, e + 8);
    head.writeUInt32LE(offset, e + 12);
    offset += f.data.length;
  });
  return Buffer.concat([head, ...frames.map((f) => f.data)]);
}

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage();
  const r = await render(page);

  fs.mkdirSync(path.join(dir, 'png'), { recursive: true });
  for (const s of SIZES) {
    fs.writeFileSync(path.join(dir, 'png', `icon-${s}.png`), Buffer.from(r[s].png, 'base64'));
  }
  fs.writeFileSync(
    path.join(dir, 'worldhop.ico'),
    ico(SIZES.map((s) => ({
      size: s,
      data: s >= 256 ? Buffer.from(r[s].png, 'base64') : dib(s, r[s].rgba),
    }))),
  );

  // Web UI favicon.
  const st = path.join(root, 'ui', 'static');
  fs.mkdirSync(st, { recursive: true });
  fs.writeFileSync(path.join(st, 'favicon.svg'), svg);
  fs.copyFileSync(path.join(dir, 'png', 'icon-32.png'), path.join(st, 'favicon.png'));

  // Preview sheet: real sizes on light and dark backgrounds, plus 4x zoom.
  const row = (bg) => `<div style="background:${bg};padding:12px;display:flex;gap:16px;align-items:flex-end">` +
    [16, 32, 48, 64, 128, 256].map((s) => `<img src="data:image/png;base64,${r[s].png}" width=${s} height=${s}>`).join('') +
    `<img src="data:image/png;base64,${r[16].png}" width=64 height=64 style="image-rendering:pixelated">` +
    `<img src="data:image/png;base64,${r[32].png}" width=128 height=128 style="image-rendering:pixelated"></div>`;
  await page.setViewportSize({ width: 800, height: 600 });
  await page.setContent(`<body style="margin:0">${row('#ffffff')}${row('#1e1e1e')}</body>`);
  await page.screenshot({ path: path.join(dir, 'preview.png'), fullPage: true });
  await browser.close();
  console.log('wrote', SIZES.length, 'PNGs, worldhop.ico, preview.png, ui/static favicon');
})();
