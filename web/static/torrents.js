(() => {
  'use strict';

  const byId = (id) => document.getElementById(id);
  const destination = byId('torrent-endpoint');
  const list = byId('torrent-list');
  const refreshButton = byId('torrent-refresh');
  const statuses = ['Stopped', 'Queued to verify', 'Verifying', 'Queued to download', 'Downloading', 'Queued to seed', 'Seeding'];
  let configured = false;
  let torrents = [];
  let controller = null;
  let generation = 0;
  let timer = null;
  let busy = false;
  let actionError = '';

  function element(tag, text, className) {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = text;
    if (className) node.className = className;
    return node;
  }

  function showError(text) {
    byId('torrent-error').textContent = text;
    byId('torrent-error').hidden = !text;
  }

  async function api(path, options = {}) {
    const response = await fetch(path, { credentials: 'same-origin', mode: 'same-origin', ...options });
    let data;
    try {
      data = await response.json();
    } catch (error) {
      if (error.name === 'AbortError') throw error;
      throw new Error('The server returned an unreadable response.');
    }
    if (response.status === 401) throw new Error('Authentication failed. Reload and sign in with the configured web credentials.');
    if (!response.ok) throw new Error(data.error || `Request failed (HTTP ${response.status}).`);
    return data;
  }

  function bytes(value) {
    if (!Number.isFinite(value) || value < 0) return 'Unknown';
    if (value < 1024) return `${Math.round(value)} B`;
    const unit = Math.min(Math.floor(Math.log(value) / Math.log(1024)), 4);
    return `${(value / 1024 ** unit).toFixed(1)} ${['B', 'KiB', 'MiB', 'GiB', 'TiB'][unit]}`;
  }

  function etaText(seconds) {
    if (seconds < 0 || !Number.isFinite(seconds)) return 'Unknown';
    if (seconds < 60) return `${Math.ceil(seconds)}s`;
    if (seconds < 3600) return `${Math.ceil(seconds / 60)}m`;
    if (seconds < 86400) return `${Math.ceil(seconds / 3600)}h`;
    return `${Math.ceil(seconds / 86400)}d`;
  }

  function setBusy(value) {
    busy = value;
    destination.disabled = busy || !destination.options.length;
    refreshButton.disabled = busy;
    for (const button of list.querySelectorAll('button')) button.disabled = busy;
  }

  function render() {
    const filter = byId('torrent-filter').value.trim().toLowerCase();
    const visible = torrents.filter((torrent) => torrent.name.toLowerCase().includes(filter));
    list.replaceChildren();
    byId('torrent-status').textContent = `${visible.length} of ${torrents.length} tagged torrents on ${destination.value}.`;
    if (!visible.length) {
      list.append(element('p', torrents.length ? 'No torrents match this filter.' : 'No torrents labeled added-with-ts on this destination. Add a search result to get started.'));
      return;
    }
    for (const torrent of visible) {
      const card = element('article', undefined, 'torrent-card');
      card.append(element('h3', torrent.name || '(Untitled torrent)'));
      const progress = Math.min(1, Math.max(0, Number(torrent.progress) || 0));
      const bar = element('progress');
      bar.max = 1;
      bar.value = progress;
      bar.setAttribute('aria-label', `Download progress for ${torrent.name}`);
      card.append(bar, element('p', `${(progress * 100).toFixed(1)}% · ${statuses[torrent.status] || 'Unknown state'} · ${bytes(torrent.size)}`));
      card.append(element('p', `↓ ${bytes(torrent.download_rate)}/s · ↑ ${bytes(torrent.upload_rate)}/s · ETA: ${etaText(torrent.eta)}`));
      const ratio = torrent.ratio === -2 ? '∞' : torrent.ratio < 0 || !Number.isFinite(torrent.ratio) ? 'Unknown' : torrent.ratio.toFixed(2);
      card.append(element('p', `Downloaded: ${bytes(torrent.downloaded)} · Uploaded: ${bytes(torrent.uploaded)} · Ratio: ${ratio}`));
      if (torrent.error) card.append(element('p', torrent.error, 'status-error'));
      const actions = element('div', undefined, 'd-flex flex-wrap gap-2');
      for (const [action, label] of [
        [torrent.status === 0 ? 'start' : 'stop', torrent.status === 0 ? 'Start' : 'Pause'],
        ['remove', 'Remove (keep files)…'],
        ['remove-delete', 'Remove and delete files…'],
      ]) {
        const button = element('button', label, `btn-outline ${action === 'remove-delete' ? 'theme-danger' : 'theme-secondary'} btn-sm`);
        button.type = 'button';
        button.disabled = busy;
        button.setAttribute('aria-label', `${label.replace('…', '')} ${torrent.name}`);
        button.addEventListener('click', () => act(torrent, action));
        actions.append(button);
      }
      card.append(actions);
      list.append(card);
    }
  }

  function schedule() {
    clearTimeout(timer);
    if (!document.hidden) {
      timer = setTimeout(() => {
        // Don't replace a focused control while someone is using the panel.
        if (busy || list.contains(document.activeElement)) schedule();
        else refresh();
      }, 5000);
    }
  }

  async function refresh() {
    if (busy) return;
    clearTimeout(timer);
    controller?.abort();
    const current = ++generation;
    const request = new AbortController();
    controller = request;
    list.setAttribute('aria-busy', 'true');
    refreshButton.disabled = true;
    byId('torrent-status').textContent = 'Loading tagged torrents…';
    try {
      if (!configured) {
        const config = await api('/api/config', { signal: request.signal });
        if (current !== generation) return;
        destination.replaceChildren();
        for (const endpoint of config.endpoints || []) {
          const option = element('option', endpoint.name);
          option.value = endpoint.name;
          destination.append(option);
        }
        configured = true;
        destination.disabled = !destination.options.length;
      }
      if (!destination.options.length) {
        byId('torrent-status').textContent = 'No Transmission destinations configured.';
        return;
      }
      const data = await api(`/api/torrents?${new URLSearchParams({ endpoint: destination.value })}`, { signal: request.signal });
      if (current !== generation) return;
      torrents = data.torrents;
      showError(actionError);
      render();
    } catch (error) {
      if (current !== generation || error.name === 'AbortError') return;
      torrents = [];
      list.replaceChildren();
      byId('torrent-status').textContent = 'Could not load torrents. Use Refresh to retry.';
      showError(error.message);
    } finally {
      if (current === generation) {
        controller = null;
        list.setAttribute('aria-busy', 'false');
        refreshButton.disabled = busy;
        schedule();
      }
    }
  }

  async function act(torrent, action) {
    if (busy) return;
    if (action === 'remove' && !window.confirm(`Remove “${torrent.name}” from ${destination.value}? Downloaded files will be kept.`)) return;
    if (action === 'remove-delete' && !window.confirm(`Remove “${torrent.name}” from ${destination.value} AND DELETE its downloaded files on the Transmission host? This cannot be undone from ts. Other torrents using these files may be affected.`)) return;
    const endpoint = destination.value;
    controller?.abort();
    ++generation;
    clearTimeout(timer);
    setBusy(true);
    actionError = '';
    showError('');
    list.setAttribute('aria-busy', 'true');
    byId('torrent-status').textContent = `${action === 'start' ? 'Starting' : action === 'stop' ? 'Pausing' : 'Removing'} ${torrent.name}…`;
    try {
      await api('/api/torrents/action', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ endpoint, hash: torrent.hash, action }),
      });
    } catch (error) {
      actionError = error.message;
      showError(error.message);
    } finally {
      setBusy(false);
      list.setAttribute('aria-busy', 'false');
      await refresh();
    }
  }

  destination.addEventListener('change', () => {
    actionError = '';
    torrents = [];
    list.replaceChildren();
    showError('');
    refresh();
  });
  byId('torrent-filter').addEventListener('input', render);
  refreshButton.addEventListener('click', () => { actionError = ''; showError(''); refresh(); });
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) clearTimeout(timer);
    else refresh();
  });
  window.addEventListener('pagehide', () => {
    clearTimeout(timer);
    controller?.abort();
    ++generation;
  });
  window.addEventListener('pageshow', (event) => {
    if (event.persisted) refresh();
  });
  refresh();
})();
