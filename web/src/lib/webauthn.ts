// WebAuthn client helpers for passkey registration/login. These bridge the
// base64url-encoded values from the server to the ArrayBuffers required by the
// WebAuthn browser API, and back.

export function bufferToBase64Url(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let bin = '';
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export function base64UrlToBuffer(s: string): ArrayBuffer {
  let v = s.replace(/-/g, '+').replace(/_/g, '/');
  while (v.length % 4) v += '=';
  const bin = atob(v);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes.buffer;
}

// prepareCreationOptions converts server-sent creation options for
// navigator.credentials.create.
export function prepareCreationOptions(publicKey: any): any {
  const pk = { ...publicKey };
  pk.challenge = base64UrlToBuffer(pk.challenge);
  pk.user = { ...pk.user, id: base64UrlToBuffer(pk.user.id) };
  if (pk.excludeCredentials) {
    pk.excludeCredentials = pk.excludeCredentials.map((c: any) => ({ ...c, id: base64UrlToBuffer(c.id) }));
  }
  return pk;
}

// serializeCredential converts a PublicKeyCredential to the server's JSON shape.
export function serializeCredential(cred: any): any {
  const transports =
    typeof cred.response.getTransports === 'function' ? cred.response.getTransports() : undefined;
  return {
    id: cred.id,
    rawId: bufferToBase64Url(cred.rawId),
    type: cred.type,
    response: {
      clientDataJSON: bufferToBase64Url(cred.response.clientDataJSON),
      attestationObject: cred.response.attestationObject
        ? bufferToBase64Url(cred.response.attestationObject)
        : undefined,
      authenticatorData: cred.response.authenticatorData
        ? bufferToBase64Url(cred.response.authenticatorData)
        : undefined,
      signature: cred.response.signature ? bufferToBase64Url(cred.response.signature) : undefined,
      transports,
    },
    clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
  };
}
