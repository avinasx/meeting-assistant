// app.js — UI wiring for the overlay. Talks to Go through the Wails bridge:
// window.go.main.App.<Method>() for calls, window.runtime.EventsOn for events.
(function () {
  'use strict';

  function $(id) {
    var el = document.getElementById(id);
    if (!el) {
      throw new Error('missing element #' + id);
    }
    return el;
  }

  function api() {
    return window.go.main.App;
  }

  function errorText(err) {
    if (err == null) {
      return 'Unknown error';
    }
    if (typeof err === 'string') {
      return err;
    }
    return err.message || String(err);
  }

  function sleep(ms) {
    return new Promise(function (resolve) { setTimeout(resolve, ms); });
  }

  // "cmd+shift+return" -> "⌘⇧↩"
  var KEY_SYMBOLS = {
    cmd: '⌘', command: '⌘', shift: '⇧', opt: '⌥', option: '⌥', alt: '⌥', ctrl: '⌃', control: '⌃',
    'return': '↩', enter: '↩', space: 'Space', tab: '⇥', backslash: '\\', slash: '/', comma: ',',
    period: '.', semicolon: ';', quote: "'", minus: '-', equal: '=', backtick: '`',
    left: '←', right: '→', up: '↑', down: '↓'
  };
  function formatHotkey(spec) {
    if (!spec) {
      return '';
    }
    return String(spec).toLowerCase().split('+').map(function (part) {
      part = part.trim();
      return KEY_SYMBOLS[part] || part.toUpperCase();
    }).join('');
  }

  var PROVIDER_HINTS = {
    gemini: { key: 'Gemini API key', base: 'https://generativelanguage.googleapis.com/v1beta' },
    anthropic: { key: 'Anthropic API key', base: 'https://api.anthropic.com/v1' },
    openai: { key: 'API key (optional for local servers)', base: 'https://api.openai.com/v1' }
  };

  var state = null;          // last State from Go
  var settings = null;       // SettingsView while the settings form is open
  var draftProviders = {};   // per-provider form values while the form is open
  var formProvider = 'gemini';
  var answer = { id: 0, text: '', active: false };
  var renderQueued = false;
  var transcriptEls = {};    // line id -> element
  var transcriptOrder = [];  // line ids, oldest first
  var transcriptOpen = true;
  var MAX_TRANSCRIPT_LINES = 300;

  // ---------------------------------------------------------------- state

  function applyState(next) {
    if (!next) {
      return;
    }
    state = next;
    document.documentElement.style.setProperty('--bg-alpha', String(next.opacity));

    var dot = $('listen-dot');
    dot.className = 'dot' + (next.listening ? ' on' : '');
    var label = 'Not listening';
    if (next.listening) {
      var sources = [];
      if (next.system) { sources.push('meeting'); }
      if (next.mic) { sources.push('mic'); }
      label = 'Listening · ' + sources.join(' + ');
    }
    $('listen-label').textContent = label;
    $('listen-toggle').title = next.listening
      ? 'Stop listening' + (next.onDevice ? ' (speech is recognized on this Mac)' : ' (speech is sent to Apple for recognition)')
      : 'Start listening';

    $('model-chip').textContent = next.model || '';
    $('help-hotkey').textContent = formatHotkey(next.hotkeyHelp);
    $('hide-window').title = 'Hide (' + formatHotkey(next.hotkeyToggle) + ' brings it back)';

    var notes = [];
    if (!next.supported) {
      notes.push('This build has no screen or audio access: macOS 14 or newer is required.');
    }
    if (!next.ready && next.readyHint) {
      notes.push(next.readyHint + '.');
    }
    if (next.listenNote) {
      notes.push(next.listenNote);
    }
    (next.hotkeyErrors || []).forEach(function (msg) { notes.push(msg); });
    showNotice(notes.join('\n'));

    if (!$('settings-view').hidden) {
      renderPermissions();
    }
  }

  function showNotice(text) {
    var el = $('notice');
    el.textContent = text || '';
    el.hidden = !text;
  }

  function refreshState() {
    return api().GetState().then(applyState);
  }

  // ------------------------------------------------------------ listening

  function toggleListening() {
    var button = $('listen-toggle');
    if (button.disabled) {
      return;
    }
    button.disabled = true;
    var call;
    if (state && state.listening) {
      call = api().StopListening();
    } else {
      $('listen-dot').className = 'dot busy';
      $('listen-label').textContent = 'Starting…';
      call = api().StartListening();
    }
    call.then(applyState).catch(function (err) {
      return refreshState().then(function () { showNotice(errorText(err)); });
    }).then(function () {
      button.disabled = false;
    });
  }

  // ------------------------------------------------------------ answering

  function renderAnswer() {
    renderQueued = false;
    var el = $('answer');
    var nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    el.innerHTML = window.renderMarkdown(answer.text);
    if (answer.active && nearBottom) {
      el.scrollTop = el.scrollHeight;
    }
  }

  function queueRender() {
    if (renderQueued) {
      return;
    }
    renderQueued = true;
    window.requestAnimationFrame(renderAnswer);
  }

  function appendToAnswer(className, text) {
    var p = document.createElement('p');
    p.className = className;
    p.textContent = text;
    $('answer').appendChild(p);
  }

  function onAnswerStart(d) {
    answer = { id: d.id, text: '', active: true };
    $('answer-title').textContent = d.question ? 'Q: ' + d.question : 'Help';
    $('answer').innerHTML = '';
    appendToAnswer('thinking', 'Looking at your screen and the conversation…');
    $('answer-stop').hidden = false;
    $('answer-notice').hidden = true;
  }

  function onAnswerDelta(d) {
    if (d.id !== answer.id) {
      return; // a request that has since been replaced
    }
    answer.text += d.text;
    queueRender();
  }

  function onAnswerEnd(d) {
    if (d.id !== answer.id) {
      return;
    }
    answer.active = false;
    $('answer-stop').hidden = true;
    renderAnswer();
    if (d.error) {
      appendToAnswer('error', d.error);
    } else if (d.cancelled) {
      appendToAnswer('thinking', '(stopped)');
    }
    var notice = $('answer-notice');
    notice.textContent = d.notice || '';
    notice.hidden = !d.notice;
    if (!d.error && !d.cancelled && d.firstMs > 0) {
      var title = $('answer-title');
      title.textContent = title.textContent + ' · ' + (d.firstMs / 1000).toFixed(1) + 's to first word';
    }
  }

  function askHelp() {
    api().Help();
  }

  function submitQuestion(event) {
    event.preventDefault();
    var input = $('ask-input');
    var question = input.value.trim();
    input.value = '';
    if (question) {
      api().Ask(question);
    } else {
      api().Help();
    }
  }

  // ----------------------------------------------------------- transcript

  function updateTranscriptCount() {
    $('transcript-count').textContent = transcriptOrder.length ? '(' + transcriptOrder.length + ')' : '';
  }

  function clearTranscriptView() {
    transcriptEls = {};
    transcriptOrder = [];
    var box = $('transcript');
    box.innerHTML = '';
    var empty = document.createElement('div');
    empty.className = 'empty';
    empty.textContent = 'Nothing heard yet.';
    box.appendChild(empty);
    updateTranscriptCount();
  }

  function onTranscriptLine(line) {
    if (!line || !line.text) {
      return;
    }
    var box = $('transcript');
    var nearBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 30;
    var key = String(line.id);
    var el = transcriptEls[key];
    if (!el) {
      if (!transcriptOrder.length) {
        box.innerHTML = '';
      }
      el = document.createElement('div');
      var who = document.createElement('span');
      who.className = 'who';
      who.textContent = line.speaker === 'me' ? 'Me' : 'Others';
      var text = document.createElement('span');
      text.className = 'text';
      el.appendChild(who);
      el.appendChild(text);
      box.appendChild(el);
      transcriptEls[key] = el;
      transcriptOrder.push(key);
      while (transcriptOrder.length > MAX_TRANSCRIPT_LINES) {
        var oldKey = transcriptOrder.shift();
        var oldEl = transcriptEls[oldKey];
        delete transcriptEls[oldKey];
        if (oldEl && oldEl.parentNode) {
          oldEl.parentNode.removeChild(oldEl);
        }
      }
      updateTranscriptCount();
    }
    el.className = 'line ' + (line.speaker === 'me' ? 'me' : 'others') + (line.final ? '' : ' partial');
    el.lastChild.textContent = line.text;
    if (nearBottom) {
      box.scrollTop = box.scrollHeight;
    }
  }

  function toggleTranscript() {
    transcriptOpen = !transcriptOpen;
    $('transcript').hidden = !transcriptOpen;
    $('transcript-toggle').textContent = 'Transcript ' + (transcriptOpen ? '▾' : '▸');
  }

  // ------------------------------------------------------------- settings

  function storeProviderFields() {
    draftProviders[formProvider] = {
      apiKey: $('set-api-key').value.trim(),
      model: $('set-model').value.trim(),
      baseUrl: $('set-base-url').value.trim(),
      reasoning: $('set-reasoning').value
    };
  }

  function loadProviderFields() {
    var p = draftProviders[formProvider] || {};
    var hint = PROVIDER_HINTS[formProvider] || { key: 'API key', base: 'default' };
    var hasKey = !!(settings && settings.hasKey && settings.hasKey[formProvider]);

    var key = $('set-api-key');
    key.value = p.apiKey || '';
    key.placeholder = hasKey ? 'Saved — leave blank to keep' : hint.key;
    $('set-clear-key').hidden = !hasKey;

    $('set-model').value = p.model || '';
    var base = $('set-base-url');
    base.value = p.baseUrl || '';
    base.placeholder = hint.base;
    $('set-reasoning').value = p.reasoning || '';
    // The Anthropic request does not use a thinking setting.
    $('set-reasoning-row').hidden = formProvider === 'anthropic';

    $('set-model-pick-row').hidden = true;
    $('set-model-pick').innerHTML = '';
    $('set-model-status').hidden = true;
  }

  function onProviderChange() {
    storeProviderFields();
    formProvider = $('set-provider').value;
    loadProviderFields();
  }

  function setModelStatus(text, kind) {
    var el = $('set-model-status');
    el.textContent = text;
    el.className = 'hint' + (kind ? ' ' + kind : '');
    el.hidden = !text;
  }

  function loadModels() {
    storeProviderFields();
    var kind = formProvider;
    var draft = draftProviders[kind];
    setModelStatus('Loading models…', '');
    api().ListModels(kind, draft.apiKey, draft.baseUrl).then(function (models) {
      if (kind !== formProvider) {
        return; // the provider was switched while loading
      }
      models = models || [];
      var pick = $('set-model-pick');
      pick.innerHTML = '';
      var first = document.createElement('option');
      first.value = '';
      first.textContent = models.length ? 'Choose from ' + models.length + ' models…' : 'No models returned';
      pick.appendChild(first);
      models.forEach(function (id) {
        var option = document.createElement('option');
        option.value = id;
        option.textContent = id;
        pick.appendChild(option);
      });
      var current = $('set-model').value.trim();
      pick.value = models.indexOf(current) >= 0 ? current : '';
      $('set-model-pick-row').hidden = false;
      setModelStatus('', '');
    }).catch(function (err) {
      if (kind === formProvider) {
        setModelStatus(errorText(err), 'warn');
      }
    });
  }

  function onModelPicked() {
    var value = $('set-model-pick').value;
    if (value) {
      $('set-model').value = value;
    }
  }

  function clearKey() {
    storeProviderFields();
    var kind = formProvider;
    api().ClearAPIKey(kind).then(function () {
      if (settings && settings.hasKey) {
        settings.hasKey[kind] = false;
      }
      if (draftProviders[kind]) {
        draftProviders[kind].apiKey = '';
      }
      if (kind === formProvider) {
        loadProviderFields();
      }
      return refreshState();
    }).catch(showSettingsError);
  }

  function formatChars(n) {
    if (n >= 1000) {
      return (n / 1000).toFixed(n >= 10000 ? 0 : 1) + 'k chars';
    }
    return n + ' chars';
  }

  function renderFiles(files) {
    var list = $('set-files');
    list.innerHTML = '';
    (files || []).forEach(function (file) {
      var li = document.createElement('li');
      var name = document.createElement('span');
      name.className = 'name';
      name.textContent = file.name;
      name.title = file.path;
      var detail = document.createElement('span');
      detail.className = 'detail' + (file.error ? ' warn' : '');
      detail.textContent = file.error
        ? 'Not sent: ' + file.error
        : formatChars(file.chars) + (file.truncated ? ' (truncated)' : '');
      detail.title = detail.textContent;
      var remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'link';
      remove.textContent = 'Remove';
      remove.addEventListener('click', function () {
        api().RemoveContextFile(file.path).then(renderFiles).catch(showSettingsError);
      });
      li.appendChild(name);
      li.appendChild(detail);
      li.appendChild(remove);
      list.appendChild(li);
    });
    if (!files || !files.length) {
      var empty = document.createElement('li');
      empty.className = 'hint';
      empty.textContent = 'No files added.';
      list.appendChild(empty);
    }
  }

  function addFiles() {
    api().AddContextFiles().then(renderFiles).catch(showSettingsError);
  }

  var PERMISSION_ROWS = [
    { key: 'screen', pane: 'screen', label: 'Screen & system audio recording' },
    { key: 'microphone', pane: 'microphone', label: 'Microphone' },
    { key: 'speech', pane: 'speech', label: 'Speech recognition' }
  ];

  function renderPermissions() {
    var list = $('set-permissions');
    list.innerHTML = '';
    var perms = (state && state.permissions) || {};
    PERMISSION_ROWS.forEach(function (row) {
      var status = perms[row.key] || 'unknown';
      var li = document.createElement('li');
      var name = document.createElement('span');
      name.className = 'name';
      name.textContent = row.label;
      var detail = document.createElement('span');
      detail.className = 'detail ' + (status === 'granted' ? 'ok' : 'warn');
      detail.textContent = status === 'granted' ? 'Granted' : (status === 'denied' ? 'Denied' : 'Not granted yet');
      var open = document.createElement('button');
      open.type = 'button';
      open.className = 'link';
      open.textContent = 'Open settings';
      open.addEventListener('click', function () { api().OpenPrivacySettings(row.pane); });
      li.appendChild(name);
      li.appendChild(detail);
      li.appendChild(open);
      list.appendChild(li);
    });
  }

  function fillLanguages(locales, current) {
    var select = $('set-language');
    select.innerHTML = '';
    var all = (locales || []).slice();
    if (current && all.indexOf(current) < 0) {
      all.unshift(current);
    }
    all.forEach(function (id) {
      var option = document.createElement('option');
      option.value = id;
      option.textContent = id;
      select.appendChild(option);
    });
    select.value = current || '';
  }

  function showSettingsError(err) {
    var el = $('set-error');
    var text = err ? errorText(err) : '';
    el.textContent = text;
    el.hidden = !text;
  }

  function openSettings() {
    Promise.all([api().GetSettings(), api().ContextFiles(), api().SpeechLocales(), api().GetState()])
      .then(function (results) {
        settings = results[0];
        applyState(results[3]);

        draftProviders = {};
        Object.keys(settings.providers || {}).forEach(function (kind) {
          var p = settings.providers[kind] || {};
          draftProviders[kind] = { apiKey: '', model: p.model || '', baseUrl: p.baseUrl || '', reasoning: p.reasoning || '' };
        });
        formProvider = settings.provider;
        $('set-provider').value = formProvider;
        loadProviderFields();

        $('set-max-tokens').value = settings.maxOutputTokens ? String(settings.maxOutputTokens) : '';
        $('set-answer-prompt').value = settings.answerPrompt || '';
        renderFiles(results[1]);

        $('set-capture-system').checked = !!settings.captureSystem;
        $('set-capture-mic').checked = !!settings.captureMic;
        $('set-auto-listen').checked = !!settings.autoListen;
        fillLanguages(results[2], settings.language);
        $('set-transcript-minutes').value = String(settings.transcriptMinutes);
        $('set-include-screenshot').checked = !!settings.includeScreenshot;

        $('set-stealth').checked = !!settings.stealth;
        $('set-opacity').value = String(settings.opacity);
        $('set-hotkey-toggle').value = settings.hotkeyToggle || '';
        $('set-hotkey-help').value = settings.hotkeyHelp || '';
        $('set-stealth-result').hidden = true;

        $('set-path').textContent = 'Settings file (holds your API keys): ' + settings.path;
        showSettingsError(null);
        renderPermissions();

        $('main-view').hidden = true;
        $('settings-view').hidden = false;
        $('settings-view').scrollTop = 0;
      })
      .catch(function (err) { showNotice(errorText(err)); });
  }

  function closeSettings() {
    $('settings-view').hidden = true;
    $('main-view').hidden = false;
    settings = null;
    if (state) {
      // Drop any unsaved opacity preview.
      document.documentElement.style.setProperty('--bg-alpha', String(state.opacity));
    }
  }

  function intValue(id) {
    var n = parseInt($(id).value, 10);
    return isNaN(n) || n < 0 ? 0 : n;
  }

  function saveSettings(event) {
    event.preventDefault();
    storeProviderFields();
    var opacity = parseFloat($('set-opacity').value);
    var payload = {
      provider: formProvider,
      providers: draftProviders,
      answerPrompt: $('set-answer-prompt').value,
      contextFiles: [],
      language: $('set-language').value,
      captureMic: $('set-capture-mic').checked,
      captureSystem: $('set-capture-system').checked,
      autoListen: $('set-auto-listen').checked,
      includeScreenshot: $('set-include-screenshot').checked,
      transcriptMinutes: intValue('set-transcript-minutes'),
      maxOutputTokens: intValue('set-max-tokens'),
      stealth: $('set-stealth').checked,
      opacity: isNaN(opacity) ? 0.82 : opacity,
      hotkeyToggle: $('set-hotkey-toggle').value.trim(),
      hotkeyHelp: $('set-hotkey-help').value.trim()
    };
    var save = $('set-save');
    save.disabled = true;
    api().SaveSettings(payload).then(function (next) {
      applyState(next);
      closeSettings();
    }).catch(showSettingsError).then(function () {
      save.disabled = false;
    });
  }

  function testStealth() {
    var button = $('set-stealth-test');
    var result = $('set-stealth-result');
    button.disabled = true;
    result.className = 'hint';
    result.textContent = 'Testing…';
    result.hidden = false;
    document.body.classList.add('stealth-test');
    // Give the page time to paint the test colour before it is captured.
    sleep(400).then(function () {
      return api().StealthCheck();
    }).then(function (res) {
      result.className = 'hint ' + (res.verdict === 'hidden' ? 'ok' : 'warn');
      result.textContent = res.message;
    }).catch(function (err) {
      result.className = 'hint warn';
      result.textContent = 'Test failed: ' + errorText(err);
    }).then(function () {
      document.body.classList.remove('stealth-test');
      button.disabled = false;
    });
  }

  // ----------------------------------------------------------------- init

  function wire() {
    $('listen-toggle').addEventListener('click', toggleListening);
    $('open-settings').addEventListener('click', function () {
      if ($('settings-view').hidden) {
        openSettings();
      } else {
        closeSettings();
      }
    });
    $('hide-window').addEventListener('click', function () {
      api().Hide().catch(function (err) { showNotice(errorText(err)); });
    });
    $('quit-app').addEventListener('click', function () { api().Quit(); });

    $('help-button').addEventListener('click', askHelp);
    $('ask-form').addEventListener('submit', submitQuestion);
    $('answer-stop').addEventListener('click', function () { api().CancelAnswer(); });

    $('transcript-toggle').addEventListener('click', toggleTranscript);
    $('transcript-clear').addEventListener('click', function () { api().ClearTranscript(); });

    $('set-provider').addEventListener('change', onProviderChange);
    $('set-load-models').addEventListener('click', loadModels);
    $('set-model-pick').addEventListener('change', onModelPicked);
    $('set-clear-key').addEventListener('click', clearKey);
    $('set-add-files').addEventListener('click', addFiles);
    $('set-stealth-test').addEventListener('click', testStealth);
    $('set-opacity').addEventListener('input', function () {
      document.documentElement.style.setProperty('--bg-alpha', $('set-opacity').value);
    });
    $('set-cancel').addEventListener('click', closeSettings);
    $('settings-form').addEventListener('submit', saveSettings);

    document.addEventListener('keydown', function (event) {
      if (event.key === 'Escape' && !$('settings-view').hidden) {
        closeSettings();
      }
    });

    var on = window.runtime.EventsOn;
    on('state', applyState);
    on('answer:start', onAnswerStart);
    on('answer:delta', onAnswerDelta);
    on('answer:end', onAnswerEnd);
    on('transcript', onTranscriptLine);
    on('transcript:cleared', clearTranscriptView);
  }

  function start() {
    wire();
    clearTranscriptView();
    refreshState().catch(function (err) { showNotice(errorText(err)); });
    api().Transcript().then(function (lines) {
      (lines || []).forEach(onTranscriptLine);
    }).catch(function () { /* the transcript simply starts empty */ });
  }

  // The Wails bridge is injected by the host; wait for it before starting.
  function boot(attempt) {
    if (window.go && window.go.main && window.go.main.App && window.runtime) {
      start();
      return;
    }
    if (attempt > 100) {
      document.getElementById('notice').hidden = false;
      document.getElementById('notice').textContent =
        'The app bridge did not load. Build with `make build` and open MeetingAssistant.app.';
      return;
    }
    setTimeout(function () { boot(attempt + 1); }, 50);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { boot(0); });
  } else {
    boot(0);
  }
})();
