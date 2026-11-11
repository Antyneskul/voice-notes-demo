import test from 'node:test';
import assert from 'node:assert/strict';
import { resolveApiUrl } from './apiUrl.ts';

test('an explicit URL always wins over the dev server host', () => {
  assert.equal(resolveApiUrl({ envUrl: 'http://10.0.0.9:9000', hostUri: '192.168.1.20:8081' }), 'http://10.0.0.9:9000');
});

test('trims whitespace and trailing slashes from an explicit URL', () => {
  assert.equal(resolveApiUrl({ envUrl: '  http://10.0.0.9:9000//  ' }), 'http://10.0.0.9:9000');
});

test('a blank explicit URL is ignored', () => {
  assert.equal(resolveApiUrl({ envUrl: '   ', hostUri: '192.168.1.20:8081' }), 'http://192.168.1.20:8080');
  assert.equal(resolveApiUrl({ envUrl: '', hostUri: '192.168.1.20:8081' }), 'http://192.168.1.20:8080');
});

test('without an explicit URL, uses the dev server host with the backend port', () => {
  assert.equal(resolveApiUrl({ hostUri: '192.168.1.20:8081' }), 'http://192.168.1.20:8080');
});

test('follows the dev server host when it changes', () => {
  assert.equal(resolveApiUrl({ hostUri: '192.168.1.20:8081' }), 'http://192.168.1.20:8080');
  assert.equal(resolveApiUrl({ hostUri: '10.1.20.33:8081' }), 'http://10.1.20.33:8080');
});

test('copes with a missing port or a trailing path on the host', () => {
  assert.equal(resolveApiUrl({ hostUri: '192.168.1.20' }), 'http://192.168.1.20:8080');
  assert.equal(resolveApiUrl({ hostUri: '192.168.1.20:8081/--/some/path' }), 'http://192.168.1.20:8080');
});

test('localhost and .local hostnames are used as they are', () => {
  assert.equal(resolveApiUrl({ hostUri: 'localhost:8081' }), 'http://localhost:8080');
  assert.equal(resolveApiUrl({ hostUri: '127.0.0.1:8081' }), 'http://127.0.0.1:8080');
  assert.equal(resolveApiUrl({ hostUri: 'my-mac.local:8081' }), 'http://my-mac.local:8080');
});

test('a tunnel host cannot reach a backend on your computer, so fall back to localhost', () => {
  assert.equal(resolveApiUrl({ hostUri: 'abc-123.exp.direct:80' }), 'http://localhost:8080');
  assert.equal(resolveApiUrl({ hostUri: 'something.ngrok.io' }), 'http://localhost:8080');
});

test('no host at all falls back to localhost', () => {
  assert.equal(resolveApiUrl({}), 'http://localhost:8080');
  assert.equal(resolveApiUrl({ hostUri: null }), 'http://localhost:8080');
  assert.equal(resolveApiUrl({ hostUri: '' }), 'http://localhost:8080');
});

test('an explicit URL without a scheme gets http:// (a common typo), and https is left alone', () => {
  assert.equal(resolveApiUrl({ envUrl: '192.168.1.5:8080' }), 'http://192.168.1.5:8080');
  assert.equal(resolveApiUrl({ envUrl: 'https://api.example.com' }), 'https://api.example.com');
  assert.equal(resolveApiUrl({ envUrl: 'HTTP://10.0.0.9:9000' }), 'HTTP://10.0.0.9:9000');
});

test('.local is matched case-insensitively', () => {
  assert.equal(resolveApiUrl({ hostUri: 'My-Mac.LOCAL:8081' }), 'http://My-Mac.LOCAL:8080');
});

test('IPv6 literals and scheme-prefixed values fall back to localhost instead of producing a mangled URL', () => {
  for (const hostUri of ['[::1]:8081', '[fe80::1]:8081', 'fe80::1', 'exp://192.168.1.20:8081']) {
    assert.equal(resolveApiUrl({ hostUri }), 'http://localhost:8080', hostUri);
  }
});

test('LAN-style names other than .local (for example .lan) fall back to localhost; edit the field in the app', () => {
  assert.equal(resolveApiUrl({ hostUri: 'nas.lan:8081' }), 'http://localhost:8080');
});
