// Frontend checks, run with Node (no dependencies):
//
//   node frontend_test/run.js
//
// 1. Unit tests for the Markdown renderer (md.js).
// 2. A smoke test of app.js against a stub DOM and a stub Go bridge. The stub
//    DOM only knows the element ids that exist in index.html, so a typo in an
//    id fails the test, as does any exception while wiring up or handling the
//    events and clicks exercised below.
//
// This does not replace trying the app: layout and real WebKit behaviour are
// not covered.
'use strict';

const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const frontend = path.join(__dirname, '..', 'frontend');
let failures = 0;

function test(name, fn) {
  try {
    const result = fn();
    if (result && typeof result.then === 'function') {
      return result.then(
        () => console.log('ok   ' + name),
        (err) => { failures++; console.log('FAIL ' + name + '\n     ' + (err && err.stack || err)); }
      );
    }
    console.log('ok   ' + name);
  } catch (err) {
    failures++;
    console.log('FAIL ' + name + '\n     ' + (err && err.stack || err));
  }
  return Promise.resolve();
}

// ------------------------------------------------------------------ md.js

const { renderMarkdown } = require(path.join(frontend, 'md.js'));

const mdTests = [
  ['escapes HTML in text', () => {
    assert.strictEqual(renderMarkdown('<script>alert(1)</script>'),
      '<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>');
  }],
  ['escapes HTML inside inline code and code blocks', () => {
    assert.strictEqual(renderMarkdown('use `<b>`'), '<p>use <code>&lt;b&gt;</code></p>');
    assert.strictEqual(renderMarkdown('```html\n<img src=x onerror=1>\n```'),
      '<pre><code>&lt;img src=x onerror=1&gt;</code></pre>');
  }],
  ['escapes quotes so attributes cannot be forged', () => {
    assert.ok(!renderMarkdown('" onclick="x').includes('"'));
  }],
  ['bold and italic', () => {
    assert.strictEqual(renderMarkdown('a **b** and *c*.'), '<p>a <strong>b</strong> and <em>c</em>.</p>');
  }],
  ['asterisks used as multiplication are left alone', () => {
    assert.strictEqual(renderMarkdown('2 * 3 * 4'), '<p>2 * 3 * 4</p>');
  }],
  ['bullet and numbered lists', () => {
    assert.strictEqual(renderMarkdown('- one\n- two'), '<ul><li>one</li><li>two</li></ul>');
    assert.strictEqual(renderMarkdown('1. one\n2. two'), '<ol><li>one</li><li>two</li></ol>');
  }],
  ['paragraph then list then paragraph', () => {
    assert.strictEqual(renderMarkdown('Intro\n- a\n- b\n\nOutro'),
      '<p>Intro</p><ul><li>a</li><li>b</li></ul><p>Outro</p>');
  }],
  ['headings are rendered small', () => {
    assert.strictEqual(renderMarkdown('# Title'), '<h3>Title</h3>');
    assert.strictEqual(renderMarkdown('### Deep'), '<h5>Deep</h5>');
  }],
  ['line breaks inside a paragraph', () => {
    assert.strictEqual(renderMarkdown('a\nb'), '<p>a<br>b</p>');
  }],
  ['an unterminated code fence still renders (mid-stream)', () => {
    assert.strictEqual(renderMarkdown('text\n```go\nfmt.Println("hi")'),
      '<p>text</p><pre><code>fmt.Println(&quot;hi&quot;)</code></pre>');
  }],
  ['markdown inside a code block is not interpreted', () => {
    assert.strictEqual(renderMarkdown('```\n**x**\n- y\n```'), '<pre><code>**x**\n- y</code></pre>');
  }],
  ['empty and null input', () => {
    assert.strictEqual(renderMarkdown(''), '');
    assert.strictEqual(renderMarkdown(null), '');
  }],
];

// ----------------------------------------------------------------- app.js

function htmlIds() {
  // id -> whether the element starts with the `hidden` attribute
  const html = fs.readFileSync(path.join(frontend, 'index.html'), 'utf8');
  const ids = new Map();
  const re = /<[a-z0-9]+\b[^>]*\bid="([^"]+)"[^>]*>/gi;
  let m;
  while ((m = re.exec(html))) {
    assert.ok(!ids.has(m[1]), 'duplicate id in index.html: ' + m[1]);
    ids.set(m[1], /\shidden(\s|>|=)/.test(m[0]));
  }
  return ids;
}

function makeElement(id) {
  const listeners = {};
  const el = {
    id,
    hidden: false,
    disabled: false,
    checked: false,
    value: '',
    textContent: '',
    className: '',
    title: '',
    placeholder: '',
    type: '',
    scrollTop: 0,
    scrollHeight: 0,
    clientHeight: 0,
    children: [],
    parentNode: null,
    style: { setProperty() {} },
    classList: { add() {}, remove() {} },
    addEventListener(type, fn) { (listeners[type] = listeners[type] || []).push(fn); },
    dispatch(type, event) {
      (listeners[type] || []).forEach((fn) => fn(event || { preventDefault() {} }));
      return (listeners[type] || []).length;
    },
    appendChild(child) { child.parentNode = el; el.children.push(child); return child; },
    removeChild(child) { el.children = el.children.filter((c) => c !== child); child.parentNode = null; return child; },
    get lastChild() { return el.children[el.children.length - 1] || null; },
    set innerHTML(v) { el._html = v; el.children = []; },
    get innerHTML() { return el._html || ''; },
  };
  return el;
}

function loadApp(bridge) {
  const ids = htmlIds();
  const elements = {};
  const events = {};
  const docListeners = {};

  const document = {
    readyState: 'complete',
    documentElement: makeElement('html'),
    body: makeElement('body'),
    getElementById(id) {
      if (!ids.has(id)) {
        return null; // app.js turns this into "missing element #id"
      }
      if (!elements[id]) {
        elements[id] = makeElement(id);
        elements[id].hidden = ids.get(id);
      }
      return elements[id];
    },
    createElement() { return makeElement(''); },
    addEventListener(type, fn) { (docListeners[type] = docListeners[type] || []).push(fn); },
  };
  const window = {
    document,
    go: { main: { App: bridge } },
    runtime: { EventsOn(name, fn) { events[name] = fn; } },
    requestAnimationFrame(fn) { fn(); },
  };
  window.window = window;
  const context = vm.createContext({ window, document, setTimeout, Promise, console, globalThis: window });
  vm.runInContext(fs.readFileSync(path.join(frontend, 'md.js'), 'utf8'), context, { filename: 'md.js' });
  vm.runInContext(fs.readFileSync(path.join(frontend, 'app.js'), 'utf8'), context, { filename: 'app.js' });
  return { el: (id) => document.getElementById(id), events, docListeners, document };
}

function sampleState(overrides) {
  return Object.assign({
    supported: true, listening: false, mic: false, system: false, onDevice: true, listenNote: '',
    visible: true, stealth: true, opacity: 0.82, provider: 'gemini', model: 'gemini-3.8-flash',
    ready: true, readyHint: '', answering: false,
    permissions: { screen: 'granted', microphone: 'unknown', speech: 'denied' },
    canRequestPermissions: true,
    hotkeyToggle: 'cmd+shift+backslash', hotkeyHelp: 'cmd+shift+return', hotkeyErrors: [],
  }, overrides || {});
}

function sampleSettings() {
  return {
    provider: 'gemini',
    providers: {
      gemini: { apiKey: '', model: 'gemini-3.8-flash', baseUrl: '', reasoning: 'low' },
      anthropic: { apiKey: '', model: '', baseUrl: '', reasoning: '' },
      openai: { apiKey: '', model: '', baseUrl: '', reasoning: '' },
    },
    answerPrompt: 'Be brief.', contextFiles: [], language: 'en-US',
    captureMic: true, captureSystem: true, autoListen: true, includeScreenshot: true,
    transcriptMinutes: 15, maxOutputTokens: 0, stealth: true, opacity: 0.82,
    hotkeyToggle: 'cmd+shift+backslash', hotkeyHelp: 'cmd+shift+return',
    hasKey: { gemini: true, anthropic: false, openai: false },
    path: '/tmp/settings.json',
  };
}

function makeBridge(calls) {
  const record = (name, value) => (...args) => {
    calls.push([name].concat(args));
    return Promise.resolve(typeof value === 'function' ? value(...args) : value);
  };
  return {
    GetState: record('GetState', () => sampleState()),
    GetSettings: record('GetSettings', () => sampleSettings()),
    SaveSettings: record('SaveSettings', () => sampleState({ opacity: 0.5 })),
    ClearAPIKey: record('ClearAPIKey'),
    ListModels: record('ListModels', ['model-a', 'model-b']),
    SpeechLocales: record('SpeechLocales', ['en-GB', 'en-US']),
    ContextFiles: record('ContextFiles', [{ path: '/a/cv.pdf', name: 'cv.pdf', chars: 12345, truncated: false }]),
    AddContextFiles: record('AddContextFiles', [{ path: '/a/n.md', name: 'n.md', chars: 10, truncated: false, error: 'file not found' }]),
    RemoveContextFile: record('RemoveContextFile', []),
    Help: record('Help'),
    Ask: record('Ask'),
    CancelAnswer: record('CancelAnswer'),
    StartListening: record('StartListening', () => sampleState({ listening: true, mic: true, system: true })),
    StopListening: record('StopListening', () => sampleState()),
    Transcript: record('Transcript', [{ id: 1, speaker: 'others', text: 'earlier line', final: true }]),
    ClearTranscript: record('ClearTranscript'),
    OpenPrivacySettings: record('OpenPrivacySettings'),
    StealthCheck: record('StealthCheck', { verdict: 'hidden', message: 'Hidden: ok' }),
    Hide: record('Hide'),
    Quit: record('Quit'),
  };
}

const tick = () => new Promise((resolve) => setTimeout(resolve, 5));

async function appTests() {
  await test('app.js boots, wires every element and renders state', async () => {
    const calls = [];
    const app = loadApp(makeBridge(calls));
    await tick();
    assert.ok(calls.some((c) => c[0] === 'GetState'));
    assert.strictEqual(app.el('listen-label').textContent, 'Not listening');
    assert.strictEqual(app.el('model-chip').textContent, 'gemini-3.8-flash');
    assert.strictEqual(app.el('help-hotkey').textContent, '⌘⇧↩');
    assert.strictEqual(app.el('notice').hidden, true);
    // the transcript fetched at startup is rendered
    assert.strictEqual(app.el('transcript').children.length, 1);
    for (const name of ['state', 'answer:start', 'answer:delta', 'answer:end', 'transcript', 'transcript:cleared']) {
      assert.strictEqual(typeof app.events[name], 'function', 'no handler for event ' + name);
    }
  });

  await test('state problems are shown in the notice bar', async () => {
    const app = loadApp(makeBridge([]));
    await tick();
    app.events['state'](sampleState({ ready: false, readyHint: 'Add an API key in Settings', hotkeyErrors: ['Help hotkey: taken'] }));
    assert.strictEqual(app.el('notice').hidden, false);
    assert.ok(app.el('notice').textContent.includes('Add an API key in Settings'));
    assert.ok(app.el('notice').textContent.includes('Help hotkey: taken'));
  });

  await test('Help button and typed question call Go', async () => {
    const calls = [];
    const app = loadApp(makeBridge(calls));
    await tick();
    assert.strictEqual(app.el('help-button').dispatch('click'), 1);
    app.el('ask-input').value = '  what is our SLA?  ';
    app.el('ask-form').dispatch('submit');
    assert.ok(calls.some((c) => c[0] === 'Help'));
    assert.deepStrictEqual(calls.find((c) => c[0] === 'Ask'), ['Ask', 'what is our SLA?']);
    assert.strictEqual(app.el('ask-input').value, '');
  });

  await test('answer stream renders, ignores stale ids, shows errors', async () => {
    const app = loadApp(makeBridge([]));
    await tick();
    app.events['answer:start']({ id: 7, question: '' });
    assert.strictEqual(app.el('answer-stop').hidden, false);
    app.events['answer:delta']({ id: 7, text: 'Say **yes**' });
    app.events['answer:delta']({ id: 6, text: ' STALE' });
    assert.strictEqual(app.el('answer').innerHTML, '<p>Say <strong>yes</strong></p>');
    app.events['answer:end']({ id: 7, firstMs: 800, totalMs: 1500, notice: 'Answered without the screen: denied' });
    assert.strictEqual(app.el('answer-stop').hidden, true);
    assert.ok(app.el('answer-title').textContent.includes('0.8s'));
    assert.strictEqual(app.el('answer-notice').hidden, false);

    app.events['answer:start']({ id: 8, question: 'why?' });
    app.events['answer:end']({ id: 8, error: 'Gemini API error (HTTP 401): bad key', firstMs: 0, totalMs: 10 });
    const last = app.el('answer').lastChild;
    assert.strictEqual(last.className, 'error');
    assert.ok(last.textContent.includes('401'));
  });

  await test('transcript lines are added, updated in place and cleared', async () => {
    const app = loadApp(makeBridge([]));
    await tick();
    const box = app.el('transcript');
    const before = box.children.length;
    app.events['transcript']({ id: 42, speaker: 'me', text: 'hel', final: false });
    app.events['transcript']({ id: 42, speaker: 'me', text: 'hello there', final: true });
    assert.strictEqual(box.children.length, before + 1);
    const line = box.lastChild;
    assert.strictEqual(line.className, 'line me');
    assert.strictEqual(line.lastChild.textContent, 'hello there');
    app.events['transcript:cleared']();
    assert.strictEqual(box.children.length, 1); // the "nothing heard" placeholder
    assert.strictEqual(app.el('transcript-count').textContent, '');
  });

  await test('listen toggle starts and stops', async () => {
    const calls = [];
    const app = loadApp(makeBridge(calls));
    await tick();
    app.el('listen-toggle').dispatch('click');
    await tick();
    assert.ok(calls.some((c) => c[0] === 'StartListening'));
    assert.strictEqual(app.el('listen-label').textContent, 'Listening · meeting + mic');
    assert.strictEqual(app.el('listen-toggle').disabled, false);
    app.el('listen-toggle').dispatch('click');
    await tick();
    assert.ok(calls.some((c) => c[0] === 'StopListening'));
  });

  await test('a failed start shows the reason', async () => {
    const calls = [];
    const bridge = makeBridge(calls);
    bridge.StartListening = () => Promise.reject('Speech Recognition permission is required.');
    const app = loadApp(bridge);
    await tick();
    app.el('listen-toggle').dispatch('click');
    await tick();
    await tick();
    assert.ok(app.el('notice').textContent.includes('Speech Recognition permission'));
    assert.strictEqual(app.el('listen-toggle').disabled, false);
  });

  await test('settings: open, switch provider, load models, save', async () => {
    const calls = [];
    const app = loadApp(makeBridge(calls));
    await tick();
    app.el('open-settings').dispatch('click');
    await tick();
    assert.strictEqual(app.el('settings-view').hidden, false);
    assert.strictEqual(app.el('main-view').hidden, true);
    assert.strictEqual(app.el('set-model').value, 'gemini-3.8-flash');
    assert.strictEqual(app.el('set-api-key').value, '');
    assert.ok(app.el('set-api-key').placeholder.startsWith('Saved'));
    assert.strictEqual(app.el('set-clear-key').hidden, false);
    assert.strictEqual(app.el('set-files').children.length, 1);
    assert.strictEqual(app.el('set-permissions').children.length, 3);
    assert.strictEqual(app.el('set-language').children.length, 2);

    // type a Gemini key, then switch to Anthropic and fill that in
    app.el('set-api-key').value = 'g-key';
    app.el('set-provider').value = 'anthropic';
    app.el('set-provider').dispatch('change');
    assert.strictEqual(app.el('set-api-key').value, '');
    assert.strictEqual(app.el('set-reasoning-row').hidden, true);
    assert.strictEqual(app.el('set-clear-key').hidden, true);
    app.el('set-api-key').value = 'a-key';

    app.el('set-load-models').dispatch('click');
    await tick();
    assert.deepStrictEqual(calls.find((c) => c[0] === 'ListModels'), ['ListModels', 'anthropic', 'a-key', '']);
    assert.strictEqual(app.el('set-model-pick-row').hidden, false);
    assert.strictEqual(app.el('set-model-pick').children.length, 3);
    app.el('set-model-pick').value = 'model-b';
    app.el('set-model-pick').dispatch('change');
    assert.strictEqual(app.el('set-model').value, 'model-b');

    app.el('set-answer-prompt').value = 'Short answers.';
    app.el('set-transcript-minutes').value = '20';
    app.el('set-max-tokens').value = '';
    app.el('settings-form').dispatch('submit');
    await tick();
    // Round-trip through JSON: the payload was built in another VM realm, and
    // this is also exactly what crosses the bridge to Go.
    const saved = JSON.parse(JSON.stringify(calls.find((c) => c[0] === 'SaveSettings')[1]));
    assert.strictEqual(saved.provider, 'anthropic');
    assert.deepStrictEqual(saved.providers.anthropic, { apiKey: 'a-key', model: 'model-b', baseUrl: '', reasoning: '' });
    assert.deepStrictEqual(saved.providers.gemini, { apiKey: 'g-key', model: 'gemini-3.8-flash', baseUrl: '', reasoning: 'low' });
    assert.strictEqual(saved.answerPrompt, 'Short answers.');
    assert.strictEqual(saved.transcriptMinutes, 20);
    assert.strictEqual(saved.maxOutputTokens, 0);
    assert.strictEqual(saved.stealth, true);
    assert.strictEqual(saved.language, 'en-US');
    // saved -> back to the main view
    assert.strictEqual(app.el('settings-view').hidden, true);
    assert.strictEqual(app.el('main-view').hidden, false);
  });

  await test('settings: a save error keeps the form open and shows it', async () => {
    const bridge = makeBridge([]);
    bridge.SaveSettings = () => Promise.reject('invalid hotkey "x": needs cmd, ctrl or option');
    const app = loadApp(bridge);
    await tick();
    app.el('open-settings').dispatch('click');
    await tick();
    app.el('settings-form').dispatch('submit');
    await tick();
    assert.strictEqual(app.el('settings-view').hidden, false);
    assert.strictEqual(app.el('set-error').hidden, false);
    assert.ok(app.el('set-error').textContent.includes('invalid hotkey'));
    assert.strictEqual(app.el('set-save').disabled, false);
  });

  await test('settings: add files, remove key, stealth test, permissions link', async () => {
    const calls = [];
    const app = loadApp(makeBridge(calls));
    await tick();
    app.el('open-settings').dispatch('click');
    await tick();

    app.el('set-add-files').dispatch('click');
    await tick();
    const row = app.el('set-files').children[0];
    assert.ok(row.children[1].textContent.startsWith('Not sent: file not found'));

    app.el('set-clear-key').dispatch('click');
    await tick();
    assert.deepStrictEqual(calls.find((c) => c[0] === 'ClearAPIKey'), ['ClearAPIKey', 'gemini']);
    assert.strictEqual(app.el('set-clear-key').hidden, true);

    app.el('set-permissions').children[0].children[2].dispatch('click');
    assert.deepStrictEqual(calls.find((c) => c[0] === 'OpenPrivacySettings'), ['OpenPrivacySettings', 'screen']);

    app.el('set-stealth-test').dispatch('click');
    await new Promise((resolve) => setTimeout(resolve, 450));
    assert.ok(calls.some((c) => c[0] === 'StealthCheck'));
    assert.strictEqual(app.el('set-stealth-result').textContent, 'Hidden: ok');
    assert.strictEqual(app.el('set-stealth-result').className, 'hint ok');
    assert.strictEqual(app.el('set-stealth-test').disabled, false);
  });

  await test('header buttons: hide, quit, stop, clear transcript', async () => {
    const calls = [];
    const app = loadApp(makeBridge(calls));
    await tick();
    for (const [id, method] of [['hide-window', 'Hide'], ['quit-app', 'Quit'], ['answer-stop', 'CancelAnswer'], ['transcript-clear', 'ClearTranscript']]) {
      assert.strictEqual(app.el(id).dispatch('click'), 1, id + ' has no click handler');
      assert.ok(calls.some((c) => c[0] === method), method + ' was not called');
    }
    assert.strictEqual(app.el('transcript-toggle').dispatch('click'), 1);
    assert.strictEqual(app.el('transcript').hidden, true);
  });
}

(async () => {
  for (const [name, fn] of mdTests) {
    await test('md: ' + name, fn);
  }
  await appTests();
  console.log(failures ? '\n' + failures + ' test(s) failed' : '\nall frontend tests passed');
  process.exit(failures ? 1 : 0);
})();
