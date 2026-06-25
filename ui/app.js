'use strict';

// Log de diagnostico: no-op salvo que el host inyecte __LUMEN_DEBUG__ (con LUMEN_DEBUG=1).
window.__log = (m) => { if (!window.__LUMEN_DEBUG__) return; try { fetch('/log?m=' + encodeURIComponent(m)); } catch (e) {} };
window.addEventListener('error', (e) => window.__log('ERR ' + e.message + ' @' + (e.filename || '') + ':' + e.lineno));
window.addEventListener('unhandledrejection', (e) => window.__log('REJECT ' + (e.reason && (e.reason.message || e.reason))));

// Lumen — logica de la UI. WebView2 NO soporta -webkit-app-region, asi que el arrastre y el
// redimensionado de la ventana frameless se piden al host por los puentes lumenDrag/lumenResize
// (igual que el host de IA History Reader). El resto es visor: ajuste, zoom al cursor, paneo,
// navegacion con crossfade y cromo que se autooculta.

const $ = (id) => document.getElementById(id);
const body = document.body;
const stage = $('stage');
const canvas = $('canvas');
const photo = $('photo');
const ambient = $('ambient');

// ---- puente al host -----------------------------------------------------
function bridge(name, ...args) {
  try { if (typeof window[name] === 'function') return window[name](...args); }
  catch (e) { /* dev en navegador: sin host */ }
}

// ---- estado -------------------------------------------------------------
const state = { srcs: [], names: [], count: 0, idx: 0 };
let natW = 0, natH = 0;          // tamaño nativo de la imagen
let vw = 0, vh = 0;              // viewport (escenario)
let scale = 1, base = 1, maxS = 8, tx = 0, ty = 0;
let loadToken = 0;
let isFs = false;
let fitToImage = window.__LUMEN_FIT__ === true;  // ¿ajustar la VENTANA a la imagen? (persistido)
let firstLoad = true;                            // la 1ª imagen ya nace ajustada desde el host

const clamp = (v, lo, hi) => Math.max(lo, Math.min(hi, v));
const wrap = (i) => ((i % state.count) + state.count) % state.count;
const fitMode = () => Math.abs(scale - base) < base * 0.004;

// ---- medicion / transform ----------------------------------------------
function measure() {
  const r = stage.getBoundingClientRect();
  vw = r.width; vh = r.height;
}
function stageXY(e) {
  const r = stage.getBoundingClientRect();
  return [e.clientX - r.left, e.clientY - r.top];
}
function computeBase() {
  base = Math.min(vw / natW, vh / natH);
  if (base > 1) base = 1;            // no agrandar de mas: el "ajuste" tope es 100% nativo
  maxS = Math.max(base, 8);
}
function clampPan() {
  const iw = natW * scale, ih = natH * scale;
  tx = iw <= vw ? (vw - iw) / 2 : clamp(tx, vw - iw, 0);
  ty = ih <= vh ? (vh - ih) / 2 : clamp(ty, vh - ih, 0);
}
function apply(animate) {
  canvas.style.width = natW + 'px';
  canvas.style.height = natH + 'px';
  canvas.classList.toggle('animate', !!animate);
  canvas.style.transform = `translate(${tx}px, ${ty}px) scale(${scale})`;
  const zoomed = scale > base * 1.004;
  canvas.classList.toggle('zoomed', zoomed);
  canvas.style.cursor = zoomed ? 'grab' : 'default';
  updateHud();
  if (animate) setTimeout(() => canvas.classList.remove('animate'), 240);
}
function fitNow() {
  computeBase();
  scale = base;
  tx = (vw - natW * scale) / 2;
  ty = (vh - natH * scale) / 2;
  apply(false);
}
function zoomAt(target, cx, cy, animate) {
  const ns = clamp(target, base, maxS);
  const ratio = ns / scale;
  tx = cx - (cx - tx) * ratio;
  ty = cy - (cy - ty) * ratio;
  scale = ns;
  clampPan();
  apply(animate);
}
function refit() {
  if (!state.count) return;
  const wasFit = fitMode();
  measure();
  computeBase();
  if (wasFit || scale < base) {
    scale = base;
    tx = (vw - natW * scale) / 2;
    ty = (vh - natH * scale) / 2;
  }
  clampPan();
  apply(false);
}

// ---- HUD / caption ------------------------------------------------------
function updateHud() {
  $('hudCount').textContent = state.count ? (state.idx + 1) + ' / ' + state.count : '';
  $('hud').classList.toggle('no-zoom', fitMode());
  $('hudZoom').textContent = Math.round(scale * 100) + '%';
}
function updateCaption() {
  $('capName').textContent = state.names[state.idx] || '';
  $('capCount').textContent = state.count > 1 ? (state.idx + 1) + ' / ' + state.count : '';
  $('capDot').style.display = state.count > 1 ? '' : 'none';
}

let toastTimer;
function toast(msg) {
  const t = $('toast');
  t.textContent = msg;
  t.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('show'), 2400);
}

// ---- carga / navegacion -------------------------------------------------
function fileURL(p) { return '/file?path=' + encodeURIComponent(p); }
function baseName(p) { return p.split(/[\\/]/).pop(); }

async function openPath(path) {
  try {
    const r = await fetch('/api/open?path=' + encodeURIComponent(path));
    const j = await r.json();
    if (!j.ok || !j.images || !j.images.length) { toast('No se pudo abrir'); return; }
    state.srcs = j.images.map(fileURL);
    state.names = j.images.map(baseName);
    state.count = j.images.length;
    showIndex(typeof j.index === 'number' ? j.index : 0);
  } catch (e) { toast('Error al abrir'); }
}
window.__lumenOpen = openPath;   // el host lo llama por Eval (dialogo / "abrir con")

function openBlob(url, name) {     // arrastrar-y-soltar desde el Explorador (sin ruta -> una sola)
  state.srcs = [url]; state.names = [name]; state.count = 1;
  showIndex(0);
}

function showIndex(i) {
  if (!state.count) return;
  state.idx = wrap(i);
  const src = state.srcs[state.idx];
  const token = ++loadToken;

  canvas.classList.add('swap');     // fundido de salida
  const pre = new Image();
  pre.onload = () => {
    if (token !== loadToken) return;
    natW = pre.naturalWidth; natH = pre.naturalHeight;
    photo.src = src;
    ambient.style.backgroundImage = `url("${src}")`;
    body.classList.add('has-image');
    measure();
    fitNow();
    if (fitToImage && !firstLoad) bridge('lumenFitTo', natW, natH); // navegación: reajustar la ventana
    firstLoad = false;
    updateCaption();
    requestAnimationFrame(() => canvas.classList.remove('swap'));  // fundido de entrada
    window.__log('image-shown ' + state.names[state.idx]);
    preloadNeighbors();
    bump();
    $('navPrev').classList.toggle('disabled', state.count <= 1);
    $('navNext').classList.toggle('disabled', state.count <= 1);
  };
  pre.onerror = () => {
    if (token !== loadToken) return;
    canvas.classList.remove('swap');
    toast('No se pudo cargar ' + (state.names[state.idx] || ''));
  };
  pre.src = src;
}
function preloadNeighbors() {
  if (state.count <= 1) return;
  [state.idx - 1, state.idx + 1].forEach((k) => {
    const w = wrap(k);
    if (w !== state.idx) { const im = new Image(); im.src = state.srcs[w]; }
  });
}
const prev = () => showIndex(state.idx - 1);
const next = () => showIndex(state.idx + 1);

// ---- pantalla completa --------------------------------------------------
function setFullscreen(on) {
  isFs = on;
  body.classList.toggle('fullscreen', on);
  bridge('lumenFullscreen', on);
  setTimeout(refit, 70);
  setTimeout(refit, 340);
}
const toggleFullscreen = () => setFullscreen(!isFs);

// ---- actividad / inactividad (autoocultar cromo) ------------------------
let idleTimer;
function bump() {
  body.classList.add('active');
  body.classList.remove('idle');
  clearTimeout(idleTimer);
  idleTimer = setTimeout(() => {
    body.classList.remove('active');
    body.classList.add('idle');
  }, 2200);
}

// ---- zoom con rueda -----------------------------------------------------
stage.addEventListener('wheel', (e) => {
  if (!state.count) return;
  e.preventDefault();
  const [cx, cy] = stageXY(e);
  zoomAt(scale * Math.exp(-e.deltaY * 0.0015), cx, cy, false);
  bump();
}, { passive: false });

// ---- doble clic: alterna ajuste / detalle -------------------------------
stage.addEventListener('dblclick', (e) => {
  if (!state.count || e.target.closest('.edge') || e.target.closest('#hud')) return;
  measure();
  if (scale > base * 1.01) {
    scale = base; tx = (vw - natW * scale) / 2; ty = (vh - natH * scale) / 2; apply(true);
  } else {
    const [cx, cy] = stageXY(e);
    zoomAt(base >= 1 ? base * 2.4 : 1, cx, cy, true);
  }
});

// ---- paneo (cuando hay zoom) -------------------------------------------
let panning = false, lastX = 0, lastY = 0;
stage.addEventListener('pointerdown', (e) => {
  if (e.button !== 0 || !state.count) return;
  if (e.target.closest('.edge') || e.target.closest('#hud')) return;
  if (scale <= base * 1.004) return;
  panning = true; lastX = e.clientX; lastY = e.clientY;
  try { stage.setPointerCapture(e.pointerId); } catch (_) {}
  body.style.cursor = 'grabbing';
});
stage.addEventListener('pointermove', (e) => {
  bump();
  if (!panning) return;
  tx += e.clientX - lastX; ty += e.clientY - lastY;
  lastX = e.clientX; lastY = e.clientY;
  clampPan(); apply(false);
});
function endPan(e) {
  if (!panning) return;
  panning = false; body.style.cursor = '';
  try { stage.releasePointerCapture(e.pointerId); } catch (_) {}
}
stage.addEventListener('pointerup', endPan);
stage.addEventListener('pointercancel', endPan);

// ---- navegacion por bordes / botones -----------------------------------
$('navPrev').addEventListener('click', prev);
$('navNext').addEventListener('click', next);
$('btnOpen').addEventListener('click', () => bridge('lumenPick'));

// ---- botones de ventana -------------------------------------------------
$('btnMin').addEventListener('click', () => bridge('lumenMin'));
$('btnClose').addEventListener('click', () => bridge('lumenClose'));
$('btnMax').addEventListener('click', () => {
  bridge('lumenMaxToggle');
  body.classList.toggle('maximized');
});

// ---- toggle: ajustar ventana a la imagen (persistido) ------------------
const btnFit = $('btnFit');
function updateFitBtn() {
  if (!btnFit) return;
  btnFit.classList.toggle('on', fitToImage);
  btnFit.title = fitToImage ? 'Ventana ajustada a la imagen (clic: conservar tamaño)' : 'Ajustar ventana a la imagen';
}
updateFitBtn();
if (btnFit) btnFit.addEventListener('click', () => {
  fitToImage = !fitToImage;
  bridge('lumenSetFit', fitToImage);
  updateFitBtn();
  if (fitToImage && state.count) bridge('lumenFitTo', natW, natH);
});

// ---- arrastre / redimension de la ventana frameless --------------------
let lastTbDown = 0;
$('titlebar').addEventListener('pointerdown', (e) => {
  if (e.button !== 0 || e.target.closest('.winbtn')) return;
  const now = Date.now();
  if (now - lastTbDown < 300) {          // doble clic en la barra = maximizar
    lastTbDown = 0;
    bridge('lumenMaxToggle');
    body.classList.toggle('maximized');
    return;
  }
  lastTbDown = now;
  bridge('lumenDrag');
});
document.querySelectorAll('.rsz').forEach((el) => {
  el.addEventListener('pointerdown', (e) => {
    if (e.button !== 0) return;
    bridge('lumenResize', el.dataset.dir);
  });
});

// ---- teclado ------------------------------------------------------------
window.addEventListener('keydown', (e) => {
  if (e.ctrlKey && (e.key === 'o' || e.key === 'O')) { e.preventDefault(); bridge('lumenPick'); return; }
  switch (e.key) {
    case 'ArrowLeft': case 'PageUp': e.preventDefault(); prev(); break;
    case 'ArrowRight': case 'PageDown': case ' ': e.preventDefault(); next(); break;
    case 'Home': e.preventDefault(); showIndex(0); break;
    case 'End': e.preventDefault(); showIndex(state.count - 1); break;
    case '+': case '=': e.preventDefault(); measure(); zoomAt(scale * 1.25, vw / 2, vh / 2, true); break;
    case '-': case '_': e.preventDefault(); measure(); zoomAt(scale / 1.25, vw / 2, vh / 2, true); break;
    case '0': e.preventDefault(); if (state.count) { measure(); fitNow(); } break;
    case 'f': case 'F': case 'F11': e.preventDefault(); toggleFullscreen(); break;
    case 'Enter': e.preventDefault(); toggleFullscreen(); break;
    case 'Escape':
      e.preventDefault();
      if (isFs) setFullscreen(false);
      else if (scale > base * 1.01 && state.count) { measure(); fitNow(); }
      break;
  }
  bump();
});

// ---- arrastrar y soltar -------------------------------------------------
window.addEventListener('dragover', (e) => { e.preventDefault(); });
window.addEventListener('drop', (e) => {
  e.preventDefault();
  const f = e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files[0];
  if (!f) return;
  const ok = /^image\//.test(f.type) || /\.(jpe?g|jpe|jfif|jif|png|apng|gif|webp|avif|bmp|dib|ico|cur|svg|tiff?)$/i.test(f.name);
  if (!ok) return;
  openBlob(URL.createObjectURL(f), f.name);
});

// ---- varios -------------------------------------------------------------
window.addEventListener('contextmenu', (e) => e.preventDefault());
window.addEventListener('resize', () => {
  refit();
  body.classList.toggle('maximized', !isFs && window.innerWidth >= screen.availWidth - 6);
});

// ---- arranque -----------------------------------------------------------
// La ventana nace fuera de pantalla (anti-flash) y el host la muestra cuando avisamos
// lumenReady. OJO: NO usar requestAnimationFrame para avisar — mientras la ventana esta
// oculta el documento esta "hidden" y el navegador PAUSA rAF, lo que congelaria el aviso
// para siempre. setTimeout / load siguen corriendo aunque este oculto.
let readySent = false;
function sendReady(why) {
  if (readySent) return;
  readySent = true;
  window.__log('ready-sent via ' + why);
  bridge('lumenReady');
}
document.addEventListener('visibilitychange', () => window.__log('vis ' + document.visibilityState));

function boot() {
  window.__log('boot host=' + (typeof window.lumenReady) + ' vis=' + document.visibilityState);
  if (document.readyState === 'complete') sendReady('complete');
  else window.addEventListener('load', () => sendReady('load'));
  setTimeout(() => sendReady('timeout'), 250);
  fetch('/api/initial').then((r) => r.json()).then((j) => {
    if (j && j.path) openPath(j.path);
  }).catch(() => {});
}
boot();
