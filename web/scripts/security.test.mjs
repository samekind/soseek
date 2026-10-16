import test from "node:test";
import assert from "node:assert/strict";
import { api, APIError, money, safeLink } from "../src/lib/api.ts";

test("only public HTTP protocols become clickable result links", () => {
  assert.equal(safeLink("javascript:alert(1)"), undefined);
  assert.equal(safeLink("data:text/html,test"), undefined);
  assert.equal(safeLink("//example.com"), undefined);
  assert.equal(
    safeLink("https://example.com/article"),
    "https://example.com/article",
  );
  assert.equal(
    safeLink("http://example.com/article"),
    "http://example.com/article",
  );
});
test("mutation uses same-origin cookies and CSRF guard, without bearer persistence", async () => {
  const original = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async (url, options) => {
    calls++;
    assert.equal(url, "/admin/api/principals");
    assert.equal(options.credentials, "same-origin");
    assert.equal(options.cache, "no-store");
    assert.equal(options.headers["X-Soseek-Admin"], "1");
    assert.equal(options.headers.Authorization, undefined);
    assert.deepEqual(JSON.parse(options.body), {
      name: "Example",
      daily_usd: "1",
      providers: [],
    });
    return new Response(
      JSON.stringify({
        principal: { id: "p1", name: "Example" },
        token: "one-time-test-token",
      }),
      { status: 200 },
    );
  };
  try {
    const response = await api("/principals", {
      method: "POST",
      body: { name: "Example", daily_usd: "1", providers: [] },
    });
    assert.equal(response.token, "one-time-test-token");
    assert.equal(calls, 1);
  } finally {
    globalThis.fetch = original;
  }
});
test("failed mutation is not automatically retried", async () => {
  const original = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async () => {
    calls++;
    return new Response(
      JSON.stringify({ error: { message: "Denied", code: "admin_required" } }),
      { status: 403 },
    );
  };
  try {
    await assert.rejects(
      api("/credentials/test/disable", { method: "POST", body: {} }),
      (error) => error instanceof APIError && error.status === 403,
    );
    assert.equal(calls, 1);
  } finally {
    globalThis.fetch = original;
  }
});
test("missing monetary fields are not shown as known zero", () => {
  assert.equal(money(undefined), "—");
  assert.equal(money(0), "$0.00");
  assert.equal(money(7000), "$0.007");
  assert.equal(money(1), "$0.000001");
});

test("initial session probe does not announce expiry; authenticated requests still do", async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = globalThis.window;
  const events = [];
  globalThis.window = { dispatchEvent: event => { events.push(event.type); return true; } };
  globalThis.fetch = async () => new Response(JSON.stringify({ error: { code: "unauthorized" } }), { status: 401 });
  try {
    await assert.rejects(api("/session", { notifyUnauthorized: false }), error => error instanceof APIError && error.status === 401);
    assert.deepEqual(events, []);
    await assert.rejects(api("/overview"), error => error instanceof APIError && error.status === 401);
    assert.deepEqual(events, ["soseek:unauthorized"]);
  } finally {
    globalThis.fetch = originalFetch;
    if (originalWindow === undefined) delete globalThis.window;
    else globalThis.window = originalWindow;
  }
});
