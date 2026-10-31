import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, api, setUnauthorizedHandler, terminalURL } from "./api";

function answer(status: number, body: unknown) {
  return vi.fn().mockResolvedValue({ status, ok: status < 400, text: async () => (typeof body === "string" ? body : JSON.stringify(body)) });
}

afterEach(() => vi.unstubAllGlobals());

describe("the API client", () => {
  it("reads JSON and sends nothing but GET for a read", async () => {
    const fetch = answer(200, { user: { id: "u1" } });
    vi.stubGlobal("fetch", fetch);
    const s = await api.session();
    expect(s.user.id).toBe("u1");
    expect(fetch).toHaveBeenCalledWith("/api/v1/session", expect.objectContaining({ method: "GET", credentials: "same-origin" }));
    expect(fetch.mock.calls[0]![1].body).toBeUndefined();
  });

  it("sends JSON on a change, even with no body, and encodes names", async () => {
    const fetch = answer(200, { agent: "a b" });
    vi.stubGlobal("fetch", fetch);
    await api.operate("p 1", "a b", "stop");
    const [url, init] = fetch.mock.calls[0]!;
    expect(url).toBe("/api/v1/p/p%201/a/a%20b/stop");
    expect(init.method).toBe("POST");
    expect(init.headers["Content-Type"]).toBe("application/json");
    expect(init.body).toBe("{}");
  });

  it("turns an error answer into an ApiError with its code and message", async () => {
    vi.stubGlobal("fetch", answer(403, { code: "forbidden", message: "You are not allowed to do that." }));
    await expect(api.members("p1")).rejects.toMatchObject({ status: 403, code: "forbidden", message: "You are not allowed to do that." });
    vi.stubGlobal("fetch", answer(502, "<html>bad gateway</html>"));
    await expect(api.projects()).rejects.toBeInstanceOf(ApiError);
  });

  it("signs in with a password outside the API, and a wrong password does not count as a session that ended", async () => {
    const ended = vi.fn();
    setUnauthorizedHandler(ended);
    const fetch = answer(401, { code: "bad_login", message: "Wrong username or password." });
    vi.stubGlobal("fetch", fetch);
    await expect(api.passwordLogin("admin", "nope")).rejects.toMatchObject({ status: 401, message: "Wrong username or password." });
    const [url, init] = fetch.mock.calls[0]!;
    expect(url).toBe("/auth/login");
    expect(JSON.parse(init.body)).toEqual({ username: "admin", password: "nope" });
    expect(ended).not.toHaveBeenCalled();
  });

  it("tells the app when the session has ended", async () => {
    const ended = vi.fn();
    setUnauthorizedHandler(ended);
    vi.stubGlobal("fetch", answer(401, { code: "unauthorized", message: "Sign in again." }));
    await expect(api.projects()).rejects.toMatchObject({ status: 401 });
    expect(ended).toHaveBeenCalledOnce();
    setUnauthorizedHandler(() => {});
  });

  it("reports a hub that cannot be reached", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("failed")));
    await expect(api.projects()).rejects.toMatchObject({ status: 0, code: "network" });
  });

  it("builds the terminal address from the page's own", () => {
    expect(terminalURL("p1", "coder 1", false)).toBe("ws://localhost:3000/api/v1/p/p1/a/coder%201/terminal".replace("localhost:3000", location.host));
    expect(terminalURL("p1", "c", true)).toMatch(/\/terminal\?takeover=1$/);
  });
});
