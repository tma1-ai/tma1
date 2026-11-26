import { readFileSync } from "node:fs";
import vm from "node:vm";
import test from "node:test";
import assert from "node:assert/strict";

const context = vm.createContext({});
vm.runInContext(readFileSync(new URL("../js/core.js", import.meta.url), "utf8"), context);
vm.runInContext(readFileSync(new URL("../js/sessions.js", import.meta.url), "utf8"), context);

test("session list keeps sessions whose keywords only appear in tool results", () => {
  // Session: PreToolUse "ls" (the activity marker), PostToolUse results
  // "flow ok" and "disk ok". A keyword prefilter in WHERE drops the
  // PreToolUse row, so HAVING never sees the session as active.
  const scope = "ts > NOW() - INTERVAL '1 day'";
  const sql = context.sess_listSessionIdsSQL(scope, context.splitSearchTerms("flow disk"));
  const [where, having] = sql.split(" GROUP BY session_id HAVING ");
  assert.equal(where, "SELECT session_id FROM tma1_hook_events WHERE " + scope);
  assert.match(having, /^SUM\(CASE WHEN event_type IN \('UserPromptSubmit','PreToolUse'\)/);
  assert.match(having, /SUM\(CASE WHEN \(tool_name LIKE '%flow%'.* THEN 1 ELSE 0 END\) > 0/);
  assert.match(having, /SUM\(CASE WHEN \(tool_name LIKE '%disk%'.* THEN 1 ELSE 0 END\) > 0/);
});

test("hook keyword patterns match literally", () => {
  assert.match(context.sess_hookTermSQL("_"), /tool_result LIKE '%\\_%'/);
  assert.match(context.sess_hookTermSQL("100%"), /tool_result LIKE '%100\\%%'/);
  assert.match(context.sess_hookTermSQL("a\\b"), /tool_result LIKE '%a\\\\b%'/);
  assert.match(context.sess_hookTermSQL("it's"), /tool_result LIKE '%it''s%'/);
});
