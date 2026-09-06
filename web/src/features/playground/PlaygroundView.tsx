import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { User } from '../../api';
import {
  PLAYGROUND_LIMITS,
  PlaygroundContractError,
  buildPlaygroundRequest,
  loadPlaygroundGroups,
  loadPlaygroundModels,
  streamPlaygroundCompletion,
  type PlaygroundConversationTurn,
  type PlaygroundFetch,
  type PlaygroundGroup,
  type PlaygroundParameters,
} from './playground-api';
import './playground.css';

type LoadState<T> =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'error' }
  | { kind: 'ready'; value: T };

type MessageStatus = 'complete' | 'streaming' | 'stopped' | 'error';

interface PresentedMessage {
  id: string;
  role: 'user' | 'assistant';
  content: string;
  reasoning: string;
  status: MessageStatus;
}

interface ParameterDraft {
  temperature: string;
  topP: string;
  maxTokens: string;
  frequencyPenalty: string;
  presencePenalty: string;
  seed: string;
}

const DEFAULT_PARAMETERS: ParameterDraft = {
  temperature: '0.7',
  topP: '1',
  maxTokens: '4096',
  frequencyPenalty: '0',
  presencePenalty: '0',
  seed: '',
};

const MAX_PARAMETER_CHARACTERS = 32;
const DECIMAL_PATTERN = /^-?(?:0|[1-9]\d*)(?:\.\d+)?$/u;
const INTEGER_PATTERN = /^-?(?:0|[1-9]\d*)$/u;

export interface PlaygroundViewProps {
  user: User;
  onNavigate: (target: string) => void;
  fetcher?: PlaygroundFetch;
  moduleEnabled?: boolean;
}

function validAuthenticatedUser(user: User): boolean {
  return Number.isSafeInteger(user.id) && user.id > 0
    && Number.isSafeInteger(user.role) && user.role >= 1;
}

function safeAccountName(user: User): string {
  const candidate = typeof user.display_name === 'string' && user.display_name.trim()
    ? user.display_name
    : user.username;
  if (typeof candidate !== 'string') return '';
  let result = '';
  for (const character of candidate) {
    if (result.length + character.length > 128) break;
    const code = character.codePointAt(0) ?? 0;
    const bidiControl = code === 0x061c || code === 0x200e || code === 0x200f
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069);
    if (!bidiControl && ((code > 0x1f && code < 0x7f) || code > 0x9f)) result += character;
  }
  return result.trim();
}

function numericDraft(value: string, minimum: number, maximum: number, integer = false): number {
  if (value.length === 0 || value.length > MAX_PARAMETER_CHARACTERS || value.trim() !== value
      || !(integer ? INTEGER_PATTERN : DECIMAL_PATTERN).test(value)) {
    throw new PlaygroundContractError('invalid-input');
  }
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < minimum || parsed > maximum
      || (integer && !Number.isSafeInteger(parsed))) {
    throw new PlaygroundContractError('invalid-input');
  }
  return parsed;
}

function parametersFromDraft(draft: ParameterDraft): PlaygroundParameters {
  return {
    temperature: numericDraft(draft.temperature, 0, 2),
    topP: numericDraft(draft.topP, 0, 1),
    maxTokens: numericDraft(draft.maxTokens, 1, 32_768, true),
    frequencyPenalty: numericDraft(draft.frequencyPenalty, -2, 2),
    presencePenalty: numericDraft(draft.presencePenalty, -2, 2),
    seed: draft.seed === '' ? null : numericDraft(draft.seed, -2_147_483_648, 2_147_483_647, true),
  };
}

function requestErrorMessage(error: unknown): string {
  if (!(error instanceof PlaygroundContractError)) return 'Unable to complete the playground request.';
  if (error.status === 401) return 'Your session expired. Sign in again.';
  if (error.status === 403) return 'This group is not available to your account.';
  if (error.status === 429) return 'The playground is busy. Try again shortly.';
  if (error.code === 'response-too-large') return 'The playground response was too large.';
  if (error.code === 'truncated') return 'The response ended before generation completed.';
  return 'Unable to complete the playground request.';
}

function completedConversation(messages: PresentedMessage[]): PlaygroundConversationTurn[] {
  return messages
    .filter((message) => message.status === 'complete' && message.content !== '')
    .map((message) => ({ role: message.role, content: message.content }))
    .slice(-PLAYGROUND_LIMITS.conversationTurns);
}

function updateMessage(
  messages: PresentedMessage[],
  id: string,
  update: (message: PresentedMessage) => PresentedMessage,
): PresentedMessage[] {
  return messages.map((message) => message.id === id ? update(message) : message);
}

export function PlaygroundView({
  user,
  onNavigate,
  fetcher = fetch,
  moduleEnabled = true,
}: PlaygroundViewProps) {
  const { t } = useTranslation();
  const authenticated = validAuthenticatedUser(user);
  const accountName = safeAccountName(user);
  const [groupsState, setGroupsState] = useState<LoadState<PlaygroundGroup[]>>({ kind: 'idle' });
  const [modelsState, setModelsState] = useState<LoadState<string[]>>({ kind: 'idle' });
  const [groupsAttempt, setGroupsAttempt] = useState(0);
  const [modelsAttempt, setModelsAttempt] = useState(0);
  const [group, setGroup] = useState('');
  const [model, setModel] = useState('');
  const [systemPrompt, setSystemPrompt] = useState('');
  const [composer, setComposer] = useState('');
  const [parameterDraft, setParameterDraft] = useState<ParameterDraft>(DEFAULT_PARAMETERS);
  const [messages, setMessages] = useState<PresentedMessage[]>([]);
  const [inputError, setInputError] = useState(false);
  const [requestError, setRequestError] = useState('');
  const [generating, setGenerating] = useState(false);
  const groupGeneration = useRef(0);
  const modelGeneration = useRef(0);
  const requestGeneration = useRef(0);
  const requestController = useRef<AbortController | null>(null);
  const activeAssistantID = useRef('');
  const messageSequence = useRef(0);

  useEffect(() => {
    if (!authenticated) onNavigate('/sign-in');
    else if (!moduleEnabled) onNavigate('/dashboard');
  }, [authenticated, moduleEnabled, onNavigate]);

  useEffect(() => {
    if (!authenticated || !moduleEnabled) return undefined;
    const controller = new AbortController();
    const generation = groupGeneration.current + 1;
    groupGeneration.current = generation;
    setGroupsState({ kind: 'loading' });
    void loadPlaygroundGroups(controller.signal, fetcher)
      .then((groups) => {
        if (controller.signal.aborted || groupGeneration.current !== generation) return;
        setGroupsState({ kind: 'ready', value: groups });
        setGroup((current) => {
          if (groups.some((candidate) => candidate.id === current)) return current;
          const own = typeof user.group === 'string'
            ? groups.find((candidate) => candidate.id === user.group)
            : undefined;
          return groups.find((candidate) => candidate.id === 'default')?.id
            ?? own?.id
            ?? groups[0]?.id
            ?? '';
        });
      })
      .catch(() => {
        if (controller.signal.aborted || groupGeneration.current !== generation) return;
        setGroupsState({ kind: 'error' });
        setGroup('');
        setModelsState({ kind: 'idle' });
        setModel('');
      });
    return () => controller.abort();
  }, [authenticated, fetcher, groupsAttempt, moduleEnabled, user.group]);

  useEffect(() => {
    if (!authenticated || !moduleEnabled || groupsState.kind !== 'ready' || group === '') {
      setModelsState({ kind: 'idle' });
      setModel('');
      return undefined;
    }
    const controller = new AbortController();
    const generation = modelGeneration.current + 1;
    modelGeneration.current = generation;
    setModelsState({ kind: 'loading' });
    setModel('');
    void loadPlaygroundModels(group, controller.signal, fetcher)
      .then((models) => {
        if (controller.signal.aborted || modelGeneration.current !== generation) return;
        setModelsState({ kind: 'ready', value: models });
        setModel(models[0] ?? '');
      })
      .catch(() => {
        if (controller.signal.aborted || modelGeneration.current !== generation) return;
        setModelsState({ kind: 'error' });
        setModel('');
      });
    return () => controller.abort();
  }, [authenticated, fetcher, group, groupsState.kind, modelsAttempt, moduleEnabled]);

  useEffect(() => () => {
    requestGeneration.current += 1;
    requestController.current?.abort();
    requestController.current = null;
  }, []);

  const activeGroup = useMemo(
    () => groupsState.kind === 'ready'
      ? groupsState.value.find((candidate) => candidate.id === group)
      : undefined,
    [group, groupsState],
  );
  const activeGroupDescription = activeGroup?.description
    || (activeGroup?.automatic ? t('Automatic routing') : '');

  const optionsReady = groupsState.kind === 'ready' && groupsState.value.length > 0
    && modelsState.kind === 'ready' && modelsState.value.length > 0;

  function setParameter<Key extends keyof ParameterDraft>(key: Key, value: ParameterDraft[Key]) {
    if (value.length <= MAX_PARAMETER_CHARACTERS) {
      setParameterDraft((current) => ({ ...current, [key]: value }));
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (generating || !optionsReady) return;
    let payload;
    try {
      payload = buildPlaygroundRequest({
        model,
        group,
        systemPrompt,
        conversation: completedConversation(messages),
        message: composer,
        parameters: parametersFromDraft(parameterDraft),
      });
    } catch {
      setInputError(true);
      return;
    }

    setInputError(false);
    setRequestError('');
    const messageText = composer;
    setComposer('');
    const userID = `playground-user-${messageSequence.current + 1}`;
    messageSequence.current += 1;
    const assistantID = `playground-assistant-${messageSequence.current + 1}`;
    messageSequence.current += 1;
    const requestMessages: PresentedMessage[] = [
      { id: userID, role: 'user', content: messageText, reasoning: '', status: 'complete' },
      { id: assistantID, role: 'assistant', content: '', reasoning: '', status: 'streaming' },
    ];
    setMessages((current) => [...current, ...requestMessages].slice(-64));

    const generation = requestGeneration.current + 1;
    requestGeneration.current = generation;
    requestController.current?.abort();
    const controller = new AbortController();
    requestController.current = controller;
    activeAssistantID.current = assistantID;
    setGenerating(true);

    try {
      await streamPlaygroundCompletion(payload, {
        onContent: (chunk) => {
          if (controller.signal.aborted || requestGeneration.current !== generation) return;
          setMessages((current) => updateMessage(current, assistantID, (message) => ({
            ...message,
            content: message.content + chunk,
          })));
        },
        onReasoning: (chunk) => {
          if (controller.signal.aborted || requestGeneration.current !== generation) return;
          setMessages((current) => updateMessage(current, assistantID, (message) => ({
            ...message,
            reasoning: message.reasoning + chunk,
          })));
        },
      }, controller.signal, fetcher);
      if (controller.signal.aborted || requestGeneration.current !== generation) return;
      setMessages((current) => updateMessage(current, assistantID, (message) => ({
        ...message,
        status: 'complete',
      })));
    } catch (error) {
      if (controller.signal.aborted || requestGeneration.current !== generation) return;
      setRequestError(requestErrorMessage(error));
      setMessages((current) => updateMessage(current, assistantID, (message) => ({
        ...message,
        status: 'error',
      })));
    } finally {
      if (requestGeneration.current === generation) {
        requestController.current = null;
        activeAssistantID.current = '';
        setGenerating(false);
      }
    }
  }

  function stopGeneration() {
    if (!generating) return;
    requestGeneration.current += 1;
    requestController.current?.abort();
    requestController.current = null;
    const assistantID = activeAssistantID.current;
    activeAssistantID.current = '';
    setMessages((current) => updateMessage(current, assistantID, (message) => ({
      ...message,
      status: 'stopped',
    })));
    setGenerating(false);
  }

  function clearConversation() {
    if (generating) return;
    setMessages([]);
    setRequestError('');
    setInputError(false);
  }

  if (!authenticated) {
    return (
      <main className="app playground-page" aria-label={t('API playground')}>
        <p className="muted">{t('Returning to sign in…')}</p>
      </main>
    );
  }

  if (!moduleEnabled) {
    return (
      <main className="app playground-page" aria-label={t('API playground')}>
        <p className="muted">{t('Returning to dashboard…')}</p>
      </main>
    );
  }

  return (
    <main className="app playground-page" aria-label={t('API playground')}>
      <header className="header row playground-heading">
        <div>
          <p className="playground-eyebrow">{t('API playground')}</p>
          <h1>{t('Playground')}</h1>
          <p className="tagline">{t('Test an available model through your authenticated gateway session.')}</p>
          <p className="playground-account">
            {user.role >= 10 ? t('Administrator account') : t('Your account')}
            {accountName ? ` · ${accountName}` : ''}
          </p>
        </div>
        <button className="link" type="button" onClick={() => onNavigate('/dashboard')}>{t('Dashboard')}</button>
      </header>

      <section className="card playground-options" aria-labelledby="playground-options-title">
        <div className="playground-section-heading">
          <div>
            <h2 id="playground-options-title">{t('Request settings')}</h2>
            <p className="muted">{t('Only models and groups available to your account are shown.')}</p>
          </div>
        </div>

        {groupsState.kind === 'loading' && <p className="muted" role="status">{t('Loading playground groups…')}</p>}
        {groupsState.kind === 'error' && (
          <div className="playground-alert" role="alert">
            <p>{t('Unable to load playground groups.')}</p>
            <button type="button" onClick={() => setGroupsAttempt((value) => value + 1)}>{t('Retry')}</button>
          </div>
        )}
        {groupsState.kind === 'ready' && groupsState.value.length === 0 && (
          <p className="playground-empty">{t('No playground groups are available for this account.')}</p>
        )}
        {groupsState.kind === 'ready' && groupsState.value.length > 0 && (
          <div className="playground-option-grid">
            <label>
              {t('Group')}
              <select
                value={group}
                disabled={generating}
                onChange={(event) => setGroup(event.target.value)}
              >
                {groupsState.value.map((option) => (
                  <option key={option.id} value={option.id}>
                    {option.id}{option.ratio === null ? ` · ${t('Automatic')}` : ` · ${option.ratio}×`}
                  </option>
                ))}
              </select>
            </label>
            <label>
              {t('Model')}
              <select
                value={model}
                disabled={generating || modelsState.kind !== 'ready' || modelsState.value.length === 0}
                onChange={(event) => setModel(event.target.value)}
              >
                {modelsState.kind === 'loading' && <option value="">{t('Loading models…')}</option>}
                {modelsState.kind === 'ready' && modelsState.value.length === 0 && <option value="">{t('No models available')}</option>}
                {modelsState.kind === 'ready' && modelsState.value.map((option) => (
                  <option key={option} value={option}>{option}</option>
                ))}
              </select>
            </label>
          </div>
        )}
        {activeGroupDescription && (
          <p className="playground-group-description">{activeGroupDescription}</p>
        )}
        {modelsState.kind === 'error' && (
          <div className="playground-alert" role="alert">
            <p>{t('Unable to load models for this group.')}</p>
            <button type="button" onClick={() => setModelsAttempt((value) => value + 1)}>{t('Retry')}</button>
          </div>
        )}
        {modelsState.kind === 'ready' && modelsState.value.length === 0 && (
          <p className="playground-empty">{t('No models are available in this group.')}</p>
        )}

        <label>
          {t('System instructions (optional)')}
          <textarea
            rows={3}
            value={systemPrompt}
            disabled={generating}
            maxLength={PLAYGROUND_LIMITS.systemCharacters}
            onChange={(event) => setSystemPrompt(event.target.value)}
            placeholder={t('Describe how the assistant should respond.')}
          />
        </label>

        <details className="playground-advanced">
          <summary>{t('Advanced parameters')}</summary>
          <div className="playground-parameter-grid">
            <ParameterInput label="Temperature" value={parameterDraft.temperature} minimum="0" maximum="2" step="0.1" disabled={generating} onChange={(value) => setParameter('temperature', value)} />
            <ParameterInput label="Top P" value={parameterDraft.topP} minimum="0" maximum="1" step="0.05" disabled={generating} onChange={(value) => setParameter('topP', value)} />
            <ParameterInput label="Maximum output tokens" value={parameterDraft.maxTokens} minimum="1" maximum="32768" step="1" disabled={generating} onChange={(value) => setParameter('maxTokens', value)} />
            <ParameterInput label="Frequency penalty" value={parameterDraft.frequencyPenalty} minimum="-2" maximum="2" step="0.1" disabled={generating} onChange={(value) => setParameter('frequencyPenalty', value)} />
            <ParameterInput label="Presence penalty" value={parameterDraft.presencePenalty} minimum="-2" maximum="2" step="0.1" disabled={generating} onChange={(value) => setParameter('presencePenalty', value)} />
            <ParameterInput label="Seed (optional)" value={parameterDraft.seed} minimum="-2147483648" maximum="2147483647" step="1" disabled={generating} onChange={(value) => setParameter('seed', value)} />
          </div>
        </details>
      </section>

      <section className="card playground-conversation" aria-labelledby="playground-conversation-title" aria-busy={generating}>
        <div className="playground-section-heading">
          <div>
            <h2 id="playground-conversation-title">{t('Conversation')}</h2>
            <p className="muted">{t('Responses are rendered as plain text.')}</p>
          </div>
          <button className="link" type="button" disabled={generating || messages.length === 0} onClick={clearConversation}>{t('Clear conversation')}</button>
        </div>

        {messages.length === 0 && (
          <div className="playground-empty">
            <p>{t('No playground messages yet.')}</p>
            <p className="muted">{t('Choose a model and send a message to begin.')}</p>
          </div>
        )}
        {messages.length > 0 && (
          <ol className="playground-messages" aria-live="polite">
            {messages.map((message) => (
              <li className={`playground-message playground-message-${message.role}`} key={message.id}>
                <article aria-label={message.role === 'user' ? t('Your message') : t('Assistant message')}>
                  <div className="playground-message-heading">
                    <strong>{message.role === 'user' ? t('You') : t('Assistant')}</strong>
                    {message.status === 'streaming' && <span className="muted">{t('Generating…')}</span>}
                    {message.status === 'stopped' && <span className="muted">{t('Stopped')}</span>}
                    {message.status === 'error' && <span className="error">{t('Failed')}</span>}
                  </div>
                  {message.reasoning && (
                    <details className="playground-reasoning">
                      <summary>{t('Reasoning')}</summary>
                      <pre>{message.reasoning}</pre>
                    </details>
                  )}
                  {message.content
                    ? <pre className="playground-message-content">{message.content}</pre>
                    : message.status === 'complete'
                      ? <p className="muted">{t('The model returned no text.')}</p>
                      : null}
                </article>
              </li>
            ))}
          </ol>
        )}
        {requestError && <p className="playground-request-error" role="alert">{t(requestError)}</p>}

        <form className="playground-composer" onSubmit={submit}>
          <label htmlFor="playground-message">{t('Message')}</label>
          <textarea
            id="playground-message"
            rows={5}
            value={composer}
            disabled={generating || !optionsReady}
            maxLength={PLAYGROUND_LIMITS.messageCharacters}
            onChange={(event) => {
              setComposer(event.target.value);
              if (inputError) setInputError(false);
            }}
            placeholder={optionsReady ? t('Write a message…') : t('Choose an available group and model first.')}
          />
          {inputError && <p className="playground-request-error" role="alert">{t('Check the message and request parameters.')}</p>}
          <div className="playground-composer-actions">
            {generating
              ? <button type="button" className="playground-stop" onClick={stopGeneration}>{t('Stop generation')}</button>
              : <button type="submit" disabled={!optionsReady || composer.trim() === ''}>{t('Send message')}</button>}
            <span className="muted">{t('Uses your signed-in session; no API key is exposed.')}</span>
          </div>
        </form>
      </section>
    </main>
  );
}

function ParameterInput({
  label,
  value,
  minimum,
  maximum,
  step,
  disabled,
  onChange,
}: {
  label: string;
  value: string;
  minimum: string;
  maximum: string;
  step: string;
  disabled: boolean;
  onChange: (value: string) => void;
}) {
  const { t } = useTranslation();
  return (
    <label>
      {t(label)}
      <input
        type="number"
        value={value}
        min={minimum}
        max={maximum}
        step={step}
        disabled={disabled}
        inputMode="decimal"
        onChange={(event) => onChange(event.target.value)}
      />
    </label>
  );
}
