import { describe, expect, it } from 'vitest';
import type { User } from '../../shared/api/client';
import {
  authenticatedUserFromBundle,
  isAuthenticatedUser,
  isLoginChallenge,
  isValidAffiliateCode,
  MAX_LOGIN_FLOW_TOKEN_CHARACTERS,
  oauthStartTarget,
  parseOAuthFlowState,
  parsePasswordResetLocation,
  parseTelegramAuthorization,
  parseWeChatOAuthEntry,
  telegramLoginTarget,
  type LoginChallenge,
} from './auth-flow';

const user: User = {
  id: 7,
  username: 'alice',
  display_name: '',
  role: 1,
  group: 'default',
  quota: 500_000,
  used_quota: 10,
  request_count: 2,
  email: 'alice@example.test',
};

describe('password login flow classification', () => {
  it('keeps a 2FA challenge out of the authenticated user path', () => {
    const challenge: LoginChallenge = { twofa_required: true, flow_token: 'single-use-flow' };
    expect(isLoginChallenge(challenge)).toBe(true);
  });

  it('accepts a completed user response and rejects empty challenges', () => {
    expect(isLoginChallenge(user)).toBe(false);
    expect(isLoginChallenge({ twofa_required: true, flow_token: '' })).toBe(false);
    expect(isLoginChallenge({
      twofa_required: true,
      flow_token: 'x'.repeat(MAX_LOGIN_FLOW_TOKEN_CHARACTERS + 1),
    })).toBe(false);
    expect(isLoginChallenge(null)).toBe(false);
    expect(isAuthenticatedUser(user)).toBe(true);
    expect(authenticatedUserFromBundle({ user, access_token: 'not-consumed-by-ui' })).toBe(user);
    expect(authenticatedUserFromBundle({ user: { id: 7, username: 'partial' } })).toBeNull();
    expect(isAuthenticatedUser({ id: 0, username: 'invalid' })).toBe(false);
    expect(isAuthenticatedUser({ id: 7, username: '' })).toBe(false);
  });
});

describe('external-auth input contracts', () => {
  it('accepts only an unambiguous bounded WeChat callback', () => {
    expect(parseWeChatOAuthEntry('/sign-in', '?provider=wechat&code=one'))
      .toEqual({ kind: 'not-callback' });
    expect(parseWeChatOAuthEntry('/oauth', '?provider=wechat&code=one&redirect=%2Fwallet'))
      .toEqual({ kind: 'wechat', code: 'one' });
    expect(parseWeChatOAuthEntry('/oauth', '?provider=wechat&code=one&code=two'))
      .toEqual({ kind: 'invalid' });
    expect(parseWeChatOAuthEntry('/oauth', '?provider=github&code=one'))
      .toEqual({ kind: 'invalid' });
    expect(parseWeChatOAuthEntry('/oauth', '?provider=wechat&code=line%0Abreak'))
      .toEqual({ kind: 'invalid' });
    expect(parseWeChatOAuthEntry('/oauth', '?provider=wechat&code=%20one'))
      .toEqual({ kind: 'invalid' });
    expect(parseWeChatOAuthEntry('/oauth', '?provider=wechat&code=bad%escape'))
      .toEqual({ kind: 'invalid' });
    expect(parseWeChatOAuthEntry('/oauth', '?provider=wechat&code=one&unexpected=value'))
      .toEqual({ kind: 'invalid' });
  });

  it('selects signed Telegram fields and builds the exact same-origin endpoint', () => {
    const authorization = parseTelegramAuthorization({
      id: 42,
      first_name: 'Alice',
      username: 'alice_bot_user',
      auth_date: '1788566400',
      hash: 'A'.repeat(64),
      private: 'discarded',
    });
    expect(authorization).toEqual({
      id: '42',
      first_name: 'Alice',
      username: 'alice_bot_user',
      auth_date: '1788566400',
      hash: 'A'.repeat(64),
    });
    const flowToken = 'S'.repeat(64);
    expect(telegramLoginTarget(authorization!, flowToken)).toBe(
      `/api/oauth/telegram/login?flow_token=${flowToken}&id=42&first_name=Alice&username=alice_bot_user&auth_date=1788566400&hash=${'A'.repeat(64)}`,
    );
    expect(telegramLoginTarget(authorization!, 'short')).toBeNull();
    expect(parseTelegramAuthorization({
      id: '42', first_name: ' Alice ', auth_date: '1788566400', hash: 'a'.repeat(64),
    })?.first_name).toBe(' Alice ');
    expect(parseTelegramAuthorization({
      id: ' 42', auth_date: '1788566400', hash: 'a'.repeat(64),
    })).toBeNull();
    expect(parseTelegramAuthorization({ id: 0, auth_date: 1, hash: 'a'.repeat(64) })).toBeNull();
    expect(parseTelegramAuthorization({ id: 1, auth_date: 1, hash: 'not-a-signature' })).toBeNull();
    expect(parseTelegramAuthorization({
      id: 1, auth_date: 1, hash: 'a'.repeat(64), first_name: 'spoof\u202etext',
    })).toBeNull();
  });

  it('constructs only bounded provider starts with the backend affiliate contract', () => {
    expect(isValidAffiliateCode('partner.code')).toBe(true);
    expect(oauthStartTarget('company-sso', 'partner.code'))
      .toBe('/api/oauth/company-sso?aff=partner.code');
    expect(oauthStartTarget('github', '', '/wallet?section=topup'))
      .toBe('/api/oauth/github?redirect=%2Fwallet%3Fsection%3Dtopup');
    expect(oauthStartTarget('github', 'partner', '/dashboard/models'))
      .toBe('/api/oauth/github?aff=partner&redirect=%2Fdashboard%2Fmodels');
    expect(oauthStartTarget('../provider', 'partner')).toBeNull();
    expect(oauthStartTarget('github', ' spaced ')).toBeNull();
    expect(oauthStartTarget('github', 'x'.repeat(33))).toBeNull();
    expect(oauthStartTarget('github', '\u4f60'.repeat(11))).toBeNull();
    expect(oauthStartTarget('github', '', 'https://attacker.example/')).toBeNull();
    expect(oauthStartTarget('github', '', '//attacker.example/')).toBeNull();
    expect(oauthStartTarget('github', '', '/oauth/github')).toBeNull();
    expect(oauthStartTarget('github', '', '/a/../wallet')).toBeNull();
  });

  it('accepts only the exact bounded one-time OAuth state response', () => {
    const flowToken = 'A'.repeat(64);
    expect(parseOAuthFlowState({ flow_token: flowToken, expires_at: 1_788_566_400 }))
      .toEqual({ flow_token: flowToken, expires_at: 1_788_566_400 });
    expect(parseOAuthFlowState({ flow_token: 'short', expires_at: 1 })).toBeNull();
    expect(parseOAuthFlowState({ flow_token: flowToken, expires_at: 1, extra: true })).toBeNull();
    expect(parseOAuthFlowState({ flow_token: flowToken, expires_at: 1.5 })).toBeNull();
  });
});

describe('password-reset link parsing', () => {
  it('supports reference tokens and the compatible code form', () => {
    expect(parsePasswordResetLocation('?email=person%40example.test&token=opaque-token'))
      .toEqual({
        valid: true,
        email: 'person@example.test',
        credential: 'opaque-token',
        generatedMode: true,
      });
    expect(parsePasswordResetLocation('?email=person%40example.test&code=123456'))
      .toEqual({
        valid: true,
        email: 'person@example.test',
        credential: '123456',
        generatedMode: false,
      });
  });

  it.each([
    '?email=person%40example.test&token=one&token=two',
    '?email=person%40example.test&token=one&code=two',
    '?email=person%40example.test&token=line%0Abreak',
    '?email=person%40example.test&token=bad%escape',
    '?email=person%40example.test&unexpected=value',
    `?email=person%40example.test&token=${'x'.repeat(257)}`,
  ])('rejects ambiguous or unsafe input: %s', (search) => {
    expect(parsePasswordResetLocation(search).valid).toBe(false);
  });
});
