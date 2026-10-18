(function () {
  'use strict';
  const token = () => { try { return localStorage.getItem('rag_token') || ''; } catch (_) { return ''; } };
  const headers = (extra = {}) => Object.assign({}, extra, token() ? { Authorization: 'Bearer ' + token() } : {});
  async function request(path, options = {}) {
    const opts = Object.assign({}, options, {headers: headers(options.headers)});
    if (options.body && !(options.body instanceof FormData)) {
      opts.body = JSON.stringify(options.body);
      opts.headers['Content-Type'] = 'application/json';
    }
    const response = await fetch('/api/v1' + path, opts);
    const envelope = await response.json();
    if (!response.ok || envelope.code !== 0) throw new Error(envelope.message || '请求失败：' + response.status);
    return envelope.data;
  }
  async function readImage(file) {
    if (!file || !/^image\/(jpeg|png|webp)$/.test(file.type)) throw new Error('请选择 JPG、PNG 或 WebP 图片');
    if (file.size > 20 * 1024 * 1024) throw new Error('原图不能超过 20 MB');
    const source = await new Promise((resolve, reject) => {
      const reader = new FileReader(); reader.onload = () => resolve(reader.result); reader.onerror = () => reject(new Error('读取图片失败')); reader.readAsDataURL(file);
    });
    const image = await new Promise((resolve, reject) => {
      const img = new Image(); img.onload = () => resolve(img); img.onerror = () => reject(new Error('图片内容无法解码')); img.src = source;
    });
    const ratio = Math.min(1, 1600 / Math.max(image.naturalWidth, image.naturalHeight));
    const canvas = document.createElement('canvas'); canvas.width = Math.max(1, Math.round(image.naturalWidth * ratio)); canvas.height = Math.max(1, Math.round(image.naturalHeight * ratio));
    const ctx = canvas.getContext('2d'); ctx.fillStyle = '#fff'; ctx.fillRect(0, 0, canvas.width, canvas.height); ctx.drawImage(image, 0, 0, canvas.width, canvas.height);
    return canvas.toDataURL('image/jpeg', 0.85);
  }
  async function sampleImage() {
    const res = await fetch('/static/shuji/assets/wuhou/image1.png');
    if (!res.ok) throw new Error('示例照片加载失败');
    return readImage(await res.blob());
  }
  window.ShujiAPI = {request, readImage, headers, sampleImage};
})();
