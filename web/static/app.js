(() => {
  'use strict';

  const byId = (id) => document.getElementById(id);
  const form = byId('search');
  const modes = new Map([
    ['search', 'General search'], ['tvsearch', 'TV'], ['movie', 'Movies'],
    ['music', 'Music'], ['book', 'Books'],
  ]);
  const labels = new Map([
    ['season', 'Season'], ['ep', 'Episode'], ['imdbid', 'IMDb ID'],
    ['tmdbid', 'TMDb ID'], ['tvdbid', 'TVDB ID'], ['rid', 'TVRage ID'],
    ['tvrageid', 'TVRage ID'], ['tvmazeid', 'TVmaze ID'], ['traktid', 'Trakt ID'],
    ['doubanid', 'Douban ID'], ['artist', 'Artist'], ['album', 'Album'],
    ['label', 'Record label'], ['track', 'Track'], ['year', 'Year'],
    ['genre', 'Genre'], ['author', 'Author'], ['title', 'Title'],
    ['publisher', 'Publisher'], ['isbn', 'ISBN'], ['maxage', 'Maximum age (days)'],
  ]);
  const reserved = new Set(['q', 't', 'indexer', 'cat', 'limit', 'offset', 'apikey']);
  const sortLabels = new Map([
    ['title', 'Title'], ['indexer', 'Tracker'], ['size', 'Size'],
    ['seeders', 'Seeds'], ['peers', 'Peers'], ['published', 'Age'],
  ]);
  for (const [key, label] of sortLabels) {
    const option = document.createElement('option');
    option.value = key;
    option.textContent = label;
    byId('mobile-sort').append(option);
  }
  const compactResults = window.matchMedia('(width < 75rem)');
  const state = {
    indexers: [], endpoints: [], results: [], errors: [], fields: new Map(),
    fieldValues: new Map(), downloads: new Map(), destinations: new Map(), controller: null, generation: 0,
    lastSearch: null, sort: 'seeders', direction: 'desc', searched: false,
  };

  function element(tag, text, className) {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = String(text);
    if (className) node.className = className;
    return node;
  }

  function message(id, text, className) {
    const node = byId(id);
    node.textContent = text;
    node.hidden = !text;
    if (className !== undefined) node.className = className;
    else node.classList.remove('status-error', 'status-success', 'status-warning');
  }

  async function api(path, options = {}) {
    const response = await fetch(path, { credentials: 'same-origin', mode: 'same-origin', ...options });
    let data;
    try {
      data = await response.json();
    } catch (error) {
      if (error.name === 'AbortError') throw error;
      if (response.ok) throw new Error('The server returned an unreadable response.');
    }
    if (response.status === 401) throw new Error('Authentication failed. Reload this page and sign in with the configured web credentials.');
    if (response.status === 403) throw new Error(data?.error || 'Access denied. Open this app directly on its configured origin and try again.');
    if (!response.ok) throw new Error(data?.error || `Request failed (HTTP ${response.status}).`);
    return data;
  }

  function selectedIndexers() {
    const ids = new Set(Array.from(byId('indexer-list').querySelectorAll('input:checked'), (input) => input.value));
    return state.indexers.filter((indexer) => ids.has(indexer.id));
  }

  function supported(indexer, mode) {
    return Object.prototype.hasOwnProperty.call(indexer.modes || {}, mode);
  }

  function parameters(indexer, mode) {
    return new Set((indexer.modes?.[mode] || []).map((name) => String(name).toLowerCase()));
  }

  function updateCapabilities(preferredMode, preferredCategories) {
    for (const [name, input] of state.fields) state.fieldValues.set(name, input.value);
    const selected = selectedIndexers();
    const modeSelect = byId('mode');
    const previousMode = preferredMode || modeSelect.value || 'search';
    const available = new Set(selected.flatMap((indexer) => Object.keys(indexer.modes || {})));
    available.add(previousMode);
    modeSelect.replaceChildren();
    for (const mode of available) {
      const option = element('option', modes.get(mode) || mode);
      option.value = mode;
      option.disabled = !selected.length || !selected.every((indexer) => supported(indexer, mode));
      if (option.disabled) option.textContent += ' — not supported by all selected indexers';
      modeSelect.append(option);
    }
    modeSelect.value = previousMode;
    const mode = modeSelect.value;
    const missing = selected.filter((indexer) => !supported(indexer, mode));
    const validMode = selected.length > 0 && missing.length === 0;
    const sets = selected.map((indexer) => parameters(indexer, mode));
    const allSupport = (name) => validMode && sets.every((set) => set.has(name));
    byId('query').disabled = !allSupport('q');
    byId('search-button').disabled = !validMode;
    let note;
    if (!selected.length) note = 'Select at least one indexer before searching.';
    else if (missing.length) note = `This mode is not supported by ${missing.map((indexer) => indexer.name).join(', ')}. Choose another mode or change the indexer selection. No request will be sent.`;
    else if (!allSupport('q')) note = 'Query text is not supported by every selected indexer in this mode and will not be sent. Use the enabled advanced fields, or search for latest releases.';
    else note = '';
    message('capability-note', note, validMode && allSupport('q') ? 'mt-2 small' : 'mt-2 small status-warning');

    state.fields.clear();
    byId('advanced-fields').replaceChildren();
    const names = new Set(sets.flatMap((set) => Array.from(set)));
    for (const name of Array.from(names).sort()) {
      if (reserved.has(name) || !/^[a-z][a-z0-9_-]{0,63}$/.test(name)) continue;
      const wrapper = element('div', undefined, 'col-12 md:col-4');
      const label = element('label', labels.get(name) || name, 'form-label');
      label.htmlFor = `field-${name}`;
      const input = element('input', undefined, 'form-control');
      input.id = `field-${name}`;
      input.name = name;
      input.type = 'text';
      input.value = state.fieldValues.get(name) || '';
      input.disabled = !allSupport(name);
      input.autocomplete = 'off';
      wrapper.append(label, input);
      if (input.disabled) {
        const help = element('p', 'Not supported by every selected indexer; not sent.', 'form-text');
        help.id = `help-${name}`;
        input.setAttribute('aria-describedby', help.id);
        wrapper.append(help);
      }
      state.fields.set(name, input);
      byId('advanced-fields').append(wrapper);
    }
    if (!state.fields.size) byId('advanced-fields').append(element('p', 'No additional fields advertised for this selection.', 'form-text'));

    const categoryList = byId('categories');
    const chosen = new Set(preferredCategories || Array.from(categoryList.querySelectorAll('input:checked'), (input) => input.value));
    const categories = new Map();
    for (const indexer of selected) {
      for (const category of indexer.categories || []) {
        if (!categories.has(String(category.id))) categories.set(String(category.id), category.name);
      }
    }
    categoryList.replaceChildren();
    for (const [id, name] of Array.from(categories).sort((a, b) => a[0].localeCompare(b[0], undefined, { numeric: true }))) {
      const wrapper = element('div', undefined, 'selector-choice');
      const input = element('input', undefined, 'check');
      input.type = 'checkbox';
      input.name = 'cat';
      input.id = `category-${id}`;
      input.value = id;
      input.checked = chosen.has(id);
      const label = element('label', `${id} — ${name}`);
      label.htmlFor = input.id;
      wrapper.append(input, label);
      categoryList.append(wrapper);
    }
    if (!categories.size) categoryList.append(element('p', 'No categories available for the selected indexers.', 'small'));
    byId('clear-categories').disabled = !categories.size;
    renderCapabilities(selected);
  }

  function renderCapabilities(selected) {
    const container = byId('capabilities');
    container.replaceChildren();
    if (!selected.length) container.append(element('p', 'No indexers selected.'));
    for (const indexer of selected) {
      const entry = element('section', undefined, 'capability-entry');
      entry.append(element('h3', indexer.name, 'h6'));
      const description = [indexer.description, indexer.type, indexer.language].filter(Boolean).join(' · ');
      if (description) entry.append(element('p', description, 'small'));
      for (const [mode, fields] of Object.entries(indexer.modes || {})) {
        entry.append(element('p', `${modes.get(mode) || mode}: ${fields?.length ? fields.join(', ') : 'no optional parameters advertised'}`, 'small'));
      }
      if (!Object.keys(indexer.modes || {}).length) entry.append(element('p', 'No supported search modes advertised.', 'small'));
      const categories = (indexer.categories || []).map((category) => `${category.id} ${category.name}`);
      entry.append(element('p', `Categories: ${categories.join(', ') || 'none advertised'}`, 'small'));
      container.append(entry);
    }
  }

  function requestParameters() {
    const selected = selectedIndexers();
    const mode = byId('mode').value;
    if (!selected.length) throw new Error('Select at least one indexer.');
    if (!selected.every((indexer) => supported(indexer, mode))) throw new Error('The selected search mode is not supported by every selected indexer.');
    const params = new URLSearchParams();
    for (const indexer of selected) params.append('indexer', indexer.id);
    params.set('t', mode);
    if (!byId('query').disabled && byId('query').value.trim()) params.set('q', byId('query').value.trim());
    for (const [name, input] of state.fields) {
      if (!input.disabled && input.value.trim()) params.set(name, input.value.trim());
    }
    for (const input of byId('categories').querySelectorAll('input:checked')) params.append('cat', input.value);
    params.set('limit', byId('limit').value);
    params.set('offset', byId('offset').value);
    return params;
  }

  function persistURL(params, push) {
    const query = new URLSearchParams(params);
    if (byId('result-filter').value) query.set('filter', byId('result-filter').value);
    if (Number(byId('min-seeds').value) > 0) query.set('min_seeds', byId('min-seeds').value);
    query.set('sort', state.sort);
    query.set('direction', state.direction);
    const url = new URL(window.location.href);
    url.search = query.toString();
    if (url.href !== window.location.href) window.history[push ? 'pushState' : 'replaceState'](null, '', url);
  }

  function restoreURL() {
    const params = new URLSearchParams(window.location.search);
    const ids = new Set(params.getAll('indexer'));
    for (const input of byId('indexer-list').querySelectorAll('input')) input.checked = !input.disabled && (!ids.size || ids.has(input.value));
    byId('query').value = params.get('q') || '';
    byId('limit').value = /^\d+$/.test(params.get('limit') || '') ? params.get('limit') : '100';
    byId('offset').value = /^\d+$/.test(params.get('offset') || '') ? params.get('offset') : '0';
    byId('result-filter').value = params.get('filter') || '';
    byId('min-seeds').value = /^\d+$/.test(params.get('min_seeds') || '') ? params.get('min_seeds') : '0';
    state.sort = sortLabels.has(params.get('sort')) ? params.get('sort') : 'seeders';
    state.direction = params.get('direction') === 'asc' ? 'asc' : 'desc';
    state.fieldValues.clear();
    for (const input of state.fields.values()) input.value = '';
    for (const [name, value] of params) if (!reserved.has(name)) state.fieldValues.set(name, value);
    // Rebuild after clearing the old controls so they cannot overwrite restored values.
    state.fields.clear();
    updateCapabilities(params.get('t') || 'search', params.getAll('cat'));
    byId('advanced').open = byId('mode').value !== 'search' || Array.from(state.fields.values()).some((input) => input.value);
    return params.has('t') || params.has('q') || params.has('indexer');
  }

  function setSearching(busy) {
    byId('results-section').setAttribute('aria-busy', String(busy));
    byId('search-button').textContent = busy ? 'Search again' : 'Search';
    byId('cancel-search').hidden = !busy;
    byId('previous-page').disabled = busy || !state.lastSearch || Number(state.lastSearch.get('offset')) <= 0;
    byId('next-page').disabled = busy || !state.lastSearch || (!state.results.length && !state.errors.length);
  }

  function cancelSearch() {
    state.generation += 1;
    state.controller?.abort();
    state.controller = null;
    setSearching(false);
  }

  async function search(push = true, suppliedParams = null) {
    cancelSearch();
    if (!form.reportValidity()) return;
    let params;
    try {
      params = suppliedParams || requestParameters();
    } catch (error) {
      message('search-status', error.message, 'status-error');
      return;
    }
    const controller = new AbortController();
    const generation = ++state.generation;
    state.controller = controller;
    state.lastSearch = new URLSearchParams(params);
    state.results = [];
    state.errors = [];
    state.downloads.clear();
    state.destinations.clear();
    state.searched = true;
    persistURL(params, push);
    byId('offset').value = params.get('offset');
    byId('limit').value = params.get('limit');
    message('search-status', 'Searching indexers…');
    byId('search-errors').hidden = true;
    renderResults('Searching…');
    setSearching(true);
    try {
      const data = await api(`/api/search?${params}`, { signal: controller.signal });
      if (generation !== state.generation) return;
      state.results = Array.isArray(data.results) ? data.results : [];
      state.errors = Array.isArray(data.errors) ? data.errors : [];
      const errorBox = byId('search-errors');
      errorBox.replaceChildren();
      errorBox.hidden = !state.errors.length;
      if (state.errors.length) {
        errorBox.append(element('p', 'Some indexers could not complete the search. Available results are shown.'));
        const list = element('ul', undefined, 'mb-0');
        for (const error of state.errors) list.append(element('li', error));
        errorBox.append(list);
      }
      message('search-status', state.results.length ? `Search complete${state.errors.length ? ' with partial errors' : ''}.` : `No releases returned${state.errors.length ? '; see indexer errors below' : '. Try another query, mode, category, or offset'}.`);
      renderResults();
    } catch (error) {
      if (generation !== state.generation || error.name === 'AbortError') return;
      message('search-status', error.message, 'status-error');
      renderResults('Search failed. See the error above.');
    } finally {
      if (generation === state.generation) {
        state.controller = null;
        setSearching(false);
      }
    }
  }

  function sizeText(size) {
    if (!Number.isFinite(size) || size < 0) return 'Unknown';
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
    let value = size;
    let index = 0;
    while (value >= 1024 && index < units.length - 1) { value /= 1024; index += 1; }
    return `${value.toLocaleString(undefined, { maximumFractionDigits: index ? 2 : 0 })} ${units[index]}`;
  }

  function ageText(published) {
    const timestamp = Date.parse(published);
    if (!Number.isFinite(timestamp)) return 'Unknown';
    const seconds = Math.max(0, Math.floor((Date.now() - timestamp) / 1000));
    for (const [unit, divisor] of [['y', 31536000], ['d', 86400], ['h', 3600], ['m', 60]]) {
      if (seconds >= divisor) return `${Math.floor(seconds / divisor)}${unit}`;
    }
    return 'Just now';
  }

  function safeDetails(link) {
    try {
      const url = new URL(link);
      return ['https:', 'http:'].includes(url.protocol) && !url.username && !url.password ? url.href : null;
    } catch { return null; }
  }

  function resultValue(result, key) {
    if (key === 'published') return Date.parse(result.published) || 0;
    if (key === 'title' || key === 'indexer') return String(result[key] || '');
    return Number(result[key]) || 0;
  }

  function renderResults(emptyText) {
    const filter = byId('result-filter').value.trim().toLocaleLowerCase();
    const minSeeds = Math.max(0, Number(byId('min-seeds').value) || 0);
    const results = state.results.filter((result) => {
      const text = [result.title, result.indexer, ...(result.categories || [])].join(' ').toLocaleLowerCase();
      return text.includes(filter) && (Number(result.seeders) || 0) >= minSeeds;
    });
    results.sort((a, b) => {
      const left = resultValue(a, state.sort);
      const right = resultValue(b, state.sort);
      const comparison = typeof left === 'string' ? left.localeCompare(right, undefined, { numeric: true, sensitivity: 'base' }) : left - right;
      return state.direction === 'asc' ? comparison : -comparison;
    });
    for (const button of document.querySelectorAll('[data-sort]')) {
      const active = button.dataset.sort === state.sort;
      button.parentElement.setAttribute('aria-sort', active ? (state.direction === 'asc' ? 'ascending' : 'descending') : 'none');
      button.textContent = sortLabels.get(button.dataset.sort) + (active ? (state.direction === 'asc' ? ' ↑' : ' ↓') : '');
    }
    byId('mobile-sort').value = state.sort;
    const direction = byId('mobile-sort-direction');
    direction.textContent = state.direction === 'asc' ? 'Ascending ↑' : 'Descending ↓';
    direction.setAttribute('aria-label', `Sort ${state.direction === 'asc' ? 'ascending; switch to descending' : 'descending; switch to ascending'}`);
    byId('result-count').textContent = `${results.length} shown / ${state.results.length} returned on this page`;
    const body = byId('result-rows');
    const cards = byId('result-cards');
    body.replaceChildren();
    cards.replaceChildren();
    if (!results.length) {
      const text = emptyText || (state.results.length ? 'No releases match the page filters.' : state.searched ? 'No releases returned.' : 'No search yet.');
      if (compactResults.matches) {
        cards.append(element('p', text));
      } else {
        const row = element('tr');
        const cell = element('td', text);
        cell.colSpan = 8;
        row.append(cell);
        body.append(row);
      }
    }
    for (const result of results) {
      if (compactResults.matches) cards.append(resultCard(result));
      else body.append(resultRow(result));
    }
    const offset = Number(state.lastSearch?.get('offset')) || 0;
    const limit = Number(state.lastSearch?.get('limit')) || Number(byId('limit').value) || 100;
    byId('page-info').textContent = `Offset ${offset.toLocaleString()} · up to ${limit.toLocaleString()} results per indexer`;
  }

  function appendResultTitle(container, result) {
    container.append(element('div', result.title || '(Untitled release)', 'result-title'));
    const detailsURL = safeDetails(result.details);
    if (detailsURL) {
      const link = element('a', 'Details', 'small');
      link.href = detailsURL;
      link.target = '_blank';
      link.rel = 'noopener noreferrer';
      link.setAttribute('aria-label', `Details for ${result.title} (opens in a new tab)`);
      container.append(link);
    }
  }

  function appendResultFactors(container, result) {
    container.append(element('div', (result.categories || []).join(', ') || 'No categories', 'result-meta'));
    const factorText = (value) => Number.isFinite(value) ? `${value.toLocaleString()}×` : 'unknown';
    container.append(element('div', `Download ${factorText(result.download_volume_factor)} · Upload ${factorText(result.upload_volume_factor)}`, 'result-meta'));
  }

  function resultRow(result) {
    const row = element('tr');
    const title = element('td');
    appendResultTitle(title, result);
    const age = element('td', ageText(result.published));
    if (Number.isFinite(Date.parse(result.published))) age.title = new Date(result.published).toLocaleString();
    const factors = element('td');
    appendResultFactors(factors, result);
    row.append(title, element('td', result.indexer || result.indexer_id || 'Unknown'), element('td', sizeText(result.size)), element('td', Number(result.seeders) || 0), element('td', Number(result.peers) || 0), age, factors, downloadActions(result));
    return row;
  }

  function resultCard(result) {
    const card = element('article', undefined, 'result-card');
    card.setAttribute('aria-label', result.title || 'Untitled release');
    const title = element('header');
    appendResultTitle(title, result);
    const metadata = element('dl', undefined, 'result-card-metadata');
    for (const [label, value] of [
      ['Tracker', result.indexer || result.indexer_id || 'Unknown'],
      ['Size', sizeText(result.size)],
      ['Seeds', Number(result.seeders) || 0],
      ['Peers', Number(result.peers) || 0],
      ['Age', ageText(result.published)],
    ]) {
      const group = element('div');
      group.append(element('dt', label), element('dd', value));
      metadata.append(group);
    }
    const factors = element('div', undefined, 'result-card-factors');
    const values = element('dd');
    appendResultFactors(values, result);
    factors.append(element('dt', 'Categories / factors'), values);
    metadata.append(factors);
    card.append(title, metadata, downloadActions(result, 'div'));
    return card;
  }

  function downloadActions(result, tag = 'td') {
    const cell = element(tag, undefined, 'result-actions');
    if (!result.has_magnet && !result.has_torrent) {
      cell.append(element('p', 'No download available.', 'small'));
      return cell;
    }
    if (state.endpoints.length) {
      const controls = element('div', undefined, 'destination-controls');
      const destination = element('select', undefined, 'form-control');
      destination.setAttribute('aria-label', `Transmission destination for ${result.title}`);
      for (const endpoint of state.endpoints) {
        const option = element('option', endpoint.name);
        option.value = endpoint.name;
        destination.append(option);
      }
      const selected = state.destinations.get(result.id);
      if (state.endpoints.some((endpoint) => endpoint.name === selected)) destination.value = selected;
      const button = element('button', 'Add', 'btn-solid theme-primary btn-sm');
      button.type = 'button';
      const status = element('p', '', 'action-status');
      status.setAttribute('role', 'status');
      const keyFor = () => JSON.stringify([result.id, destination.value]);
      const displayStatus = () => {
        const value = state.downloads.get(keyFor());
        button.disabled = Boolean(value?.pending);
        destination.disabled = Boolean(value?.pending);
        button.textContent = value?.pending ? 'Adding…' : 'Add';
        button.setAttribute('aria-label', `Add ${result.title} to ${destination.value}`);
        status.textContent = value?.message || '';
        status.className = `action-status${value?.error ? ' status-error' : value?.message ? ' status-success' : ''}`;
      };
      destination.addEventListener('change', () => {
        state.destinations.set(result.id, destination.value);
        displayStatus();
      });
      displayStatus();
      button.addEventListener('click', async () => {
        const endpoint = destination.value;
        const key = keyFor();
        if (state.downloads.get(key)?.pending) return;
        state.destinations.set(result.id, endpoint);
        state.downloads.set(key, { pending: true, message: `Sending to ${endpoint}…` });
        displayStatus();
        try {
          const data = await api('/api/download', {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: result.id, endpoint }),
          });
          state.downloads.set(key, { message: `${data.duplicate ? 'Already present' : 'Added'} on ${endpoint}: ${data.name || result.title}` });
        } catch (error) {
          state.downloads.set(key, { error: true, message: `${endpoint}: ${error.message}` });
        }
        if (button.isConnected) displayStatus();
        else renderResults();
      });
      controls.append(destination, button);
      cell.append(controls, status);
    } else {
      cell.append(element('p', 'No Transmission endpoints available.', 'small'));
    }
    const token = new URLSearchParams({ id: result.id });
    if (result.has_torrent) {
      const link = element('a', 'Download .torrent', 'btn-outline theme-secondary btn-sm');
      link.href = `/api/torrent?${token}`;
      link.download = '';
      cell.append(link);
    }
    if (result.has_magnet) {
      const button = element('button', 'Copy magnet', 'btn-outline theme-secondary btn-sm');
      button.type = 'button';
      button.setAttribute('aria-label', `Copy magnet for ${result.title}`);
      const status = element('p', '', 'action-status');
      status.setAttribute('role', 'status');
      let manualInput;
      button.addEventListener('click', async () => {
        button.disabled = true;
        status.textContent = 'Fetching magnet…';
        try {
          const data = await api(`/api/magnet?${token}`);
          if (typeof data.magnet !== 'string' || !data.magnet.startsWith('magnet:?')) throw new Error('The server did not return a valid magnet link.');
          try {
            if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable');
            await navigator.clipboard.writeText(data.magnet);
            status.textContent = 'Magnet copied.';
          } catch {
            if (!manualInput) {
              manualInput = element('input', undefined, 'form-control magnet-value');
              manualInput.type = 'text';
              manualInput.readOnly = true;
              manualInput.setAttribute('aria-label', `Magnet link for ${result.title}; select and copy`);
              cell.append(manualInput);
            }
            manualInput.value = data.magnet;
            manualInput.focus();
            manualInput.select();
            status.textContent = 'Automatic clipboard access is unavailable. Copy the selected magnet link manually.';
          }
        } catch (error) {
          status.textContent = error.message;
        } finally {
          button.disabled = false;
        }
      });
      cell.append(button, status);
    }
    return cell;
  }

  async function setup() {
    byId('retry-setup').hidden = true;
    message('app-error', '');
    byId('search-controls').disabled = true;
    try {
      const [config, discovery] = await Promise.all([api('/api/config'), api('/api/indexers')]);
      state.endpoints = Array.isArray(config.endpoints) ? config.endpoints : [];
      state.indexers = Array.isArray(discovery.indexers) ? discovery.indexers : [];
      byId('indexer-list').replaceChildren();
      state.indexers.forEach((indexer, index) => {
        const wrapper = element('div', undefined, 'selector-choice');
        const input = element('input', undefined, 'check');
        input.type = 'checkbox';
        input.name = 'indexer';
        input.id = `indexer-${index}`;
        input.value = indexer.id;
        input.disabled = Boolean(indexer.capability_error);
        input.checked = !input.disabled;
        const label = element('label', indexer.name || indexer.id);
        label.htmlFor = input.id;
        if (indexer.description) label.title = indexer.description;
        wrapper.append(input, label);
        if (indexer.capability_error) {
          const error = element('span', `Unavailable: ${indexer.capability_error}`, 'small status-error');
          error.id = `indexer-error-${index}`;
          input.setAttribute('aria-describedby', error.id);
          label.append(document.createTextNode(' — '), error);
        }
        byId('indexer-list').append(wrapper);
      });
      if (!state.indexers.length) byId('indexer-list').append(element('p', 'No configured indexers. Add an indexer in Jackett, then retry the connection.'));
      byId('search-controls').disabled = false;
      const shouldSearch = restoreURL();
      renderResults();
      if (!state.indexers.length || state.indexers.some((indexer) => indexer.capability_error)) byId('retry-setup').hidden = false;
      if (state.indexers.length && shouldSearch) await search(false);
    } catch (error) {
      message('app-error', error.message);
      byId('retry-setup').hidden = false;
    }
  }

  form.addEventListener('submit', (event) => { event.preventDefault(); search(); });
  byId('indexer-list').addEventListener('change', () => updateCapabilities());
  byId('mode').addEventListener('change', () => updateCapabilities());
  byId('select-all').addEventListener('click', () => {
    for (const input of byId('indexer-list').querySelectorAll('input')) input.checked = !input.disabled;
    updateCapabilities();
  });
  byId('select-none').addEventListener('click', () => {
    for (const input of byId('indexer-list').querySelectorAll('input')) input.checked = false;
    updateCapabilities();
  });
  byId('clear-categories').addEventListener('click', () => {
    for (const input of byId('categories').querySelectorAll('input')) input.checked = false;
  });
  byId('cancel-search').addEventListener('click', () => {
    cancelSearch();
    message('search-status', 'Search canceled.');
    renderResults('Search canceled.');
  });
  for (const [id, direction] of [['previous-page', -1], ['next-page', 1]]) {
    byId(id).addEventListener('click', () => {
      if (!state.lastSearch) return;
      const params = new URLSearchParams(state.lastSearch);
      const offset = Number(params.get('offset')) || 0;
      const limit = Number(params.get('limit')) || 100;
      params.set('offset', String(Math.max(0, offset + direction * limit)));
      search(true, params);
    });
  }
  for (const id of ['result-filter', 'min-seeds']) {
    byId(id).addEventListener('input', () => {
      renderResults();
      if (state.lastSearch) persistURL(state.lastSearch, false);
    });
  }
  for (const button of document.querySelectorAll('[data-sort]')) {
    button.addEventListener('click', () => {
      const key = button.dataset.sort;
      state.direction = state.sort === key ? (state.direction === 'asc' ? 'desc' : 'asc') : (key === 'title' || key === 'indexer' ? 'asc' : 'desc');
      state.sort = key;
      renderResults();
      if (state.lastSearch) persistURL(state.lastSearch, false);
    });
  }
  byId('mobile-sort').addEventListener('change', (event) => {
    state.sort = event.target.value;
    state.direction = state.sort === 'title' || state.sort === 'indexer' ? 'asc' : 'desc';
    renderResults();
    if (state.lastSearch) persistURL(state.lastSearch, false);
  });
  byId('mobile-sort-direction').addEventListener('click', () => {
    state.direction = state.direction === 'asc' ? 'desc' : 'asc';
    renderResults();
    if (state.lastSearch) persistURL(state.lastSearch, false);
  });
  byId('retry-setup').addEventListener('click', setup);
  compactResults.addEventListener('change', () => renderResults());
  window.addEventListener('popstate', () => {
    cancelSearch();
    if (restoreURL()) search(false);
    else {
      state.results = [];
      state.errors = [];
      state.lastSearch = null;
      state.searched = false;
      byId('search-errors').hidden = true;
      message('search-status', 'Choose indexers and search. An empty query is welcome.');
      renderResults();
      setSearching(false);
    }
  });
  setup();
})();
