(() => {
  const messagesEl = document.querySelector('#messages');
  const composer = document.querySelector('#composer');
  const input = document.querySelector('#message-input');
  const sendButton = document.querySelector('#send-button');
  const suggestions = document.querySelector('#suggestions');
  const sourceList = document.querySelector('#source-list');
  const evidencePanel = document.querySelector('#evidence-panel');
  const chatLayout = document.querySelector('.chat-layout');
  const toastEl = document.querySelector('#toast');

  const state = { chats: [], activeId: null };

  let toastTimer;
  let isSending = false;
  const expandedSourceIds = new Set();
  let focusedSourceId = null;

  function renderAssistantText(element, value) {
    const text = String(value || '').replace(/\\\*/g, '*');
    const boldPattern = /\*\*(.+?)\*\*/gs;
    let position = 0;

    for (const match of text.matchAll(boldPattern)) {
      element.append(document.createTextNode(text.slice(position, match.index)));
      const strong = document.createElement('strong');
      strong.textContent = match[1];
      element.append(strong);
      position = match.index + match[0].length;
    }

    element.append(document.createTextNode(text.slice(position)));
  }

  function createDraftChat() {
    return {
      id: 'draft-new-chat',
      title: 'Новый диалог',
      isDraft: true,
      messages: [welcomeMessage()]
    };
  }

  function welcomeMessage() {
    return {
      role: 'assistant',
      kind: 'welcome',
      content: 'Здравствуйте! Напишите вопрос по документам. Я покажу вывод и найденные подтверждающие фрагменты.',
      time: displayTime(),
      sources: []
    };
  }

  function activeChat() {
    return state.chats.find((chat) => chat.id === state.activeId) || state.chats[0];
  }

  function displayTime(value) {
    if (typeof value === 'string' && /^\d{2}:\d{2}$/.test(value)) return value;
    const date = value ? new Date(value) : new Date();
    if (Number.isNaN(date.getTime())) return '';
    return new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit' }).format(date);
  }

  function mapSource(source, messageId, index) {
    return {
      id: `${messageId || 'source'}:${source.doc_id || 'document'}:${source.fragment_id || index}:${index}`,
      doc_id: source.doc_id || '',
      title: source.doc_title || source.doc_id || 'Документ',
      section: source.fragment_id || 'Фрагмент документа',
      excerpt: source.text || ''
    };
  }

  function mapMessage(message) {
    return {
      id: message.id,
      role: message.role,
      kind: message.kind,
      content: message.content || '',
      created_at: message.created_at,
      sources: (message.sources || []).map((source, index) => mapSource(source, message.id, index))
    };
  }

  async function apiRequest(path, options = {}) {
    const headers = new Headers(options.headers || {});
    if (options.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');

    let response;
    try {
      response = await fetch(path, { ...options, headers, credentials: 'same-origin' });
    } catch (_) {
      throw new Error('Не удалось подключиться к backend. Проверьте, что Docker Compose запущен.');
    }

    let payload = null;
    if (response.status !== 204) {
      try {
        payload = await response.json();
      } catch (_) {
        payload = null;
      }
    }
    if (!response.ok) {
      throw new Error(payload?.error?.message || `Backend вернул ошибку HTTP ${response.status}.`);
    }
    return payload;
  }

  async function loadChat(chatId) {
    const result = await apiRequest(`/api/chats/${encodeURIComponent(chatId)}`);
    const chat = state.chats.find((item) => item.id === chatId);
    if (!chat) return;
    chat.title = result.chat.title || chat.title;
    const messages = (result.messages || []).map(mapMessage);
    chat.messages = messages.length ? [welcomeMessage(), ...messages] : [welcomeMessage()];
    state.activeId = chat.id;
    render();
  }

  async function createServerChat() {
    const result = await apiRequest('/api/chats', { method: 'POST' });
    const rawChat = result.chat;
    const chat = {
      id: rawChat.id,
      title: rawChat.title || 'Новый диалог',
      isDraft: false,
      messages: [welcomeMessage()]
    };
    state.chats = [chat, ...state.chats.filter((item) => item.id !== chat.id && !item.isDraft)];
    state.activeId = chat.id;
    return chat;
  }

  async function initialize() {
    const draft = createDraftChat();
    state.chats = [draft];
    state.activeId = draft.id;
    render();
    try {
      const result = await apiRequest('/api/chats?limit=50');
      const serverChats = (result.chats || []).map((chat) => ({
        id: chat.id,
        title: chat.title || 'Новый диалог',
        isDraft: false,
        messages: []
      }));
      state.chats = [draft, ...serverChats];
      state.activeId = draft.id;
      render();
    } catch (error) {
      state.chats = [draft];
      state.activeId = draft.id;
      render();
      showToast(error.message);
    }
  }

  function createSourceChip(source, index) {
    const chip = document.createElement('button');
    chip.type = 'button';
    chip.className = 'message-source';
    chip.dataset.sourceId = source.id;
    chip.setAttribute('aria-label', `Открыть материал: ${source.title}, ${source.section}`);
    const glyph = document.createElement('span');
    glyph.className = 'source-glyph';
    glyph.textContent = String(index + 1);
    const title = document.createElement('span');
    title.textContent = `${source.title} · ${source.section}`;
    chip.append(glyph, title);
    return chip;
  }

  function createMessage(message) {
    const row = document.createElement('article');
    row.className = `message-row ${message.role}${message.failed ? ' failed' : ''}`;

    const avatar = document.createElement('div');
    avatar.className = `message-avatar ${message.role === 'user' ? 'user-small' : ''}`;
    avatar.textContent = message.role === 'user' ? 'Вы' : 'П';

    const content = document.createElement('div');
    content.className = 'message-content';

    const meta = document.createElement('div');
    meta.className = 'message-meta';
    const name = document.createElement('strong');
    name.textContent = message.role === 'user' ? 'Вы' : 'Помощник юриста';
    const time = document.createElement('span');
    time.textContent = displayTime(message.created_at || message.time);
    meta.append(name, time);

    const bubble = document.createElement('div');
    bubble.className = 'message-bubble';
    const messageText = message.content || message.text || '';
    if (message.role === 'assistant') renderAssistantText(bubble, messageText);
    else bubble.textContent = messageText;
    content.append(meta, bubble);

    if (message.role === 'assistant' && message.kind !== 'welcome') {
      const noteText = {
        answer: message.sources?.length ? 'ОТВЕТ ПОДТВЕРЖДЁН ИСТОЧНИКОМ' : 'ОТВЕТ АССИСТЕНТА',
        clarification: 'НУЖНО УТОЧНЕНИЕ',
        refusal: 'ОТКАЗ С ОБОСНОВАНИЕМ',
        error: 'СЕРВИС НЕ ОТВЕТИЛ'
      }[message.kind] || (message.sources?.length ? 'ОТВЕТ ПОДТВЕРЖДЁН ИСТОЧНИКОМ' : 'ОТВЕТ АССИСТЕНТА');
      const note = document.createElement('div');
      note.className = 'message-note';
      note.textContent = noteText;
      content.append(note);

      if (Array.isArray(message.sources) && message.sources.length) {
        const sourceRow = document.createElement('div');
        sourceRow.className = 'message-sources';
        message.sources.forEach((source, index) => sourceRow.append(createSourceChip(source, index)));
        content.append(sourceRow);
      }

      const actions = document.createElement('div');
      actions.className = 'message-actions';
      const copy = document.createElement('button');
      copy.type = 'button';
      copy.className = 'message-action copy-message';
      copy.innerHTML = '<svg viewBox="0 0 16 16"><rect x="5" y="5" width="8" height="9" rx="1.5"></rect><path d="M10.5 5V3.8A1.3 1.3 0 0 0 9.2 2.5H4a1.5 1.5 0 0 0-1.5 1.5v6.2A1.3 1.3 0 0 0 3.8 11.5H5"></path></svg><span>Копировать</span>';
      copy.addEventListener('click', () => copyText(message.content || message.text || ''));
      actions.append(copy);
      content.append(actions);
    }

    if (message.role === 'user') row.append(content, avatar);
    else row.append(avatar, content);
    return row;
  }

  function renderHistory() {
    const list = document.querySelector('#history-list');
    list.replaceChildren();
    state.chats.forEach((chat) => {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = `history-item ${chat.id === state.activeId ? 'selected' : ''}`;
      button.dataset.chatId = chat.id;
      const dot = document.createElement('span');
      dot.className = 'history-dot';
      const title = document.createElement('span');
      title.className = 'history-title';
      title.textContent = chat.title;
      const more = document.createElement('span');
      more.className = 'history-more';
      more.textContent = '···';
      button.append(dot, title, more);
      button.addEventListener('click', async () => {
        if (chat.isDraft) {
          state.activeId = chat.id;
          render();
          return;
        }
        try {
          await loadChat(chat.id);
        } catch (error) {
          showToast(error.message);
        }
      });
      list.append(button);
    });
  }

  function renderEvidence(messages = activeChat().messages) {
    sourceList.replaceChildren();
    const sources = messages.flatMap((message) =>
      message.role === 'assistant' && Array.isArray(message.sources) ? message.sources : []
    );
    const statusTitle = document.querySelector('#evidence-status-title');
    const statusDetail = document.querySelector('#evidence-status-detail');

    if (!sources.length) {
      statusTitle.textContent = 'Источников пока нет';
      statusDetail.textContent = 'Новый ответ появится после запроса';
      const empty = document.createElement('div');
      empty.className = 'source-empty';
      const heading = document.createElement('strong');
      heading.textContent = 'Материалы ответа';
      const copy = document.createElement('span');
      copy.textContent = 'Когда ассистент найдёт подтверждающие положения, они появятся здесь.';
      empty.append(heading, copy);
      sourceList.append(empty);
      return;
    }

    statusTitle.textContent = 'Ответы подтверждены источниками';
    statusDetail.textContent = `${sources.length} источника в диалоге`;
    sources.forEach((source) => {
      const isExpanded = expandedSourceIds.has(source.id);
      const card = document.createElement('div');
      card.className = `source-card ${focusedSourceId === source.id ? 'focused' : ''}`;
      card.dataset.sourceId = source.id;
      const head = document.createElement('div');
      head.className = 'source-card-head';
      const item = document.createElement('button');
      item.type = 'button';
      item.className = 'source-item-button';
      item.setAttribute('aria-expanded', String(isExpanded));
      item.setAttribute('aria-label', `Показать выдержку: ${source.title}`);
      const title = document.createElement('div');
      title.className = 'source-card-title';
      title.textContent = source.title;
      item.append(title);
      item.addEventListener('click', () => {
        if (expandedSourceIds.has(source.id)) expandedSourceIds.delete(source.id);
        else expandedSourceIds.add(source.id);
        focusedSourceId = source.id;
        renderEvidence(messages);
      });
      head.append(item);
      const hasDocument = Boolean(source.doc_id);
      const documentLink = document.createElement(hasDocument ? 'a' : 'button');
      documentLink.className = 'source-download-button';
      documentLink.innerHTML = '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 2.5v7m0 0 2.5-2.5M8 9.5 5.5 7M3 10.5v2A1.5 1.5 0 0 0 4.5 14h7a1.5 1.5 0 0 0 1.5-1.5v-2"/></svg>';
      if (hasDocument) {
        documentLink.href = `/api/documents/${encodeURIComponent(source.doc_id)}`;
        documentLink.setAttribute('download', '');
        documentLink.setAttribute('aria-label', `Скачать документ ${source.title} в DOCX`);
        documentLink.title = 'Скачать DOCX';
      } else {
        documentLink.type = 'button';
        documentLink.disabled = true;
        documentLink.setAttribute('aria-label', `Не удалось скачать ${source.title}: ссылка на файл отсутствует`);
        documentLink.title = 'Ссылка на файл отсутствует';
      }
      head.append(documentLink);
      card.append(head);
      if (isExpanded) {
        const section = document.createElement('div');
        section.className = 'source-tag';
        section.textContent = source.section;
        const excerpt = document.createElement('p');
        excerpt.className = 'source-excerpt';
        excerpt.textContent = source.excerpt;
        const previewActions = document.createElement('div');
        previewActions.className = 'source-preview-actions';
        previewActions.append(documentLink);
        card.append(section, excerpt, previewActions);
      }
      sourceList.append(card);
    });
  }

  function render() {
    const chat = activeChat();
    messagesEl.replaceChildren();
    const divider = document.createElement('div');
    divider.className = 'day-divider';
    divider.textContent = 'Сегодня';
    messagesEl.append(divider);
    chat.messages.forEach((message) => messagesEl.append(createMessage(message)));
    suggestions.hidden = chat.messages.length > 2;
    renderHistory();
    renderEvidence(chat.messages);
    messagesEl.scrollTop = messagesEl.scrollHeight;
    document.querySelector('.chat-title-group strong').textContent = chat.title;
  }

  function showToast(text) {
    toastEl.textContent = text;
    toastEl.classList.add('visible');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => toastEl.classList.remove('visible'), 3200);
  }

  async function copyText(text) {
    try {
      await navigator.clipboard.writeText(text);
      showToast('Скопировано');
    } catch (_) {
      showToast('Не удалось скопировать текст');
    }
  }

  async function sendToAssistant(chatId, text) {
    return apiRequest(`/api/chats/${encodeURIComponent(chatId)}/messages`, {
      method: 'POST',
      body: JSON.stringify({ text })
    });
  }

  function showTyping() {
    const row = document.createElement('div');
    row.className = 'message-row typing-row';
    row.id = 'typing-indicator';
    const avatar = document.createElement('div');
    avatar.className = 'message-avatar';
    avatar.textContent = 'П';
    const content = document.createElement('div');
    content.className = 'message-content';
    const bubble = document.createElement('div');
    bubble.className = 'message-bubble';
    for (let index = 0; index < 3; index += 1) {
      const dot = document.createElement('span');
      dot.className = 'typing-dot';
      bubble.append(dot);
    }
    content.append(bubble);
    row.append(avatar, content);
    messagesEl.append(row);
    messagesEl.scrollTop = messagesEl.scrollHeight;
  }

  async function submitMessage(rawText) {
    const text = rawText.trim();
    if (!text || isSending) return;
    isSending = true;
    input.value = '';
    input.style.height = 'auto';
    sendButton.disabled = true;

    let chat = activeChat();
    let pendingMessage = null;
    try {
      if (chat.isDraft) chat = await createServerChat();
      pendingMessage = { role: 'user', kind: 'text', content: text, time: displayTime(), pending: true };
      chat.messages.push(pendingMessage);
      render();
      showTyping();
      suggestions.hidden = true;

      const result = await sendToAssistant(chat.id, text);
      document.querySelector('#typing-indicator')?.remove();
      const pendingIndex = chat.messages.indexOf(pendingMessage);
      if (pendingIndex >= 0) {
        chat.messages.splice(pendingIndex, 1, mapMessage(result.user_message), mapMessage(result.assistant_message));
      }

      try {
        const updated = await apiRequest(`/api/chats/${encodeURIComponent(chat.id)}`);
        chat.title = updated.chat.title || chat.title;
      } catch (_) {
        chat.title = text.length > 40 ? `${text.slice(0, 37).trim()}…` : text;
      }
      state.chats = [chat, ...state.chats.filter((item) => item.id !== chat.id)];
      state.activeId = chat.id;
      render();
    } catch (error) {
      document.querySelector('#typing-indicator')?.remove();
      if (pendingMessage && chat && !chat.isDraft) {
        pendingMessage.failed = true;
        chat.messages.push({
          role: 'assistant',
          kind: 'error',
          content: error.message || 'Не удалось получить ответ. Попробуйте ещё раз.',
          time: displayTime(),
          sources: []
        });
        render();
      } else {
        showToast(error.message || 'Не удалось создать чат.');
      }
    } finally {
      isSending = false;
      sendButton.disabled = !input.value.trim();
      input.focus();
    }
  }

  async function startNewChat() {
    try {
      await createServerChat();
      render();
      input.focus();
    } catch (error) {
      showToast(error.message);
    }
  }

  composer.addEventListener('submit', (event) => {
    event.preventDefault();
    submitMessage(input.value);
  });

  input.addEventListener('input', () => {
    sendButton.disabled = !input.value.trim() || isSending;
    input.style.height = 'auto';
    input.style.height = `${Math.min(input.scrollHeight, 104)}px`;
  });

  input.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      composer.requestSubmit();
    }
  });

  document.querySelectorAll('.suggestion-chip').forEach((button) => {
    button.addEventListener('click', () => submitMessage(button.textContent));
  });

  document.querySelector('#new-chat').addEventListener('click', startNewChat);

  document.querySelector('#show-sources').addEventListener('click', () => {
    evidencePanel.classList.remove('collapsed');
    chatLayout.classList.remove('no-evidence');
    if (window.matchMedia('(max-width: 840px)').matches) evidencePanel.classList.add('mobile-open');
    evidencePanel.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  });

  document.querySelector('#close-evidence').addEventListener('click', () => {
    evidencePanel.classList.add('collapsed');
    evidencePanel.classList.remove('mobile-open');
    chatLayout.classList.add('no-evidence');
  });

  messagesEl.addEventListener('click', (event) => {
    const sourceChip = event.target.closest('.message-source');
    if (!sourceChip) return;

    focusedSourceId = sourceChip.dataset.sourceId;
    evidencePanel.classList.remove('collapsed');
    chatLayout.classList.remove('no-evidence');
    if (window.matchMedia('(max-width: 840px)').matches) evidencePanel.classList.add('mobile-open');
    renderEvidence();
    const card = [...sourceList.querySelectorAll('.source-card')]
      .find((item) => item.dataset.sourceId === focusedSourceId);
    card?.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  });

  document.querySelector('#dismiss-notice').addEventListener('click', () => {
    document.querySelector('.context-notice').remove();
  });

  document.querySelector('#attach-button').addEventListener('click', () => showToast('Загрузка документов пока недоступна'));
  document.querySelector('#chat-menu').addEventListener('click', () => showToast('Дополнительные действия пока недоступны'));
  document.querySelector('.help-button').addEventListener('click', () => showToast('Для поиска по договору укажите его номер, контрагента и дату'));
  document.querySelector('#copy-link').addEventListener('click', () => copyText(window.location.href));

  document.addEventListener('keydown', (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
      event.preventDefault();
      document.querySelector('#new-chat').click();
    }
    if (event.key === 'Escape' && !evidencePanel.classList.contains('collapsed')) {
      evidencePanel.classList.add('collapsed');
      evidencePanel.classList.remove('mobile-open');
      chatLayout.classList.add('no-evidence');
    }
  });

  initialize();
})();
