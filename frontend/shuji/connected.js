(function () {
  'use strict';
  const api = window.ShujiAPI;
  const style = document.createElement('style');
  style.textContent = '.connected-photo{width:min(500px,calc(100vw - 28px));border:1px solid #c0a979;border-radius:16px;background:#faf6ec;color:#342e24;padding:24px;font-family:var(--sans,sans-serif)}.connected-photo::backdrop{background:#171c1bb3}.connected-photo .close{float:right;border:0;background:none;font-size:26px;cursor:pointer}';
  document.head.appendChild(style);
  const $ = id => document.getElementById(id);
  const serverButton = $('serverSelect'); serverButton.textContent = '知识库连接';
  serverButton.onclick = async () => {
    try { const h = await api.request('/health'); toast('库', h.status === 'ok' ? '知识库已连接' : '模型服务待就绪', '文本：' + h.chat.model + ' · 视觉：' + h.vision.model + ' · 已收录 ' + h.index.docs + ' 份资料', 4200); }
    catch (error) { toast('库','知识库连接失败',error.message,4200); }
  };
  const savePanel = document.createElement('dialog'); savePanel.className = 'connected-photo';
  savePanel.innerHTML = '<form method="dialog"><button class="close" aria-label="关闭存档管理">×</button></form><h2>本机存档</h2><p>剧情进度、记忆碎片和小游戏记录保存在这台设备的浏览器中。</p><button type="button" id="export-game-save">导出存档</button><p><label>导入存档 <input id="import-game-save" type="file" accept="application/json,.json"></label></p><p id="save-status" role="status"></p>';
  document.body.appendChild(savePanel);
  const accountButton = $('accountLogin'); accountButton.textContent = '本机存档'; accountButton.onclick = () => { $('save-status').textContent = '已完成 ' + state.main + ' / 5 个主线章节。'; savePanel.showModal(); };
  $('export-game-save').onclick = () => {
    const data = new Blob([JSON.stringify({version:1,state},null,2)],{type:'application/json'});
    const url = URL.createObjectURL(data); const link = document.createElement('a'); link.href = url; link.download = '蜀迹-蓉宝存档.json'; link.click(); setTimeout(()=>URL.revokeObjectURL(url),1000);
    $('save-status').textContent = '存档已导出。';
  };
  $('import-game-save').onchange = async event => {
    try {
      const file = event.target.files[0]; if (!file || file.size > 1024*1024) throw new Error('请选择不超过 1 MB 的存档 JSON');
      const data = JSON.parse(await file.text()); const next = data.state;
      if (data.version !== 1 || !next || !Number.isInteger(next.main) || next.main < 0 || next.main > 5 || !Array.isArray(next.done) || !next.records || typeof next.records !== 'object') throw new Error('存档格式或版本不正确');
      const known = new Set(SITES.map(site=>site.id)); if (next.done.some(id=>!known.has(id))) throw new Error('存档包含未知地点');
      localStorage.setItem(KEY + '-backup',JSON.stringify(state)); localStorage.setItem(KEY,JSON.stringify(next)); location.reload();
    } catch (error) { $('save-status').textContent = '导入失败：' + error.message; }
  };
  const site = new URLSearchParams(location.search).get('site');
  if (site && SITES.some(s => s.id === site)) openPanel(site);
})();
