// The site's service worker, served at /sw.js (app.go). It lets phones open
// the site as an app from the home screen, and shows the pages a rider has
// already opened (the calendar, a ride's page) when there's no signal.
//
// Pages always come from the site first; the saved copy is only for when the
// network fails. Nothing personal is saved: no admin, membership, RSVP, kit,
// form or link-with-a-token pages, and nothing from another site. A new
// version of the site (__V__) starts with empty caches.
var V='__V__',PAGES='pages-'+V,FILES='files-'+V,OFFLINE='/offline',MAX_PAGES=40;

// Pages never saved: personal, forms, or only for admins.
var PRIVATE=/^\/(admin|membership|rsvp|kit|unsubscribe|request-answer|e\/|strava|join|share-photos|ride-waiver|loaner|calendar\/new|calendar\/ride\/[^/]+\/loaner|minutes|files\/)/;

self.addEventListener('install',function(e){
  e.waitUntil(caches.open(PAGES).then(function(c){return c.add(new Request(OFFLINE,{cache:'reload'}))}).then(function(){return self.skipWaiting()}));
});

self.addEventListener('activate',function(e){
  e.waitUntil(caches.keys().then(function(keys){
    return Promise.all(keys.filter(function(k){return k!==PAGES&&k!==FILES}).map(function(k){return caches.delete(k)}));
  }).then(function(){return self.clients.claim()}));
});

// A page worth keeping: on this site, not private, and no query string but
// the calendar's month (?m=).
function keepable(url){
  if(url.origin!==location.origin||PRIVATE.test(url.pathname)||url.pathname===OFFLINE)return false;
  var q=[];url.searchParams.forEach(function(v,k){q.push(k)});
  return q.length===0||(q.length===1&&q[0]==='m');
}

// Keep the newest pages only.
function trim(c){
  return c.keys().then(function(keys){
    if(keys.length<=MAX_PAGES+1)return; // +1: the offline page
    var old=keys.filter(function(k){return new URL(k.url).pathname!==OFFLINE}).slice(0,keys.length-MAX_PAGES-1);
    return Promise.all(old.map(function(k){return c.delete(k)}));
  });
}

// Ride alerts (push.go) and admin alerts (adminpush.go): show the message, and open the ride when it's
// tapped (or bring the app forward if it's already showing it).
self.addEventListener('push',function(e){
  var m={};
  try{m=e.data?e.data.json():{}}catch(x){}
  e.waitUntil(self.registration.showNotification(m.title||'PaxVelo',{
    body:m.body||'',icon:/^\/static\/img\/[\w-]+\.png$/.test(m.icon||'')?m.icon:'/static/img/app-192.png',badge:'/static/img/badge.png',
    tag:m.tag||undefined,renotify:!!m.tag,data:{url:m.url||'/'}
  }));
});

self.addEventListener('notificationclick',function(e){
  e.notification.close();
  var url=new URL((e.notification.data&&e.notification.data.url)||'/',location.origin);
  if(url.origin!==location.origin)url=new URL('/',location.origin);
  e.waitUntil(self.clients.matchAll({type:'window',includeUncontrolled:true}).then(function(ws){
    for(var i=0;i<ws.length;i++)if(ws[i].url===url.href&&'focus' in ws[i])return ws[i].focus();
    return self.clients.openWindow(url.href);
  }));
});

// The push service changed this phone's address: subscribe again and tell
// the site.
self.addEventListener('pushsubscriptionchange',function(e){
  function post(path,body){return fetch(path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)})}
  e.waitUntil(fetch('/push/key').then(function(r){return r.json()}).then(function(k){
    var s=k.key.replace(/-/g,'+').replace(/_/g,'/');while(s.length%4)s+='=';
    var raw=atob(s),key=new Uint8Array(raw.length);for(var i=0;i<raw.length;i++)key[i]=raw.charCodeAt(i);
    return self.registration.pushManager.subscribe({userVisibleOnly:true,applicationServerKey:key});
  }).then(function(sub){
    // old: the site moves this phone's Ride changes / Club news choices over
    var j=sub.toJSON();if(e.oldSubscription)j.old=e.oldSubscription.endpoint;
    return post('/push/subscribe',j);
  }));
});

self.addEventListener('fetch',function(e){
  var r=e.request,url=new URL(r.url);
  if(r.method!=='GET'||url.origin!==location.origin)return;

  if(r.mode==='navigate'){
    var keep=keepable(url);
    e.respondWith(fetch(r).then(function(res){
      if(keep&&res.ok&&!res.redirected&&res.type==='basic'){
        var copy=res.clone();
        e.waitUntil(caches.open(PAGES).then(function(c){return c.delete(r.url).then(function(){return c.put(r.url,copy)}).then(function(){return trim(c)})}));
      }
      return res;
    }).catch(function(){
      return caches.open(PAGES).then(function(c){
        return (keep?c.match(r.url):Promise.resolve()).then(function(hit){return hit||c.match(OFFLINE)});
      });
    }));
    return;
  }

  // Fonts and pictures with a version in the address never change: keep them.
  if(url.pathname.indexOf('/static/')===0&&url.searchParams.has('v')){
    e.respondWith(caches.open(FILES).then(function(c){
      return c.match(r).then(function(hit){
        return hit||fetch(r).then(function(res){if(res.ok)c.put(r,res.clone());return res});
      });
    }));
  }
});
