(function(){
  'use strict';
  const FOODS = [
    {id:'mapo',name:'麻婆豆腐',glyph:'麻',image:'assets/food/mapo.png',place:'第一章后可寻',unlockAt:0,spice:3,desc:'豆腐嫩、牛肉酥、花椒香，讲究麻、辣、烫、香、酥、嫩、鲜、活。',q:'麻婆豆腐鲜明的“麻”主要来自什么？',options:['花椒','桂皮','陈皮'],answer:0},
    {id:'dandan',name:'担担面',glyph:'担',image:'assets/food/dandan.png',place:'第一章后可寻',unlockAt:0,spice:2,desc:'旧时小贩挑担沿街叫卖，面条配芽菜、肉臊和红油，故名“担担面”。',q:'“担担面”这个名字最早与什么有关？',options:['挑担叫卖','担水煮面','扁担形面条'],answer:0},
    {id:'zhong',name:'钟水饺',glyph:'钟',image:'assets/food/zhong.png',place:'都江堰记忆解锁',unlockAt:1,spice:1,desc:'皮薄馅嫩，红油中带微甜蒜香，是成都小吃里温和而鲜明的一味。',q:'钟水饺常见的味型是？',options:['红油微甜','纯酸无辣','椒盐干香'],answer:0},
    {id:'chaoshou',name:'龙抄手',glyph:'抄',image:'assets/food/chaoshou.png',place:'都江堰记忆解锁',unlockAt:1,spice:1,desc:'“抄手”是四川对馄饨的称呼，可配清汤，也可用红油调成鲜香味。',q:'在四川话里，“抄手”指的是哪类食物？',options:['馄饨','烧麦','米线'],answer:0},
    {id:'feipian',name:'夫妻肺片',glyph:'肺',image:'assets/food/feipian.png',place:'武侯祠记忆解锁',unlockAt:2,spice:3,desc:'以卤制牛肉、牛杂切片，拌入红油、花椒与芝麻，香辣爽口。',q:'夫妻肺片常见的主要食材是？',options:['牛肉与牛杂','鱼片','豆腐'],answer:0},
    {id:'huiguo',name:'回锅肉',glyph:'回',image:'assets/food/huiguo.png',place:'北伐记忆解锁',unlockAt:3,spice:2,desc:'肉片先煮再炒，因此有“回锅”之名；蒜苗与郫县豆瓣是经典搭配。',q:'回锅肉名称中的“回锅”指什么？',options:['先煮后炒','反复加水','隔夜再吃'],answer:0},
    {id:'gongbao',name:'宫保鸡丁',glyph:'宫',image:'assets/food/gongbao.png',place:'草堂记忆解锁',unlockAt:4,spice:2,desc:'鸡丁、花生与干辣椒同炒，形成糊辣荔枝味，酸甜咸辣层次相融。',q:'宫保鸡丁中常见的酥香配料是？',options:['花生米','松子','板栗'],answer:0},
    {id:'bingfen',name:'红糖冰粉',glyph:'冰',image:'assets/food/bingfen.png',place:'剑门记忆解锁',unlockAt:5,spice:0,desc:'晶莹冰粉配红糖水与果仁，清凉解辣，是成都街头常见的甜品。',q:'红糖冰粉最适合在什么时候搭配川菜？',options:['解辣收尾','爆炒增香','腌制入味'],answer:0}
  ];
  const DIALECT = [
    {phrase:'扯拐，烂了',audio:'assets/audio/dialect/trouble.mp3',answer:'出了故障，坏掉了',options:['出了故障，坏掉了','拐弯走错路了','把东西扯开了'],note:'“扯拐”常用来表示东西出了问题或发生故障。'},
    {phrase:'相因',audio:'assets/audio/dialect/cheap.mp3',answer:'便宜、划算',options:['便宜、划算','互相帮助','相亲成功'],note:'成都话里的“相因”，常用来形容价格便宜、买得划算。'},
    {phrase:'磨皮擦痒',audio:'assets/audio/dialect/restless.mp3',answer:'烦躁不安、闲不住',options:['烦躁不安、闲不住','认真打磨东西','皮肤需要上药'],note:'“磨皮擦痒”形容人烦躁、不安静，也可表示没事找事。'},
    {phrase:'勺子',audio:'assets/audio/dialect/fool.mp3',answer:'说人有点傻、有点笨',options:['说人有点傻、有点笨','吃饭用的勺子','夸人做事利落'],note:'在四川口语里，“勺子”有时用来调侃人有点傻或犯迷糊。'},
    {phrase:'你晓得个铲铲哦',audio:'assets/audio/dialect/shovel.mp3',answer:'你根本不懂、你知道什么',options:['你根本不懂、你知道什么','你知道铲子放在哪里','你做事特别能干'],note:'这里的“铲铲”是否定语气，“晓得个铲铲”就是“根本不懂”。'}
  ];
  const MATCH = [
    ['mapo','花椒麻香、豆腐嫩滑'],['huiguo','先煮后炒、蒜苗豆瓣'],['gongbao','鸡丁花生、糊辣酸甜'],['bingfen','红糖清凉、餐后解辣']
  ];
  let api=null, overlay=null, lastFocus=null, dialectIndex=0, dialectAudio=null, spiceIndex=0, matchPick='';
  let posterUrl='';
  const getState=()=>api?.getState?.()||{};
  const byId=id=>FOODS.find(f=>f.id===id);
  const stamps=()=>getState().foodStamps||[];
  const unlocked=f=>!!getState().prologue && Number(getState().main||0)>=f.unlockAt;
  const stamped=f=>stamps().includes(f.id);
  const esc=s=>String(s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

  function ensureState(){
    const s=getState();
    if(!Array.isArray(s.foodStamps)) s.foodStamps=[];
    if(!s.foodMini||typeof s.foodMini!=='object') s.foodMini={};
    if(typeof s.foodBadge!=='boolean') s.foodBadge=false;
  }
  function tell(title,detail){ api?.toast?.('味',title,detail); }
  function save(){ api?.saveState?.(); sync(); }
  function award(food,source){
    const s=getState(); ensureState();
    if(!s.foodStamps.includes(food.id)){
      s.foodStamps.push(food.id);
      if(s.foodStamps.length===FOODS.length) s.foodBadge=true;
      save();
      api?.react?.('happy',`获得「${food.name}」印章！`);
      tell(`蜀味印章 ${s.foodStamps.length}/8`,`${food.name}已收入图鉴${source?' · '+source:''}`);
      if(s.foodBadge) setTimeout(()=>tell('成都美食达人','八枚印章集齐，蓉宝美食动画与图鉴海报已解锁。'),350);
    }else{
      tell('这枚印章已经收好',`${food.name}的故事可以随时重温。`);
    }
    renderAlbum(food.id);
  }

  function build(){
    overlay=document.createElement('div');
    overlay.className='food-overlay'; overlay.hidden=true;
    overlay.innerHTML=`<section class="food-shell" role="dialog" aria-modal="true" aria-labelledby="foodTitle">
      <button class="food-close" type="button" aria-label="关闭蜀味寻宝">×</button>
      <header class="food-head"><h2 id="foodTitle">蜀味寻宝 · 印章册</h2><p>跟随章节探访成都味道，答对小问答，集齐八枚蜀味印章。</p></header>
      <span class="food-progress-label" id="foodProgressLabel">0 / 8</span><div class="food-progress" aria-hidden="true"><i id="foodProgressBar"></i></div>
      <nav class="food-tabs" aria-label="蜀味游戏栏目">
        <button class="food-tab" data-tab="album" aria-selected="true">蜀味印章</button>
        <button class="food-tab" data-tab="dialect" aria-selected="false">四川方言</button>
        <button class="food-tab" data-tab="spice" aria-selected="false">辣度挑战</button>
        <button class="food-tab" data-tab="match" aria-selected="false">美食连连看</button>
        <button class="food-tab" data-tab="poster" aria-selected="false">图鉴海报</button>
      </nav>
      <div class="food-body">
        <section class="food-panel" data-panel="album"><div class="food-album"><div class="food-grid" id="foodGrid"></div><aside class="food-detail" id="foodDetail"></aside><div id="foodBadgeSlot"></div></div></section>
        <section class="food-panel" data-panel="dialect" hidden id="dialectPanel"></section>
        <section class="food-panel" data-panel="spice" hidden id="spicePanel"></section>
        <section class="food-panel" data-panel="match" hidden id="matchPanel"></section>
        <section class="food-panel" data-panel="poster" hidden id="posterPanel"></section>
      </div>
    </section>`;
    document.body.appendChild(overlay);
    overlay.querySelector('.food-close').onclick=close;
    overlay.addEventListener('click',e=>{if(e.target===overlay) close();});
    overlay.addEventListener('keydown',e=>{
      if(e.key!=='Tab') return;
      const focusable=[...overlay.querySelectorAll('button:not([disabled]),a[href],[tabindex]:not([tabindex="-1"])')].filter(el=>el.offsetParent!==null);
      if(!focusable.length) return;
      const first=focusable[0],last=focusable[focusable.length-1];
      if(e.shiftKey&&document.activeElement===first){e.preventDefault();last.focus();}
      else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first.focus();}
    });
    overlay.querySelectorAll('.food-tab').forEach(btn=>btn.onclick=()=>selectTab(btn.dataset.tab));
    document.addEventListener('keydown',e=>{if(e.key==='Escape'&&overlay&&!overlay.hidden) close();});
  }

  function selectTab(name){
    if(name!=='dialect') stopDialectAudio();
    overlay.querySelectorAll('.food-tab').forEach(b=>b.setAttribute('aria-selected',String(b.dataset.tab===name)));
    overlay.querySelectorAll('.food-panel').forEach(p=>p.hidden=p.dataset.panel!==name);
    if(name==='album') renderAlbum();
    if(name==='dialect') renderDialect();
    if(name==='spice') renderSpice();
    if(name==='match') renderMatch();
    if(name==='poster') renderPoster();
  }

  function renderAlbum(selectId){
    if(!overlay) return;
    const grid=overlay.querySelector('#foodGrid');
    grid.innerHTML=FOODS.map(f=>`<button class="food-card ${unlocked(f)?'':'food-locked'} ${stamped(f)?'food-stamped':''}" data-food="${f.id}" ${unlocked(f)?'':'aria-disabled="true"'} aria-label="${unlocked(f)?`查看${f.name}图文介绍`:`${f.name}尚未解锁`}">
      <span class="food-card-photo"><img src="${f.image}" alt="" loading="lazy"><span class="food-card-glyph" aria-hidden="true">${f.glyph}</span></span><span class="food-card-copy"><strong>${f.name}</strong><small>${unlocked(f)?f.place:'随主线章节逐步解锁'}</small></span>
      <span class="food-seal">已阅</span>${unlocked(f)?'':'<span class="food-lock">未解锁</span>'}</button>`).join('');
    grid.querySelectorAll('.food-card').forEach(btn=>btn.onclick=()=>{
      const f=byId(btn.dataset.food);
      if(!unlocked(f)){tell('这道味道仍在沉睡','继续推进主线章节，蓉宝会带你找到它。');return;}
      renderDetail(f);
    });
    const selected=byId(selectId)||FOODS.find(f=>unlocked(f)&&!stamped(f))||FOODS.find(unlocked);
    if(selected) renderDetail(selected); else overlay.querySelector('#foodDetail').innerHTML='<div class="empty">先完成第零章，唤醒蓉宝。<br>第一批成都味道会随记忆一同出现。</div>';
    const slot=overlay.querySelector('#foodBadgeSlot');
    if(getState().foodBadge){
      slot.className='food-badge-banner';
      slot.innerHTML='<img src="assets/rongbao-happy.png" alt="开心跳动的蓉宝"><div><strong>成都美食达人 · 已达成</strong><br><span>隐藏蓉宝美食动画与长图海报已经解锁，去“图鉴海报”看看吧。</span></div>';
    }else{slot.className='';slot.innerHTML='';}
    sync();
  }

  function renderDetail(f){
    const d=overlay.querySelector('#foodDetail');
    if(!unlocked(f)){d.innerHTML='<div class="empty">继续主线即可解锁这道成都味道。</div>';return;}
    overlay.querySelectorAll('.food-card').forEach(card=>{
      const current=card.dataset.food===f.id;
      card.classList.toggle('food-current',current);
      if(current) card.setAttribute('aria-current','true'); else card.removeAttribute('aria-current');
    });
    d.innerHTML=`<figure class="food-detail-photo"><img src="${f.image}" alt="${f.name}成品图"><figcaption><span class="food-detail-badge" aria-hidden="true">${f.glyph}</span><span><small>当前菜品</small><strong>${f.name}</strong></span></figcaption></figure><div class="food-place">${f.place}</div><p>${f.desc}</p>
      <div class="spice-meter" aria-label="辣度 ${f.spice} 级">${[0,1,2].map(i=>`<i class="${i<f.spice?'on':''}"></i>`).join('')}</div>
      <div class="food-quiz"><b>${stamped(f)?'印章已获得 · 再答一次温故':'答对即可收下这枚印章'}<br>${f.q}</b>
      ${f.options.map((o,i)=>`<button class="food-answer" data-answer="${i}">${o}</button>`).join('')}<div class="food-feedback" aria-live="polite"></div></div>`;
    d.querySelectorAll('.food-answer').forEach(btn=>btn.onclick=()=>{
      const ok=Number(btn.dataset.answer)===f.answer;
      d.querySelector('.food-feedback').textContent=ok?'答对了！这道味道已经记住。':'还差一点，再想想它的做法与来历。';
      if(ok) award(f,'美食问答'); else api?.react?.('thinking','再想一想这道菜的线索。');
    });
  }

  function renderDialect(){
    const q=DIALECT[dialectIndex%DIALECT.length];
    const panel=overlay.querySelector('#dialectPanel');
    stopDialectAudio();
    const progress=dialectIndex%DIALECT.length+1;
    panel.innerHTML=`<h3 class="food-game-title">四川方言听力</h3><p class="food-game-lead">先听蓉宝说一句四川话，再选出最接近的普通话意思。</p><div class="food-game-card dialect-card">
      <div class="dialect-progress">第 ${progress} / ${DIALECT.length} 题</div>
      <button class="dialect-play" id="dialectPlay" type="button"><span aria-hidden="true">▶</span> 播放方言</button>
      <p class="dialect-audio-status" id="dialectAudioStatus" role="status" aria-live="polite">点击按钮收听本题语音</p>
      <details class="dialect-transcript"><summary>听不清？查看文字提示</summary><p>“${q.phrase}”</p></details>
      <div class="food-game-prompt">这句话最接近下面哪个意思？</div><div class="food-options">${q.options.map(text=>`<button class="food-option" data-answer="${esc(text)}">${esc(text)}</button>`).join('')}</div>
      <p class="food-game-result" aria-live="polite"></p></div>`;
    const playButton=panel.querySelector('#dialectPlay'),audioStatus=panel.querySelector('#dialectAudioStatus');
    playButton.onclick=()=>playDialect(q,playButton,audioStatus);
    panel.querySelectorAll('.food-option').forEach(btn=>btn.onclick=()=>{
      const ok=btn.dataset.answer===q.answer;
      const result=panel.querySelector('.food-game-result');
      result.textContent=ok?`答对了！${q.note}`:'还不对，再播放一次，注意这句方言的语气。';
      if(!ok){api?.react?.('thinking','再听一遍，这句很有成都味。');return;}
      stopDialectAudio();
      panel.querySelector('.dialect-transcript').open=true;
      panel.querySelectorAll('.food-option').forEach(b=>{b.disabled=true;b.classList.toggle('correct',b===btn);});
      dialectIndex++;
      if(dialectIndex%DIALECT.length===0){
        getState().foodMini.dialect=true;save();api?.react?.('happy','五句方言全部听懂啦！');tell('四川方言小达人','五句方言听力全部答对，可以继续重温。');
      }
      setTimeout(renderDialect,1200);
    });
  }

  function stopDialectAudio(){
    if(dialectAudio){dialectAudio.pause();dialectAudio.currentTime=0;dialectAudio=null;}
    window.ShujiBGM?.setDucked?.(false);
  }

  function playDialect(q,button,status){
    stopDialectAudio();
    const clip=new Audio(q.audio);dialectAudio=clip;clip.preload='auto';clip.volume=.95;
    button.disabled=true;status.textContent='正在播放方言…';
    window.ShujiBGM?.setDucked?.(true);
    const settle=text=>{if(dialectAudio!==clip)return;dialectAudio=null;button.disabled=false;status.textContent=text;window.ShujiBGM?.setDucked?.(false);};
    clip.addEventListener('ended',()=>settle('播放完毕，可以再次收听'),{once:true});
    clip.addEventListener('error',()=>settle('语音加载失败，请点击重试'),{once:true});
    Promise.resolve(clip.play()).catch(()=>settle('未能自动播放，请再次点击播放'));
  }

  function renderSpice(){
    const qs=[
      {text:'刚吃完一桌辣菜，哪一道最适合清凉解辣？',answer:'bingfen',options:['feipian','bingfen','mapo']},
      {text:'想挑战“麻辣双重奏”，哪一道辣度最高？',answer:'mapo',options:['zhong','mapo','bingfen']},
      {text:'想点一道辣度温和的红油小吃，选哪道？',answer:'zhong',options:['feipian','zhong','mapo']}
    ];
    const q=qs[spiceIndex%qs.length], panel=overlay.querySelector('#spicePanel');
    panel.innerHTML=`<h3 class="food-game-title">川菜辣度挑战</h3><p class="food-game-lead">辣不只有强弱，还有香辣、麻辣、糊辣与解辣搭配。</p><div class="food-game-card"><div class="food-game-prompt">${q.text}</div><div class="food-options">${q.options.map(id=>`<button class="food-option" data-id="${id}">${byId(id).name}</button>`).join('')}</div><p class="food-game-result" aria-live="polite"></p></div>`;
    panel.querySelectorAll('.food-option').forEach(btn=>btn.onclick=()=>{
      const ok=btn.dataset.id===q.answer;
      panel.querySelector('.food-game-result').textContent=ok?'判断正确，蓉宝已经记下这档辣度！':'这档辣度不太对，再比较一下。';
      if(ok){getState().foodMini.spice=true; const f=byId(q.answer); if(unlocked(f)) award(f,'辣度挑战'); else save(); spiceIndex++; setTimeout(renderSpice,700);}
    });
  }

  function renderMatch(){
    const panel=overlay.querySelector('#matchPanel');
    panel.innerHTML=`<h3 class="food-game-title">美食连连看</h3><p class="food-game-lead">先选左边菜名，再点右边对应的味觉线索；四组配对完成即通关。</p><div class="match-board"><div class="match-col">${MATCH.map(([id])=>`<button class="match-choice" data-food="${id}">${byId(id).name}</button>`).join('')}</div><div class="match-col">${[...MATCH].reverse().map(([id,clue])=>`<button class="match-choice" data-clue="${id}">${clue}</button>`).join('')}</div></div><p class="food-game-result" aria-live="polite">请选择一道菜。</p>`;
    panel.querySelectorAll('[data-food]').forEach(btn=>btn.onclick=()=>{
      if(btn.classList.contains('matched')) return;
      matchPick=btn.dataset.food;
      panel.querySelectorAll('[data-food]').forEach(b=>b.classList.toggle('selected',b===btn));
      panel.querySelector('.food-game-result').textContent=`已选择「${byId(matchPick).name}」，再选它的味觉线索。`;
    });
    panel.querySelectorAll('[data-clue]').forEach(btn=>btn.onclick=()=>{
      if(!matchPick){panel.querySelector('.food-game-result').textContent='请先选择左边的一道菜。';return;}
      if(btn.dataset.clue!==matchPick){panel.querySelector('.food-game-result').textContent='没有连上，再对照菜名和做法想一想。';return;}
      panel.querySelector(`[data-food="${matchPick}"]`).classList.add('matched');
      btn.classList.add('matched'); btn.disabled=true;
      const done=panel.querySelectorAll('[data-clue].matched').length;
      panel.querySelector('.food-game-result').textContent=`配对成功 ${done}/4`;
      matchPick=''; panel.querySelectorAll('[data-food]').forEach(b=>b.classList.remove('selected'));
      if(done===4){getState().foodMini.match=true; save(); tell('美食连连看通关','四道味觉线索全部配对成功。');}
    });
  }

  function renderPoster(){
    const ready=stamps().length===FOODS.length, panel=overlay.querySelector('#posterPanel');
    panel.innerHTML=`<div class="poster-wrap"><div class="poster-copy"><h3>蓉宝的成都美食图鉴</h3><p>把八枚蜀味印章、菜品故事与蓉宝合成一张离线长图。生成后可以直接保存并分享。</p><p><b>${ready?'已满足生成条件':'还需 '+(FOODS.length-stamps().length)+' 枚印章'}</b></p><div class="poster-actions"><button class="poster-btn" id="makeFoodPoster" ${ready?'':'disabled'}>一键生成图鉴海报</button><button class="poster-btn secondary" id="saveFoodPoster" disabled>保存长图</button></div></div><div class="poster-preview" id="posterPreview">${ready?'点击生成，蓉宝来排版。':'集齐八枚印章后，长图将在这里出现。'}</div></div>`;
    panel.querySelector('#makeFoodPoster').onclick=makePoster;
    panel.querySelector('#saveFoodPoster').onclick=()=>{if(!posterUrl)return;const a=document.createElement('a');a.href=posterUrl;a.download='蓉宝的成都美食图鉴.png';a.click();};
  }

  function makePoster(){
    const canvas=document.createElement('canvas'); canvas.width=1080; canvas.height=1680;
    const c=canvas.getContext('2d');
    const grad=c.createLinearGradient(0,0,1080,1680);grad.addColorStop(0,'#f8edc9');grad.addColorStop(.58,'#dfe3bd');grad.addColorStop(1,'#a9c7b1');c.fillStyle=grad;c.fillRect(0,0,1080,1680);
    c.strokeStyle='#78502b';c.lineWidth=18;c.strokeRect(32,32,1016,1616);c.strokeStyle='#d2a95b';c.lineWidth=4;c.strokeRect(55,55,970,1570);
    c.fillStyle='#173f35';c.fillRect(75,82,930,188);c.fillStyle='#ffe8a6';c.textAlign='center';c.font='bold 62px serif';c.fillText('蓉宝的成都美食图鉴',540,165);c.font='26px serif';c.fillText('八味成都 · 一城烟火 · 一路记忆',540,220);
    FOODS.forEach((f,i)=>{const col=i%2,row=Math.floor(i/2),x=90+col*485,y=310+row*230;c.fillStyle='rgba(255,248,220,.88)';roundRect(c,x,y,435,190,20);c.fill();c.strokeStyle='#a77a3f';c.lineWidth=3;c.stroke();c.fillStyle='#a6492d';c.beginPath();c.arc(x+68,y+68,45,0,Math.PI*2);c.fill();c.fillStyle='#fff1c1';c.font='bold 34px serif';c.fillText(f.glyph,x+68,y+80);c.textAlign='left';c.fillStyle='#473326';c.font='bold 31px serif';c.fillText(f.name,x+128,y+58);c.fillStyle='#78634b';c.font='21px serif';wrapText(c,f.desc,x+30,y+125,375,31,2);c.textAlign='center';});
    c.fillStyle='#173f35';roundRect(c,80,1265,920,315,26);c.fill();c.fillStyle='#f7df9f';c.font='bold 38px serif';c.textAlign='left';c.fillText('成都美食达人',420,1350);c.font='24px serif';c.fillStyle='#d8e7db';wrapText(c,'跟着蓉宝穿越古今，也把成都的烟火滋味装进行囊。',420,1400,520,38,3);c.fillStyle='#d7b25f';c.font='20px serif';c.fillText('蜀迹 · 蓉宝寻忆记',420,1525);
    const finish=()=>{posterUrl=canvas.toDataURL('image/png');const p=overlay.querySelector('#posterPreview');p.innerHTML=`<img src="${posterUrl}" alt="蓉宝的成都美食图鉴海报预览">`;overlay.querySelector('#saveFoodPoster').disabled=false;tell('图鉴海报已生成','可以保存为 PNG 长图，离线分享。');};
    const img=new Image();img.onload=()=>{const ratio=Math.min(300/img.width,280/img.height);c.drawImage(img,115,1288,img.width*ratio,img.height*ratio);finish();};img.onerror=finish;img.src='assets/rongbao-happy.png';
  }
  function roundRect(c,x,y,w,h,r){c.beginPath();c.moveTo(x+r,y);c.arcTo(x+w,y,x+w,y+h,r);c.arcTo(x+w,y+h,x,y+h,r);c.arcTo(x,y+h,x,y,r);c.arcTo(x,y,x+w,y,r);c.closePath();}
  function wrapText(c,text,x,y,maxWidth,lineHeight,maxLines){let line='',lines=0;for(const ch of text){const test=line+ch;if(c.measureText(test).width>maxWidth&&line){c.fillText(line,x,y+lines*lineHeight);line=ch;lines++;if(lines>=maxLines)return;}else line=test;}if(line&&lines<maxLines)c.fillText(line,x,y+lines*lineHeight);}

  function sync(){
    const n=stamps().length;
    const count=document.getElementById('foodRailCount');if(count)count.textContent=`${n}/8`;
    if(!overlay)return;
    const label=overlay.querySelector('#foodProgressLabel'),bar=overlay.querySelector('#foodProgressBar');
    if(label)label.textContent=`${n} / 8`;if(bar)bar.style.width=`${n/8*100}%`;
  }
  function open(){
    if(!overlay)build(); ensureState(); lastFocus=document.activeElement; overlay.hidden=false;document.body.style.overflow='hidden';selectTab('album');sync();setTimeout(()=>overlay.querySelector('.food-close')?.focus(),20);
  }
  function close(){if(!overlay)return;stopDialectAudio();overlay.hidden=true;document.body.style.overflow='';lastFocus?.focus?.();}
  function install(options){api=options;ensureState();if(!overlay)build();sync();}
  window.ShujiFoodGame={install,open,close,sync};
})();
