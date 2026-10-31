(function(){
  var d=document,root=d.documentElement;

  // Phone menu. It covers the page, so while it's open everything behind it is
  // inert (keyboard and screen readers stay in the menu) and focus starts on
  // its first link.
  var burger=d.querySelector('.burger'),nav=d.getElementById('nav');
  var behind=[d.querySelector('.skip'),d.querySelector('.hdr .logo'),d.getElementById('main'),d.querySelector('.ftr')];
  function setMenu(open){
    root.classList.toggle('menu-open',open);
    behind.forEach(function(el){if(el)el.inert=open});
    if(burger){burger.setAttribute('aria-expanded',open);burger.setAttribute('aria-label',open?'Close menu':'Open menu')}
  }
  if(burger)burger.addEventListener('click',function(){
    var open=!root.classList.contains('menu-open');
    setMenu(open);
    if(open&&nav)nav.querySelector('a,button').focus();
  });
  // Widening the window past phone size hides the burger, so close the menu with it
  if(window.matchMedia)matchMedia('(min-width:768px)').addEventListener('change',function(m){if(m.matches)setMenu(false)});

  // Info dropdown
  var dd=d.querySelector('.dd'),ddBtn=dd&&dd.querySelector('.dd-btn');
  function setDD(open){if(dd){dd.classList.toggle('open',open);ddBtn.setAttribute('aria-expanded',open)}}
  if(ddBtn){
    ddBtn.addEventListener('click',function(e){e.stopPropagation();setDD(!dd.classList.contains('open'))});
    d.addEventListener('click',function(e){if(!dd.contains(e.target))setDD(false)});
    // Tabbing on past the last link closes it too
    dd.addEventListener('focusout',function(e){if(e.relatedTarget&&!dd.contains(e.relatedTarget))setDD(false)});
  }
  d.addEventListener('keydown',function(e){
    if(e.key!=='Escape')return;
    if(root.classList.contains('menu-open')){setMenu(false);if(burger)burger.focus()}
    setDD(false);
  });
  // A page brought back by the Back button is restored exactly as it was left,
  // which would be with the menu still open after tapping one of its links
  addEventListener('pageshow',function(e){if(e.persisted){setMenu(false);setDD(false)}});

  // Instagram posts the site doesn't draw itself: load Instagram's script only
  // when they're about to scroll into view
  var insta=d.querySelectorAll('.insta-embeds');
  function loadInsta(){
    if(window.instgrm){window.instgrm.Embeds.process();return}
    if(d.getElementById('insta-js'))return;
    var s=d.createElement('script');s.id='insta-js';s.async=true;s.src='https://www.instagram.com/embed.js';d.body.appendChild(s);
  }
  if(insta.length){
    if('IntersectionObserver' in window){
      var seen=new IntersectionObserver(function(es){
        if(es.some(function(e){return e.isIntersecting})){seen.disconnect();loadInsta()}
      },{rootMargin:'400px'});
      insta.forEach(function(el){seen.observe(el)});
    }else loadInsta();
  }

  // Home page photo row: arrows step through it three at a time (a row plus
  // its 10px gap), and hide
  // at either end (wider screens only; phones swipe).
  d.querySelectorAll('.photo-strip.many').forEach(function(strip){
    var row=strip.querySelector('.photos'),prev=strip.querySelector('.prev'),next=strip.querySelector('.next');
    function update(){
      var end=row.scrollWidth-row.clientWidth;
      prev.hidden=row.scrollLeft<=2;
      next.hidden=end<=2||row.scrollLeft>=end-2;
    }
    prev.addEventListener('click',function(){row.scrollBy({left:-(row.clientWidth+10)})});
    next.addEventListener('click',function(){row.scrollBy({left:row.clientWidth+10})});
    row.addEventListener('scroll',update,{passive:true});
    window.addEventListener('resize',update);
    update();
  });

  // With more than 6 photos the row is a slideshow on wider screens: it moves
  // on a screenful every 5 seconds and starts over at the end. Phones just
  // swipe. It waits while someone's pointing at it, touching it, or has it
  // focused, and while it's off screen; not at all for people who prefer less
  // motion.
  d.querySelectorAll('.photo-strip.slides').forEach(function(strip){
    if(!window.matchMedia||matchMedia('(prefers-reduced-motion: reduce)').matches)return;
    var row=strip.querySelector('.photos'),wide=matchMedia('(min-width:700px)'),hover=false,focus=false,seen=true,until=0;
    function advance(){
      var end=row.scrollWidth-row.clientWidth;if(end<=2)return;
      if(row.scrollLeft>=end-2){row.scrollTo({left:0,behavior:'smooth'});return}
      row.scrollBy({left:row.clientWidth+10,behavior:'smooth'});
    }
    function step(){if(wide.matches&&!hover&&!focus&&seen&&!d.hidden&&Date.now()>=until)advance()}
    strip.addEventListener('mouseenter',function(){hover=true});
    strip.addEventListener('mouseleave',function(){hover=false});
    strip.addEventListener('focusin',function(){focus=true});
    strip.addEventListener('focusout',function(){focus=false});
    strip.addEventListener('pointerdown',function(){until=Date.now()+15000});
    if('IntersectionObserver' in window)new IntersectionObserver(function(es){seen=es[0].isIntersecting}).observe(strip);
    setInterval(step,5000);
  });

  // Forms that ask first (take over, step down, cancel a ride): the question
  // is in data-confirm, since the page's security policy won't run onsubmit=""
  d.addEventListener('submit',function(e){
    var q=e.target.getAttribute('data-confirm');
    if(q&&!confirm(q))e.preventDefault();
  });

  // Waiver signatures: drawn with a finger, stylus or mouse (pointer events
  // cover all three). The strokes are kept as fractions of the box, so they
  // redraw sharply when the box changes size, and saved as a 600×300 PNG.
  // A form sent with "drawn" needs a drawing: "Can't draw?" takes it away,
  // leaving the typed name as the signature, as it is without JavaScript.
  d.querySelectorAll('.sig').forEach(function(box){
    var c=box.querySelector('canvas'),out=box.querySelector('input[name=signature_image]'),msg=box.querySelector('.sig-msg'),
      typeBtn=box.querySelector('.sig-type'),typed=box.querySelector('.sig-typed'),f=box.closest('form'),strokes=[],cur=null;
    var drawn=d.createElement('input');drawn.type='hidden';drawn.name='drawn';drawn.value='1';box.appendChild(drawn);
    box.hidden=false;
    function paint(ctx,w,h,line){
      ctx.clearRect(0,0,w,h);ctx.lineWidth=line;ctx.lineCap=ctx.lineJoin='round';ctx.strokeStyle='#1d1b1a';
      strokes.forEach(function(s){
        ctx.beginPath();ctx.moveTo(s[0][0]*w,s[0][1]*h);
        for(var i=1;i<s.length;i++)ctx.lineTo(s[i][0]*w,s[i][1]*h);
        if(s.length===1)ctx.lineTo(s[0][0]*w+.5,s[0][1]*h); // a dot
        ctx.stroke();
      });
    }
    function show(){var k=window.devicePixelRatio||1;paint(c.getContext('2d'),c.width,c.height,2.4*k)}
    function size(){
      var r=c.getBoundingClientRect(),k=window.devicePixelRatio||1;
      if(!r.width)return;
      c.width=Math.round(r.width*k);c.height=Math.round(r.height*k);show();
    }
    function save(){
      if(!strokes.length){out.value='';return}
      var o=d.createElement('canvas');o.width=600;o.height=300;
      paint(o.getContext('2d'),600,300,3.5);out.value=o.toDataURL('image/png');
      msg.textContent='';
    }
    function at(e){var r=c.getBoundingClientRect();return[Math.min(1,Math.max(0,(e.clientX-r.left)/r.width)),Math.min(1,Math.max(0,(e.clientY-r.top)/r.height))]}
    c.addEventListener('pointerdown',function(e){
      if(e.button>0)return;
      e.preventDefault();
      try{c.setPointerCapture(e.pointerId)}catch(err){} // keeps the stroke going past the edge; drawing works without it
      cur=[at(e)];strokes.push(cur);show();
    });
    c.addEventListener('pointermove',function(e){
      if(!cur)return;
      // The in-between points a fast finger makes, for smooth curves
      var pts=e.getCoalescedEvents?e.getCoalescedEvents():[];
      (pts.length?pts:[e]).forEach(function(p){cur.push(at(p))});show();
    });
    function end(){if(cur){cur=null;save()}}
    c.addEventListener('pointerup',end);c.addEventListener('pointercancel',end);
    box.querySelector('.sig-clear').addEventListener('click',function(){strokes=[];show();save()});
    typeBtn.addEventListener('click',function(){
      var off=!drawn.disabled;drawn.disabled=off;out.disabled=off;c.hidden=off;typed.hidden=!off;msg.textContent='';
      box.querySelector('.sig-clear').hidden=off;
      typeBtn.textContent=off?'Draw my signature instead':'Can’t draw? Sign with your typed name';
      if(!off)size();
    });
    // Runs before the join form's own submit handler, which is added below.
    if(f)f.addEventListener('submit',function(e){
      if(drawn.disabled||out.value)return;
      e.preventDefault();e.stopImmediatePropagation();
      msg.textContent='Sign in the box with your finger, a stylus or your mouse.';
      c.scrollIntoView({block:'center',behavior:'smooth'});
    });
    window.addEventListener('resize',size);
    var det=box.closest('details');if(det)det.addEventListener('toggle',size); // sized once it's shown
    size();
  });

  // Join form: write phone numbers as 301-555-0123 while they're typed
  d.querySelectorAll('form.join input[type=tel][pattern]').forEach(function(el){
    function fmt(){
      // Count the digits before the cursor, so it can go back after the same
      // digit once the dashes move (otherwise it jumps to the end)
      var before=el.value.slice(0,el.selectionStart).replace(/\D/g,'').length;
      var n=el.value.replace(/\D/g,'');
      if(n[0]==='1'){n=n.slice(1);before=Math.max(0,before-1)} // no US area code starts with 1, so it's the country code
      n=n.slice(0,10);
      el.value=n.length>6?n.slice(0,3)+'-'+n.slice(3,6)+'-'+n.slice(6):n.length>3?n.slice(0,3)+'-'+n.slice(3):n;
      if(d.activeElement!==el)return;
      var at=0;
      for(var k=0;at<el.value.length&&k<before;at++)if(el.value[at]!=='-')k++;
      el.setSelectionRange(at,at);
    }
    el.addEventListener('input',fmt);
    if(el.value)fmt();
  });

  // Join form: submit in the background and swap in the result
  var form=d.querySelector('form.join[action="/join"]');
  if(form&&window.fetch)form.addEventListener('submit',function(e){
    e.preventDefault();
    var btn=form.querySelector('button'),msg=form.querySelector('.msg');
    function fail(text){msg.textContent=text;msg.classList.add('err');btn.disabled=false}
    btn.disabled=true;msg.textContent='';msg.classList.remove('err');
    fetch(form.action,{method:'POST',body:new URLSearchParams(new FormData(form)),headers:{Accept:'application/json'}})
      .then(function(r){return r.json()})
      .then(function(j){
        if(!j.ok)return fail(j.message);
        var done=d.querySelector('.joined'),pay=done.querySelector('.joined-pay');
        done.querySelector('.joined-title').textContent=j.message;
        done.querySelector('.joined-detail').textContent=j.detail||'';
        var link=done.querySelector('.joined-minors input[name=email]');if(link)link.value=form.querySelector('input[name=email]').value;
        if(j.pay_url){var a=pay.querySelector('a');a.href=j.pay_url;a.querySelector('span').textContent=j.pay_label;pay.hidden=false}else{pay.hidden=true}
        form.hidden=true;done.hidden=false;done.focus();
      })
      .catch(function(){fail('Couldn’t reach the server. Check your connection and try again.')});
  });

  // Share your photos: each photo is shrunk on the phone before it uploads
  // (a 5 MB photo becomes about half a megabyte), then they're sent in the
  // background with a progress count. A photo the browser can't read goes as
  // it is; the server checks everything again.
  var share=d.querySelector('form.share');
  if(share&&window.FormData&&window.XMLHttpRequest)(function(){
    var input=share.querySelector('input[type=file]'),btn=share.querySelector('button[type=submit]'),msg=share.querySelector('.msg'),
      count=share.querySelector('.share-count'),hint=count.textContent,MAX=20,SIDE=2000;
    function fail(text){msg.textContent=text;msg.classList.add('err');btn.disabled=false}
    function shrink(file){
      return new Promise(function(done){
        if(!/^image\/(jpeg|png|webp)$/.test(file.type)||!window.createImageBitmap)return done(file);
        createImageBitmap(file,{imageOrientation:'from-image'}).then(function(bmp){
          var k=Math.min(1,SIDE/Math.max(bmp.width,bmp.height));
          if(k===1&&file.size<1.5e6){if(bmp.close)bmp.close();return done(file)}
          var c=d.createElement('canvas');c.width=Math.round(bmp.width*k);c.height=Math.round(bmp.height*k);
          c.getContext('2d').drawImage(bmp,0,0,c.width,c.height);if(bmp.close)bmp.close();
          c.toBlob(function(b){done(b&&b.size<file.size?new File([b],file.name.replace(/\.\w+$/,'')+'.jpg',{type:'image/jpeg'}):file)},'image/jpeg',.85);
        }).catch(function(){done(file)});
      });
    }
    input.addEventListener('change',function(){var n=input.files.length;count.textContent=n?n+(n===1?' photo':' photos')+' chosen.'+(n>MAX?' That’s more than '+MAX+': send the rest next.':''):hint});
    share.addEventListener('submit',function(e){
      e.preventDefault();
      var files=[].slice.call(input.files),out=[],i=0;
      if(files.length>MAX)return fail('You can send up to '+MAX+' photos at a time.');
      btn.disabled=true;msg.classList.remove('err');
      (function next(){
        if(i<files.length){msg.textContent='Getting your photos ready… '+(i+1)+' of '+files.length;shrink(files[i++]).then(function(f){out.push(f);next()});return}
        send(out);
      })();
    });
    function send(files){
      var fd=new FormData(share);fd.delete('photos');files.forEach(function(f){fd.append('photos',f,f.name)});
      var x=new XMLHttpRequest();x.open('POST',share.action);x.setRequestHeader('Accept','application/json');
      x.upload.onprogress=function(e){if(e.lengthComputable)msg.textContent='Sending… '+Math.round(e.loaded/e.total*100)+'%'};
      x.onload=function(){
        var j=null;try{j=JSON.parse(x.responseText)}catch(err){}
        if(!j)return fail(x.status===413?'Those photos are too big to send together. Try a few at a time.':'Something went wrong. Try again in a minute.');
        if(!j.ok)return fail(j.message);
        var done=d.querySelector('.share-done');done.querySelector('.joined-detail').textContent=j.detail||'';
        share.hidden=true;done.hidden=false;done.focus();
      };
      x.onerror=function(){fail('Couldn’t reach the server. Check your connection and try again.')};
      x.send(fd);
    }
  })();

  // Photo gallery: a photo opens large over the page, with arrows (and the
  // arrow keys, or a swipe) to step through the others. Without JavaScript,
  // or a browser without <dialog>, each photo opens on its own.
  var view=d.querySelector('.gal-view');
  if(view&&view.showModal)(function(){
    var links=[].slice.call(d.querySelectorAll('.gal li>a')),img=view.querySelector('img'),cap=view.querySelector('.gal-view-cap'),at=0,x0=null;
    function show(i){
      at=(i+links.length)%links.length;
      img.src=links[at].getAttribute('href');img.alt=links[at].querySelector('img').alt;cap.textContent=links[at].getAttribute('data-caption');
    }
    links.forEach(function(a,i){a.addEventListener('click',function(e){
      if(e.metaKey||e.ctrlKey||e.shiftKey)return;
      e.preventDefault();show(i);view.showModal();
    })});
    view.querySelector('.gal-view-x').addEventListener('click',function(){view.close()});
    view.querySelector('.gal-view-prev').addEventListener('click',function(){show(at-1)});
    view.querySelector('.gal-view-next').addEventListener('click',function(){show(at+1)});
    view.addEventListener('click',function(e){if(e.target===view)view.close()});
    view.addEventListener('keydown',function(e){if(e.key==='ArrowLeft')show(at-1);else if(e.key==='ArrowRight')show(at+1)});
    view.addEventListener('close',function(){links[at].focus()});
    view.addEventListener('touchstart',function(e){x0=e.touches[0].clientX},{passive:true});
    view.addEventListener('touchend',function(e){
      if(x0===null)return;var dx=e.changedTouches[0].clientX-x0;x0=null;
      if(Math.abs(dx)>50)show(dx<0?at+1:at-1);
    });
    if(links.length<2)[].forEach.call(view.querySelectorAll('.gal-view-prev,.gal-view-next'),function(b){b.hidden=true});
  })();

  // Counts this page view for the admin's Stats screen (stats.go): which page,
  // and which site the link that brought you here was on. No cookies, and
  // nothing about who you are. Pages without a canonical address (like "page
  // not found") don't count, and a page loaded ahead of a click counts only
  // once it's shown.
  (function(){
    var c=d.querySelector('link[rel=canonical]');
    if(!c||!navigator.sendBeacon)return;
    function count(){navigator.sendBeacon('/admin/count',JSON.stringify({p:c.getAttribute('href'),r:d.referrer}))}
    if(d.prerendering)d.addEventListener('prerenderingchange',count,{once:true});else count();
  })();
})();
