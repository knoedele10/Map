(function(){
  'use strict';

  const TRACKS = {
    map:        {src:'assets/audio/map.mp3',        label:'蜀迹地图 · 蓉城寻迹'},
    sanxingdui: {src:'assets/audio/sanxingdui.mp3', label:'三星堆 · 神秘祭祀'},
    dujiangyan: {src:'assets/audio/dujiangyan.mp3', label:'都江堰 · 流水千年'},
    wuhou:      {src:'assets/audio/wuhou.mp3',      label:'武侯祠 · 君臣合祀'},
    dufu:       {src:'assets/audio/dufu.mp3',       label:'杜甫草堂 · 诗意清幽'},
    jianmen:    {src:'assets/audio/jianmen.mp3',    label:'剑门关 · 金戈铁马'}
  };
  const SITE_TRACK = {
    sanxingdui:'sanxingdui', dujiangyan:'dujiangyan', dufu:'dufu', jianmen:'jianmen',
    map:'map', prologue:'map', wuhou:'wuhou', finale:'map'
  };
  const STORAGE_KEY = 'shuji-bgm-enabled';
  const TARGET_VOLUME = 0.34;
  let player = null;
  let toggle = null;
  let currentKey = '';
  let requestedKey = 'map';
  let enabled = true;
  let fadeTimer = 0;
  let operation = 0;
  let envelope = 0;
  let duckGain = 1;
  let duckTimer = 0;

  try { enabled = localStorage.getItem(STORAGE_KEY) !== '0'; } catch(_) {}

  function ensureUI(){
    if(player && toggle) return;
    player = document.getElementById('bgmPlayer');
    if(!player){
      player = document.createElement('audio');
      player.id = 'bgmPlayer';
      player.loop = true;
      player.preload = 'metadata';
      player.setAttribute('aria-hidden','true');
      document.body.appendChild(player);
    }
    toggle = document.getElementById('bgmToggle');
    if(!toggle){
      toggle = document.createElement('button');
      toggle.id = 'bgmToggle';
      toggle.type = 'button';
      toggle.innerHTML = '<span class="bgm-icon" aria-hidden="true">♪</span><span class="bgm-label">背景音乐</span>';
      toggle.addEventListener('click', toggleEnabled);
      document.body.appendChild(toggle);
    }
    player.addEventListener('playing', updateUI);
    player.addEventListener('pause', updateUI);
    player.addEventListener('error', ()=>updateUI('error'));
    updateUI();
  }

  function resolveKey(scene){ return SITE_TRACK[scene] || (TRACKS[scene] ? scene : 'map'); }

  function updateUI(status){
    if(!toggle) return;
    const track = TRACKS[requestedKey] || TRACKS.map;
    const playing = enabled && player && !player.paused && !player.ended;
    toggle.classList.toggle('is-playing', playing);
    toggle.classList.toggle('is-muted', !enabled);
    toggle.classList.toggle('is-error', status === 'error');
    toggle.setAttribute('aria-pressed', String(enabled));
    toggle.dataset.track = requestedKey;
    document.documentElement.dataset.bgmTrack = requestedKey;
    document.documentElement.dataset.bgmPlaying = String(playing);
    const stateText = enabled ? (playing ? '正在播放' : '准备播放') : '已静音';
    toggle.querySelector('.bgm-label').textContent = enabled ? '背景音乐' : '音乐已关';
    toggle.title = `${stateText}：${track.label}`;
    toggle.setAttribute('aria-label', `${stateText}：${track.label}。点击${enabled?'静音':'播放'}`);
  }

  function clearFade(){ if(fadeTimer){ clearInterval(fadeTimer); fadeTimer = 0; } }

  function applyVolume(){ if(player) player.volume = Math.max(0,Math.min(1,envelope*duckGain)); }

  // 人声压低音乐只调整增益，不打断正在进行的章节音乐转场。
  function setDucked(ducked){
    if(duckTimer) clearInterval(duckTimer);
    const from = duckGain, to = ducked ? 0.22 : 1, started = performance.now();
    duckTimer = setInterval(()=>{
      const p = Math.min(1,(performance.now()-started)/220);
      duckGain = from+(to-from)*p; applyVolume();
      if(p>=1){ clearInterval(duckTimer); duckTimer=0; }
    },30);
  }

  function fadeTo(target, duration, done){
    clearFade();
    if(!player){ if(done) done(); return; }
    const from = envelope;
    const started = performance.now();
    fadeTimer = setInterval(()=>{
      const p = Math.min(1,(performance.now()-started)/duration);
      envelope = from+(target-from)*p; applyVolume();
      if(p >= 1){ clearFade(); if(done) done(); }
    },30);
  }

  function beginTrack(key, op){
    if(op !== operation || !enabled) return;
    const track = TRACKS[key] || TRACKS.map;
    if(currentKey !== key){
      player.src = track.src;
      player.load();
      currentKey = key;
    }
    envelope = 0; applyVolume();
    const result = player.play();
    if(result && typeof result.then === 'function'){
      result.then(()=>{
        if(op !== operation || !enabled) return;
        fadeTo(TARGET_VOLUME,560,updateUI);
        updateUI();
      }).catch(()=>{if(op===operation) updateUI('error');});
    }else{
      fadeTo(TARGET_VOLUME,560,updateUI);
      updateUI();
    }
  }

  function playFor(scene){
    ensureUI();
    requestedKey = resolveKey(scene);
    const op = ++operation;
    updateUI();
    if(!enabled){
      clearFade();envelope=0;applyVolume();player.pause();updateUI();return;
    }
    if(currentKey === requestedKey){
      if(player.paused){ beginTrack(requestedKey,op); }
      else fadeTo(TARGET_VOLUME,220,updateUI);
      return;
    }
    if(currentKey && !player.paused){
      fadeTo(0,360,()=>{ if(op === operation){ player.pause(); beginTrack(requestedKey,op); } });
    }else{
      beginTrack(requestedKey,op);
    }
  }

  function toggleEnabled(){
    ensureUI();
    enabled = !enabled;
    try { localStorage.setItem(STORAGE_KEY,enabled?'1':'0'); } catch(_) {}
    const op = ++operation;
    if(enabled){
      beginTrack(requestedKey,op);
    }else{
      fadeTo(0,260,()=>{ if(op === operation){ player.pause(); updateUI(); } });
      updateUI();
    }
  }

  function pause(){
    ensureUI();
    ++operation;
    fadeTo(0,220,()=>{ player.pause(); updateUI(); });
  }

  window.ShujiBGM = {playFor,toggle:toggleEnabled,pause,setDucked,getState:()=>({enabled,currentKey,requestedKey,playing:!!player&&!player.paused})};
  if(document.readyState === 'loading') document.addEventListener('DOMContentLoaded',ensureUI,{once:true});
  else ensureUI();
})();
