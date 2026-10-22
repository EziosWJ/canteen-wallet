// Requires Vite and a disposable Chromium with --remote-debugging-port=9237.
// API responses and employee sessions are fixtures; no backend is contacted.
import assert from 'node:assert/strict';

const cdpURL = process.env.CANTEEN_TEST_CDP_URL || 'http://127.0.0.1:9237';
const origin = process.env.CANTEEN_TEST_WEB_URL || 'http://127.0.0.1:4187';
const target = await fetch(`${cdpURL}/json/new?about:blank`, { method: 'PUT' }).then(response => response.json());
const socket = new WebSocket(target.webSocketDebuggerUrl);
await new Promise(resolve => socket.addEventListener('open', resolve, { once: true }));
let sequence = 0;
const pending = new Map();
const errors = [];
socket.onmessage = event => {
  const message = JSON.parse(event.data);
  if (message.id) {
    const request = pending.get(message.id);
    pending.delete(message.id);
    if (message.error) request.reject(new Error(JSON.stringify(message.error)));
    else request.resolve(message.result);
  } else if (message.method === 'Runtime.exceptionThrown') errors.push(message.params.exceptionDetails.exception?.description || message.params.exceptionDetails.text);
};
function send(method, params = {}) {
  return new Promise((resolve, reject) => {
    const id = ++sequence;
    pending.set(id, { resolve, reject });
    socket.send(JSON.stringify({ id, method, params }));
  });
}
async function evaluate(expression) {
  const result = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
  if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
  return result.result.value;
}
async function wait(expression) {
  for (let attempt = 0; attempt < 100; attempt++) {
    if (await evaluate(expression)) return;
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  throw new Error(`Timed out: ${expression}`);
}
function installFixtures() {
  if (location.protocol !== 'http:' && location.protocol !== 'https:') return;
  if (!sessionStorage.getItem('navigation-fixture-started')) {
    localStorage.clear();
    sessionStorage.setItem('navigation-fixture-started', '1');
  }
  window.testRequests = [];
  window.makeSession = (token = 'fixture-session') => ({
    access_token: token, token_type: 'Bearer', employee_id: 1,
    must_change_password: false, expires_at: new Date(Date.now() + 3600000).toISOString(),
  });
  window.seedEmployee = (token) => localStorage.setItem('canteen-employee-session', JSON.stringify(window.makeSession(token)));
  const employee = {
    id: 1, name: '测试员工', employee_no: 'T001', phone: '13800000000', department: '测试',
    status: 'ACTIVE', account_status: 'ACTIVE', balance: 5000, must_change_password: false,
  };
  const originalFetch = window.fetch.bind(window);
  const response = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  window.fetch = async (input, options = {}) => {
    const path = new URL(typeof input === 'string' ? input : input.url, location.href).pathname;
    if (!path.startsWith('/api/')) return originalFetch(input, options);
    window.testRequests.push({ path, method: options.method || 'GET' });
    if (path === '/api/me' && window.deferNextMe) {
      window.deferNextMe = false;
      await new Promise(resolve => { window.releaseOldMe = resolve; });
      return response({ code: 'UNAUTHORIZED' }, 401);
    }
    if (path === '/api/me' && window.testNetworkFailure) throw new Error('Fixture offline');
    if (path === '/api/me' && window.testUnauthorized) return response({ code: 'UNAUTHORIZED' }, 401);
    if (path === '/api/auth/login') return response({ ...window.makeSession(), must_change_password: !!window.testForced });
    if (path === '/api/me/change-password') { window.testForced = false; return response(window.makeSession('changed-password-session')); }
    if (path === '/api/me') return response(window.testForced ? { id: 1, must_change_password: true } : employee);
    if (path === '/api/me/account') return response({ balance: 5000, status: 'ACTIVE' });
    if (path === '/api/me/transactions') return response({ items: [], next_cursor: null });
    if (path === '/api/me/meal-periods') return response({ meal_periods: [] });
    if (path === '/api/consumption-modes') return response({ payment_code: true, self_service: true });
    if (path === '/api/me/payment-presentation') return response({
      presentation_id: 'fixture-presentation', state: 'WAITING', server_time: new Date().toISOString(),
    });
    if (path === '/api/me/payment-token') return response({
      token: 'fixture-dynamic-payment-token', presentation_id: 'fixture-presentation', server_time: new Date().toISOString(),
      refresh_after: new Date(Date.now() + 30000).toISOString(), expires_at: new Date(Date.now() + 60000).toISOString(),
    });
    if (path === '/api/me/self-service') return response({
      status: 'READY', intent_id: 'fixture-intent', meal_code: 'LUNCH', meal_name: '午餐',
      amount_cents: 1000, existing_count: 0, business_date: '2026-10-22',
    });
    if (path === '/api/me/self-service/fixture-intent/result') return response({ status: 'CONFIRM_REQUIRED' });
    throw new Error(`Unexpected fixture API: ${path}`);
  };
}
await send('Page.enable');
await send('Runtime.enable');
await send('Emulation.setFocusEmulationEnabled', { enabled: true });
await send('Page.addScriptToEvaluateOnNewDocument', { source: `(${installFixtures.toString()})()` });
async function go(path) {
  await send('Page.navigate', { url: origin + path });
  await wait(`location.href === ${JSON.stringify(origin + path)} && document.readyState === 'complete'`);
}
async function click(text) {
  await evaluate(`(async () => { for (let attempt = 0; attempt < 100; attempt++) { const element = Array.from(document.querySelectorAll('button,a')).find(element => (element.textContent.trim() === ${JSON.stringify(text)} || element.querySelector('strong')?.textContent === ${JSON.stringify(text)})); if (element) { element.click(); return; } await new Promise(resolve => setTimeout(resolve, 50)); } throw new Error('Button not found: ' + ${JSON.stringify(text)}); })()`);
}
async function login() {
  await evaluate(`(() => {
    const set = (selector, value) => {
      const input = document.querySelector(selector);
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, value);
      input.dispatchEvent(new Event('input', { bubbles: true }));
    };
    set('input[type=tel]', '13800000000'); set('input[autocomplete=current-password]', 'fixture-password');
  })()`);
  await evaluate('document.querySelector("form").requestSubmit()');
}
const home = `!!document.querySelector('.home-page')`;
const loginForm = `!!document.querySelector('input[type=tel]')`;
const heading = text => `document.querySelector('h1')?.textContent === ${JSON.stringify(text)}`;
const back = () => evaluate('history.back()');
try {
  // The original regression: a frozen login screen misses the storage event.
  await go('/'); await wait(loginForm);
  await evaluate('window.seedEmployee();window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }))');
  await wait(home);
  console.log('PASS: cached login screen restores the current session');

  // Direct scans need an internal home entry, while home must allow departure.
  await send('Page.navigate', { url: 'data:text/html,<h1>Outside the canteen</h1>' });
  await wait(`location.protocol === 'data:'`);
  await go('/payment-code'); await wait(heading('我的就餐码'));
  const scannedHistoryLength = await evaluate('history.length');
  await send('Page.reload'); await wait(heading('我的就餐码'));
  assert.equal(await evaluate('history.length'), scannedHistoryLength, 'Reload must not add history entries');
  await back(); await wait(home);
  assert.equal(await evaluate('location.pathname'), '/');
  await back(); await wait(`location.protocol === 'data:'`);
  console.log('PASS: scanned dining code backs to home, then exits; reload does not grow history');

  await go('/'); await wait(home);
  await click('就餐码'); await wait(heading('我的就餐码'));
  await click('记录'); await wait(heading('资金流水'));
  assert.equal(await evaluate('location.pathname'), '/transactions');
  await send('Page.reload'); await wait(heading('资金流水'));
  await click('我的'); await wait(heading('个人信息'));
  await back(); await wait(home);
  await evaluate('history.forward()'); await wait(heading('个人信息'));
  await back(); await wait(home);
  const rootLength = await evaluate('history.length');
  await click('首页'); assert.equal(await evaluate('history.length'), rootLength);
  console.log('PASS: tab switching, refresh, Back/Forward, and repeated home clicks');

  await click('我的'); await wait(heading('个人信息'));
  await click('修改登录密码'); await wait(heading('修改登录密码'));
  await back(); await wait(heading('个人信息'));
  await back(); await wait(home);
  await click('线下充值说明'); await wait(`!!document.querySelector('[role=dialog]')`);
  await back(); await wait(`!document.querySelector('[role=dialog]') && ${home}`);
  await back(); await wait(`location.protocol === 'data:'`);
  console.log('PASS: Back closes password form and recharge dialog before leaving');

  await go('/self-service'); await wait(heading('自助消费'));
  await wait(`document.body.textContent.includes('确认扣款')`);
  assert.equal(await evaluate(`window.testRequests.some(request => request.path.endsWith('/confirm'))`), false);
  await back(); await wait(home);
  await click('自助消费'); await wait(heading('自助消费'));
  await back(); await wait(home);
  console.log('PASS: self-service scan and home entry share navigation; Back never confirms payment');

  await evaluate('window.testNetworkFailure = true;window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }))');
  await wait(home);
  assert.ok(await evaluate('localStorage.getItem("canteen-employee-session")'));
  await evaluate('window.testNetworkFailure = false;window.testUnauthorized = true;window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }))');
  await wait(loginForm);
  assert.equal(await evaluate('localStorage.getItem("canteen-employee-session")'), null);
  console.log('PASS: offline resume retains login; genuine unauthorized response clears it');

  await evaluate('window.testUnauthorized = false;window.testForced = true');
  await login(); await wait(heading('请先修改临时密码'));
  await evaluate(`(() => {
    const inputs = document.querySelectorAll('input[type=password]');
    ['old-password', 'new-password', 'new-password'].forEach((value, index) => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(inputs[index], value);
      inputs[index].dispatchEvent(new Event('input', { bubbles: true }));
    });
  })()`);
  await evaluate('document.querySelector("form").requestSubmit()'); await wait(home);
  console.log('PASS: first-login password change remains mandatory and returns to requested page');

  await evaluate(`window.deferNextMe = true;window.seedEmployee('older-session');window.dispatchEvent(new StorageEvent('storage', { key: 'canteen-employee-session' }))`);
  await wait(`typeof window.releaseOldMe === 'function'`);
  await evaluate(`window.seedEmployee('newer-session');window.dispatchEvent(new StorageEvent('storage', { key: 'canteen-employee-session' }))`);
  await wait(home);
  await evaluate('window.releaseOldMe()');
  // Cross a task boundary after the rejected response has settled.
  await evaluate('new Promise(resolve => setTimeout(resolve, 50))');
  assert.equal(await evaluate(`JSON.parse(localStorage.getItem('canteen-employee-session')).access_token`), 'newer-session');
  assert.equal(await evaluate(loginForm), false);
  console.log('PASS: delayed unauthorized response from an old session cannot log out the new session');

  await click('我的'); await wait(heading('个人信息'));
  // Logout is explicitly mocked here; every other unexpected API still fails.
  await evaluate(`{ const original = window.fetch; window.fetch = (input, options) => input === '/api/auth/logout' ? Promise.resolve(new Response('{}')) : original(input, options); }`);
  await click('退出登录'); await wait(loginForm);
  await back(); await wait(`location.pathname === '/' && ${loginForm}`);
  await evaluate('window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }))');
  assert.equal(await evaluate(home), false);

  await go('/payment-code/'); await wait(loginForm);
  await login(); await wait(heading('我的就餐码'));
  await back(); await wait(home);
  console.log('PASS: fresh scan login, including trailing slash, backs to home');
  await click('记录'); await wait(heading('资金流水'));
  await evaluate(`document.querySelector('.bottom-nav button').click();document.querySelector('.bottom-nav button')?.click()`);
  await wait(home);
  await evaluate('new Promise(resolve => setTimeout(resolve, 50))');
  assert.equal(await evaluate('location.pathname'), '/', 'Rapid home clicks must not navigate past home');
  await evaluate('localStorage.removeItem("canteen-employee-session");window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }))');
  await wait(loginForm);
  console.log('PASS: rapid home clicks stay inside; cached pages honor a cleared session');
  assert.deepEqual(errors, []);
  console.log('PASS: explicit logout stays logged out after Back and page restoration');
} finally {
  await send('Page.close');
  socket.close();
}
