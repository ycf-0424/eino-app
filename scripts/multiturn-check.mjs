// 多轮对话回归检查：同一会话能否连续问第二次。
//
// 为什么需要它：2026-09-17 实测的缺陷是「每个会话只能问一次」——
// 第一轮正常，第二轮被 memory.CheckBinding 判成归属不符，HTTP 500
// "memory session scope mismatch"（13ms，模型还没被调用）。单元测试全绿、
// /health 也正常，只有真的连着问两句才会暴露。
//
// 走的是浏览器完全相同的路径（前端每轮新建一条 WebSocket）：
//   POST /auth/local → POST /sessions → ws?session_id=<id> → chat → done
//   再新建一条 ws，带同一个 session_id 问第二轮
//
// 用法（Node 22+，内置 WebSocket，无需 npm 依赖）：
//   node scripts/multiturn-check.mjs
//   node scripts/multiturn-check.mjs --base http://localhost:18180 --user admin --pass admin123
// 退出码 0 = 两轮都通过且第二轮答出了第一轮埋的数字；1 = 任一项不满足。
//
// 前提：服务端 auth 开启时需有可用的本地账号（go run ./cmd/user-admin）；
// 容器部署下 base 用 http://localhost:18180。

const args = process.argv.slice(2);
function arg(name, fallback) {
  const i = args.indexOf(`--${name}`);
  return i >= 0 && args[i + 1] ? args[i + 1] : fallback;
}

const BASE = arg("base", "http://localhost:18180").replace(/\/$/, "");
const WS = BASE.replace(/^http/, "ws");
const USER = arg("user", "admin");
const PASS = arg("pass", "admin123");
const SECRET = arg("secret", "41");
const TIMEOUT_MS = Number(arg("timeout", "240")) * 1000;

const log = (...a) => console.log(...a);

async function login() {
  const res = await fetch(`${BASE}/auth/local`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username: USER, password: PASS }),
  });
  if (!res.ok) throw new Error(`login failed: HTTP ${res.status}`);
  const cookie = (res.headers.getSetCookie() || []).map((c) => c.split(";")[0]).join("; ");
  if (!cookie) throw new Error("login succeeded but no session cookie was returned");
  return cookie;
}

async function createSession(cookie) {
  const res = await fetch(`${BASE}/sessions`, { method: "POST", headers: { Cookie: cookie } });
  if (!res.ok) throw new Error(`create session failed: HTTP ${res.status}`);
  return (await res.json()).data.id;
}

// 一轮对话：连接时带上会话 id，收到 ready 后发 chat，等 done。
function ask(cookie, sessionId, query) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(`${WS}/ws?session_id=${encodeURIComponent(sessionId)}`, {
      headers: { Cookie: cookie },
    });
    let answer = "";
    let readyId = "";
    const timer = setTimeout(() => {
      ws.close();
      reject(new Error("timed out waiting for done"));
    }, TIMEOUT_MS);
    const finish = (fn, value) => {
      clearTimeout(timer);
      ws.close();
      fn(value);
    };
    ws.onmessage = (e) => {
      const msg = JSON.parse(e.data);
      if (msg.type === "ready") {
        readyId = msg.session_id;
        ws.send(JSON.stringify({ type: "chat", query }));
      } else if (msg.type === "chunk") {
        answer += msg.content || "";
      } else if (msg.type === "done") {
        finish(resolve, { sessionId: msg.session_id || readyId, answer });
      } else if (msg.type === "error") {
        // 修复前这里就是 "memory session scope mismatch"。
        finish(reject, new Error(`server error frame: ${msg.error}`));
      }
    };
    ws.onerror = (e) => finish(reject, new Error(`ws error: ${e?.message || e?.error?.message || "unknown"}`));
  });
}

const cookie = await login();
const id = await createSession(cookie);
log(`session    ${id}`);

const first = await ask(cookie, id, `记住数字 ${SECRET}，只回复“收到”。`);
log(`round 1    sid=${first.sessionId}  answer=${first.answer.trim().slice(0, 40)}`);

const second = await ask(cookie, id, "我刚才让你记住的数字是多少？只回数字。");
log(`round 2    sid=${second.sessionId}  answer=${second.answer.trim().slice(0, 40)}`);

const sameSession = first.sessionId === id && second.sessionId === id;
const contextKept = second.answer.includes(SECRET);
log(`same_session=${sameSession}  context_kept=${contextKept}`);
if (!sameSession) log("FAIL: 第二轮没有沿用同一个 session id（服务端又签发了一个新会话）");
if (!contextKept) log(`FAIL: 第二轮回答里没有 ${SECRET}（历史没有被带上）`);
process.exit(sameSession && contextKept ? 0 : 1);
