// Generates the WebCrypto signature fixture used by web tests:
//   node internal/web/testdata/webcrypto-sig.mjs > internal/web/testdata/webcrypto-sig.json
// It uses exactly the calls the PWA makes (generateKey/exportKey spki/sign
// with ECDSA P-256 SHA-256), so the Go verifier is tested against real
// WebCrypto output (IEEE P1363 r||s).
const { subtle } = globalThis.crypto;
const b64url = (buf) => Buffer.from(buf).toString('base64url');

const pair = await subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign', 'verify']);
const spki = await subtle.exportKey('spki', pair.publicKey);
const fixture = {
  request_id: 'r_fixture001',
  behavior: 'allow',
  scope: 'session',
  ts: 1767225600000, // 2026-01-01T00:00:00Z
  nonce: 'bm9uY2Utd2ViY3J5cHRvLTE',
};
const canonical = ['pilothouse-decide:v1', fixture.request_id, fixture.behavior, fixture.scope, String(fixture.ts), fixture.nonce].join('\n');
const sig = await subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, pair.privateKey, new TextEncoder().encode(canonical));
console.log(JSON.stringify({ ...fixture, public_key: b64url(spki), canonical, sig: b64url(sig) }, null, 2));
