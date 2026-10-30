// OpenCode v1 plugin. Copy to ~/.config/opencode/plugins/tma1.js.
export const TMA1Plugin = async ({ client }) => ({
  event: async ({ event }) => {
    if (event.type !== "session.idle") return;
    const id = event.properties.sessionID;
    try {
      const [session, messages] = await Promise.all([
        client.session.get({ path: { id }, throwOnError: true }),
        client.session.messages({ path: { id }, throwOnError: true }),
      ]);
      const response = await fetch("http://127.0.0.1:14318/api/import/opencode", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ info: session.data, messages: messages.data }),
        signal: AbortSignal.timeout(120000),
      });
      if (!response.ok) throw new Error(`TMA1 import returned HTTP ${response.status}`);
    } catch (error) {
      // A later idle event retries the snapshot; existing records are skipped.
      console.error("TMA1 session import failed:", error.message);
    }
  },
});
