(() => {
  const exampleId = 'example-conversation';
  const exampleMessages = [
    {
      role: 'user',
      content: 'Какая арендная плата по договору с ООО «Северный Склад» и до какого числа её нужно вносить?',
      time: '10:40'
    },
    {
      role: 'assistant',
      kind: 'answer',
      content: 'Арендная плата — 450 000 руб. в месяц, включая НДС (п. 3.1 договора аренды А-08/2024). Вносится ежемесячно, не позднее 5-го числа текущего месяца (п. 3.2).',
      time: '10:40',
      sources: [{
        id: 'lease-a-08-2024',
        doc_id: 'lease-a-08-2024',
        title: 'Договор аренды А-08/2024',
        section: 'п. 3.1, 3.2',
        excerpt: 'Арендная плата — 450 000 руб. в месяц, включая НДС. Вносится ежемесячно, не позднее 5-го числа текущего месяца.'
      }]
    },
    {
      role: 'user',
      content: 'Мы нанимаем человека только на сезонные работы летом. Можно оформить срочный трудовой договор?',
      time: '10:42'
    },
    {
      role: 'assistant',
      kind: 'answer',
      content: 'Да, срочный договор допускается для сезонных работ, когда по природным условиям работа может выполняться только в течение определённого периода (сезона) (ст. 59 ТК РФ, ч. 1). Нужно, чтобы работа действительно была сезонной по природным условиям. В тексте есть пометка об изменении статьи с 01.03.2027.',
      time: '10:42',
      sources: [{
        id: 'tk-59',
        doc_id: 'tk-59',
        title: 'ТК РФ',
        section: 'ст. 59',
        excerpt: 'Срочный трудовой договор допускается для сезонных работ, если по природным условиям работу можно выполнять только в течение определённого периода (сезона).'
      }]
    }
  ];

  const messagesEl = document.querySelector('#messages');
  const composer = document.querySelector('#composer');
  const input = document.querySelector('#message-input');
  const sendButton = document.querySelector('#send-button');
  const suggestions = document.querySelector('#suggestions');
  const sourceList = document.querySelector('#source-list');
  const evidencePanel = document.querySelector('#evidence-panel');
  const chatLayout = document.querySelector('.chat-layout');
  const toastEl = document.querySelector('#toast');

  const state = {
    chats: [createExampleChat()],
    activeId: exampleId
  };

  let toastTimer;
  let isSending = false;
  let selectedSourceId = null;

  function cloneMessage(message) {
    return { ...message, sources: (message.sources || []).map((source) => ({ ...source })) };
  }

  function createExampleChat() {
    return {
      id: exampleId,
      title: 'Аренда и сезонные работы',
      isExample: true,
      messages: exampleMessages.map(cloneMessage)
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
    selectedSourceId = null;
    render();
  }

  async function createServerChat() {
    const result = await apiRequest('/api/chats', { method: 'POST' });
    const rawChat = result.chat;
    const chat = {
      id: rawChat.id,
      title: rawChat.title || 'Новый диалог',
      isExample: false,
      messages: [welcomeMessage()]
    };
    state.chats = [chat, ...state.chats.filter((item) => item.id !== chat.id)];
    state.activeId = chat.id;
    selectedSourceId = null;
    return chat;
  }

  async function initialize() {
    render();
    try {
      const result = await apiRequest('/api/chats?limit=50');
      const serverChats = (result.chats || []).map((chat) => ({
        id: chat.id,
        title: chat.title || 'Новый диалог',
        isExample: false,
        messages: []
      }));
      state.chats = [...serverChats, createExampleChat()];
      if (serverChats.length) await loadChat(serverChats[0].id);
      else state.activeId = exampleId;
      render();
    } catch (error) {
      state.chats = [createExampleChat()];
      state.activeId = exampleId;
      render();
      showToast(error.message);
    }
  }

  function createSourceChip(source, index) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'message-source';
    button.dataset.sourceId = source.id;
    const glyph = document.createElement('span');
    glyph.className = 'source-glyph';
    glyph.textContent = String(index + 1);
    const title = document.createElement('span');
    title.textContent = `${source.title} · ${source.section}`;
    button.append(glyph, title);
    return button;
  }

  function createMessage(message) {
    const row = document.createElement('article');
    row.className = `message-row ${message.role}${message.failed ? ' failed' : ''}`;

    const avatar = document.createElement('div');
    avatar.className = `message-avatar ${message.role === 'user' ? 'user-small' : ''}`;
    avatar.textContent = message.role === 'user' ? 'АК' : 'П';

    const content = document.createElement('div');
    content.className = 'message-content';

    const meta = document.createElement('div');
    meta.className = 'message-meta';
    const name = document.createElement('strong');
    name.textContent = message.role === 'user' ? 'Анна К.' : 'Помощник юриста';
    const time = document.createElement('span');
    time.textContent = displayTime(message.created_at || message.time);
    meta.append(name, time);

    const bubble = document.createElement('div');
    bubble.className = 'message-bubble';
    bubble.textContent = message.content || message.text || '';
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
        if (chat.isExample) {
          state.activeId = exampleId;
          selectedSourceId = null;
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
    const selected = sources.find((source) => source.id === selectedSourceId) || sources[sources.length - 1];
    sources.forEach((source) => {
      const card = document.createElement('div');
      card.className = `source-card ${source.id === selected.id ? 'selected' : ''}`;
      const head = document.createElement('div');
      head.className = 'source-card-head';
      const icon = document.createElement('span');
      icon.className = 'source-file-icon';
      icon.textContent = '§';
      const title = document.createElement('div');
      title.className = 'source-card-title';
      title.textContent = source.title;
      const tag = document.createElement('div');
      tag.className = 'source-tag';
      tag.textContent = source.section;
      title.append(tag);
      head.append(icon, title);
      const excerpt = document.createElement('p');
      excerpt.className = 'source-excerpt';
      excerpt.textContent = source.id === selected.id ? source.excerpt : 'Нажмите, чтобы открыть фрагмент.';
      card.append(head, excerpt);
      card.addEventListener('click', () => {
        selectedSourceId = source.id;
        renderEvidence(messages);
      });
      sourceList.append(card);
    });
  }

  function render() {
    const chat = activeChat();
    messagesEl.replaceChildren();
    const divider = document.createElement('div');
    divider.className = 'day-divider';
    divider.textContent = chat.isExample ? 'Пример диалога' : 'Сегодня';
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
      if (chat.isExample) chat = await createServerChat();
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
      selectedSourceId = null;
      render();
    } catch (error) {
      document.querySelector('#typing-indicator')?.remove();
      if (pendingMessage && chat && !chat.isExample) {
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

  document.querySelector('#messages').addEventListener('click', (event) => {
    const sourceButton = event.target.closest('.message-source');
    if (sourceButton) {
      selectedSourceId = sourceButton.dataset.sourceId;
      evidencePanel.classList.remove('collapsed');
      chatLayout.classList.remove('no-evidence');
      if (window.matchMedia('(max-width: 840px)').matches) evidencePanel.classList.add('mobile-open');
      renderEvidence();
    }
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
