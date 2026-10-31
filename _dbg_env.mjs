const raw = process.env.WB_KEY;
const safe = typeof raw === 'string' && /^[A-Za-z0-9_.\-]+$/.test(raw);
console.log('DEBUG typeof=' + typeof raw + ' len=' + (raw ? raw.length : -1) + ' simpleCharset=' + safe);
