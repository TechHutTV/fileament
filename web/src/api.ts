export class APIError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

export async function api(path: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers);
  if (!(init.body instanceof FormData) && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
  const res = await fetch(path, { credentials: 'include', ...init, headers });
  const body = await res.text();
  if (!res.ok) throw new APIError(res.status, body);
  return body ? JSON.parse(body) : null;
}
