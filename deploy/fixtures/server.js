// Local-only Torznab index and BitTorrent tracker for an owned test payload.
// Real Prowlarr and qBittorrent instances handle the search and peer transfer.
const http = require('node:http');
const fs = require('node:fs');
const fixture = JSON.parse(fs.readFileSync('/fixture/fixture.json'));
const peers = new Map();
const xml = s => String(s).replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;');
http.createServer((req, res) => {
  const url = new URL(req.url, 'http://fixture:8765');
  if (url.pathname === '/announce') {
    const address = req.socket.remoteAddress.replace('::ffff:', '');
    const port = Number(url.searchParams.get('port'));
    const key = `${address}:${port}`;
    if (url.searchParams.get('event') === 'stopped') peers.delete(key);
    else if (port > 0 && port < 65536) peers.set(key, {address, port, at: Date.now()});
    const list = [...peers.entries()].filter(([k,p]) => k !== key && Date.now()-p.at < 120000)
      .map(([,p]) => Buffer.from([...p.address.split('.').map(Number), p.port >> 8, p.port & 255]));
    const compact = Buffer.concat(list);
    res.end(Buffer.concat([Buffer.from(`d8:intervali5e5:peers${compact.length}:`), compact, Buffer.from('e')]));
  } else if (url.pathname === '/status') {
    res.setHeader('Content-Type','application/json');
    res.end(JSON.stringify([...peers.values()]));
  } else if (url.pathname === '/fixture.torrent') {
    res.setHeader('Content-Type', 'application/x-bittorrent');
    fs.createReadStream('/fixture/fixture.torrent').pipe(res);
  } else if (url.searchParams.get('t') === 'caps') {
    res.setHeader('Content-Type','application/xml');
    res.end('<?xml version="1.0"?><caps><server version="1.0" title="MediaDock owned fixture"/><limits max="100" default="100"/><searching><search available="yes" supportedParams="q"/><tv-search available="no"/><movie-search available="no"/></searching><categories><category id="2000" name="Movies"/></categories></caps>');
  } else {
    const query = (url.searchParams.get('q') || '').toLowerCase();
    const item = !query || fixture.title.toLowerCase().includes(query) ? `<item><title>${xml(fixture.title)}</title><guid>${fixture.hash}</guid><link>${xml(fixture.magnet)}</link><comments>http://fixture:8765/fixture.torrent</comments><pubDate>${new Date().toUTCString()}</pubDate><size>${fixture.size}</size><category>2000</category><enclosure url="${xml(fixture.magnet)}" length="${fixture.size}" type="application/x-bittorrent"/><torznab:attr name="category" value="2000"/><torznab:attr name="seeders" value="1"/><torznab:attr name="peers" value="1"/><torznab:attr name="infohash" value="${fixture.hash}"/><torznab:attr name="magneturl" value="${xml(fixture.magnet)}"/></item>` : '';
    res.setHeader('Content-Type','application/xml');
    res.end(`<?xml version="1.0"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><title>MediaDock owned fixture</title>${item}</channel></rss>`);
  }
}).listen(8765, '0.0.0.0');
