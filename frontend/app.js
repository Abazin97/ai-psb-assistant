(() => {
  const STORAGE_KEY = 'psb-legal-assistant-chat-v5';

  const leaseSource = {
    id: 'lease-a-08-2024',
    title: 'Договор аренды А-08/2024',
    section: 'п. 3.1, 3.2',
    excerpt: 'Арендная плата — 450 000 руб. в месяц, включая НДС. Вносится ежемесячно, не позднее 5-го числа текущего месяца.'
  };

  const laborSource = {
    id: 'tk-59',
    title: 'ТК РФ',
    section: 'ст. 59',
    excerpt: 'Срочный трудовой договор допускается для сезонных работ, если по природным условиям работу можно выполнять только в течение определённого периода (сезона).'
  };

  const initialMessages = [
    {
      role: 'user',
      text: 'Какая арендная плата по договору с ООО «Северный Склад» и до какого числа её нужно вносить?',
      time: '10:40'
    },
    {
      role: 'assistant',
      text: 'Арендная плата — 450 000 руб. в месяц, включая НДС (п. 3.1 договора аренды А-08/2024). Вносится ежемесячно, не позднее 5-го числа текущего месяца (п. 3.2).',
      time: '10:40',
      sources: [leaseSource]
    },
    {
      role: 'user',
      text: 'Мы нанимаем человека только на сезонные работы летом. Можно оформить срочный трудовой договор?',
      time: '10:42'
    },
    {
      role: 'assistant',
      text: 'Да, срочный договор допускается для сезонных работ, когда по природным условиям работа может выполняться только в течение определённого периода (сезона) (ст. 59 ТК РФ, ч. 1). Нужно, чтобы работа действительно была сезонной по природным условиям. В тексте есть пометка об изменении статьи с 01.03.2027.',
      time: '10:42',
      sources: [laborSource]
    }
  ];

  const clarificationResponses = [
    'Чтобы проверить условие, уточните номер договора и дату, на которую нужен ответ. Я сопоставлю основной текст с дополнительными соглашениями и приведу точный пункт.',
    'Уточните, пожалуйста, контрагента и дату события. Это поможет выбрать применимую редакцию документа и не смешать условия разных договоров.',
    'Для точного вывода нужен идентификатор документа: номер договора или дата подписания. После уточнения укажу применимое положение и связанные изменения.'
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

  function makeId() {
    return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`;
  }

  function starterChat() {
    return {
      id: makeId(),
      title: 'Аренда и сезонные работы',
      messages: structuredClone(initialMessages)
    };
  }

  function loadState() {
    try {
      const saved = JSON.parse(localStorage.getItem(STORAGE_KEY));
      if (saved && Array.isArray(saved.chats) && saved.chats.length) {
        const chats = saved.chats.filter((chat) => chat && Array.isArray(chat.messages));
        if (chats.length) {
          const activeId = chats.some((chat) => chat.id === saved.activeId) ? saved.activeId : chats[0].id;
          return { chats: chats.slice(0, 10), activeId };
        }
      }
    } catch (_) {
      // A blocked or malformed localStorage should not prevent the chat from opening.
    }
    const firstChat = starterChat();
    return { chats: [firstChat], activeId: firstChat.id };
  }

  let state = loadState();
  let responseIndex = 0;
  let toastTimer;
  let isSending = false;
  let selectedSourceId = null;

  function activeChat() {
    return state.chats.find((chat) => chat.id === state.activeId) || state.chats[0];
  }

  function saveState() {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
    } catch (_) {
      // The current conversation remains usable even when storage is unavailable.
    }
  }

  function displayTime(value) {
    return value || new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit' }).format(new Date());
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
    row.className = `message-row ${message.role}`;

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
    time.textContent = displayTime(message.time);
    meta.append(name, time);

    const bubble = document.createElement('div');
    bubble.className = 'message-bubble';
    bubble.textContent = message.text;
    content.append(meta, bubble);

    if (message.role === 'assistant') {
      const note = document.createElement('div');
      note.className = 'message-note';
      note.textContent = message.sources?.length ? 'ОТВЕТ ПОДТВЕРЖДЁН ИСТОЧНИКОМ' : 'УТОЧНЕНИЕ ПЕРЕД ПРОВЕРКОЙ';
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
      copy.addEventListener('click', () => copyText(message.text));
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
      button.addEventListener('click', () => {
        state.activeId = chat.id;
        selectedSourceId = null;
        saveState();
        render();
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
      statusTitle.textContent = 'Нужны реквизиты документа';
      statusDetail.textContent = 'Укажите номер договора или дату';
      const empty = document.createElement('div');
      empty.className = 'source-empty';
      const heading = document.createElement('strong');
      heading.textContent = 'Источники ответа';
      const copy = document.createElement('span');
      copy.textContent = 'Уточните номер договора или дату подписания. После этого здесь появятся найденные документы и подтверждающие фрагменты.';
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
    divider.textContent = 'Сегодня';
    messagesEl.append(divider);
    chat.messages.forEach((message) => messagesEl.append(createMessage(message)));
    suggestions.hidden = chat.messages.length > 2;
    document.querySelector('.history-item.selected')?.classList.remove('selected');
    document.querySelector(`[data-chat-id="${CSS.escape(chat.id)}"]`)?.classList.add('selected');
    renderHistory();
    renderEvidence(chat.messages);
    messagesEl.scrollTop = messagesEl.scrollHeight;
    document.querySelector('.chat-title-group strong').textContent = chat.title;
  }

  function showToast(text) {
    toastEl.textContent = text;
    toastEl.classList.add('visible');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => toastEl.classList.remove('visible'), 2400);
  }

  async function copyText(text) {
    try {
      await navigator.clipboard.writeText(text);
      showToast('Скопировано');
    } catch (_) {
      showToast('Не удалось скопировать текст');
    }
  }

  // Future backend integration point: replace this local reply with POST /api/chat.
  async function sendToAssistant(_message, _history) {
    await new Promise((resolve) => setTimeout(resolve, 650));
    const text = clarificationResponses[responseIndex % clarificationResponses.length];
    responseIndex += 1;
    return { text, sources: [] };
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
    const chat = activeChat();
    const chatId = chat.id;
    chat.messages.push({ role: 'user', text, time: displayTime() });
    if (chat.title === 'Новый диалог' || chat.title === 'Условия оплаты по договору') {
      chat.title = text.length > 29 ? `${text.slice(0, 27).trim()}…` : text;
    }
    saveState();
    render();
    input.value = '';
    input.style.height = 'auto';
    sendButton.disabled = true;
    showTyping();
    suggestions.hidden = true;

    try {
      // This transcript is passed to the future backend hook along with the newest question.
      const history = chat.messages.slice(0, -1).map(({ role, text: content }) => ({ role, content }));
      const result = await sendToAssistant(text, history);
      document.querySelector('#typing-indicator')?.remove();
      const targetChat = state.chats.find((item) => item.id === chatId);
      if (targetChat) {
        targetChat.messages.push({ role: 'assistant', text: result.text, time: displayTime(), sources: result.sources });
        saveState();
      }
      if (activeChat().id === chatId) render();
    } catch (_) {
      document.querySelector('#typing-indicator')?.remove();
      const targetChat = state.chats.find((item) => item.id === chatId);
      if (targetChat) {
        targetChat.messages.push({
          role: 'assistant',
          text: 'Не удалось подготовить ответ. Попробуйте повторить запрос.',
          time: displayTime(),
          sources: []
        });
        saveState();
      }
      if (activeChat().id === chatId) render();
    } finally {
      isSending = false;
      sendButton.disabled = !input.value.trim();
      input.focus();
    }
  }

  composer.addEventListener('submit', (event) => {
    event.preventDefault();
    submitMessage(input.value);
  });

  input.addEventListener('input', () => {
    sendButton.disabled = !input.value.trim();
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

  document.querySelector('#new-chat').addEventListener('click', () => {
    const chat = { id: makeId(), title: 'Новый диалог', messages: [{
      role: 'assistant',
      text: 'Здравствуйте! Укажите вопрос и, если он касается договора, его номер или дату. Я помогу найти нужное положение и проверить связанные изменения.',
      time: displayTime(),
      sources: []
    }] };
    state.chats.unshift(chat);
    state.chats = state.chats.slice(0, 10);
    state.activeId = chat.id;
    selectedSourceId = null;
    saveState();
    render();
    input.focus();
  });

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

  render();
})();
