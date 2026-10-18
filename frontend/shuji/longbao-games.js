(function(){
  'use strict';
  const el=(tag,cls,text)=>{const n=document.createElement(tag);if(cls)n.className=cls;if(text!==undefined)n.textContent=text;return n;};
  const shuffle=a=>a.map(v=>({v,r:Math.random()})).sort((x,y)=>x.r-y.r).map(x=>x.v);

  function shell(root,node){
    root.style.display='';root.innerHTML='';
    const box=el('div','lb-game');
    box.append(el('h3','',node.title),el('div','lb-sub',node.sub||'失败不会扣除资源，可随时重试。'));
    const body=el('div','lb-body'),status=el('div','lb-status','准备好了就开始。');
    box.append(body,status);root.appendChild(box);return{box,body,status};
  }
  function wrong(btn,status,msg){btn.classList.add('wrong');status.textContent=msg||'还差一点，再试一次。';if(window.Rongbao)window.Rongbao.react('confused','再观察一下，不扣分哦',1500);setTimeout(()=>btn.classList.remove('wrong'),380);}
  function progress(parent){const p=el('div','lb-progress');const i=el('i');p.appendChild(i);parent.appendChild(p);return i;}
  function tabs(parent,names,idx){const row=el('div','lb-stage-tabs');names.forEach((x,i)=>{const s=el('span',i===idx?'on':'',x);row.appendChild(s)});parent.appendChild(row)}
  function completeFactory(node,done){const start=performance.now();let closed=false;return()=>{if(closed)return;closed=true;if(window.Rongbao)window.Rongbao.react('happy','完成啦！',1900);done({game:node.game,durationMs:Math.round(performance.now()-start)});};}

  function chapterGallery(parent,title,groups){
    const gallery=el('details','lb-place-gallery');gallery.open=true;
    const summary=el('summary','','实景图册 · '+title);gallery.appendChild(summary);
    const content=el('div','lb-place-gallery-content');
    const heroWrap=el('figure','lb-place-hero');const hero=el('img','');hero.loading='eager';const caption=el('figcaption','');heroWrap.append(hero,caption);
    const browse=el('div','lb-place-browser');const groupTabs=el('div','lb-place-tabs');const thumbs=el('div','lb-place-thumbs');browse.append(groupTabs,thumbs);content.append(heroWrap,browse);gallery.appendChild(content);parent.appendChild(gallery);
    const showImage=(group,src,index)=>{hero.src=src;hero.alt=`${title} · ${group.name} · 图片 ${index+1}`;caption.textContent=`${group.name} · ${index+1} / ${group.images.length}`;thumbs.querySelectorAll('button').forEach((button,i)=>{button.classList.toggle('on',i===index);button.setAttribute('aria-current',i===index?'true':'false')})};
    const showGroup=(index)=>{const group=groups[index];groupTabs.querySelectorAll('button').forEach((button,i)=>{button.classList.toggle('on',i===index);button.setAttribute('aria-pressed',String(i===index))});thumbs.innerHTML='';group.images.forEach((src,imageIndex)=>{const button=el('button','lb-place-thumb');button.type='button';button.setAttribute('aria-label',`查看${group.name}第 ${imageIndex+1} 张图片`);const image=el('img','');image.src=src;image.alt='';image.loading='lazy';button.appendChild(image);button.onclick=()=>showImage(group,src,imageIndex);thumbs.appendChild(button)});showImage(group,group.images[0],0)};
    groups.forEach((group,index)=>{const button=el('button','lb-place-tab',group.name);button.type='button';button.setAttribute('aria-pressed',String(index===0));button.onclick=()=>showGroup(index);groupTabs.appendChild(button)});
    showGroup(0);
  }

  function prologueExplore(ui,finish){
    ui.status.textContent='先在老巷里看看。所有探索都没有错误答案。';
    const places=[
      ['盖碗茶馆','白气从茶碗边沿升起，说书人的惊堂木刚刚落下。'],
      ['糖油果子摊','甜香和火锅底料的辛香混在一起，这是成都巷陌的傍晚。'],
      ['博物馆橱窗','闭馆音乐响起，熊猫蓉宝仍盯着青铜纵目面具。']
    ];
    const row=el('div','lb-grid');let seen=0;
    places.forEach(([name,desc])=>{const b=el('button','lb-card',name);b.onclick=()=>{if(b.classList.contains('hit'))return;b.classList.add('hit');b.innerHTML=`<b>${name}</b><small>${desc}</small>`;seen++;ui.status.textContent=`已探索 ${seen} / ${places.length}`;if(seen===places.length)setTimeout(finish,650)};row.appendChild(b)});
    ui.body.appendChild(row);
  }

  function longpress(ui,finish){
    tabs(ui.body,['物品卡','触摸记忆'],0);
    const card=el('section','lb-item-card');
    card.innerHTML='<b>青铜纵目挂件</b><p>断绳处拾得的旅游纪念品。来源：四川文博文创店。触碰时似乎带着温度。</p>';
    const take=el('button','lb-big-action','确认并收入背包栏');card.appendChild(take);ui.body.appendChild(card);
    take.onclick=()=>{
      ui.body.innerHTML='';tabs(ui.body,['物品卡','触摸记忆'],1);
      const ring=el('button','lb-hold','按住挂件 1.5 秒'),bar=progress(ui.body);ui.body.appendChild(ring);ui.status.textContent='长按建立“触碰即闪回”的规则。';
      let timer=0,raf=0,start=0;
      const stop=()=>{clearTimeout(timer);cancelAnimationFrame(raf);if(start){start=0;bar.style.width='0';ui.status.textContent='松早了也没关系，再按住一次。'}};
      const tick=()=>{if(!start)return;bar.style.width=Math.min(100,(performance.now()-start)/15)+'%';raf=requestAnimationFrame(tick)};
      const begin=e=>{e.preventDefault();if(start)return;start=performance.now();ring.setPointerCapture&&ring.setPointerCapture(e.pointerId);ui.status.textContent='青铜正在发热……';tick();timer=setTimeout(()=>{start=0;cancelAnimationFrame(raf);bar.style.width='100%';ring.textContent='记忆已唤醒';ring.classList.add('hit');setTimeout(finish,500)},1500)};
      ring.addEventListener('pointerdown',begin);ring.addEventListener('pointerup',stop);ring.addEventListener('pointercancel',stop);ring.addEventListener('pointerleave',e=>{if(e.buttons===0)stop()});
    };
  }

  function bronze(ui,finish,standalone=false){
    const storyScreen=document.getElementById('storyScreen');if(storyScreen&&!standalone)storyScreen.classList.add('relic-viewer-active');
    const relics=[
      {id:'standing',mark:'立',name:'商青铜立人像',role:'核心文物 · 离线三维',model:'assets/models/guardian-statue.glb',cover:'assets/sanxingdui/standing-1.png',images:['assets/sanxingdui/standing-1.png','assets/sanxingdui/standing-2.png','assets/sanxingdui/standing-3.png','assets/sanxingdui/standing-4.png'],brief:'高冠长衣、双手环握，立于高台之上，是三星堆最具代表性的青铜人物形象之一。',detail:'立人像以夸张而庄严的比例突出身份。双手呈环握姿态，衣纹、冠饰与台座保留了大量可供观察的细节。三维模型用于游戏化观察，并非文物扫描件。',memory:'我记得这双手——它们曾托举祭礼中最重要的信物。'},
      {id:'tree',mark:'树',name:'商青铜神树',role:'通天神树 · 离线三维',model:'assets/models/bronze-tree.glb',cover:'assets/sanxingdui/tree-1.png',images:['assets/sanxingdui/tree-1.png','assets/sanxingdui/tree-2.png','assets/sanxingdui/tree-3.png','assets/sanxingdui/tree-4.png','assets/sanxingdui/tree-5.png'],brief:'树干、枝叶、神鸟与盘曲的龙共同组成复杂结构，呈现古蜀人关于天地沟通的想象。',detail:'青铜神树由树座、树干和多层枝条构成，枝端可见神鸟等形象。三维模型用于展示复杂轮廓，具体祭仪仍需结合考古语境谨慎理解。',memory:'火光沿着枝干向上，我听见鼓声，也听见人群在等待回应。'},
      {id:'goldHead',mark:'金',name:'商带金面罩青铜人头像',role:'核心文物 · 离线三维',model:'assets/models/golden-guardian-mask.glb',cover:'assets/sanxingdui/gold-head-1.png',images:['assets/sanxingdui/gold-head-1.png','assets/sanxingdui/gold-head-2.png','assets/sanxingdui/gold-head-3.png','assets/sanxingdui/gold-head-4.png'],brief:'青铜头像表面覆以金面罩，金色与青铜锈色形成强烈对比，强化了神圣与权威感。',detail:'这件头像把金箔面罩与青铜人物造型结合在一起。三维模型便于观察面罩边缘、发式、耳部与颈部的处理，属于游戏美术化还原。',memory:'金光闪过时，众人低下头；被看见的不是一个人，而是一种共同相信的秩序。'},
      {id:'mask',mark:'目',name:'商青铜纵目面具',role:'纵目面具 · 离线三维',model:'assets/models/verdigris-guardian.glb',cover:'assets/sanxingdui/mask-1.png',images:['assets/sanxingdui/mask-1.png','assets/sanxingdui/mask-2.png','assets/sanxingdui/mask-3.png'],brief:'突出的眼部、夸张的耳朵和宽阔面容构成极具辨识度的造型。',detail:'纵目面具通过超常比例强调“看见”与“聆听”。三维模型帮助观察眼部、耳部及面部纹饰，也是第零章挂件与蓉宝记忆苏醒的重要视觉来源。',memory:'就是这双眼睛唤醒了我。它看见山川，也看见部族每个人的愿望。'},
      {id:'altar',mark:'坛',name:'青铜神坛',role:'祭祀空间 · 文档图片',cover:'assets/sanxingdui/altar-1.png',images:['assets/sanxingdui/altar-1.png','assets/sanxingdui/altar-2.png','assets/sanxingdui/altar-3.png','assets/sanxingdui/altar-4.png'],brief:'青铜神坛以人物、器物等形象构成丰富的祭祀场景。',detail:'这些图片来自用户提供的三星堆资料，可逐张查看整体与细部；其具体含义仍需结合考古研究理解。',memory:'层层叠起的形象，让我记起祭礼与人群。'},
      {id:'beast',mark:'兽',name:'古蜀青铜神兽',role:'神兽意象 · 离线三维',model:'assets/models/bronze-beast.glb',cover:'assets/sanxingdui/altar-2.png',images:['assets/sanxingdui/altar-2.png'],brief:'青铜神兽把角、獠牙和昂首姿态组合为强烈的守护形象。',detail:'本展项根据提供的青铜神兽模型制作，用来表现古蜀祭祀空间中的神兽意象。模型属于艺术化展示，并非对某件出土文物的精确数字化扫描。',memory:'神兽守在火光之外，像是在替所有无法说出口的愿望站岗。'},
      {id:'guardian',mark:'卫',name:'古蜀青铜守护像',role:'守护者形象 · 离线三维',model:'assets/models/ancient-guardian.glb',cover:'assets/sanxingdui/altar-1.png',images:['assets/sanxingdui/altar-1.png'],brief:'守护像以庄严姿态进入蓉宝的祭祀记忆，连接人物、神兽与仪式空间。',detail:'本展项使用提供的古蜀守护者模型，是剧情中的艺术化记忆形象。它帮助玩家理解蓉宝“守护历史记忆”的角色来源，不作为考古复原结论。',memory:'我终于想起守护的含义：不是把历史锁住，而是让后来的人仍能听见。'}
    ];
    const coreIds=new Set(['standing','goldHead']);
    const viewed=new Set();
    let selected=0,angle=0,frame=0,dragging=false,startX=0,startAngle=0,lastTrigger=null;

    ui.box.classList.add('lb-relic-game');
    ui.status.setAttribute('aria-live','polite');
    ui.status.textContent='先从文物目录选择展品；点开后才进入该文物的离线三维观察与具体介绍。';
    const intro=el('div','lb-relic-intro');
    intro.innerHTML='<img src="assets/sanxingdui/overview.png" alt="三星堆展厅中的金面具展陈"><div><b>三星堆记忆展柜</b><span>自由浏览文物；展柜不影响剧情进度。</span></div>';
    const layout=el('div','lb-relic-layout lb-relic-catalog');
    const nav=el('nav','lb-relic-nav');nav.setAttribute('aria-label','三星堆文物目录');
    const navTitle=el('div','lb-relic-nav-title','文物小目录');nav.appendChild(navTitle);
    const stage=el('section','lb-relic-stage');
    const stageHead=el('div','lb-relic-stage-head');
    const stageKicker=el('span','', '当前文物');
    const stageName=el('h4','');stageHead.append(stageKicker,stageName);
    const model=el('div','lb-relic-model');model.tabIndex=0;model.setAttribute('role','group');model.setAttribute('aria-roledescription','可旋转文物观察器');
    const turntable=el('div','lb-turntable');const modelImg=el('img','lb-model-image');const modelGlow=el('i','lb-model-glow');turntable.append(modelGlow,modelImg);model.appendChild(turntable);
    const hint=el('div','lb-drag-hint','↔ 拖拽旋转 · ← → 按键观察');
    const controls=el('div','lb-model-controls');
    const left=el('button','lb-rotate','向左旋转');left.type='button';left.setAttribute('aria-label','文物向左旋转');
    const reset=el('button','lb-rotate','回到正面');reset.type='button';
    const right=el('button','lb-rotate','向右旋转');right.type='button';right.setAttribute('aria-label','文物向右旋转');controls.append(left,reset,right);
    const viewDetail=el('button','lb-detail-open','查看这件文物的具体介绍');viewDetail.type='button';
    stage.append(stageHead,model,hint,controls,viewDetail);

    const guide=el('aside','lb-era-guide');
    const guidePortrait=el('div','lb-era-portrait');guidePortrait.setAttribute('role','img');guidePortrait.setAttribute('aria-label','蓉宝在古蜀祭祀时期的形象');
    const guideCopy=el('div','lb-era-copy');guideCopy.innerHTML='<span>记忆形态 01</span><h4>蓉宝 · 古蜀祭司</h4><p>青铜冠饰与羽纹礼服来自蓉宝对古蜀祭祀身份的艺术化记忆。</p>';
    const quote=el('blockquote','lb-era-quote');
    const progressText=el('div','lb-relic-progress','核心文物 0 / 2');
    const finishBtn=el('button','lb-relic-finish','查看两件核心文物后继续');finishBtn.type='button';finishBtn.disabled=true;
    guide.append(guidePortrait,guideCopy,quote);if(!standalone)guide.append(progressText,finishBtn);
    layout.append(nav,stage,guide);ui.body.append(intro,layout);

    const dialog=el('div','lb-relic-dialog');dialog.hidden=true;dialog.setAttribute('role','dialog');dialog.setAttribute('aria-modal','true');dialog.setAttribute('aria-labelledby','lbRelicDialogTitle');
    const dialogCard=el('section','lb-relic-dialog-card');
    const close=el('button','lb-dialog-close','关闭');close.type='button';close.setAttribute('aria-label','关闭文物介绍');
    const gallery=el('div','lb-relic-gallery');
    const live=el('div','lb-relic-live');live.hidden=true;
    const liveViewport=el('div','lb-relic-live-viewport');
    const liveStatus=el('div','lb-relic-live-status','正在准备三维文物……');
    const liveToolbar=el('div','lb-relic-live-toolbar');
    const liveReset=el('button','lb-relic-live-button','重置视角');liveReset.type='button';
    const liveRotate=el('button','lb-relic-live-button','暂停旋转');liveRotate.type='button';liveRotate.setAttribute('aria-pressed','true');
    liveToolbar.append(liveReset,liveRotate);live.append(liveViewport,liveStatus,liveToolbar);
    const hero=el('img','lb-relic-hero');const thumbs=el('div','lb-relic-thumbs');gallery.append(live,hero,thumbs);
    const copy=el('div','lb-relic-detail');const badge=el('span','lb-relic-badge');const title=el('h4','');title.id='lbRelicDialogTitle';const brief=el('p','lb-relic-brief');const detail=el('p','lb-relic-description');const memory=el('blockquote','lb-relic-memory');
    const back=el('button','lb-dialog-back','关闭介绍 · 回到展柜');back.type='button';copy.append(badge,title,brief,detail,memory,back);dialogCard.append(close,gallery,copy);dialog.appendChild(dialogCard);ui.box.appendChild(dialog);
    let active3D=null,autoRotate=true;

    const normalize=a=>((a%360)+360)%360;
    function updateModel(){
      const r=relics[selected],norm=normalize(angle),nextFrame=Math.round(norm/360*r.images.length)%r.images.length;
      if(nextFrame!==frame||modelImg.src===''){frame=nextFrame;modelImg.src=r.images[frame];modelImg.alt=`${r.name}第 ${frame+1} 个观察角度`;}
      const tilt=Math.max(-18,Math.min(18,(normalize(angle+180)-180)/8));
      turntable.style.transform=`rotateY(${tilt}deg) rotateX(${Math.abs(tilt)*-.08}deg)`;
      model.setAttribute('aria-label',`${r.name}，第 ${frame+1} 个观察角度，共 ${r.images.length} 个。可拖拽或使用左右方向键旋转。`);
    }
    function rotate(delta){angle+=delta;updateModel();}
    left.onclick=()=>rotate(-360/relics[selected].images.length);
    right.onclick=()=>rotate(360/relics[selected].images.length);
    reset.onclick=()=>{angle=0;updateModel();model.focus();};
    model.onkeydown=e=>{if(e.key==='ArrowLeft'){e.preventDefault();rotate(-360/relics[selected].images.length)}else if(e.key==='ArrowRight'){e.preventDefault();rotate(360/relics[selected].images.length)}else if(e.key==='Home'){e.preventDefault();angle=0;updateModel()}};
    model.onpointerdown=e=>{dragging=true;startX=e.clientX;startAngle=angle;model.setPointerCapture&&model.setPointerCapture(e.pointerId);model.classList.add('dragging')};
    model.onpointermove=e=>{if(!dragging)return;angle=startAngle+(e.clientX-startX)*.75;updateModel()};
    const stopDrag=()=>{dragging=false;model.classList.remove('dragging')};model.onpointerup=stopDrag;model.onpointercancel=stopDrag;

    function updateProgress(){
      if(standalone){ui.status.textContent='自由查看文物。关闭展柜后将回到原来的剧情位置。';return}
      const count=[...coreIds].filter(id=>viewed.has(id)).length;
      progressText.textContent=`核心文物 ${count} / ${coreIds.size}`;
      finishBtn.disabled=count<coreIds.size;finishBtn.textContent=count<coreIds.size?'查看两件核心文物后继续':'完成鉴赏 · 回到剧情';
      ui.status.textContent=count<coreIds.size?`已确认 ${count} / ${coreIds.size} 件核心文物。其余文物也可自由查看。`:'核心记忆已经确认，可以继续剧情；其余文物仍可自由查看。';
    }
    function closeDialog(){
      if(active3D){active3D.dispose();active3D=null}
      dialog.hidden=true;dialog.classList.remove('open');document.removeEventListener('keydown',trapDialog,true);if(lastTrigger)lastTrigger.focus();
      if(standalone)document.getElementById('chapterCabinetClose').hidden=false;
    }
    function trapDialog(e){
      if(e.key==='Escape'){e.preventDefault();e.stopPropagation();closeDialog();return}
      if(e.key!=='Tab')return;
      const focusable=[...dialog.querySelectorAll('button:not([disabled])')];if(!focusable.length)return;
      const first=focusable[0],last=focusable[focusable.length-1];
      if(e.shiftKey&&document.activeElement===first){e.preventDefault();last.focus()}else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first.focus()}
    }
    function openDialog(trigger){
      const r=relics[selected];lastTrigger=trigger||viewDetail;viewed.add(r.id);nav.querySelectorAll('.lb-relic-choice').forEach((b,i)=>b.classList.toggle('viewed',viewed.has(relics[i].id)));updateProgress();
      if(standalone)document.getElementById('chapterCabinetClose').hidden=true;
      if(active3D){active3D.dispose();active3D=null}
      badge.textContent=r.model?`${r.role} · 离线 GLB 三维模型`:`${r.role} · 文档图片 ${r.images.length} 张`;title.textContent=r.name;brief.textContent=r.brief;detail.textContent=r.detail;memory.textContent=`蓉宝：“${r.memory}”`;hero.src=r.images[0];hero.alt=`${r.name}整体图`;thumbs.innerHTML='';
      live.hidden=!r.model;hero.hidden=!!r.model;thumbs.hidden=!!r.model;gallery.classList.toggle('has-3d',!!r.model);
      if(!r.model)r.images.forEach((src,i)=>{const b=el('button','lb-relic-thumb'+(i===0?' on':''));b.type='button';const im=el('img','');im.src=src;im.alt=`${r.name}细节 ${i+1}`;b.appendChild(im);b.onclick=()=>{hero.src=src;hero.alt=im.alt;thumbs.querySelectorAll('button').forEach(x=>x.classList.remove('on'));b.classList.add('on')};thumbs.appendChild(b)});
      dialog.hidden=false;requestAnimationFrame(()=>{
        dialog.classList.add('open');
        if(r.model){
          autoRotate=!(window.matchMedia&&window.matchMedia('(prefers-reduced-motion: reduce)').matches);liveRotate.textContent=autoRotate?'暂停旋转':'继续旋转';liveRotate.setAttribute('aria-pressed',String(autoRotate));liveStatus.textContent='正在载入离线三维模型……';
          const start3D=(attempt=0)=>{
            if(dialog.hidden||!r.model)return;
            if(window.ShujiRelic3D){
              active3D=window.ShujiRelic3D.mount(liveViewport,{modelUrl:r.model,label:r.name,autoRotate,onProgress:p=>{liveStatus.textContent=`正在载入离线三维模型 ${p}%`},onStatus:(state)=>{liveStatus.textContent=state==='ready'?'拖动旋转 · 滚轮缩放 · 右键平移':state==='error'?'三维模型载入失败，可关闭后查看文档图片。':'正在载入离线三维模型……'}});
            }else if(attempt<30)setTimeout(()=>start3D(attempt+1),100);
            else liveStatus.textContent='三维引擎未能启动，请重新打开文物介绍。';
          };
          start3D();
        }
      });document.addEventListener('keydown',trapDialog,true);close.focus();
    }
    liveReset.onclick=()=>{active3D?.reset();liveViewport.focus()};
    liveRotate.onclick=()=>{autoRotate=!autoRotate;active3D?.setAutoRotate(autoRotate);liveRotate.textContent=autoRotate?'暂停旋转':'继续旋转';liveRotate.setAttribute('aria-pressed',String(autoRotate))};
    close.onclick=closeDialog;back.onclick=closeDialog;dialog.onclick=e=>{if(e.target===dialog)closeDialog()};viewDetail.onclick=()=>openDialog(viewDetail);
    finishBtn.onclick=()=>{if(!finishBtn.disabled){if(storyScreen)storyScreen.classList.remove('relic-viewer-active');finish()}};

    function selectRelic(index,showDetail,trigger){
      selected=index;angle=0;frame=-1;const r=relics[index];stageName.textContent=r.name;quote.textContent=`“${r.memory}”`;nav.querySelectorAll('.lb-relic-choice').forEach((b,i)=>{const on=i===index;b.classList.toggle('selected',on);b.setAttribute('aria-current',on?'true':'false')});updateModel();if(showDetail)openDialog(trigger);
    }
    relics.forEach((r,i)=>{const b=el('button','lb-relic-choice');b.type='button';b.innerHTML=`<img src="${r.cover}" alt=""><span><b>${r.name}</b><small>${r.role}</small></span><i aria-hidden="true">${r.mark}</i>`;b.onclick=()=>selectRelic(i,true,b);nav.appendChild(b)});
    selectRelic(0,false,null);updateProgress();
  }

  function water(ui,finish){
    const galleryGroups=[
      {name:'水利全景',images:['assets/dujiangyan/image1.png']},
      {name:'秦堰楼',images:['assets/dujiangyan/image2.png','assets/dujiangyan/image3.png','assets/dujiangyan/image4.png','assets/dujiangyan/image5.png','assets/dujiangyan/image6.png']},
      {name:'宝瓶口',images:['assets/dujiangyan/image7.png','assets/dujiangyan/image8.png','assets/dujiangyan/image9.png']},
      {name:'飞沙堰',images:['assets/dujiangyan/image10.png','assets/dujiangyan/image11.png','assets/dujiangyan/image12.png']},
      {name:'南桥蓝眼泪',images:['assets/dujiangyan/image13.png','assets/dujiangyan/image14.png','assets/dujiangyan/image15.png','assets/dujiangyan/image16.png']}
    ];
    const rounds=[
      {name:'鱼嘴分水',prompt:'洪峰抵达鱼嘴，应如何分配江水？',answer:'顺势分入内外二江',choices:['筑高坝堵死岷江','顺势分入内外二江','全部引入成都平原']},
      {name:'飞沙排洪',prompt:'泥沙在弯道淤积，应怎样处理？',answer:'利用飞沙堰泄洪排沙',choices:['利用飞沙堰泄洪排沙','填平外江','封闭溢洪道']},
      {name:'宝瓶引水',prompt:'最后如何控制进入成都平原的水量？',answer:'以宝瓶口约束流量',choices:['挖开整座玉垒山','以宝瓶口约束流量','让洪水自由漫灌']}
    ];
    let step=0;
    const flood=(reason)=>{
      ui.body.innerHTML='';const box=el('section','lb-failure flood');
      box.innerHTML=`<div class="lb-failure-glyph">洪</div><h4>治水失败 · 洪水漫过平原</h4><p>${reason}<br>三项工程必须彼此配合，请从鱼嘴分水重新开始。</p>`;
      const retry=el('button','lb-big-action','重头再来');retry.onclick=()=>{step=0;draw()};box.appendChild(retry);ui.body.appendChild(box);ui.status.textContent='洪水退去后，重新推演整套无坝治水方案。';if(window.Rongbao)window.Rongbao.react('confused','洪水来了，重新治水！',1800);
    };
    const draw=()=>{
      ui.body.innerHTML='';tabs(ui.body,rounds.map(x=>x.name),step);const r=rounds[step];
      ui.body.append(el('div','lb-water-stage',`岷江水势 · 第 ${step+1} 决策`),el('p','lb-game-question',r.prompt));const row=el('div','lb-grid');
      shuffle(r.choices).forEach(x=>{const b=el('button','lb-card',x);b.onclick=()=>{if(x!==r.answer){flood(`错误决策：“${x}”。`);return}b.classList.add('hit');ui.status.textContent=`${r.name}正确，水势暂时稳定。`;step++;if(step===rounds.length){ui.body.querySelector('.lb-water-stage').textContent='水旱从人 · 天府之土建成';setTimeout(finish,900)}else setTimeout(draw,550)};row.appendChild(b)});ui.body.appendChild(row);ui.status.textContent='依次完成分水、排沙泄洪与限量引水。任何一步错误都会发洪水。';
    };
    draw();
  }

  function wuhou(ui,finish){
    const galleryGroups=[
      {name:'武侯祠概览',images:['assets/wuhou/image1.png']},
      {name:'文臣武将廊',images:['assets/wuhou/image2.png','assets/wuhou/image3.png','assets/wuhou/image4.png']},
      {name:'刘备殿',images:['assets/wuhou/image5.png','assets/wuhou/image6.png','assets/wuhou/image7.png']},
      {name:'诸葛亮殿',images:['assets/wuhou/image8.png','assets/wuhou/image9.png','assets/wuhou/image10.png']},
      {name:'三义庙',images:['assets/wuhou/image11.png','assets/wuhou/image12.png','assets/wuhou/image13.png']}
    ];
    const rounds=[
      {name:'稳住粮道',enemy:'魏军准备截断蜀军粮道。',answer:'以方阵护送粮车，弩兵据高处掩护',choices:['全军弃粮追击','以方阵护送粮车，弩兵据高处掩护','焚毁栈道困住自己']},
      {name:'诱敌出营',enemy:'司马懿闭营不战，正等待蜀军粮尽。',answer:'佯装撤退，以空营引魏军进入谷口',choices:['正面强攻魏营','原地等待粮尽','佯装撤退，以空营引魏军进入谷口']},
      {name:'两翼合围',enemy:'魏军进入伏击圈，中军与两翼如何配合？',answer:'中军坚守，两翼伏兵同时合拢',choices:['中军坚守，两翼伏兵同时合拢','两翼先退，只留中军','各部自行出击']}
    ];
    let step=0;
    const defeat=(reason)=>{
      ui.body.innerHTML='';const box=el('section','lb-failure battle');
      box.innerHTML=`<div class="lb-failure-glyph">败</div><h4>布阵失败 · 全军覆没</h4><p>${reason}<br>请从保护粮道开始，重新推演五丈原战局。</p>`;
      const retry=el('button','lb-big-action','重新布阵');retry.onclick=()=>{step=0;draw()};box.appendChild(retry);ui.body.appendChild(box);ui.status.textContent='失败不会写入进度；重新布阵后才能开启下一章。';if(window.Rongbao)window.Rongbao.react('confused','阵线崩溃，重新布阵！',1800);
    };
    const draw=()=>{
      ui.body.innerHTML='';tabs(ui.body,rounds.map(x=>x.name),step);const r=rounds[step];
      const board=el('div','lb-battle-board',`五丈原沙盘 · ${r.name}`);ui.body.append(board,el('p','lb-game-question',r.enemy));const row=el('div','lb-grid');
      shuffle(r.choices).forEach(x=>{const b=el('button','lb-card',x);b.onclick=()=>{if(x!==r.answer){defeat(`错误军令：“${x}”。`);return}b.classList.add('hit');board.classList.add('won');ui.status.textContent=`${r.name}成功，阵线推进。`;step++;if(step===rounds.length){board.textContent='两翼合围 · 司马懿退兵';setTimeout(finish,900)}else setTimeout(draw,600)};row.appendChild(b)});ui.body.appendChild(row);ui.status.textContent='三道军令必须全部正确；任何错误都会导致全军覆没。';
    };
    draw();
  }

  function poetry(ui,finish){
    ui.status.textContent='两个选择没有对错，也不会改变后续剧情。';
    const scene=el('div','lb-poetry-scene');scene.innerHTML='<b>秋风卷走茅草</b><p>南村群童抱着茅草跑进竹林，年迈的杜甫倚杖叹息。</p>';
    const row=el('div','lb-grid');
    [
      ['加入南村群童','你跟着孩子们跑进竹林，才发现他们也想用茅草补自家的破屋。乱世里，没有谁真正富足。'],
      ['救济杜甫','你扶杜甫回到草堂，又把干粮和茅草分给他。杜甫谢过你，却仍惦记天下更多无屋可住的人。']
    ].forEach(([name,feedback])=>{const b=el('button','lb-card',name);b.onclick=()=>{row.querySelectorAll('button').forEach(x=>x.disabled=true);b.classList.add('hit');scene.innerHTML=`<b>${name}</b><p>${feedback}</p>`;ui.status.textContent='选择已记录。个人善意与时代困境在这一刻相遇，故事继续。';setTimeout(finish,1500)};row.appendChild(b)});
    ui.body.append(scene,row);
  }

  function jianmen(ui,finish){
    const names=['蜀道攀爬','史实时间线','纪念献花'];let stage=0;
    const next=()=>{stage++;if(stage===3){ui.status.textContent='没有评分，只有记住。';setTimeout(finish,900)}else render()};
    function render(){ui.body.innerHTML='';tabs(ui.body,names,stage);stage===0?climb():stage===1?timeline():flower()}
    function climb(){const safe=['左侧石阶','抓住铁索','避入凹壁','登上关楼'];const bad=['踏上松石','穿过落石区','跃过断崖','逆风站立'];let i=0;const draw=()=>{ui.body.innerHTML='';tabs(ui.body,names,stage);ui.body.appendChild(el('p','',`攀爬进度 ${i} / 4`));const row=el('div','lb-grid');shuffle([safe[i],bad[i]]).forEach(x=>{const b=el('button','lb-card',x);b.onclick=()=>{if(x===safe[i]){i++;if(i===4)setTimeout(next,550);else draw()}else wrong(b,ui.status,'落脚点不稳，退回上一步重新选。')};row.appendChild(b)});ui.body.appendChild(row)};draw()}
    function timeline(){const a=['1937 全面抗战爆发','川军集结出川','长途跋涉赴前线','转战大江南北'];let i=0;const row=el('div','lb-grid');shuffle(a).forEach(x=>{const b=el('button','lb-card',x);b.onclick=()=>{if(x===a[i]){b.classList.add('hit');b.disabled=true;i++;if(i===a.length)setTimeout(next,650)}else wrong(b,ui.status,'时间线尚未接上，再试一次。')};row.appendChild(b)});ui.body.appendChild(row)}
    function flower(){ui.status.textContent='挑一朵花，再按住三秒，郑重放在碑前。';const row=el('div','lb-grid');['白菊','山茶','素兰'].forEach(x=>{const b=el('button','lb-card',x);b.onclick=()=>{row.querySelectorAll('button').forEach(z=>z.classList.remove('hit'));b.classList.add('hit');place.disabled=false;ui.status.textContent=`已选择${x}。按住“敬献”三秒。`};row.appendChild(b)});const place=el('button','lb-big-action','按住敬献');place.disabled=true;let t=0,start=0,raf=0;const stop=()=>{cancelAnimationFrame(raf);start=0;place.style.background='';};place.onpointerdown=e=>{if(place.disabled)return;start=performance.now();place.setPointerCapture&&place.setPointerCapture(e.pointerId);const tick=()=>{if(!start)return;const p=(performance.now()-start)/3000;place.style.background=`linear-gradient(90deg,#4e7c6b ${p*100}%,#8b3a2e ${p*100}%)`;if(p>=1){start=0;place.textContent='花已敬献';place.disabled=true;setTimeout(next,800)}else raf=requestAnimationFrame(tick)};tick()};place.onpointerup=stop;place.onpointercancel=stop;ui.body.append(row,place)}
    render();
  }

  function fusion(ui,finish){
    const items=[['本源之忆','#b78b43','祭台与青铜神树亮起'],['守护之忆','#4e8f8a','岷江与离堆水光浮现'],['家国之忆','#b95b3f','营火和北伐舆图展开'],['文脉之忆','#d4b861','雨夜诗灯重新燃亮'],['山河之忆','#b9bdb4','剑门关外队伍远行']];let count=0;const row=el('div','lb-fragments'),core=el('div','lb-core','将五块碎片拖入或点入中心'),memory=el('div','lb-memory-reel','等待第一段记忆影像');ui.body.append(row,core,memory);
    function place(f){if(f.classList.contains('used'))return;f.classList.add('used');f.style.opacity=.2;count++;core.textContent=`记忆融合 ${count} / 5`;memory.textContent=f.dataset.memory;memory.classList.remove('flash');void memory.offsetWidth;memory.classList.add('flash');if(count===5){core.classList.add('complete');core.textContent='五世归一 · 记忆不灭';memory.textContent='金杖、竹竿、长剑、油灯与步枪在蓉宝手中重叠。';setTimeout(finish,1300)}}
    items.forEach(([name,color,recall])=>{const f=el('div','lb-fragment',name);f.style.background=color;f.draggable=true;f.ondragstart=e=>e.dataTransfer.setData('text/plain',name);f.onclick=()=>place(f);f.dataset.name=name;f.dataset.memory=recall;row.appendChild(f)});
    core.ondragover=e=>e.preventDefault();core.ondrop=e=>{e.preventDefault();const name=e.dataTransfer.getData('text/plain');const f=[...row.children].find(x=>x.dataset.name===name);if(f)place(f)};
  }

  const cabinetData={
    dujiangyan:{title:'都江堰景点展柜',portrait:'assets/rongbao-dujiangyan-waterworker.png',guide:'蓉宝 · 治水者',items:[
      {name:'都江堰全景',desc:'都江堰水利工程由鱼嘴、飞沙堰与宝瓶口等部分共同构成，顺应岷江水势分水引流。',images:[1]},
      {name:'秦堰楼',desc:'秦堰楼位于都江堰景区，可俯瞰岷江水利工程与河道。',images:[2,3,4,5,6]},
      {name:'宝瓶口',desc:'宝瓶口是内江引水的咽喉，借狭窄口门控制进入成都平原的水量。',images:[7,8,9]},
      {name:'飞沙堰',desc:'飞沙堰连接内外江，承担泄洪、排沙等功能，是都江堰水利系统的重要组成部分。',images:[10,11,12]},
      {name:'南桥蓝眼泪',desc:'都江堰南桥附近的夜间水景，灯光映照水面，呈现蓝色光影。',images:[13,14,15,16]}
    ].map(x=>({...x,images:x.images.map(i=>`assets/dujiangyan/image${i}.png`)}))},
    wuhou:{title:'武侯祠景点展柜',portrait:'assets/rongbao-wuhou-strategist.png',guide:'蓉宝 · 蜀汉谋士',items:[
      {name:'武侯祠概览',desc:'成都武侯祠以纪念诸葛亮等蜀汉人物为核心，保留了君臣合祀的文化记忆。',images:[1]},
      {name:'文臣武将廊',desc:'廊内塑像呈现蜀汉文臣武将群像，是了解三国人物的重要空间。',images:[2,3,4]},
      {name:'刘备殿',desc:'刘备殿以蜀汉昭烈帝刘备为中心，展现武侯祠君臣合祀的历史文化。',images:[5,6,7]},
      {name:'诸葛亮殿',desc:'诸葛亮殿纪念蜀汉丞相诸葛亮，其治蜀、北伐与后世纪念共同构成武侯祠的重要主题。',images:[8,9,10]},
      {name:'三义庙',desc:'三义庙与刘备、关羽、张飞的历史叙事和后世文化记忆有关。',images:[11,12,13]}
    ].map(x=>({...x,images:x.images.map(i=>`assets/wuhou/image${i}.png`)}))},
    dufu:{title:'杜甫草堂景点展柜',portrait:'assets/rongbao-dufu-poet.png',guide:'蓉宝 · 草堂友人',items:[
      {name:'杜甫草堂概览',desc:'杜甫于安史之乱期间辗转入蜀，在成都浣花溪畔营建草堂；这里承载着诗人与成都的生活记忆。',images:[1]},
      {name:'大廨',desc:'大廨是杜甫草堂中轴线上的重要建筑，以陈列与诗人相关的资料和塑像呈现草堂文化。',images:[2,3,4,5]},
      {name:'茅屋故居',desc:'茅屋故居再现杜甫在成都草堂生活时的居所意象，也是《茅屋为秋风所破歌》叙事的重要空间。',images:[6,7,8,9]},
      {name:'万佛楼',desc:'万佛楼位于草堂景区内，登临可感受草堂园林与成都历史文化景观。',images:[10,11,12,13]},
      {name:'草堂书院',desc:'草堂书院延续杜甫草堂的诗歌与文脉主题，是阅读、展示和文化交流的空间。',images:[14,15,16,17]}
    ].map(x=>({...x,images:x.images.map(i=>`assets/dufu/image${i}.jpg`)}))},
    jianmen:{title:'剑门关景点展柜',portrait:'assets/rongbao-jianmen-soldier.png',guide:'蓉宝 · 出川士兵',items:[
      {name:'剑门关概览',desc:'剑门关位于蜀道要冲，群山与关隘共同构成连接四川盆地与北方的地理通道。',images:[1]},
      {name:'天梯峡栈道',desc:'天梯峡栈道沿峡谷峭壁铺展，集中展现剑门山地势的险峻与蜀道通行的艰难。',images:[2,3,4]},
      {name:'鸟道',desc:'鸟道依山就势、临崖而行，以狭窄陡峭的山路呈现剑门蜀道“一夫当关”的地形特征。',images:[5,6,7,4]},
      {name:'梁山寺',desc:'梁山寺坐落于剑门山景区高处，古刹与山林共同构成剑门关的人文景观。',images:[8,9,10,11]},
      {name:'翠云廊',desc:'翠云廊以古柏夹道著称，古树与蜀道相伴，保存了交通、生态与历史记忆。',images:[12,13,14,15,16]}
    ].map(x=>({...x,images:x.images.map(i=>`assets/jianmen/image${i}.jpg`)}))}
  };
  function mountCabinet(root,id){
    const config=cabinetData[id];
    if(id==='sanxingdui'){const ui=shell(root,{title:'三星堆文物展柜',sub:'点击文物查看三维模型和介绍；展柜与剧情进度相互独立。'});bronze(ui,()=>{},true);return}
    if(!config)return;
    root.innerHTML='';const panel=el('section','lb-cabinet-panel');
    const heading=el('div','lb-cabinet-heading');heading.append(el('h3','',config.title),el('p','','点击景点查看介绍；关闭后回到原来的剧情位置。'));panel.appendChild(heading);
    const layout=el('div','lb-cabinet-layout'),list=el('nav','lb-cabinet-list');list.setAttribute('aria-label',config.title+'目录');
    const listTitle=el('h4','','景点目录');list.appendChild(listTitle);
    const guide=el('aside','lb-cabinet-guide');const portrait=el('img','');portrait.src=config.portrait;portrait.alt=config.guide+'形象';guide.append(portrait,el('strong','',config.guide),el('p','','打开景点后可查看介绍'+(config.items.some(x=>x.images.length)?'与提供的图片。':'。')));
    config.items.forEach((item)=>{const b=el('button','lb-cabinet-item');b.type='button';if(item.images.length){const im=el('img','');im.src=item.images[0];im.alt='';b.appendChild(im)}else b.appendChild(el('span','lb-cabinet-icon','景'));const copy=el('span','');copy.append(el('b','',item.name),el('small','',item.images.length?'查看景点介绍与图片':'查看景点介绍'));b.append(copy,el('span','lb-cabinet-arrow','›'));b.onclick=()=>showPlace(item,b);list.appendChild(b)});
    layout.append(list,guide);panel.appendChild(layout);root.appendChild(panel);
    function showPlace(item,trigger){
      const detail=el('div','lb-place-detail');detail.setAttribute('role','dialog');detail.setAttribute('aria-modal','true');detail.setAttribute('aria-label',item.name+'介绍');
      const card=el('section','lb-place-detail-card');const close=el('button','lb-place-close','返回目录');close.type='button';
      const title=el('h3','',item.name),description=el('p','',item.desc);card.append(close,title,description);
      if(item.images.length){const image=el('img','lb-place-detail-hero');image.src=item.images[0];image.alt=item.name+'景点图片 1';const thumbs=el('div','lb-place-detail-thumbs');item.images.forEach((src,index)=>{const b=el('button','');b.type='button';b.setAttribute('aria-label',`查看${item.name}第 ${index+1} 张图片`);const thumb=el('img','');thumb.src=src;thumb.alt='';b.appendChild(thumb);b.onclick=()=>{image.src=src;image.alt=item.name+'景点图片 '+(index+1);thumbs.querySelectorAll('button').forEach(x=>x.classList.remove('on'));b.classList.add('on')};if(index===0)b.classList.add('on');thumbs.appendChild(b)});card.append(image,thumbs)}else card.append(el('p','lb-place-no-photo','本景点暂无提供的照片，先展示文字介绍。'));
      detail.appendChild(card);root.appendChild(detail);const outerClose=document.getElementById('chapterCabinetClose');outerClose.hidden=true;const dismiss=()=>{detail.remove();outerClose.hidden=false;trigger.focus()};close.onclick=dismiss;detail.onclick=e=>{if(e.target===detail)dismiss()};detail.onkeydown=e=>{if(e.key==='Escape'){e.stopPropagation();dismiss()}else if(e.key==='Tab'){const buttons=[...detail.querySelectorAll('button')],first=buttons[0],last=buttons[buttons.length-1];if(e.shiftKey&&document.activeElement===first){e.preventDefault();last.focus()}else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first.focus()}}};close.focus();
    }
  }

  const games={prologueExplore,longpress,bronze,water,wuhou,poetry,jianmen,fusion};
  function mount(root,node,done){const ui=shell(root,node),finish=completeFactory(node,done);(games[node.game]||bronze)(ui,finish)}

  function installBook(getState,startById,forms,mainline){
    if(document.getElementById('memoryBookBtn'))return;
    const open=el('button','','记忆图鉴');open.id='memoryBookBtn';const hub=el('a','','知识库后台');hub.id='serverHubBtn';hub.href='/';hub.target='_blank';hub.rel='noopener';hub.title='打开 server 提供的文档、入库、检索、问答、景点与运行状态管理页';const modal=el('div');modal.id='memoryBook';const book=el('div','lb-book');modal.appendChild(book);document.body.append(open,hub,modal);
    const close=()=>modal.classList.remove('open');modal.onclick=e=>{if(e.target===modal)close()};
    open.onclick=()=>{const state=getState();book.innerHTML='';const head=el('div','lb-book-head');head.append(el('h2','','蓉宝记忆图鉴'));const x=el('button','', '关闭');x.onclick=close;head.appendChild(x);book.appendChild(head);const sheet=el('img','lb-character-sheet');sheet.src='assets/longbao-six-forms.png';sheet.alt='蓉宝从当代失忆熊猫守护者到古蜀祭司、先秦水工、蜀汉谋士、唐代隐士与川军士兵的六形态设定图';book.appendChild(sheet);const grid=el('div','lb-book-grid');
      mainline.forEach((ch,i)=>{const unlocked=state.main>=ch.ch;const card=el('section','lb-memory'+(unlocked?'':' locked'));card.innerHTML=`<h4>${unlocked?ch.fragment:'未苏醒的碎片'}</h4><p>${ch.title}<br>${unlocked?ch.memory:'完成前一章后解锁'}</p>`;if(unlocked){const b=el('button','', '重温本章');b.onclick=()=>{close();startById(ch.id)};card.appendChild(b)}grid.appendChild(card)});
      const art=el('section','lb-memory');const form=forms[Math.min(5,state.main)]||forms[0];art.innerHTML=`<h4>当前形态 · ${form.name}</h4><p>${form.era}<br>${form.art}</p>`;grid.appendChild(art);const rec=el('section','lb-memory');const records=state.records||{};const vals=Object.values(records);rec.innerHTML=`<h4>小游戏记录</h4><p>已完成 ${vals.length} 类机制。${vals.length?'最快 '+(Math.min(...vals)/1000).toFixed(1)+' 秒':'完成小游戏后记录最佳用时。'}</p>`;grid.appendChild(rec);book.appendChild(grid);modal.classList.add('open')};
  }

  window.LongbaoGames={mount,mountCabinet,installBook};
})();
