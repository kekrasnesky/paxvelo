// shrinkPhoto makes a photo small enough to upload quickly before it leaves
// the phone or computer: at most `side` pixels on its long side (2000 unless
// given), as a JPEG. An iPhone photo can be 10 MB or more (48 megapixels),
// over the site's limit; this makes it about half a megabyte. The photo is
// decoded with an <img> first, which phones manage for huge photos without
// running out of memory, and which turns it the right way up. Small photos,
// GIFs, and PNGs under 8 MB (they may have see-through parts) go as they are,
// and so does anything the browser can't read: the server checks again.
// Shared by the public site (site.js) and the admin pages.
window.shrinkPhoto = function (file, side) {
  side = side || 2000;
  var heic = /^image\/hei[cf]$/i.test(file.type) || /\.hei[cf]$/i.test(file.name);
  if (!heic && (!/^image\/(jpeg|png|webp)$/i.test(file.type) || file.size < 1.5e6 || (file.type === 'image/png' && file.size < 8e6))) return Promise.resolve(file);
  function decode() {
    return new Promise(function (ok, no) {
      if (!window.URL || !URL.createObjectURL) return no();
      var url = URL.createObjectURL(file), img = new Image();
      img.onload = function () { ok({ src: img, w: img.naturalWidth, h: img.naturalHeight, done: function () { URL.revokeObjectURL(url) } }) };
      img.onerror = function () { URL.revokeObjectURL(url); no() };
      img.src = url;
    }).catch(function () {
      if (!window.createImageBitmap) throw new Error('no decoder');
      return createImageBitmap(file).then(function (b) { return { src: b, w: b.width, h: b.height, done: function () { if (b.close) b.close() } } });
    });
  }
  return decode().then(function (d) {
    var k = Math.min(1, side / Math.max(d.w, d.h)), c = document.createElement('canvas');
    c.width = Math.max(1, Math.round(d.w * k)); c.height = Math.max(1, Math.round(d.h * k));
    var g = c.getContext('2d'); g.imageSmoothingQuality = 'high'; g.drawImage(d.src, 0, 0, c.width, c.height); d.done();
    return new Promise(function (ok) {
      c.toBlob(function (b) {
        ok(b && (heic || b.size < file.size) ? new File([b], file.name.replace(/\.\w+$/, '') + '.jpg', { type: 'image/jpeg', lastModified: file.lastModified }) : file);
      }, 'image/jpeg', 0.86);
    });
  }).catch(function () { return file });
};

// shrinkPicked makes the photos picked in any upload box on the page smaller
// before anything else sees them: it holds back the box's "change", swaps in
// the smaller photos, then lets it go on. Boxes of other files are left alone.
window.shrinkPicked = function () {
  function big(f) { return (/^image\//.test(f.type) && f.size >= 1.5e6) || /\.hei[cf]$/i.test(f.name) }
  document.addEventListener('change', function (e) {
    var inp = e.target;
    if (!inp || inp.type !== 'file' || inp._shrunk || !window.DataTransfer || !inp.files || ![].some.call(inp.files, big)) return;
    e.stopImmediatePropagation();
    var label = inp.closest('label'); if (label) label.setAttribute('aria-busy', 'true');
    Promise.all([].map.call(inp.files, function (f) { return shrinkPhoto(f) })).then(function (out) {
      var dt = new DataTransfer(); out.forEach(function (f) { dt.items.add(f) });
      try { inp.files = dt.files } catch (err) {}
      if (label) label.removeAttribute('aria-busy');
      inp._shrunk = true; inp.dispatchEvent(new Event('change', { bubbles: true })); inp._shrunk = false;
    });
  }, true);
};
