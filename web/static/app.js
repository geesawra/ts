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
  const reserved = new Set(['q', 't', 'indexer', 'cat', 'limit', 'offset', 'apikey', 'complete_seasons', 'series_id', 'filter', 'min_seeds', 'sort', 'direction']);
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
    fieldValues: new Map(), downloads: new Map(), destinations: new Map(),
    seasonChoices: new Map(), seasonDestinations: new Map(), seasonDownloads: new Map(),
    omdbEnabled: false, seriesID: '', seriesController: null, seriesGeneration: 0,
    seasons: [], aggregation: null, resultVersion: 0,
    controller: null, generation: 0, lastSearch: null, sort: 'seeders', direction: 'desc', searched: false,
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

  function seasonMode() {
    return state.omdbEnabled && byId('complete-seasons').checked;
  }

  function resetSeries() {
    state.seriesGeneration += 1;
    state.seriesController?.abort();
    state.seriesController = null;
    state.seriesID = '';
    const placeholder = element('option', 'Find and select a series');
    placeholder.value = '';
    byId('series-choice').replaceChildren(placeholder);
    message('series-status', '');
  }

  function updateSeasonControls() {
    const supportedMode = ['search', 'tvsearch'].includes(byId('mode').value);
    byId('season-search-controls').hidden = !state.omdbEnabled;
    byId('complete-seasons').disabled = !state.omdbEnabled || !supportedMode;
    if (!supportedMode && byId('complete-seasons').checked) {
      byId('complete-seasons').checked = false;
      resetSeries();
    }
    const active = seasonMode();
    byId('series-options').hidden = !active;
    byId('series-choice').disabled = !active;
    byId('season-number').disabled = !active;
    byId('find-series').disabled = !active || !byId('query').value.trim() || Boolean(state.seriesController);
    byId('limit').disabled = active;
    byId('offset').disabled = active;
    const advancedSeason = state.fields.get('season');
    if (advancedSeason && active) advancedSeason.disabled = true;
    message('season-mode-note', supportedMode ? '' : 'Season search is available in General search or TV mode.');
  }

  async function findSeries() {
    const query = byId('query').value.trim();
    if (!seasonMode() || !query) return;
    resetSeries();
    const generation = state.seriesGeneration;
    const controller = new AbortController();
    state.seriesController = controller;
    updateSeasonControls();
    message('series-status', 'Looking up series in OMDb…');
    try {
      const data = await api(`/api/series?${new URLSearchParams({ q: query })}`, { signal: controller.signal });
      if (generation !== state.seriesGeneration) return;
      const series = Array.isArray(data.series) ? data.series : [];
      for (const item of series) {
        const option = element('option', `${item.title} (${item.year || 'year unknown'}) · IMDb ${item.id}${item.total_seasons ? ` · ${item.total_seasons} seasons` : ''}`);
        option.value = item.id;
        byId('series-choice').append(option);
      }
      message('series-status', series.length ? 'Select the exact series before searching.' : 'No series found. Change the query and try again.');
    } catch (error) {
      if (generation === state.seriesGeneration && error.name !== 'AbortError') message('series-status', error.message, 'form-text status-error');
    } finally {
      if (generation === state.seriesGeneration) {
        state.seriesController = null;
        updateSeasonControls();
      }
    }
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
    updateSeasonControls();
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
    if (seasonMode()) {
      if (!['search', 'tvsearch'].includes(mode)) throw new Error('Choose General search or TV mode for season search.');
      if (!state.seriesID) throw new Error('Find and select an exact series before searching complete seasons.');
      params.set('complete_seasons', '1');
      params.set('series_id', state.seriesID);
      params.delete('season');
      if (byId('season-number').value) params.set('season', byId('season-number').value);
      params.set('offset', '0');
      params.set('limit', '100');
    }
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
    resetSeries();
    byId('complete-seasons').checked = state.omdbEnabled && params.get('complete_seasons') === '1';
    byId('season-number').value = /^\d+$/.test(params.get('season') || '') ? params.get('season') : '';
    if (seasonMode() && /^tt\d+$/.test(params.get('series_id') || '')) {
      state.seriesID = params.get('series_id');
      const option = element('option', `Restored series · IMDb ${state.seriesID} (find series to change)`);
      option.value = state.seriesID;
      byId('series-choice').append(option);
      byId('series-choice').value = state.seriesID;
    }
    byId('limit').value = /^\d+$/.test(params.get('limit') || '') ? params.get('limit') : '100';
    byId('offset').value = /^\d+$/.test(params.get('offset') || '') ? params.get('offset') : '0';
    byId('result-filter').value = params.get('filter') || '';
    byId('min-seeds').value = /^\d+$/.test(params.get('min_seeds') || '') ? params.get('min_seeds') : '0';
    state.sort = sortLabels.has(params.get('sort')) ? params.get('sort') : 'seeders';
    state.direction = params.get('direction') === 'asc' ? 'asc' : 'desc';
    state.fieldValues.clear();
    for (const input of state.fields.values()) input.value = '';
    for (const [name, value] of params) if (!reserved.has(name) && !(seasonMode() && name === 'season')) state.fieldValues.set(name, value);
    // Rebuild after clearing the old controls so they cannot overwrite restored values.
    state.fields.clear();
    updateCapabilities(params.get('t') || 'search', params.getAll('cat'));
    byId('advanced').open = byId('mode').value !== 'search' || Array.from(state.fields.values()).some((input) => input.value);
    return params.has('t') || params.has('q') || params.has('indexer') || seasonMode();
  }

  function setSearching(busy) {
    byId('results-section').setAttribute('aria-busy', String(busy));
    byId('search-button').textContent = busy ? 'Search again' : 'Search';
    byId('cancel-search').hidden = !busy;
    const aggregated = state.lastSearch?.get('complete_seasons') === '1';
    byId('previous-page').disabled = busy || aggregated || !state.lastSearch || Number(state.lastSearch.get('offset')) <= 0;
    byId('next-page').disabled = busy || aggregated || !state.lastSearch || (!state.results.length && !state.errors.length);
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
    state.searched = true;
    state.resultVersion += 1;
    state.seasons = [];
    state.aggregation = null;
    state.results = [];
    state.errors = [];
    state.downloads.clear();
    state.destinations.clear();
    state.seasonChoices.clear();
    state.seasonDestinations.clear();
    state.seasonDownloads.clear();
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
      state.seasons = state.omdbEnabled && params.get('complete_seasons') === '1' && Array.isArray(data.seasons) ? data.seasons : [];
      state.aggregation = params.get('complete_seasons') === '1' ? data.aggregation : null;
      const errorBox = byId('search-errors');
      errorBox.replaceChildren();
      errorBox.hidden = !state.errors.length;
      if (state.errors.length) {
        errorBox.append(element('p', 'Some search or metadata requests could not complete. Available results and candidates are shown.'));
        const list = element('ul', undefined, 'mb-0');
        for (const error of state.errors) list.append(element('li', error));
        errorBox.append(list);
      }
      const aggregationNote = state.aggregation ? ` Aggregated ${state.aggregation.pages || 0} pages${state.aggregation.partial ? '; partial aggregation — more releases may exist' : ''}.` : '';
      message('search-status', (state.results.length ? `Search complete${state.errors.length ? ' with partial errors' : ''}.` : `No releases returned${state.errors.length ? '; see errors below' : '. Try another query, mode, category, or offset'}.`) + aggregationNote, state.aggregation?.partial ? 'status-warning' : '');
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


  function compareSeasonCandidates(a, b) {
    return (Number(b.result.seeders) || 0) - (Number(a.result.seeders) || 0)
      || String(a.result.title || '').localeCompare(String(b.result.title || ''), undefined, { numeric: true, sensitivity: 'base' });
  }
  function seasonDownloadable(candidate) {
    return candidate.result.has_magnet || candidate.result.has_torrent;
  }

  function seasonQuality(candidate) {
    const parsed = candidate.parsed;
    const resolution = (parsed.resolution || '').toLowerCase().replace(/^4k$/, '2160p');
    const source = (parsed.source || '').toUpperCase().replace(/[ ._-]/g, '')
      .replace(/^WEBDL$/, 'WEB-DL').replace(/^WEBRIP$/, 'WEBRip')
      .replace(/^BLURAY$/, 'BluRay').replace(/^BDRIP$/, 'BDRip').replace(/^DVDRIP$/, 'DVDRip');
    const codec = (parsed.codec || '').toUpperCase().replace(/[ .]/g, '')
      .replace(/^(H265|HEVC)$/, 'HEVC').replace(/^(H264|AVC)$/, 'H.264').replace(/^X26([45])$/, 'x26$1');
    const releaseGroup = parsed.releaseGroup || '';
    return {
      key: JSON.stringify([resolution, source, codec, releaseGroup.toLowerCase()]),
      releaseGroup,
      label: [resolution || 'Unknown resolution', source || 'Unknown source', codec || 'Unknown codec', releaseGroup || 'Unknown release group'].join(' · '),
    };
  }

  function seasonEpisodeSelection(candidates, episodeNumbers, initial = {}) {
    const selection = { ...initial };
    const relevant = new Set(episodeNumbers);
    const covered = new Set(Object.keys(selection).filter((number) => selection[number]).map(Number));
    const choices = candidates.filter(seasonDownloadable).sort(compareSeasonCandidates);
    for (const allowOverlap of [false, true]) {
      for (const candidate of choices) {
        const episodes = candidate.parsed.episodes.filter((number) => relevant.has(number));
        if (!episodes.some((number) => !covered.has(number))) continue;
        if (!allowOverlap && episodes.some((number) => covered.has(number))) continue;
        for (const number of episodes) {
          if (!covered.has(number)) selection[number] = candidate.result.id;
          covered.add(number);
        }
      }
    }
    return selection;
  }

  function seasonBundles(candidates, episodeNumbers) {
    const bundles = new Map();
    for (const candidate of candidates) {
      if (candidate.parsed.pack || !seasonDownloadable(candidate)) continue;
      const quality = seasonQuality(candidate);
      if (!bundles.has(quality.key)) bundles.set(quality.key, { ...quality, candidates: [] });
      bundles.get(quality.key).candidates.push(candidate);
    }
    for (const bundle of bundles.values()) {
      bundle.candidates.sort(compareSeasonCandidates);
      bundle.episodes = seasonEpisodeSelection(bundle.candidates, episodeNumbers);
      bundle.covered = Object.keys(bundle.episodes).length;
      bundle.releaseCount = new Set(Object.values(bundle.episodes)).size;
    }
    return Array.from(bundles.values()).sort((a, b) => b.covered - a.covered
      || compareSeasonCandidates(a.candidates[0], b.candidates[0]) || a.key.localeCompare(b.key));
  }

  function seasonPresentationGroups() {
    if (!state.omdbEnabled || state.lastSearch?.get('complete_seasons') !== '1') return [];
    const results = new Map(state.results.map((result) => [result.id, result]));
    return state.seasons.map((season) => {
      const candidates = (season.candidates || []).filter((candidate) => results.has(candidate.id))
        .map((candidate) => ({ result: results.get(candidate.id), parsed: candidate.parsed }));
      const episodes = new Map();
      const packs = [];
      for (const candidate of candidates) {
        if (candidate.parsed.pack) packs.push(candidate);
        else for (const episode of candidate.parsed.episodes || []) {
          if (!episodes.has(episode)) episodes.set(episode, []);
          episodes.get(episode).push(candidate);
        }
      }
      packs.sort(compareSeasonCandidates);
      for (const choices of episodes.values()) choices.sort(compareSeasonCandidates);
      const expected = season.expected || [];
      const targetEpisodes = expected.length ? expected.map((episode) => episode.number) : Array.from(episodes.keys());
      return { ...season, candidates, packs, episodes, targetEpisodes, bundles: seasonBundles(candidates, targetEpisodes), expected, missing: season.missing || [] };
    });
  }

  function seasonEpisodeLabel(season, episode) {
    return `S${String(season).padStart(2, '0')}E${String(episode).padStart(2, '0')}`;
  }

  function seasonCandidateMeta(candidate) {
    const parsed = candidate.parsed;
    const coverage = parsed.pack
      ? `Season ${parsed.season} pack`
      : parsed.episodes.length > 1
        ? `Episodes ${parsed.episodes.join(', ')}`
        : seasonEpisodeLabel(parsed.season, parsed.episodes[0]);
    const quality = [parsed.resolution, parsed.source, parsed.codec].filter(Boolean).join(' · ') || 'Quality not identified';
    const values = [
      coverage,
      `Release group: ${parsed.releaseGroup || 'not identified'}`,
      quality,
      `Tracker: ${candidate.result.indexer || candidate.result.indexer_id || 'unknown'}`,
      `${Number(candidate.result.seeders) || 0} seeds`,
      sizeText(Number(candidate.result.size)),
    ];
    if (!candidate.result.has_magnet && !candidate.result.has_torrent) values.push('No downloadable file or magnet');
    return values.join(' · ');
  }

  function seasonChoice(name, value, title, meta, checked, disabled, onChange) {
    const wrapper = element('div', undefined, 'season-choice');
    const label = element('label');
    const input = element('input');
    input.type = 'radio';
    input.name = name;
    input.value = value;
    input.checked = checked;
    input.dataset.unavailable = String(disabled);
    input.disabled = disabled;
    input.addEventListener('change', onChange);
    const text = element('span');
    text.append(element('span', title, 'season-choice-title'), element('p', meta, 'season-choice-meta'));
    label.append(input, text);
    wrapper.append(label);
    return { wrapper, input };
  }

  function seasonGroupNode(group, groupIndex) {
    const downloadable = seasonDownloadable;
    let selection = state.seasonChoices.get(group.key);
    const episodeNumbers = Array.from(new Set([...group.episodes.keys(), ...group.expected.map((episode) => episode.number)])).sort((a, b) => a - b);
    const candidatesByID = new Map(group.candidates.filter((candidate) => !candidate.parsed.pack && downloadable(candidate))
      .map((candidate) => [candidate.result.id, candidate]));
    const preferredBundle = group.bundles.find((bundle) => bundle.releaseGroup.toLowerCase() === (group.preferred_release_group || '').toLowerCase()) || group.bundles[0];
    const recommendedEpisodes = seasonEpisodeSelection(
      (group.recommended_ids || []).map((id) => candidatesByID.get(id)).filter(Boolean),
      group.targetEpisodes, preferredBundle?.episodes);
    if (!selection) {
      const pack = group.packs.find(downloadable);
      const exactBundle = group.bundles.find((bundle) => Object.keys(bundle.episodes).length === Object.keys(recommendedEpisodes).length
        && Object.entries(recommendedEpisodes).every(([episode, id]) => bundle.episodes[episode] === id));
      selection = {
        mode: Object.keys(recommendedEpisodes).length || !pack ? 'episodes' : `pack:${pack.result.id}`,
        episodes: recommendedEpisodes, bundle: exactBundle?.key || 'recommended',
        inspectOpen: false, openEpisodes: new Set(),
      };
      state.seasonChoices.set(group.key, selection);
    }
    const details = element('details', undefined, 'season-group');
    details.dataset.seasonKey = group.key;
    const coverage = group.metadata_error ? 'Metadata unavailable — completeness unknown' : group.coverage_complete ? 'OMDb inventory covered' : 'Incomplete OMDb coverage';
    details.append(element('summary', `${group.show} · Season ${group.season} · ${coverage} · ${group.bundles.length} quality bundle${group.bundles.length === 1 ? '' : 's'} · ${group.candidates.length} releases`));
    const content = element('div', undefined, 'season-group-content');
    content.append(element('p', 'Coverage checks only the OMDb episode inventory; this does not mean the season has finished airing. Pack titles alone do not verify their contents. All original releases and download actions remain below.', 'season-group-note'));
    if (group.preferred_release_group) content.append(element('p', `Preferred release group: ${group.preferred_release_group} — broadest distinct episode coverage. The recommended selection starts with its best-covered quality bundle, then fills gaps with other recommended releases.`, 'season-group-note'));
    if (group.metadata_error) content.append(element('p', group.metadata_error, 'season-group-note status-error'));
    content.append(element('p', group.missing.length ? `Missing downloadable episodes: ${group.missing.map((number) => seasonEpisodeLabel(group.season, number)).join(', ')}` : group.expected.length ? 'No missing episodes in the OMDb inventory.' : 'No OMDb episode inventory available; completeness is unknown.', `season-group-note${group.missing.length || !group.expected.length ? ' status-warning' : ''}`));
    const inventory = element('details');
    inventory.append(element('summary', `Expected episodes from OMDb (${group.expected.length})`));
    const inventoryList = element('ul', undefined, 'season-inventory');
    for (const episode of group.expected) inventoryList.append(element('li', `${seasonEpisodeLabel(group.season, episode.number)} · ${episode.title || 'Title unknown'} · Released: ${episode.released || 'unknown'}${group.missing.includes(episode.number) ? ' · Missing downloadable candidate' : ''}`));
    inventory.append(inventoryList);
    content.append(inventory);

    const packField = element('fieldset', undefined, 'season-choice-list');
    packField.append(element('legend', 'Choose a season pack or episode bundles'));
    for (const candidate of group.packs) {
      const id = candidate.result.id;
      const choice = seasonChoice(`season-mode-${groupIndex}`, `pack:${id}`, candidate.result.title || '(Untitled season pack)',
        `Unverified pack contents · ${seasonCandidateMeta(candidate)}`, selection.mode === `pack:${id}`, !downloadable(candidate),
        () => { selection.mode = `pack:${id}`; refresh(); });
      choice.input.dataset.mode = `pack:${id}`;
      packField.append(choice.wrapper);
    }
    const episodeMode = seasonChoice(`season-mode-${groupIndex}`, 'episodes', 'Episode bundles',
      'Choose a quality/release-group bundle below, or inspect and override individual torrents.', selection.mode === 'episodes', !group.bundles.length,
      () => { selection.mode = 'episodes'; refresh(); });
    episodeMode.input.dataset.mode = 'episodes';
    packField.append(episodeMode.wrapper);
    content.append(packField);

    const bundleField = element('fieldset', undefined, 'season-choice-list season-bundles');
    bundleField.append(element('legend', 'Resolution, quality, and release group'));
    const recommendedChoice = seasonChoice(`season-bundle-${groupIndex}`, 'recommended', 'Recommended selection (gap fills may mix qualities or groups)',
      `${Object.keys(recommendedEpisodes).length}/${group.targetEpisodes.length} episodes · Starts with ${preferredBundle?.label || 'available releases'}. Inspect the selection below.`,
      selection.bundle === 'recommended', !Object.keys(recommendedEpisodes).length,
      () => { selection.mode = 'episodes'; selection.bundle = 'recommended'; selection.episodes = { ...recommendedEpisodes }; refresh(); });
    recommendedChoice.input.dataset.bundle = 'recommended';
    bundleField.append(recommendedChoice.wrapper);
    for (const bundle of group.bundles) {
      const gaps = group.targetEpisodes.length - bundle.covered;
      const choice = seasonChoice(`season-bundle-${groupIndex}`, bundle.key, bundle.label,
        `${bundle.covered}/${group.targetEpisodes.length} episodes · ${gaps ? `${gaps} gap${gaps === 1 ? '' : 's'}` : 'Full episode coverage'} · ${bundle.releaseCount} torrent${bundle.releaseCount === 1 ? '' : 's'} · ${bundle.candidates.length} alternative${bundle.candidates.length === 1 ? '' : 's'} · No automatic gap fills`,
        selection.bundle === bundle.key, !bundle.releaseCount,
        () => { selection.mode = 'episodes'; selection.bundle = bundle.key; selection.episodes = { ...bundle.episodes }; refresh(); });
      choice.input.dataset.bundle = bundle.key;
      bundleField.append(choice.wrapper);
    }
    if (!group.bundles.length) bundleField.append(element('p', 'No downloadable episode bundles identified. Season packs, if available, remain selectable above.', 'season-group-note status-warning'));
    const selectedSummary = element('p', '', 'season-selection-summary');
    const selectedQualities = element('ul', undefined, 'season-selection-qualities');
    const inspector = element('details', undefined, 'season-inspector');
    inspector.append(element('summary', `Inspect or override individual torrents (${episodeNumbers.length} episodes)`));
    const episodeList = element('div', undefined, 'season-episode-list');
    inspector.append(episodeList);
    const episodeSummaries = new Map();
    let inspectorBuilt = false;
    const buildInspector = () => {
      if (inspectorBuilt) return;
      inspectorBuilt = true;
      for (const episode of episodeNumbers) {
        const episodeDetails = element('details', undefined, 'season-episode');
        const summary = element('summary');
        episodeSummaries.set(episode, summary);
        episodeDetails.append(summary);
        const candidates = group.episodes.get(episode) || [];
        let choicesBuilt = false;
        const buildChoices = () => {
          if (choicesBuilt) return;
          choicesBuilt = true;
          const field = element('fieldset', undefined, 'season-choice-list');
          field.append(element('legend', seasonEpisodeLabel(group.season, episode)));
          if (!candidates.some(downloadable)) field.append(element('p', 'No individual downloadable candidate identified.', 'season-group-note status-warning'));
          for (const candidate of candidates) {
            const id = candidate.result.id;
            const choice = seasonChoice(`season-episode-${groupIndex}-${episode}`, id, candidate.result.title || '(Untitled release)',
              seasonCandidateMeta(candidate), selection.mode === 'episodes' && selection.episodes[episode] === id, !downloadable(candidate),
              () => {
                selection.mode = 'episodes';
                selection.bundle = 'custom';
                for (const number of candidate.parsed.episodes) selection.episodes[number] = id;
                refresh();
              });
            choice.input.dataset.episode = String(episode);
            field.append(choice.wrapper);
          }
          if (candidates.length) {
            const skip = seasonChoice(`season-episode-${groupIndex}-${episode}`, '', 'Skip this episode',
              'A multi-episode torrent containing this episode is skipped for all its episodes.',
              selection.mode === 'episodes' && !selection.episodes[episode], false,
              () => {
                selection.mode = 'episodes';
                selection.bundle = 'custom';
                const skipped = new Set(selectedCandidates().filter((candidate) => candidate.parsed.episodes.includes(episode)).map((candidate) => candidate.result.id));
                for (const number of Object.keys(selection.episodes)) {
                  if (skipped.has(selection.episodes[number])) selection.episodes[number] = '';
                }
                selection.episodes[episode] = '';
                refresh();
              });
            skip.input.dataset.episode = String(episode);
            field.append(skip.wrapper);
          }
          episodeDetails.append(field);
        };
        episodeDetails.open = selection.openEpisodes.has(episode);
        if (episodeDetails.open) buildChoices();
        episodeDetails.addEventListener('toggle', () => {
          if (episodeDetails.open) {
            selection.openEpisodes.add(episode);
            buildChoices();
          } else selection.openEpisodes.delete(episode);
          refresh();
        });
        episodeList.append(episodeDetails);
      }
    };
    inspector.open = selection.inspectOpen;
    if (inspector.open) buildInspector();
    inspector.addEventListener('toggle', () => {
      selection.inspectOpen = inspector.open;
      if (inspector.open) buildInspector();
      refresh();
    });
    content.append(bundleField, selectedSummary, selectedQualities, inspector);

    const actions = element('div', undefined, 'season-group-actions');
    const destination = element('select', undefined, 'form-control');
    destination.setAttribute('aria-label', `Transmission destination for ${group.show} season ${group.season}`);
    for (const endpoint of state.endpoints) {
      const option = element('option', endpoint.name);
      option.value = endpoint.name;
      destination.append(option);
    }
    const previousDestination = state.seasonDestinations.get(group.key);
    if (state.endpoints.some((endpoint) => endpoint.name === previousDestination)) destination.value = previousDestination;
    destination.addEventListener('change', () => state.seasonDestinations.set(group.key, destination.value));
    const button = element('button', 'Add season', 'btn-solid theme-primary');
    button.type = 'button';
    const warning = element('p', '', 'season-group-note status-warning');
    const status = element('p', '', 'season-group-status');
    status.setAttribute('role', 'status');
    const outcomes = element('ul', undefined, 'season-outcomes');
    const selectedCandidates = () => {
      if (selection.mode.startsWith('pack:')) return group.packs.filter((candidate) => candidate.result.id === selection.mode.slice(5) && downloadable(candidate));
      const unique = new Map();
      for (const episode of episodeNumbers) {
        const candidate = group.episodes.get(episode)?.find((item) => item.result.id === selection.episodes[episode] && downloadable(item));
        if (candidate) unique.set(candidate.result.id, candidate);
      }
      return Array.from(unique.values());
    };
    const refresh = () => {
      const record = state.seasonDownloads.get(group.key) || { pending: false, message: '', items: [] };
      const selected = selectedCandidates();
      button.disabled = record.pending || !selected.length || !state.endpoints.length;
      destination.disabled = record.pending || !state.endpoints.length;
      for (const input of details.querySelectorAll('input[type="radio"]')) {
        input.disabled = record.pending || input.dataset.unavailable === 'true';
        input.checked = input.dataset.bundle !== undefined
          ? selection.mode === 'episodes' && selection.bundle === input.dataset.bundle
          : input.dataset.mode !== undefined ? selection.mode === input.dataset.mode
            : selection.mode === 'episodes' && (selection.episodes[input.dataset.episode] || '') === input.value;
      }
      const counts = new Map();
      if (selection.mode === 'episodes') for (const candidate of selected) {
        for (const episode of candidate.parsed.episodes) counts.set(episode, (counts.get(episode) || 0) + 1);
      }
      for (const [episode, summary] of episodeSummaries) {
        const candidate = candidatesByID.get(selection.episodes[episode]);
        summary.textContent = `${seasonEpisodeLabel(group.season, episode)} · ${candidate ? candidate.result.title : 'Not selected'}${selection.mode !== 'episodes' ? ' (episode selection inactive)' : ''}`;
      }
      const qualityCounts = new Map();
      for (const candidate of selected) {
        const quality = seasonQuality(candidate);
        const entry = qualityCounts.get(quality.key) || { label: quality.label, count: 0 };
        entry.count += 1;
        qualityCounts.set(quality.key, entry);
      }
      const bundle = group.bundles.find((item) => item.key === selection.bundle);
      const label = selection.mode.startsWith('pack:') ? 'Season pack (contents unverified)'
        : selection.bundle === 'custom' ? 'Custom episode selection'
          : selection.bundle === 'recommended' ? 'Recommended selection with gap fills' : bundle?.label || 'Episode selection';
      selectedSummary.textContent = `${label} · ${selected.length} selected torrent${selected.length === 1 ? '' : 's'} · ${sizeText(selected.reduce((total, candidate) => total + (Number(candidate.result.size) || 0), 0))} total reported size`;
      selectedQualities.replaceChildren();
      for (const { label: quality, count } of qualityCounts.values()) selectedQualities.append(element('li', `${quality} · ${count} torrent${count === 1 ? '' : 's'}`));
      selectedQualities.hidden = qualityCounts.size < 2;
      const overlap = Array.from(counts).filter(([, count]) => count > 1).map(([episode]) => episode);
      const unselected = selection.mode === 'episodes' ? group.expected.filter((episode) => !counts.has(episode.number)).map((episode) => episode.number) : [];
      warning.textContent = [
        overlap.length ? `Overlapping episode coverage: ${overlap.map((number) => seasonEpisodeLabel(group.season, number)).join(', ')}. Review selections to avoid redundant downloads.` : '',
        unselected.length ? `Current selection does not cover: ${unselected.map((number) => seasonEpisodeLabel(group.season, number)).join(', ')}.` : '',
        selection.mode.startsWith('pack:') ? 'Selected pack contents are unverified; the title is not evidence of complete coverage.' : '',
      ].filter(Boolean).join(' ');
      warning.hidden = !warning.textContent;
      button.textContent = record.pending ? `Adding ${record.done}/${record.total}…`
        : selection.mode.startsWith('pack:') ? 'Add season pack' : `Add ${selected.length} selected release${selected.length === 1 ? '' : 's'}`;
      status.textContent = record.message || '';
      status.className = `season-group-status${record.error ? ' status-error' : record.message ? ' status-success' : ''}`;
      outcomes.replaceChildren();
      for (const item of record.items || []) outcomes.append(element('li', `${item.title}: ${item.message}`, item.error ? 'status-error' : item.pending ? '' : 'status-success'));
      outcomes.hidden = !(record.items || []).length;
    };
    button.addEventListener('click', async () => {
      const selected = selectedCandidates();
      const endpoint = destination.value;
      if (!selected.length || !endpoint || state.seasonDownloads.get(group.key)?.pending) return;
      const version = state.resultVersion;
      const record = { pending: true, done: 0, total: selected.length, message: '', error: false,
        items: selected.map((candidate) => ({ title: candidate.result.title || 'Untitled release', pending: true, message: 'Queued' })) };
      state.seasonDownloads.set(group.key, record);
      state.seasonDestinations.set(group.key, endpoint);
      const current = () => version === state.resultVersion && state.seasonDownloads.get(group.key) === record;
      const update = () => { if (!current()) return; if (details.isConnected) refresh(); else renderResults(); };
      update();
      let added = 0;
      let duplicates = 0;
      let failed = 0;
      for (const [index, candidate] of selected.entries()) {
        const item = record.items[index];
        item.message = `Sending to ${endpoint}…`;
        update();
        try {
          const data = await api('/api/download', {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: candidate.result.id, endpoint }),
          });
          if (data.duplicate) duplicates += 1;
          else added += 1;
          item.message = `${data.duplicate ? 'Already present' : 'Added'} on ${endpoint}: ${data.name || item.title}`;
        } catch (error) {
          failed += 1;
          item.error = true;
          item.message = `${endpoint}: ${error.message}`;
        }
        item.pending = false;
        record.done += 1;
        update();
      }
      record.pending = false;
      record.error = failed > 0;
      record.message = `${added} added · ${duplicates} already present · ${failed} failed on ${endpoint}.`;
      update();
    });
    actions.append(destination, button);
    content.append(warning, actions, status, outcomes);
    if (!state.endpoints.length) content.append(element('p', 'No Transmission endpoints available.', 'season-group-note'));
    details.append(content);
    refresh();
    return details;
  }

  function renderSeasonGroups(groups) {
    const container = byId('season-groups');
    const open = new Set(Array.from(container.querySelectorAll('details.season-group[open]'), (details) => details.dataset.seasonKey));
    container.replaceChildren();
    container.hidden = !groups.length;
    groups.forEach((group, index) => {
      const node = seasonGroupNode(group, index);
      node.open = open.has(group.key);
      container.append(node);
    });
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
    const groups = seasonPresentationGroups();
    renderSeasonGroups(groups);
    for (const button of document.querySelectorAll('[data-sort]')) {
      const active = button.dataset.sort === state.sort;
      button.parentElement.setAttribute('aria-sort', active ? (state.direction === 'asc' ? 'ascending' : 'descending') : 'none');
      button.textContent = sortLabels.get(button.dataset.sort) + (active ? (state.direction === 'asc' ? ' ↑' : ' ↓') : '');
    }
    byId('mobile-sort').value = state.sort;
    const direction = byId('mobile-sort-direction');
    direction.textContent = state.direction === 'asc' ? 'Ascending ↑' : 'Descending ↓';
    direction.setAttribute('aria-label', `Sort ${state.direction === 'asc' ? 'ascending; switch to descending' : 'descending; switch to ascending'}`);
    byId('result-count').textContent = `${results.length} shown / ${state.results.length} returned${state.lastSearch?.get('complete_seasons') === '1' ? ' across aggregated pages' : ' on this page'}${groups.length ? ` · ${groups.length} season proposal${groups.length === 1 ? '' : 's'} above (unfiltered)` : ''}`;
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
    byId('page-info').textContent = state.lastSearch?.get('complete_seasons') === '1'
      ? `Aggregated season search · ${state.aggregation?.pages || 0} pages${state.aggregation?.partial ? ' · Partial results' : ''} · Pagination disabled`
      : `Offset ${offset.toLocaleString()} · up to ${limit.toLocaleString()} results per indexer`;
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
        const version = state.resultVersion;
        if (state.downloads.get(key)?.pending) return;
        state.destinations.set(result.id, endpoint);
        state.downloads.set(key, { pending: true, message: `Sending to ${endpoint}…` });
        displayStatus();
        try {
          const data = await api('/api/download', {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: result.id, endpoint }),
          });
          if (version !== state.resultVersion) return;
          state.downloads.set(key, { message: `${data.duplicate ? 'Already present' : 'Added'} on ${endpoint}: ${data.name || result.title}` });
        } catch (error) {
          if (version !== state.resultVersion) return;
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
      state.omdbEnabled = config.omdb_enabled === true;
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
  byId('query').addEventListener('input', () => {
    resetSeries();
    updateSeasonControls();
  });
  byId('complete-seasons').addEventListener('change', () => {
    resetSeries();
    updateCapabilities();
  });
  byId('find-series').addEventListener('click', findSeries);
  byId('series-choice').addEventListener('change', () => {
    state.seriesID = byId('series-choice').value;
    message('series-status', state.seriesID ? `Selected IMDb ${state.seriesID}.` : 'Select an exact series before searching.');
  });
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
      if (!state.lastSearch || state.lastSearch.get('complete_seasons') === '1') return;
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
      state.resultVersion += 1;
      state.seasons = [];
      state.aggregation = null;
      state.seasonChoices.clear();
      state.seasonDestinations.clear();
      state.seasonDownloads.clear();
      state.downloads.clear();
      state.destinations.clear();
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
