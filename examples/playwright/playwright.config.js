const { defineConfig } = require("@playwright/test");

module.exports = defineConfig({
  testDir: ".",
  testMatch: "*.spec.js",
  timeout: 60_000,
  use: { baseURL: "http://localhost:3000" },
  // The webServer inherits the environment, so the app sends the same
  // X-Run-Id (SANDPOST_RUN_ID) the test filters on.
  webServer: { command: "node demo-app.js", url: "http://localhost:3000", reuseExistingServer: false },
});
