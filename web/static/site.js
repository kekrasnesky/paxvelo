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

  // Ride calendar: on touch screens the first tap on a ride in the month grid
  // opens its details card (hover does it elsewhere), and a second tap, or the
  // card's link, goes to the ride. Tapping anywhere else closes it.
  var popOpen=null;
  function closePop(){if(popOpen){popOpen.classList.remove('pop-open');popOpen=null}}
  if(d.querySelector('.cal-grid .cal-item')){
    d.addEventListener('click',function(e){
      var item=e.target.closest('.cal-grid .cal-item');
      if(item!==popOpen)closePop();
      if(!item||!matchMedia('(hover:none)').matches)return;
      var ride=e.target.closest('.cal-ride');
      if(ride&&!item.classList.contains('pop-open')){e.preventDefault();item.classList.add('pop-open');popOpen=item}
    });
    d.addEventListener('keydown',function(e){if(e.key==='Escape')closePop()});
  }

  // A ride's Share button: the phone's share sheet, or else the link copied
  var rideShare=d.querySelector('.ride-share');
  if(rideShare&&(navigator.share||navigator.clipboard)){
    rideShare.hidden=false;
    rideShare.addEventListener('click',function(){
      var url=location.href.split(/[?#]/)[0];
      if(navigator.share)navigator.share({title:d.querySelector('h1').textContent,url:url}).catch(function(){});
      else navigator.clipboard.writeText(url).then(function(){rideShare.lastChild.textContent='Link copied'});
    });
  }

  // Forms that ask first (step down, cancel a ride): the question is in
  // data-confirm, on the form or the button pressed, since the page's
  // security policy won't run onsubmit=""
  d.addEventListener('submit',function(e){
    var q=(e.submitter&&e.submitter.getAttribute('data-confirm'))||e.target.getAttribute('data-confirm');
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

  // Share your photos: photos can be picked (more than once) or dropped on the
  // box, and show as tiles with a × and a caption box. Sending shrinks each
  // one on the phone first (a 5 MB photo becomes about half a megabyte), then
  // sends them together, filling each tile as its part goes up. A photo the
  // browser can't read goes as it is; the server checks everything again.
  var share=d.querySelector('form.share');
  if(share&&window.FormData&&window.XMLHttpRequest&&window.URL&&URL.createObjectURL)(function(){
    var input=share.querySelector('input[type=file]'),drop=share.querySelector('.share-drop'),list=share.querySelector('.share-previews'),
      btn=share.querySelector('button[type=submit]'),msg=share.querySelector('.msg'),count=share.querySelector('.share-count'),
      bar=share.querySelector('.share-progress'),album=share.querySelector('.share-album'),named=share.querySelector('.share-new'),
      hint=count.textContent,MAX=+input.getAttribute('data-max')||30,SIDE=2000,chosen=[],busy=false;
    input.required=false;
    function fail(text){msg.textContent=text;msg.classList.add('err');btn.disabled=busy=false;bar.hidden=true}
    function counted(){
      var n=chosen.length;list.hidden=!n;
      count.textContent=n?n+(n===1?' photo':' photos')+' ready. Add more, or send them.'+(n>MAX?' That’s more than '+MAX+': take some out and send them next.':''):hint;
    }
    function add(files){
      [].forEach.call(files,function(f){
        if(!/^image\//.test(f.type)&&!/\.(jpe?g|png|webp|gif|heic)$/i.test(f.name))return;
        var li=d.createElement('li'),img=d.createElement('img'),x=d.createElement('button'),cap=d.createElement('input'),fill=d.createElement('span'),
          item={file:f,li:li,cap:cap,fill:fill};
        img.alt='';img.src=URL.createObjectURL(f);img.onerror=function(){li.classList.add('noprev');li.setAttribute('data-name',f.name)};
        x.type='button';x.className='share-x';x.textContent='×';x.setAttribute('aria-label','Take out '+f.name);
        x.addEventListener('click',function(){if(busy)return;chosen.splice(chosen.indexOf(item),1);URL.revokeObjectURL(img.src);li.remove();counted()});
        cap.type='text';cap.maxLength=200;cap.placeholder='Caption (optional)';cap.setAttribute('aria-label','Caption for '+f.name);
        fill.className='share-fill';
        li.appendChild(img);li.appendChild(x);li.appendChild(cap);li.appendChild(fill);list.appendChild(li);chosen.push(item);
      });
      counted();msg.textContent='';msg.classList.remove('err');
    }
    input.addEventListener('change',function(){add(input.files);input.value=''});
    ['dragenter','dragover'].forEach(function(t){drop.addEventListener(t,function(e){e.preventDefault();drop.classList.add('over')})});
    ['dragleave','drop'].forEach(function(t){drop.addEventListener(t,function(){drop.classList.remove('over')})});
    drop.addEventListener('drop',function(e){e.preventDefault();if(!busy&&e.dataTransfer)add(e.dataTransfer.files)});
    // "Something else…" asks for the new album's name
    function showNew(){named.hidden=album.value!=='new'}
    if(album&&named){album.addEventListener('change',function(){showNew();if(!named.hidden)named.querySelector('input').focus()});showNew()}
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
    share.addEventListener('submit',function(e){
      e.preventDefault();
      if(busy)return;
      if(!chosen.length){fail('Add at least one photo.');input.focus();return}
      if(chosen.length>MAX)return fail('You can send up to '+MAX+' photos at a time.');
      busy=btn.disabled=true;msg.classList.remove('err');
      var out=[],i=0;
      (function next(){
        if(i<chosen.length){msg.textContent='Getting your photos ready… '+(i+1)+' of '+chosen.length;shrink(chosen[i].file).then(function(f){out.push(f);i++;next()});return}
        send(out);
      })();
    });
    function send(files){
      var fd=new FormData(share),ends=[],sum=0;
      fd.delete('photos');
      files.forEach(function(f,k){fd.append('photos',f,f.name);fd.append('caption',chosen[k].cap.value);sum+=f.size;ends.push(sum)});
      var x=new XMLHttpRequest();x.open('POST',share.action);x.setRequestHeader('Accept','application/json');
      bar.hidden=false;
      x.upload.onprogress=function(e){
        if(!e.lengthComputable)return;
        var k=e.loaded/e.total,at=k*sum;
        bar.firstChild.style.width=Math.round(k*100)+'%';msg.textContent='Sending… '+Math.round(k*100)+'%';
        chosen.forEach(function(it,j){var from=j?ends[j-1]:0;it.fill.style.width=Math.max(0,Math.min(1,(at-from)/(ends[j]-from)))*100+'%';it.li.classList.toggle('sent',at>=ends[j])});
      };
      x.onload=function(){
        var j=null;try{j=JSON.parse(x.responseText)}catch(err){}
        if(!j)return fail(x.status===413?'Those photos are too big to send together. Try a few at a time.':'Something went wrong. Try again in a minute.');
        if(!j.ok)return fail(j.message);
        var done=d.querySelector('.share-done'),see=done.querySelector('.share-see');done.querySelector('.joined-detail').textContent=j.detail||'';
        if(j.link){see.href=j.link;see.textContent='See them'}
        share.hidden=true;done.hidden=false;done.focus();
      };
      x.onerror=function(){fail('Couldn’t reach the server. Check your connection and try again.')};
      x.send(fd);
    }
  })();

  // Photo albums: "Load more" adds the next page of photos without leaving
  // the page. A photo opens full screen over the page, with arrows (and the
  // arrow keys, or a swipe) to step through the album, its caption and
  // credit, and buttons to share a link to it or download it. While it's
  // open the address bar shows the photo's own page, so that link works too.
  // Without JavaScript, or a browser without <dialog>, each photo opens on
  // its own page.
  var grid=d.querySelector('.gal'),more=d.querySelector('.gal-more a');
  function loadMore(){
    if(!more||!window.fetch||!window.DOMParser)return Promise.resolve(false);
    more.textContent='Loading…';
    return fetch(more.href).then(function(r){return r.text()}).then(function(t){
      var doc=new DOMParser().parseFromString(t,'text/html');
      [].forEach.call(doc.querySelectorAll('.gal>li'),function(li){grid.appendChild(d.importNode(li,true))});
      var nx=doc.querySelector('.gal-more a');
      if(nx){more.href=nx.getAttribute('href');more.textContent='Load more photos'}else{more.parentNode.remove();more=null}
      return true;
    }).catch(function(){if(more)more.textContent='Load more photos';return false});
  }
  if(grid&&more&&window.fetch)more.addEventListener('click',function(e){e.preventDefault();loadMore()});
  var view=d.querySelector('.gal-view');
  if(grid&&view&&view.showModal)(function(){
    var img=view.querySelector('img'),cap=view.querySelector('.gal-view-cap'),by=view.querySelector('.gal-view-by'),n=view.querySelector('.gal-view-n'),
      dl=view.querySelector('.gal-view-dl'),shareBtn=view.querySelector('.gal-view-share'),total=+grid.getAttribute('data-total'),albumURL=location.href,at=0,x0=null,y0=null;
    function links(){return [].slice.call(grid.querySelectorAll('li>a'))}
    function load(el,a){
      var ss=a.getAttribute('data-srcset');
      el.removeAttribute('srcset');if(ss){el.sizes='100vw';el.srcset=ss}
      el.src=a.getAttribute('data-full');
    }
    function show(i){
      var ls=links();at=(i+ls.length)%ls.length;var a=ls[at];
      load(img,a);img.alt=a.querySelector('img').alt;
      cap.textContent=a.getAttribute('data-caption');by.textContent='Photo by '+a.getAttribute('data-by');
      n.textContent=(at+1)+' of '+total;
      dl.href=a.getAttribute('data-full');dl.setAttribute('download',a.getAttribute('data-download'));
      shareBtn.textContent='Share';
      if(history.replaceState)history.replaceState(null,'',a.href);
      var nx=ls[at+1];if(nx)load(new Image(),nx); // starts loading the next one, so stepping feels instant
    }
    function step(by){
      var ls=links();
      if(by>0&&at===ls.length-1&&more)loadMore().then(function(){show(at+1)});
      else show(at+by);
    }
    grid.addEventListener('click',function(e){
      var a=e.target.closest('li>a');
      if(!a||e.metaKey||e.ctrlKey||e.shiftKey)return;
      e.preventDefault();show(links().indexOf(a));view.showModal();
    });
    view.querySelector('.gal-view-x').addEventListener('click',function(){view.close()});
    view.querySelector('.gal-view-prev').addEventListener('click',function(){step(-1)});
    view.querySelector('.gal-view-next').addEventListener('click',function(){step(1)});
    view.addEventListener('click',function(e){if(e.target===view||e.target.tagName==='FIGURE')view.close()});
    view.addEventListener('keydown',function(e){if(e.key==='ArrowLeft')step(-1);else if(e.key==='ArrowRight')step(1)});
    view.addEventListener('close',function(){if(history.replaceState)history.replaceState(null,'',albumURL);var a=links()[at];if(a)a.focus()});
    view.addEventListener('touchstart',function(e){x0=e.touches[0].clientX;y0=e.touches[0].clientY},{passive:true});
    view.addEventListener('touchend',function(e){
      if(x0===null)return;var dx=e.changedTouches[0].clientX-x0,dy=e.changedTouches[0].clientY-y0;x0=null;
      if(Math.abs(dx)>50&&Math.abs(dx)>Math.abs(dy))step(dx<0?1:-1);
      else if(dy>90&&Math.abs(dy)>Math.abs(dx)*2)view.close(); // a swipe down closes it
    });
    shareBtn.addEventListener('click',function(){
      var url=links()[at].href;
      if(navigator.share)navigator.share({title:d.title,url:url}).catch(function(){});
      else if(navigator.clipboard)navigator.clipboard.writeText(url).then(function(){shareBtn.textContent='Link copied'});
      else prompt('Copy this link',url);
    });
    if(total<2)[].forEach.call(view.querySelectorAll('.gal-view-prev,.gal-view-next'),function(b){b.hidden=true});
  })();
  // A photo's own page: the arrow keys step to the next and previous photos
  if(d.querySelector('.photo-page'))d.addEventListener('keydown',function(e){
    var a=d.querySelector(e.key==='ArrowLeft'?'.photo-nav [rel=prev]':e.key==='ArrowRight'?'.photo-nav [rel=next]':null);
    if(a)location.href=a.href;
  });

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
