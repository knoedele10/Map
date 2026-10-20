(function(){
  'use strict';
  const SITES = {sanxingdui:'三星堆',dujiangyan:'都江堰',wuhou:'武侯祠',dufu:'杜甫草堂',jianmen:'剑门关'};
  const AFTER_SOURCES = {
    sanxingdui:'assets/audio/revisit/sanxingdui.mp3',
    dujiangyan:'assets/audio/revisit/dujiangyan.mp3',
    wuhou:'assets/audio/revisit/wuhou.mp3',
    dufu:'assets/audio/revisit/dufu.m4a',
    jianmen:'assets/audio/revisit/jianmen.m4a'
  };
  const KEY = 'shuji-exploration-voice-enabled';
  let enabled = true;
  try { enabled = localStorage.getItem(KEY) !== '0'; } catch(_) {}
  let audio,bar,status,playButton,muteButton;
  let current = null, generation = 0, phase = 'idle';

  function duck(value){ if(window.ShujiBGM) window.ShujiBGM.setDucked(value); }

  function ensureUI(){
    if(audio) return;
    audio = document.createElement('audio');
    audio.id = 'explorationVoicePlayer';audio.preload = 'metadata';audio.volume = 0.95;
    audio.setAttribute('aria-hidden','true');document.body.appendChild(audio);
    bar = document.createElement('div');bar.id = 'explorationVoice';bar.hidden = true;
    bar.setAttribute('role','group');bar.setAttribute('aria-label','章节探索语音');
    status = document.createElement('span');status.id = 'explorationVoiceStatus';
    status.setAttribute('role','status');status.setAttribute('aria-live','polite');
    playButton = document.createElement('button');playButton.type = 'button';playButton.id = 'voicePlay';
    playButton.onclick = ()=>{if(!audio.paused && !audio.ended){stop(false);}else{play();}};
    muteButton = document.createElement('button');muteButton.type = 'button';muteButton.id = 'voiceMute';
    muteButton.onclick = ()=>{
      enabled = !enabled;
      try { localStorage.setItem(KEY,enabled?'1':'0'); } catch(_) {}
      if(enabled) play(); else stop(false);
      updateUI();
    };
    bar.append(status,playButton,muteButton);document.body.appendChild(bar);
    audio.addEventListener('ended',()=>{phase='ended';duck(false);updateUI();});
    audio.addEventListener('error',()=>{if(current){phase='error';duck(false);updateUI();}});
  }

  function updateUI(){
    if(!bar) return;
    bar.hidden = !current;
    if(!current) return;
    const playing = phase === 'playing' && !audio.paused;
    const label = current.label || `${SITES[current.site]} · ${current.kind==='after'?'探索完成':'探索前'}`;
    const text = !enabled ? '语音已关闭' : ({loading:'语音加载中',playing:'蓉宝正在讲述',ended:'讲述结束',stopped:'讲述已停止',error:'语音暂未播放，可点击重播'}[phase] || '准备讲述');
    status.textContent = `${label} · ${text}`;
    playButton.textContent = playing ? '停止讲述' : phase==='loading'?'正在加载…':'重播语音';
    playButton.disabled = !enabled || phase==='loading';
    playButton.setAttribute('aria-label',`${playing?'停止':'重播'}${label}语音`);
    muteButton.textContent = enabled ? '关闭语音' : '开启语音';
    muteButton.setAttribute('aria-pressed',String(enabled));
    muteButton.setAttribute('aria-label',enabled?'关闭探索语音，背景音乐继续播放':'开启探索语音');
    bar.dataset.site=current.site||'special';bar.dataset.kind=current.kind||'before';bar.dataset.state=phase;
  }

  function play(){
    if(!current || !enabled) return;
    const token = ++generation;
    audio.pause();audio.currentTime=0;phase='loading';updateUI();
    const promise=audio.play();
    Promise.resolve(promise).then(()=>{
      if(token!==generation || !current || !enabled) return;
      phase='playing';duck(true);updateUI();
    }).catch(()=>{
      if(token!==generation || !current) return;
      phase='error';duck(false);updateUI();
    });
  }

  function stop(clear=true){
    ++generation;
    if(audio){audio.pause();audio.currentTime=0;}
    phase='stopped';duck(false);
    if(clear) current=null;
    updateUI();
  }

  function begin(site){
    ensureUI();stop();
    if(!SITES[site]) return;
    const src=`assets/audio/first/${site}.wav`;
    current={site,src,kind:'before'};
    audio.src=src;
    audio.load();phase='idle';updateUI();
    if(enabled) play();
  }

  function playAfter(site){
    ensureUI();stop();
    if(!SITES[site] || !AFTER_SOURCES[site]) return;
    const src=AFTER_SOURCES[site];
    current={site,src,kind:'after'};
    audio.src=src;
    audio.load();phase='idle';updateUI();
    if(enabled) play();
  }

  function playClip(src,label){
    ensureUI();stop();
    if(!src) return;
    current={site:'',src,label:label||'蓉宝语音',kind:'special'};
    audio.src=src;audio.load();phase='idle';updateUI();
    if(enabled) play();
  }

  window.ShujiVoice={begin,playAfter,playClip,stop,has:site=>!!SITES[site]};
})();
